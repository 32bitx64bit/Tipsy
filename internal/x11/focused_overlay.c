/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

#include "focused_overlay.h"
#include "focused_text_foreground.h"

#include <cairo/cairo.h>
#include <fontconfig/fontconfig.h>
#include <fontconfig/fcfreetype.h>
#include <pango/pangocairo.h>
#include <pango/pangofc-fontmap.h>
#include <errno.h>
#include <math.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#ifndef M_PI
#define M_PI 3.14159265358979323846
#endif

/*
 * Font loading and Pango drawing happen on the writer-owned Cairo surface
 * with no reader-visible lock held, so a compositor's frame acquire waits at
 * most for the final pixel copy into rgba, never for a font load.
 */
struct tipsy_focused_overlay {
	/* Serializes update, query, and free; frame readers never take it. The
	 * published frame below changes only while it is held. */
	pthread_mutex_t writer;
	pthread_mutex_t mutex;
	pthread_cond_t readers_done;
	unsigned readers;
	/* Deferred cleanup when a reader lease outlives a bounded drain, guarded
	 * by mutex: 0 = live, 1 = wipe the frame on the last release, 2 = the
	 * owner freed the overlay and the last release must complete the free. */
	int retire;
	int published;
	int x, y, width, height, stride;
	uint64_t version, generation;
	uint8_t *rgba;
	/* Writer-owned raster state. */
	cairo_surface_t *surface;
	cairo_t *cr;
	int surface_width, surface_height;
	PangoFontDescription *font_desc;
	double font_px;
	int font_id;
	char *font_file;
	uint32_t requested_argb;
	unsigned long glyph_pixels, caret_pixels, antialias_pixels, bright_pixels;
	int caret_valid, caret_x, caret_y, caret_height;
	int text_origin_x, line_box_top, line_box_height, baseline_y;
};

/* The update card is an independent, passive layer. It shares the text
 * compositor's lease API so both remain visible without an X11 child window. */
struct tipsy_update_notice {
	pthread_mutex_t writer;
	pthread_mutex_t mutex;
	pthread_cond_t readers_done;
	unsigned readers;
	/* Same deferred-cleanup states as the text overlay, guarded by mutex. */
	int retire;
	int published;
	int x, y, width, height, stride;
	int viewport_width, viewport_height;
	uint64_t generation;
	uint8_t *rgba;
	cairo_surface_t *surface;
	cairo_t *cr;
};

struct tipsy_composite_lease {
	struct tipsy_focused_overlay *text;
	struct tipsy_update_notice *notice;
	uint8_t *rgba;
	size_t bytes;
};

static pthread_mutex_t published_mutex = PTHREAD_MUTEX_INITIALIZER;
static struct tipsy_focused_overlay *published_overlay;
static struct tipsy_update_notice update_notice = {
	.writer = PTHREAD_MUTEX_INITIALIZER,
	.mutex = PTHREAD_MUTEX_INITIALIZER,
	.readers_done = PTHREAD_COND_INITIALIZER,
};
static struct tipsy_update_notice *published_notice;
static _Atomic uint32_t overlay_live;

/* The notice is a static object, so its cond starts as PTHREAD_COND_INITIALIZER
 * (CLOCK_REALTIME). Re-init it once against CLOCK_MONOTONIC so a bounded drain
 * is immune to wall-clock jumps; fall back to REALTIME if that ever fails. */
static pthread_once_t notice_cond_once = PTHREAD_ONCE_INIT;
static clockid_t notice_cond_clock = CLOCK_REALTIME;
static void notice_cond_init(void)
{
	pthread_condattr_t attr;
	if (pthread_condattr_init(&attr) != 0) return;
	if (pthread_condattr_setclock(&attr, CLOCK_MONOTONIC) == 0 &&
		pthread_cond_destroy(&update_notice.readers_done) == 0) {
		if (pthread_cond_init(&update_notice.readers_done, &attr) == 0) {
			notice_cond_clock = CLOCK_MONOTONIC;
		} else {
			/* The static cond was already destroyed; restore a usable
			 * one. REALTIME is an acceptable fallback, but leaving it
			 * destroyed would make the next timedwait undefined. */
			(void)pthread_cond_init(&update_notice.readers_done, NULL);
		}
	}
	pthread_condattr_destroy(&attr);
}

static void wipe(void *p, size_t n) { if (p != NULL && n != 0) memset(p, 0, n); }

/* A compositor reader can wedge in a guest fence wait, so the writer must
 * never block the launch loop on a lease indefinitely. Drain waits are
 * bounded; on timeout the caller defers the destructive wipe/free to the
 * last reader release instead of corrupting a live lease. */
#define TIPSY_OVERLAY_DRAIN_TIMEOUT_NS 120000000L

static void overlay_drain_deadline(struct timespec *out, clockid_t clock)
{
	if (clock_gettime(clock, out) != 0) {
		/* A deadline in the past makes timedwait time out at once, which
		 * is the safe non-blocking degradation. */
		out->tv_sec = 0;
		out->tv_nsec = 0;
		return;
	}
	out->tv_nsec += TIPSY_OVERLAY_DRAIN_TIMEOUT_NS;
	if (out->tv_nsec >= 1000000000L) {
		out->tv_sec++;
		out->tv_nsec -= 1000000000L;
	}
}

static void tipsy_focused_overlay_update_live_locked(void)
{
	atomic_store_explicit(&overlay_live,
		published_overlay != NULL || published_notice != NULL, memory_order_release);
}

static void tipsy_focused_overlay_mark_live_locked(void)
{
	atomic_store_explicit(&overlay_live, 1, memory_order_release);
}

static void tipsy_focused_overlay_clear_publish_locked(struct tipsy_focused_overlay *o)
{
	if (published_overlay == o) published_overlay = NULL;
	if (o != NULL) o->published = 0;
	tipsy_focused_overlay_update_live_locked();
}

