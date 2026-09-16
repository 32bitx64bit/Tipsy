/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "focused_overlay.h"
#include "focused_text_foreground.h"

#include <cairo/cairo.h>
#include <fontconfig/fontconfig.h>
#include <fontconfig/fcfreetype.h>
#include <pango/pangocairo.h>
#include <pango/pangofc-fontmap.h>
#include <math.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

struct tipsy_focused_overlay {
	pthread_mutex_t mutex;
	pthread_cond_t readers_done;
	unsigned readers;
	int published;
	int x, y, width, height, stride;
	uint64_t version, generation;
	uint8_t *rgba;
	cairo_surface_t *surface;
	cairo_t *cr;
	PangoFontDescription *font_desc;
	double font_px;
	int font_id;
	char *font_file;
	uint32_t requested_argb;
	unsigned long glyph_pixels, caret_pixels, antialias_pixels, bright_pixels;
	int caret_valid, caret_x, caret_y, caret_height;
	int text_origin_x, line_box_top, line_box_height, baseline_y;
};

static pthread_mutex_t published_mutex = PTHREAD_MUTEX_INITIALIZER;
static struct tipsy_focused_overlay *published_overlay;

static void wipe(void *p, size_t n) { if (p != NULL && n != 0) memset(p, 0, n); }

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

static void clear_pixels(struct tipsy_focused_overlay *o) {
	if (o->rgba != NULL) wipe(o->rgba, (size_t)o->stride * (size_t)o->height);
	if (o->cr != NULL) {
		cairo_save(o->cr);
		cairo_set_operator(o->cr, CAIRO_OPERATOR_CLEAR);
		cairo_paint(o->cr);
		cairo_restore(o->cr);
		cairo_surface_flush(o->surface);
	}
}

static void discard_surface(struct tipsy_focused_overlay *o) {
	clear_pixels(o);
	if (o->cr != NULL) cairo_destroy(o->cr);
	o->cr = NULL;
	if (o->surface != NULL) cairo_surface_destroy(o->surface);
	o->surface = NULL;
	if (o->rgba != NULL) free(o->rgba);
	o->rgba = NULL;
	o->x = o->y = o->width = o->height = o->stride = 0;
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

static int ensure_surface(struct tipsy_focused_overlay *o, int x, int y,
	int width, int height) {
	if (width < 1 || height < 1 || width > INT32_MAX / 4 ||
		(size_t)height > SIZE_MAX / ((size_t)width * 4)) return -1;
	if (o->surface != NULL && o->x == x && o->y == y && o->width == width &&
		o->height == height) return 0;
	discard_surface(o);
	o->x = x; o->y = y; o->width = width; o->height = height; o->stride = width * 4;
	o->surface = cairo_image_surface_create(CAIRO_FORMAT_ARGB32, width, height);
	if (o->surface == NULL || cairo_surface_status(o->surface) != CAIRO_STATUS_SUCCESS) {
		discard_surface(o); return -1;
	}
	o->cr = cairo_create(o->surface);
	if (o->cr == NULL || cairo_status(o->cr) != CAIRO_STATUS_SUCCESS) {
		discard_surface(o); return -1;
	}
	o->rgba = calloc((size_t)o->stride, (size_t)height);
	if (o->rgba == NULL) { discard_surface(o); return -1; }
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
	int content_width = o->width - left - right, content_height = o->height - top - bottom;
	if (content_width < 1 || content_height < 1) return -1;
	clear_pixels(o);
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
	else if (y_alignment == 2) box_top = o->height - bottom - box.height;
	int origin_y = box_top - box.y, origin_x = left - box.x;
	if (!multi) {
		if (x_alignment == 1) origin_x = o->width - right - box.width - box.x;
		else if (x_alignment == 2) origin_x = left + (content_width - box.width) / 2 - box.x;
		PangoRectangle strong, weak;
		pango_layout_get_cursor_pos(layout, cursor_byte, &strong, &weak);
		int cursor_x = origin_x + PANGO_PIXELS(strong.x);
		if (cursor_x > o->width - right) origin_x -= cursor_x - (o->width - right);
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
	convert_to_rgba(o);
	return 0;
}

uintptr_t tipsy_focused_overlay_new(void) {
	struct tipsy_focused_overlay *o = calloc(1, sizeof(*o));
	if (o == NULL || pthread_mutex_init(&o->mutex, NULL) != 0 ||
		pthread_cond_init(&o->readers_done, NULL) != 0) { free(o); return 0; }
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
	pthread_mutex_lock(&published_mutex); pthread_mutex_lock(&o->mutex);
	while (o->readers != 0) pthread_cond_wait(&o->readers_done, &o->mutex);
	if (!visible) {
		if (published_overlay == o) published_overlay = NULL;
		o->published = 0; clear_pixels(o); o->requested_argb = 0;
		o->glyph_pixels = o->caret_pixels = o->antialias_pixels = o->bright_pixels = 0;
		o->version = 0; pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex); return 0;
	}
	if (width < 1 || height < 1 || font_px <= 0 || text == NULL || text_len < 0 ||
		cursor_byte < 0 || cursor_byte > text_len || left < 0 || top < 0 || right < 0 || bottom < 0) {
		pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex); return -1;
	}
	int dirty = !o->published || o->version != version || o->x != x || o->y != y ||
		o->width != width || o->height != height;
	if (dirty && (ensure_surface(o, x, y, width, height) != 0 ||
		open_font(o, font_px, font_id, font_file) != 0 ||
		rasterize(o, text, text_len, cursor_byte, argb, x_alignment, y_alignment,
			multiline, wrapped, editable, cursor_visible, spacing, left, top, right,
			bottom, include_font_padding) != 0)) {
		if (published_overlay == o) published_overlay = NULL;
		o->published = 0; clear_pixels(o);
		pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex); return -1;
	}
	if (dirty) { o->version = version; o->generation++; o->requested_argb = argb; }
	o->published = 1; published_overlay = o;
	pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex);
	return 0;
}