static void close_font(struct tipsy_focused_overlay *o) {
	if (o->font_desc != NULL) pango_font_description_free(o->font_desc);
	o->font_desc = NULL;
	if (o->font_file != NULL) {
		wipe(o->font_file, strlen(o->font_file));
		free(o->font_file);
	}
	o->font_file = NULL;
	o->font_px = 0;
	o->font_id = 0;
}

static void clear_surface(struct tipsy_focused_overlay *o) {
	if (o->cr == NULL) return;
	cairo_save(o->cr);
	cairo_set_operator(o->cr, CAIRO_OPERATOR_CLEAR);
	cairo_paint(o->cr);
	cairo_restore(o->cr);
	cairo_surface_flush(o->surface);
}

static void discard_surface(struct tipsy_focused_overlay *o) {
	clear_surface(o);
	if (o->cr != NULL) cairo_destroy(o->cr);
	o->cr = NULL;
	if (o->surface != NULL) cairo_surface_destroy(o->surface);
	o->surface = NULL;
	o->surface_width = o->surface_height = 0;
}

/* Returns 1 when every reader lease returned, 0 if the bounded drain timed
 * out with a reader still active. The caller must not wipe or free the frame
 * in the latter case. */
static int wait_readers_locked(struct tipsy_focused_overlay *o) {
	struct timespec deadline;
	overlay_drain_deadline(&deadline, CLOCK_MONOTONIC);
	while (o->readers != 0) {
		int rc = pthread_cond_timedwait(&o->readers_done, &o->mutex, &deadline);
		if (rc != 0) return o->readers == 0;
	}
	return 1;
}

/* Unpublishes o, then wipes its frame once every lease returns. The published
 * lock is dropped first so other acquires return immediately meanwhile. If a
 * reader is stuck past the bounded drain, the wipe is deferred to the last
 * release rather than zeroing pixels under a live lease. */
static void unpublish_and_wipe(struct tipsy_focused_overlay *o) {
	pthread_mutex_lock(&published_mutex);
	pthread_mutex_lock(&o->mutex);
	tipsy_focused_overlay_clear_publish_locked(o);
	pthread_mutex_unlock(&published_mutex);
	if (wait_readers_locked(o)) {
		if (o->rgba != NULL) wipe(o->rgba, (size_t)o->stride * (size_t)o->height);
		o->version = 0;
		o->retire = 0;
	} else {
		o->retire = 1;
	}
	pthread_mutex_unlock(&o->mutex);
	clear_surface(o);
}

static PangoFontDescription *font_description(const char *file, double px,
	int font_id) {
	PangoFontDescription *desc = NULL;
	if (file != NULL && file[0] != 0) {
		FcConfig *config = FcConfigGetCurrent();
		if (config != NULL && FcConfigAppFontAddFile(config, (const FcChar8 *)file)) {
			PangoFontMap *map = pango_cairo_font_map_get_default();
			if (PANGO_IS_FC_FONT_MAP(map)) pango_fc_font_map_config_changed(PANGO_FC_FONT_MAP(map));
		}
		int count = 0;
		FcPattern *pattern = FcFreeTypeQuery((const FcChar8 *)file, 0, NULL, &count);
		if (pattern != NULL) {
			desc = pango_fc_font_description_from_pattern(pattern, FALSE);
			FcPatternDestroy(pattern);
		}
	}
	if (desc == NULL && file != NULL && file[0] != 0) return NULL;
	if (desc == NULL) {
		desc = pango_font_description_new();
		pango_font_description_set_family(desc, "Source Sans Pro");
		pango_font_description_set_weight(desc, font_id == 4 ? PANGO_WEIGHT_BOLD :
			(font_id == 5 ? PANGO_WEIGHT_LIGHT : PANGO_WEIGHT_NORMAL));
	}
	if (desc == NULL) return NULL;
	pango_font_description_set_absolute_size(desc, px * PANGO_SCALE);
	return desc;
}

static int open_font(struct tipsy_focused_overlay *o, double px, int font_id,
	const char *font_file) {
	const char *wanted = font_file == NULL ? "" : font_file;
	const char *current = o->font_file == NULL ? "" : o->font_file;
	if (o->font_desc != NULL && fabs(o->font_px - px) < .001 && o->font_id == font_id &&
		strcmp(current, wanted) == 0) return 0;
	close_font(o);
	o->font_desc = font_description(wanted, px, font_id);
	if (o->font_desc == NULL) return -1;
	o->font_file = strdup(wanted);
	if (o->font_file == NULL) { close_font(o); return -1; }
	o->font_px = px;
	o->font_id = font_id;
	return 0;
}

static int ensure_surface(struct tipsy_focused_overlay *o, int width, int height) {
	if (width < 1 || height < 1 || width > INT32_MAX / 4 ||
		(size_t)height > SIZE_MAX / ((size_t)width * 4)) return -1;
	if (o->surface != NULL && o->surface_width == width &&
		o->surface_height == height) return 0;
	discard_surface(o);
	o->surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, width, height);
	if (o->surface == NULL || cairo_surface_status(o->surface) != CAIRO_STATUS_SUCCESS) {
		discard_surface(o); return -1;
	}
	o->cr = cairo_create(o->surface);
	if (o->cr == NULL || cairo_status(o->cr) != CAIRO_STATUS_SUCCESS) {
		discard_surface(o); return -1;
	}
	o->surface_width = width;
	o->surface_height = height;
	return 0;
}

static void convert_to_rgba(struct tipsy_focused_overlay *o);

/* Caller holds published_mutex and mutex, and no reader holds the frame:
 * the finished surface becomes the reader-visible frame at x,y. */
static int publish_pixels_locked(struct tipsy_focused_overlay *o, int x, int y) {
	int width = o->surface_width, height = o->surface_height;
	if (o->rgba == NULL || o->width != width || o->height != height) {
		if (o->rgba != NULL) {
			wipe(o->rgba, (size_t)o->stride * (size_t)o->height);
			free(o->rgba);
		}
		o->rgba = calloc((size_t)width * 4, (size_t)height);
		if (o->rgba == NULL) {
			o->x = o->y = o->width = o->height = o->stride = 0;
			return -1;
		}
	}
	o->x = x; o->y = y; o->width = width; o->height = height; o->stride = width * 4;
	convert_to_rgba(o);
	return 0;
}

static void convert_to_rgba(struct tipsy_focused_overlay *o) {
	cairo_surface_flush(o->surface);
	uint8_t *source = cairo_image_surface_get_data(o->surface);
	int source_stride = cairo_image_surface_get_stride(o->surface);
	for (int y = 0; y < o->height; ++y) for (int x = 0; x < o->width; ++x) {
		uint32_t pixel = 0;
		memcpy(&pixel, source + (size_t)y * source_stride + (size_t)x * 4, 4);
		uint8_t *out = o->rgba + (size_t)y * o->stride + (size_t)x * 4;
		/* Cairo ARGB32 is native-endian storage of logical 0xAARRGGBB. */
		out[0] = (uint8_t)(pixel >> 16);
		out[1] = (uint8_t)(pixel >> 8);
		out[2] = (uint8_t)pixel;
		out[3] = (uint8_t)(pixel >> 24);
	}
}

static void measure(struct tipsy_focused_overlay *o, uint32_t argb) {
	o->glyph_pixels = o->caret_pixels = o->antialias_pixels = o->bright_pixels = 0;
	unsigned alpha = argb >> 24;
	for (int y = 0; y < o->height; ++y) for (int x = 0; x < o->width; ++x) {
		const uint8_t *p = o->rgba + (size_t)y * o->stride + (size_t)x * 4;
		if (p[3] == 0) continue;
		if (o->caret_valid && x == o->caret_x && y >= o->caret_y &&
			y < o->caret_y + o->caret_height) { o->caret_pixels++; continue; }
		o->glyph_pixels++;
		if (p[3] < alpha) o->antialias_pixels++;
		if (p[0] * 299u + p[1] * 587u + p[2] * 114u >= 192000u) o->bright_pixels++;
	}
}

static int rasterize(struct tipsy_focused_overlay *o, const unsigned char *text,
	int text_len, int cursor_byte, uint32_t argb, int x_alignment, int y_alignment,
	int multiline, int wrapped, int editable, int cursor_visible, double spacing,
	int left, int top, int right, int bottom, int include_font_padding) {
	int width = o->surface_width, height = o->surface_height;
	int content_width = width - left - right, content_height = height - top - bottom;
	if (content_width < 1 || content_height < 1) return -1;
	clear_surface(o);
	cairo_save(o->cr);
	cairo_set_operator(o->cr, CAIRO_OPERATOR_OVER);
	cairo_rectangle(o->cr, left, top, content_width, content_height);
	cairo_clip(o->cr);
	PangoLayout *layout = pango_cairo_create_layout(o->cr);
	if (layout == NULL) { cairo_restore(o->cr); return -1; }
	pango_layout_set_font_description(layout, o->font_desc);
	pango_layout_set_text(layout, (const char *)text, text_len);
	int multi = multiline || wrapped;
	if (multi) {
		pango_layout_set_width(layout, content_width * PANGO_SCALE);
		pango_layout_set_wrap(layout, PANGO_WRAP_WORD_CHAR);
		pango_layout_set_alignment(layout, x_alignment == 1 ? PANGO_ALIGN_RIGHT :
			(x_alignment == 2 ? PANGO_ALIGN_CENTER : PANGO_ALIGN_LEFT));
	} else { pango_layout_set_width(layout, -1); pango_layout_set_single_paragraph_mode(layout, TRUE); }
	if (fabs(spacing) > .00001 && text_len > 0) {
		PangoAttrList *attrs = pango_attr_list_new();
		PangoAttribute *tracking = pango_attr_letter_spacing_new((int)lrint(spacing * o->font_px * PANGO_SCALE));
		tracking->start_index = 0; tracking->end_index = (guint)text_len;
		pango_attr_list_insert(attrs, tracking); pango_layout_set_attributes(layout, attrs);
		pango_attr_list_unref(attrs);
	}
	PangoRectangle ink, logical;
	pango_layout_get_pixel_extents(layout, &ink, &logical);
	PangoRectangle box = include_font_padding ? logical : ink;
	if (box.height < 1) box.height = (int)ceil(o->font_px);
	int box_top = top;
	if (y_alignment == 1) box_top = top + (content_height - box.height) / 2;
	else if (y_alignment == 2) box_top = height - bottom - box.height;
	int origin_y = box_top - box.y, origin_x = left - box.x;
	if (!multi) {
		if (x_alignment == 1) origin_x = width - right - box.width - box.x;
		else if (x_alignment == 2) origin_x = left + (content_width - box.width) / 2 - box.x;
		PangoRectangle strong, weak;
		pango_layout_get_cursor_pos(layout, cursor_byte, &strong, &weak);
		int cursor_x = origin_x + PANGO_PIXELS(strong.x);
		if (cursor_x > width - right) origin_x -= cursor_x - (width - right);
		if (cursor_x < left) origin_x += left - cursor_x;
	}
	cairo_set_source_rgba(o->cr, ((argb >> 16) & 255) / 255.0,
		((argb >> 8) & 255) / 255.0, (argb & 255) / 255.0, (argb >> 24) / 255.0);
	cairo_move_to(o->cr, origin_x, origin_y);
	pango_cairo_show_layout(o->cr, layout);
	o->caret_valid = 0;
	if (editable && cursor_visible) {
		PangoRectangle strong, weak;
		pango_layout_get_cursor_pos(layout, cursor_byte, &strong, &weak);
		o->caret_valid = 1; o->caret_x = origin_x + PANGO_PIXELS(strong.x);
		o->caret_y = origin_y + PANGO_PIXELS(strong.y); o->caret_height = PANGO_PIXELS(strong.height);
		if (o->caret_height < 1) o->caret_height = box.height;
		cairo_rectangle(o->cr, o->caret_x, o->caret_y, 1, o->caret_height); cairo_fill(o->cr);
	}
	o->text_origin_x = origin_x; o->line_box_top = box_top;
	o->line_box_height = box.height;
	o->baseline_y = origin_y + PANGO_PIXELS(pango_layout_get_baseline(layout));
	g_object_unref(layout); cairo_restore(o->cr);
	return 0;
}