void tipsy_focused_overlay_free(uintptr_t ptr) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL) return;
	pthread_mutex_lock(&published_mutex); pthread_mutex_lock(&o->mutex);
	if (published_overlay == o) published_overlay = NULL;
	o->published = 0;
	while (o->readers != 0) pthread_cond_wait(&o->readers_done, &o->mutex);
	discard_surface(o); close_font(o);
	pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex);
	pthread_cond_destroy(&o->readers_done); pthread_mutex_destroy(&o->mutex); wipe(o, sizeof(*o)); free(o);
}

int tipsy_focused_text_frame_acquire(struct tipsy_focused_text_frame *out) {
	if (out == NULL) return 0;
	memset(out, 0, sizeof(*out)); pthread_mutex_lock(&published_mutex);
	struct tipsy_focused_overlay *o = published_overlay;
	if (o == NULL) { pthread_mutex_unlock(&published_mutex); return 0; }
	pthread_mutex_lock(&o->mutex);
	if (!o->published || o->rgba == NULL) { pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex); return 0; }
	o->readers++;
	out->rgba = o->rgba; out->x = o->x; out->y = o->y; out->width = o->width;
	out->height = o->height; out->stride = o->stride; out->generation = o->generation;
	out->lease = (uintptr_t)o;
	pthread_mutex_unlock(&o->mutex); pthread_mutex_unlock(&published_mutex); return 1;
}

void tipsy_focused_text_frame_release(uintptr_t lease) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)lease;
	if (o == NULL) return;
	pthread_mutex_lock(&o->mutex);
	if (o->readers != 0 && --o->readers == 0) pthread_cond_broadcast(&o->readers_done);
	pthread_mutex_unlock(&o->mutex);
}

int tipsy_focused_overlay_query(uintptr_t ptr,
	struct tipsy_focused_overlay_metrics *out) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	int published;
	if (o == NULL || out == NULL) return 0;
	memset(out, 0, sizeof(*out));
	pthread_mutex_lock(&o->mutex);
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
	pthread_mutex_unlock(&o->mutex);
	return published;
}

int tipsy_focused_overlay_test_foreground_alpha(uintptr_t ptr, int x, int y, unsigned long *alpha) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || alpha == NULL) return -1;
	pthread_mutex_lock(&o->mutex);
	if (o->rgba == NULL || x < 0 || y < 0 || x >= o->width || y >= o->height) {
		pthread_mutex_unlock(&o->mutex);
		return -1;
	}
	*alpha = o->rgba[(size_t)y * o->stride + (size_t)x * 4 + 3];
	pthread_mutex_unlock(&o->mutex);
	return 0;
}