uintptr_t tipsy_focused_overlay_new(void) {
	struct tipsy_focused_overlay *o = calloc(1, sizeof(*o));
	if (o == NULL) return 0;
	if (pthread_mutex_init(&o->writer, NULL) != 0) { free(o); return 0; }
	if (pthread_mutex_init(&o->mutex, NULL) != 0) {
		pthread_mutex_destroy(&o->writer); free(o); return 0;
	}
	pthread_condattr_t attr;
	if (pthread_condattr_init(&attr) != 0) {
		pthread_mutex_destroy(&o->mutex); pthread_mutex_destroy(&o->writer);
		free(o); return 0;
	}
	int cond_rc = pthread_condattr_setclock(&attr, CLOCK_MONOTONIC);
	if (cond_rc == 0) cond_rc = pthread_cond_init(&o->readers_done, &attr);
	pthread_condattr_destroy(&attr);
	if (cond_rc != 0) {
		pthread_mutex_destroy(&o->mutex); pthread_mutex_destroy(&o->writer);
		free(o); return 0;
	}
	return (uintptr_t)o;
}

int tipsy_focused_overlay_update(uintptr_t ptr, int visible, uint64_t version,
	int x, int y, int width, int height, double font_px, int font_id,
	const char *font_file, uint32_t argb, double spacing, int left, int top,
	int right, int bottom, int include_font_padding, int x_alignment,
	int y_alignment, int multiline, int wrapped, int editable, int cursor_visible,
	const unsigned char *text, int text_len, int cursor_byte) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL) return -1;
	pthread_mutex_lock(&o->writer);
	if (!visible) {
		unpublish_and_wipe(o); o->requested_argb = 0;
		o->glyph_pixels = o->caret_pixels = o->antialias_pixels = o->bright_pixels = 0;
		pthread_mutex_unlock(&o->writer); return 0;
	}
	if (width < 1 || height < 1 || font_px <= 0 || text == NULL || text_len < 0 ||
		cursor_byte < 0 || cursor_byte > text_len || left < 0 || top < 0 || right < 0 || bottom < 0) {
		pthread_mutex_unlock(&o->writer); return -1;
	}
	/* Only a writer changes the published frame, so it is read here unlocked.
	 * An unchanged repaint therefore never waits on a reader. */
	int dirty = !o->published || o->version != version || o->x != x || o->y != y ||
		o->width != width || o->height != height;
	if (dirty && (ensure_surface(o, width, height) != 0 ||
		open_font(o, font_px, font_id, font_file) != 0 ||
		rasterize(o, text, text_len, cursor_byte, argb, x_alignment, y_alignment,
			multiline, wrapped, editable, cursor_visible, spacing, left, top, right,
			bottom, include_font_padding) != 0)) {
		unpublish_and_wipe(o);
		pthread_mutex_unlock(&o->writer); return -1;
	}
	pthread_mutex_lock(&published_mutex); pthread_mutex_lock(&o->mutex);
	if (dirty) {
		if (!wait_readers_locked(o)) {
			/* A stuck reader still holds the old frame; replacing pixels
			 * under it would corrupt the lease. Keep the old frame published
			 * and let the next update retry once the lease returns. */
			pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex);
			pthread_mutex_unlock(&o->writer); return -1;
		}
		o->retire = 0;
		if (publish_pixels_locked(o, x, y) != 0) {
			tipsy_focused_overlay_clear_publish_locked(o);
			pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex);
			clear_surface(o);
			pthread_mutex_unlock(&o->writer); return -1;
		}
		o->version = version; o->generation++; o->requested_argb = argb;
	}
	tipsy_focused_overlay_mark_live_locked();
	o->published = 1; published_overlay = o;
	pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex);
	pthread_mutex_unlock(&o->writer);
	return 0;
}

void tipsy_focused_overlay_free(uintptr_t ptr) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL) return;
	pthread_mutex_lock(&o->writer);
	unpublish_and_wipe(o);
	discard_surface(o); close_font(o);
	pthread_mutex_lock(&o->mutex);
	if (o->readers != 0) {
		/* A stuck reader still references the frame and the sync objects;
		 * freeing them here would be a use-after-free. The last reader
		 * release completes the free instead (retire == 2). */
		o->retire = 2;
		pthread_mutex_unlock(&o->mutex);
		pthread_mutex_unlock(&o->writer);
		return;
	}
	o->retire = 0;
	pthread_mutex_unlock(&o->mutex);
	free(o->rgba); o->rgba = NULL;
	pthread_mutex_unlock(&o->writer);
	pthread_cond_destroy(&o->readers_done); pthread_mutex_destroy(&o->mutex);
	pthread_mutex_destroy(&o->writer); wipe(o, sizeof(*o)); free(o);
}

static int notice_wait_readers_locked(struct tipsy_update_notice *n) {
	struct timespec deadline;
	pthread_once(&notice_cond_once, notice_cond_init);
	overlay_drain_deadline(&deadline, notice_cond_clock);
	while (n->readers != 0) {
		int rc = pthread_cond_timedwait(&n->readers_done, &n->mutex, &deadline);
		if (rc != 0) return n->readers == 0;
	}
	return 1;
}

static void notice_clear_surface(struct tipsy_update_notice *n) {
	if (n->cr == NULL) return;
	cairo_save(n->cr);
	cairo_set_operator(n->cr, CAIRO_OPERATOR_CLEAR);
	cairo_paint(n->cr);
	cairo_restore(n->cr);
	cairo_surface_flush(n->surface);
}

static void notice_discard_surface(struct tipsy_update_notice *n) {
	notice_clear_surface(n);
	if (n->cr != NULL) cairo_destroy(n->cr);
	n->cr = NULL;
	if (n->surface != NULL) cairo_surface_destroy(n->surface);
	n->surface = NULL;
}

static void notice_unpublish(struct tipsy_update_notice *n) {
	pthread_mutex_lock(&published_mutex);
	pthread_mutex_lock(&n->mutex);
	if (published_notice == n) published_notice = NULL;
	n->published = 0;
	tipsy_focused_overlay_update_live_locked();
	pthread_mutex_unlock(&published_mutex);
	if (notice_wait_readers_locked(n)) {
		if (n->rgba != NULL) wipe(n->rgba, (size_t)n->stride * (size_t)n->height);
		n->retire = 0;
	} else {
		n->retire = 1;
	}
	pthread_mutex_unlock(&n->mutex);
}

static int notice_ensure_surface(struct tipsy_update_notice *n, int width, int height) {
	if (width < 1 || height < 1 || width > INT32_MAX / 4 ||
		(size_t)height > SIZE_MAX / ((size_t)width * 4)) return -1;
	if (n->surface != NULL && cairo_image_surface_get_width(n->surface) == width &&
		cairo_image_surface_get_height(n->surface) == height) return 0;
	notice_discard_surface(n);
	n->surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, width, height);
	if (n->surface == NULL || cairo_surface_status(n->surface) != CAIRO_STATUS_SUCCESS) {
		notice_discard_surface(n); return -1;
	}
	n->cr = cairo_create(n->surface);
	if (n->cr == NULL || cairo_status(n->cr) != CAIRO_STATUS_SUCCESS) {
		notice_discard_surface(n); return -1;
	}
	return 0;
}

static cairo_surface_t *notice_icon_surface(const uint32_t *pixels, int width, int height) {
	if (pixels == NULL || width < 1 || height < 1 || width > INT32_MAX / 4 ||
		(size_t)height > SIZE_MAX / ((size_t)width * 4)) return NULL;
	cairo_surface_t *surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, width, height);
	if (surface == NULL || cairo_surface_status(surface) != CAIRO_STATUS_SUCCESS) {
		if (surface != NULL) cairo_surface_destroy(surface);
		return NULL;
	}
	uint8_t *destination = cairo_image_surface_get_data(surface);
	int stride = cairo_image_surface_get_stride(surface);
	for (int y = 0; y < height; ++y) for (int x = 0; x < width; ++x) {
		uint32_t source = pixels[(size_t)y * (size_t)width + (size_t)x];
		uint32_t alpha = source >> 24;
		uint32_t red = (((source >> 16) & 255) * alpha + 127) / 255;
		uint32_t green = (((source >> 8) & 255) * alpha + 127) / 255;
		uint32_t blue = ((source & 255) * alpha + 127) / 255;
		uint32_t converted = (alpha << 24) | (red << 16) | (green << 8) | blue;
		memcpy(destination + (size_t)y * (size_t)stride + (size_t)x * 4, &converted, 4);
	}
	cairo_surface_mark_dirty(surface);
	return surface;
}

static void notice_rounded_rectangle(cairo_t *cr, double width, double height, double radius) {
	cairo_new_sub_path(cr);
	cairo_arc(cr, width - radius, radius, radius, -M_PI / 2, 0);
	cairo_arc(cr, width - radius, height - radius, radius, 0, M_PI / 2);
	cairo_arc(cr, radius, height - radius, radius, M_PI / 2, M_PI);
	cairo_arc(cr, radius, radius, radius, M_PI, 3 * M_PI / 2);
	cairo_close_path(cr);
}

static int notice_rasterize(struct tipsy_update_notice *n, int width, int height,
	double angle_degrees, double opacity, double scale, const char *text, int text_len,
	const uint32_t *icon_argb, int icon_width, int icon_height) {
	if (notice_ensure_surface(n, width, height) != 0) return -1;
	notice_clear_surface(n);
	cairo_save(n->cr);
	cairo_set_operator(n->cr, CAIRO_OPERATOR_OVER);
	cairo_translate(n->cr, (double)width / 2, (double)height / 2);
	cairo_scale(n->cr, scale, scale);
	cairo_translate(n->cr, -(double)width / 2, -(double)height / 2);
	cairo_push_group(n->cr);
	int result = -1;
	notice_rounded_rectangle(n->cr, width, height, 14);
	cairo_set_source_rgba(n->cr, 0.075, 0.085, 0.12, 0.96);
	cairo_fill_preserve(n->cr);
	cairo_set_source_rgba(n->cr, 0.45, 0.55, 0.78, 0.55);
	cairo_set_line_width(n->cr, 1);
	cairo_stroke(n->cr);

	double icon_size = height - 24;
	if (icon_size < 1 || icon_size > width - 48) goto group_done;
	cairo_surface_t *icon = notice_icon_surface(icon_argb, icon_width, icon_height);
	if (icon == NULL) goto group_done;
	cairo_save(n->cr);
	cairo_translate(n->cr, 12 + icon_size / 2, (double)height / 2);
	cairo_rotate(n->cr, angle_degrees * M_PI / 180);
	cairo_scale(n->cr, icon_size / icon_width, icon_size / icon_height);
	cairo_set_source_surface(n->cr, icon, -(double)icon_width / 2, -(double)icon_height / 2);
	cairo_paint(n->cr);
	cairo_restore(n->cr);
	cairo_surface_destroy(icon);

	int text_x = 24 + (int)icon_size;
	int text_width = width - text_x - 14;
	if (text_width < 1) goto group_done;
	PangoLayout *layout = pango_cairo_create_layout(n->cr);
	PangoFontDescription *font = pango_font_description_new();
	if (layout == NULL || font == NULL) {
		if (layout != NULL) g_object_unref(layout);
		if (font != NULL) pango_font_description_free(font);
		goto group_done;
	}
	pango_font_description_set_family(font, "Sans");
	pango_font_description_set_weight(font, PANGO_WEIGHT_SEMIBOLD);
	pango_font_description_set_absolute_size(font, 15 * PANGO_SCALE);
	pango_layout_set_font_description(layout, font);
	pango_layout_set_single_paragraph_mode(layout, TRUE);
	pango_layout_set_ellipsize(layout, PANGO_ELLIPSIZE_END);
	pango_layout_set_width(layout, text_width * PANGO_SCALE);
	pango_layout_set_text(layout, text, text_len);
	PangoRectangle ink, logical;
	pango_layout_get_pixel_extents(layout, &ink, &logical);
	cairo_set_source_rgba(n->cr, 0.96, 0.97, 1, 1);
	cairo_move_to(n->cr, text_x - logical.x, (height - logical.height) / 2 - logical.y);
	pango_cairo_show_layout(n->cr, layout);
	pango_font_description_free(font);
	g_object_unref(layout);
	result = 0;

group_done:
	if (result == 0) {
		cairo_pop_group_to_source(n->cr);
		cairo_paint_with_alpha(n->cr, opacity);
	} else {
		cairo_pattern_destroy(cairo_pop_group(n->cr));
	}
	cairo_restore(n->cr);
	return result == 0 && cairo_status(n->cr) == CAIRO_STATUS_SUCCESS ? 0 : -1;
}

int tipsy_update_notice_update(int visible, int viewport_width, int viewport_height,
	int x, int y, int width, int height, double angle_degrees,
	double opacity, double scale, const char *text, int text_len,
	const uint32_t *icon_argb, int icon_width, int icon_height) {
	struct tipsy_update_notice *n = &update_notice;
	pthread_mutex_lock(&n->writer);
	if (!visible) {
		notice_unpublish(n);
		pthread_mutex_unlock(&n->writer);
		return 0;
	}
	if (viewport_width < 1 || viewport_height < 1 || x < 0 || y < 0 ||
		x >= viewport_width || y >= viewport_height || width < 1 || height < 1 ||
		width > viewport_width || height > viewport_height || x > viewport_width - width ||
		y > viewport_height - height || text == NULL || text_len < 1 || !isfinite(angle_degrees) ||
		!isfinite(opacity) || opacity < 0 || opacity > 1 || !isfinite(scale) || scale <= 0) {
		notice_unpublish(n);
		pthread_mutex_unlock(&n->writer);
		return -1;
	}
	if (notice_rasterize(n, width, height, angle_degrees, opacity, scale, text, text_len,
		icon_argb, icon_width, icon_height) != 0) {
		notice_unpublish(n);
		pthread_mutex_unlock(&n->writer);
		return -1;
	}

	pthread_mutex_lock(&published_mutex);
	pthread_mutex_lock(&n->mutex);
	if (!notice_wait_readers_locked(n)) {
		/* A stuck reader still holds the old card; do not realloc or
		 * overwrite its pixels. Keep it published and retry next update. */
		pthread_mutex_unlock(&n->mutex);
		pthread_mutex_unlock(&published_mutex);
		pthread_mutex_unlock(&n->writer);
		return -1;
	}
	n->retire = 0;
	if (n->rgba == NULL || n->width != width || n->height != height) {
		if (n->rgba != NULL) {
			wipe(n->rgba, (size_t)n->stride * (size_t)n->height);
			free(n->rgba);
		}
		n->rgba = calloc((size_t)width * 4, (size_t)height);
		if (n->rgba == NULL) {
			n->x = n->y = n->width = n->height = n->stride = 0;
			published_notice = NULL;
			n->published = 0;
			tipsy_focused_overlay_update_live_locked();
			pthread_mutex_unlock(&n->mutex);
			pthread_mutex_unlock(&published_mutex);
			pthread_mutex_unlock(&n->writer);
			return -1;
		}
	}
	int stride = width * 4;
	cairo_surface_flush(n->surface);
	const uint8_t *source = cairo_image_surface_get_data(n->surface);
	int source_stride = cairo_image_surface_get_stride(n->surface);
	for (int row = 0; row < height; ++row) for (int column = 0; column < width; ++column) {
		uint32_t pixel = 0;
		memcpy(&pixel, source + (size_t)row * (size_t)source_stride + (size_t)column * 4, 4);
		uint8_t *out = n->rgba + (size_t)row * (size_t)stride + (size_t)column * 4;
		out[0] = (uint8_t)(pixel >> 16);
		out[1] = (uint8_t)(pixel >> 8);
		out[2] = (uint8_t)pixel;
		out[3] = (uint8_t)(pixel >> 24);
	}
	n->x = x; n->y = y; n->width = width; n->height = height; n->stride = stride;
	n->viewport_width = viewport_width; n->viewport_height = viewport_height;
	if (++n->generation == 0) n->generation = 1;
	n->published = 1; published_notice = n;
	tipsy_focused_overlay_mark_live_locked();
	pthread_mutex_unlock(&n->mutex);
	pthread_mutex_unlock(&published_mutex);
	pthread_mutex_unlock(&n->writer);
	return 0;
}

void tipsy_update_notice_free(void) {
	struct tipsy_update_notice *n = &update_notice;
	pthread_mutex_lock(&n->writer);
	notice_unpublish(n);
	notice_discard_surface(n);
	pthread_mutex_lock(&n->mutex);
	if (n->readers != 0) {
		/* A stuck reader still references rgba; defer its wipe/free to the
		 * last release instead of freeing under a live lease. */
		n->retire = 2;
		pthread_mutex_unlock(&n->mutex);
		pthread_mutex_unlock(&n->writer);
		return;
	}
	n->retire = 0;
	pthread_mutex_unlock(&n->mutex);
	if (n->rgba != NULL) wipe(n->rgba, (size_t)n->stride * (size_t)n->height);
	free(n->rgba);
	n->rgba = NULL;
	n->x = n->y = n->width = n->height = n->stride = 0;
	n->viewport_width = n->viewport_height = 0;
	n->generation = 0;
	pthread_mutex_unlock(&n->writer);
}

static void release_text_reader(struct tipsy_focused_overlay *o) {
	pthread_mutex_lock(&o->mutex);
	if (o->readers == 0 || --o->readers != 0) {
		pthread_mutex_unlock(&o->mutex);
		return;
	}
	pthread_cond_broadcast(&o->readers_done);
	int retire = o->retire;
	o->retire = 0;
	if (retire == 1) {
		/* The writer timed out unpublishing; wipe now that no lease remains. */
		if (o->rgba != NULL) wipe(o->rgba, (size_t)o->stride * (size_t)o->height);
		o->version = 0;
	}
	pthread_mutex_unlock(&o->mutex);
	if (retire != 2) return;
	/* The owner's free timed out on this stuck lease. Complete it now that
	 * the frame is unreferenced. Taking the writer lock serializes with the
	 * owner's free returning, so the sync objects are never destroyed while
	 * it still holds them. */
	pthread_mutex_lock(&o->writer);
	if (o->rgba != NULL) wipe(o->rgba, (size_t)o->stride * (size_t)o->height);
	free(o->rgba); o->rgba = NULL;
	pthread_cond_destroy(&o->readers_done);
	pthread_mutex_destroy(&o->mutex);
	pthread_mutex_unlock(&o->writer);
	pthread_mutex_destroy(&o->writer);
	wipe(o, sizeof(*o));
	free(o);
}

/* Caller holds n->mutex. */
static void release_notice_reader_locked(struct tipsy_update_notice *n) {
	if (n->readers == 0 || --n->readers != 0) return;
	pthread_cond_broadcast(&n->readers_done);
	int retire = n->retire;
	n->retire = 0;
	if (retire == 0) return;
	if (n->rgba != NULL) wipe(n->rgba, (size_t)n->stride * (size_t)n->height);
	if (retire == 2) {
		/* The notice is static; only its frame is owned. The owner's free
		 * already returned without touching rgba, so this is safe. */
		free(n->rgba);
		n->rgba = NULL;
		n->x = n->y = n->width = n->height = n->stride = 0;
		n->viewport_width = n->viewport_height = 0;
		n->generation = 0;
	}
}

static void release_notice_reader(struct tipsy_update_notice *n) {
	pthread_mutex_lock(&n->mutex);
	release_notice_reader_locked(n);
	pthread_mutex_unlock(&n->mutex);
}

static void composite_over(uint8_t *destination, int destination_width, int destination_height,
	int destination_stride, const uint8_t *source, int source_width, int source_height,
	int source_stride, int x, int y) {
	int source_x0 = x < 0 ? -x : 0;
	int source_y0 = y < 0 ? -y : 0;
	int source_x1 = x + source_width > destination_width ? destination_width - x : source_width;
	int source_y1 = y + source_height > destination_height ? destination_height - y : source_height;
	if (source_x1 < 0) source_x1 = 0;
	if (source_y1 < 0) source_y1 = 0;
	if (source_x1 > source_width) source_x1 = source_width;
	if (source_y1 > source_height) source_y1 = source_height;
	for (int sy = source_y0; sy < source_y1; ++sy) for (int sx = source_x0; sx < source_x1; ++sx) {
		const uint8_t *s = source + (size_t)sy * (size_t)source_stride + (size_t)sx * 4;
		uint8_t *d = destination + (size_t)(y + sy) * (size_t)destination_stride + (size_t)(x + sx) * 4;
		unsigned inverse = 255u - s[3];
		for (int channel = 0; channel < 3; ++channel) {
			d[channel] = (uint8_t)(s[channel] + ((unsigned)d[channel] * inverse + 127u) / 255u);
		}
		d[3] = (uint8_t)(s[3] + ((unsigned)d[3] * inverse + 127u) / 255u);
	}
}

static int clip_to_viewport(int x, int y, int width, int height, int viewport_width,
	int viewport_height, int *left, int *top, int *right, int *bottom) {
	int64_t x0 = x < 0 ? 0 : x;
	int64_t y0 = y < 0 ? 0 : y;
	int64_t x1 = (int64_t)x + width > viewport_width ? viewport_width : (int64_t)x + width;
	int64_t y1 = (int64_t)y + height > viewport_height ? viewport_height : (int64_t)y + height;
	if (x0 >= x1 || y0 >= y1 || x1 <= 0 || y1 <= 0) return 0;
	*left = (int)x0; *top = (int)y0; *right = (int)x1; *bottom = (int)y1;
	return 1;
}

int tipsy_focused_text_frame_acquire(struct tipsy_focused_text_frame *out) {
	if (out == NULL) return 0;
	memset(out, 0, sizeof(*out));
	pthread_mutex_lock(&published_mutex);
	struct tipsy_focused_overlay *o = published_overlay;
	struct tipsy_update_notice *n = published_notice;
	if (o != NULL) {
		pthread_mutex_lock(&o->mutex);
		if (!o->published || o->rgba == NULL) {
			pthread_mutex_unlock(&o->mutex);
			o = NULL;
		} else {
			o->readers++;
		}
	}
	if (n != NULL) {
		pthread_mutex_lock(&n->mutex);
		if (!n->published || n->rgba == NULL) {
			pthread_mutex_unlock(&n->mutex);
			n = NULL;
		} else {
			n->readers++;
		}
	}
	if (o == NULL && n == NULL) {
		pthread_mutex_unlock(&published_mutex);
		return 0;
	}
	if (o != NULL && n != NULL) {
		int tx0, ty0, tx1, ty1, nx0, ny0, nx1, ny1;
		int text_visible = clip_to_viewport(o->x, o->y, o->width, o->height,
			n->viewport_width, n->viewport_height, &tx0, &ty0, &tx1, &ty1);
		int notice_visible = clip_to_viewport(n->x, n->y, n->width, n->height,
			n->viewport_width, n->viewport_height, &nx0, &ny0, &nx1, &ny1);
		if (notice_visible) {
			int x0 = text_visible && tx0 < nx0 ? tx0 : nx0;
			int y0 = text_visible && ty0 < ny0 ? ty0 : ny0;
			int x1 = text_visible && tx1 > nx1 ? tx1 : nx1;
			int y1 = text_visible && ty1 > ny1 ? ty1 : ny1;
			int width = x1 - x0, height = y1 - y0;
			struct tipsy_composite_lease *composite = calloc(1, sizeof(*composite));
			if (composite != NULL && width > 0 && height > 0 && width <= INT32_MAX / 4 &&
				(size_t)height <= SIZE_MAX / ((size_t)width * 4)) {
				composite->bytes = (size_t)width * (size_t)height * 4;
				composite->rgba = calloc(1, composite->bytes);
			}
			if (composite != NULL && composite->rgba != NULL) {
				composite->text = o; composite->notice = n;
				if (text_visible) {
					composite_over(composite->rgba, width, height, width * 4,
						o->rgba, o->width, o->height, o->stride, o->x - x0, o->y - y0);
				}
				composite_over(composite->rgba, width, height, width * 4,
					n->rgba, n->width, n->height, n->stride, n->x - x0, n->y - y0);
				out->rgba = composite->rgba; out->x = x0; out->y = y0;
				out->width = width; out->height = height; out->stride = width * 4;
				out->generation = (o->generation * UINT64_C(16777619)) ^ n->generation;
				if (out->generation == 0) out->generation = 1;
				out->lease = ((uintptr_t)composite) | 1u;
				pthread_mutex_unlock(&n->mutex);
				pthread_mutex_unlock(&o->mutex);
				pthread_mutex_unlock(&published_mutex);
				return 1;
			}
			if (composite != NULL) {
				wipe(composite->rgba, composite->bytes);
				free(composite->rgba); free(composite);
			}
		}
		/* An off-viewport card or an allocation failure drops only the cosmetic
		 * card; focused text keeps its own lease below. */
	}
	if (o != NULL) {
		out->rgba = o->rgba; out->x = o->x; out->y = o->y; out->width = o->width;
		out->height = o->height; out->stride = o->stride; out->generation = o->generation;
		out->lease = (uintptr_t)o;
		if (n != NULL) {
			release_notice_reader_locked(n);
			pthread_mutex_unlock(&n->mutex);
		}
		pthread_mutex_unlock(&o->mutex);
		pthread_mutex_unlock(&published_mutex);
		return 1;
	}
	out->rgba = n->rgba; out->x = n->x; out->y = n->y; out->width = n->width;
	out->height = n->height; out->stride = n->stride; out->generation = n->generation;
	out->lease = ((uintptr_t)n) | 2u;
	pthread_mutex_unlock(&n->mutex);
	pthread_mutex_unlock(&published_mutex);
	return 1;
}

void tipsy_focused_text_frame_release(uintptr_t lease) {
	if (lease == 0) return;
	uintptr_t raw = lease & ~(uintptr_t)3u;
	switch (lease & 3u) {
	case 1: {
		struct tipsy_composite_lease *composite = (struct tipsy_composite_lease *)raw;
		release_text_reader(composite->text);
		release_notice_reader(composite->notice);
		wipe(composite->rgba, composite->bytes);
		free(composite->rgba); wipe(composite, sizeof(*composite)); free(composite);
		break;
	}
	case 2:
		release_notice_reader((struct tipsy_update_notice *)raw);
		break;
	default:
		release_text_reader((struct tipsy_focused_overlay *)raw);
		break;
	}
}

int tipsy_focused_text_overlay_live(void) {
	return atomic_load_explicit(&overlay_live, memory_order_acquire) != 0;
}

int tipsy_focused_overlay_query(uintptr_t ptr,
	struct tipsy_focused_overlay_metrics *out) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	int published;
	if (o == NULL || out == NULL) return 0;
	memset(out, 0, sizeof(*out));
	/* The writer lock pins the published frame; readers only read it. */
	pthread_mutex_lock(&o->writer);
	if (o->rgba != NULL) measure(o, o->requested_argb);
	out->x = o->x;
	out->y = o->y;
	out->width = o->width;
	out->height = o->height;
	out->requested_argb = o->requested_argb;
	out->glyph_pixels = o->glyph_pixels;
	out->caret_pixels = o->caret_pixels;
	out->antialias_pixels = o->antialias_pixels;
	out->bright_pixels = o->bright_pixels;
	out->text_origin_x = o->text_origin_x;
	out->line_box_top = o->line_box_top;
	out->line_box_height = o->line_box_height;
	out->baseline_y = o->baseline_y;
	published = o->published;
	pthread_mutex_unlock(&o->writer);
	return published;
}

int tipsy_focused_overlay_test_foreground_alpha(uintptr_t ptr, int x, int y, unsigned long *alpha) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || alpha == NULL) return -1;
	pthread_mutex_lock(&o->writer);
	if (o->rgba == NULL || x < 0 || y < 0 || x >= o->width || y >= o->height) {
		pthread_mutex_unlock(&o->writer);
		return -1;
	}
	*alpha = o->rgba[(size_t)y * o->stride + (size_t)x * 4 + 3];
	pthread_mutex_unlock(&o->writer);
	return 0;
}
