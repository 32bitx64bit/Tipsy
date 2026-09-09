/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "focused_overlay.h"

#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/shape.h>
#include <cairo/cairo-xlib.h>
#include <fontconfig/fontconfig.h>
#include <fontconfig/fcfreetype.h>
#include <pango/pangocairo.h>
#include <pango/pangofc-fontmap.h>
#include <math.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

// A normal depth-24 child is used on every X11 desktop. The Roblox pixels
// beneath the transparent Android EditText are captured while the child is
// unmapped, then Pango/Cairo blends shaped, antialiased text into an offscreen
// copy. The completed pixmap is installed as the child background before map.
struct tipsy_focused_overlay {
	Display *dpy;
	Window parent;
	Window child;
	Visual *visual;
	Colormap colormap;
	int depth;
	int screen;
	int mapped;
	int x, y, width, height;
	int parent_width, parent_height;
	Pixmap background;
	Pixmap backing;
	GC copy_gc;
	cairo_surface_t *surface;
	cairo_t *cr;
	PangoFontDescription *font_desc;
	double font_px;
	int font_id;
	char *font_file;
	uint64_t version;
	int background_refresh_pending;
	uint32_t requested_argb;
	unsigned long glyph_pixels;
	unsigned long caret_pixels;
	unsigned long antialias_pixels;
	unsigned long bright_pixels;
	unsigned long background_pixels;
	int caret_valid, caret_x, caret_y, caret_height;
	int text_origin_x, line_box_top, line_box_height, baseline_y;
};

static void tipsy_overlay_wipe_pixmap(struct tipsy_focused_overlay *o,
	Pixmap pixmap) {
	if (o == NULL || pixmap == None || o->width <= 0 || o->height <= 0) return;
	GC gc = XCreateGC(o->dpy, pixmap, 0, NULL);
	if (gc == NULL) return;
	XSetForeground(o->dpy, gc, 0);
	XFillRectangle(o->dpy, pixmap, gc, 0, 0,
		(unsigned)o->width, (unsigned)o->height);
	XFreeGC(o->dpy, gc);
}

static void tipsy_overlay_close_font(struct tipsy_focused_overlay *o) {
	if (o == NULL) return;
	if (o->font_desc != NULL) {
		pango_font_description_free(o->font_desc);
		o->font_desc = NULL;
	}
	if (o->font_file != NULL) {
		memset(o->font_file, 0, strlen(o->font_file));
		free(o->font_file);
		o->font_file = NULL;
	}
	o->font_px = 0;
	o->font_id = 0;
}

static void tipsy_overlay_destroy_surface(struct tipsy_focused_overlay *o) {
	if (o == NULL) return;
	if (o->child != None && o->mapped) {
		XUnmapWindow(o->dpy, o->child);
		o->mapped = 0;
	}
	tipsy_overlay_close_font(o);
	if (o->cr != NULL) {
		cairo_destroy(o->cr);
		o->cr = NULL;
	}
	if (o->surface != NULL) {
		cairo_surface_destroy(o->surface);
		o->surface = NULL;
	}
	if (o->background != None) {
		tipsy_overlay_wipe_pixmap(o, o->background);
		XFreePixmap(o->dpy, o->background);
		o->background = None;
	}
	if (o->backing != None) {
		tipsy_overlay_wipe_pixmap(o, o->backing);
		XFreePixmap(o->dpy, o->backing);
		o->backing = None;
	}
	if (o->copy_gc != NULL) {
		XFreeGC(o->dpy, o->copy_gc);
		o->copy_gc = NULL;
	}
	if (o->child != None) {
		XDestroyWindow(o->dpy, o->child);
		o->child = None;
	}
	o->width = 0;
	o->height = 0;
	o->background_refresh_pending = 0;
}

static int tipsy_overlay_empty_input_shape(struct tipsy_focused_overlay *o) {
	int shape_event = 0, shape_error = 0;
	if (!XShapeQueryExtension(o->dpy, &shape_event, &shape_error)) return -1;
	Pixmap empty = XCreatePixmap(o->dpy, o->parent, 1, 1, 1);
	if (empty == None) return -1;
	GC gc = XCreateGC(o->dpy, empty, 0, NULL);
	if (gc == NULL) {
		XFreePixmap(o->dpy, empty);
		return -1;
	}
	XSetForeground(o->dpy, gc, 0);
	XFillRectangle(o->dpy, empty, gc, 0, 0, 1, 1);
	XShapeCombineMask(o->dpy, o->child, ShapeInput, 0, 0, empty, ShapeSet);
	XFreeGC(o->dpy, gc);
	XFreePixmap(o->dpy, empty);
	return 0;
}

static int tipsy_overlay_capture_visible_background(
	struct tipsy_focused_overlay *o) {
	if (o == NULL || o->background == None || o->copy_gc == NULL) return -1;
	Window root = RootWindow(o->dpy, o->screen);
	Window child = None;
	int root_x = 0, root_y = 0;
	// A Vulkan/EGL window's own drawable can be stale beneath visible child or
	// redirected content. Translate the official view bounds to the root and
	// copy the pixels the user can actually see while our child is unmapped.
	if (!XTranslateCoordinates(o->dpy, o->parent, root, o->x, o->y,
		&root_x, &root_y, &child)) return -1;
	XWindowAttributes root_attrs;
	if (!XGetWindowAttributes(o->dpy, root, &root_attrs)) return -1;
	if (root_x < 0 || root_y < 0 ||
		root_x + o->width > root_attrs.width ||
		root_y + o->height > root_attrs.height) return -1;
	XSetSubwindowMode(o->dpy, o->copy_gc, IncludeInferiors);
	XCopyArea(o->dpy, root, o->background, o->copy_gc,
		root_x, root_y, (unsigned)o->width, (unsigned)o->height, 0, 0);
	XSetSubwindowMode(o->dpy, o->copy_gc, ClipByChildren);
	return 0;
}

static int tipsy_overlay_create_surface(struct tipsy_focused_overlay *o,
	int x, int y, int width, int height, int parent_width, int parent_height) {
	if (o == NULL || o->dpy == NULL || o->parent == None ||
		width <= 0 || height <= 0) return -1;
	tipsy_overlay_destroy_surface(o);
	o->screen = DefaultScreen(o->dpy);
	o->visual = DefaultVisual(o->dpy, o->screen);
	o->colormap = DefaultColormap(o->dpy, o->screen);
	o->depth = DefaultDepth(o->dpy, o->screen);
	o->x = x;
	o->y = y;
	o->width = width;
	o->height = height;
	o->parent_width = parent_width;
	o->parent_height = parent_height;
	o->background = XCreatePixmap(o->dpy, o->parent,
		(unsigned)width, (unsigned)height, (unsigned)o->depth);
	o->backing = XCreatePixmap(o->dpy, o->parent,
		(unsigned)width, (unsigned)height, (unsigned)o->depth);
	if (o->background == None || o->backing == None) goto fail;
	o->copy_gc = XCreateGC(o->dpy, o->parent, 0, NULL);
	if (o->copy_gc == NULL) goto fail;
	// The view is not mapped yet, so root readback cannot contain our previous
	// text. Read visible pixels rather than the potentially stale EGL drawable.
	XSync(o->dpy, False);
	if (tipsy_overlay_capture_visible_background(o) != 0) goto fail;
	o->background_refresh_pending = 1;
	XCopyArea(o->dpy, o->background, o->backing, o->copy_gc,
		0, 0, (unsigned)width, (unsigned)height, 0, 0);

	XSetWindowAttributes attrs;
	memset(&attrs, 0, sizeof(attrs));
	attrs.colormap = o->colormap;
	attrs.border_pixel = 0;
	attrs.background_pixmap = o->backing;
	attrs.event_mask = ExposureMask;
	attrs.do_not_propagate_mask = 0;
	o->child = XCreateWindow(o->dpy, o->parent, x, y,
		(unsigned)width, (unsigned)height, 0, o->depth, InputOutput,
		o->visual, CWColormap | CWBorderPixel | CWBackPixmap |
		CWDontPropagate | CWEventMask, &attrs);
	if (o->child == None || tipsy_overlay_empty_input_shape(o) != 0) goto fail;
	o->surface = cairo_xlib_surface_create(o->dpy, o->backing, o->visual,
		width, height);
	if (o->surface == NULL || cairo_surface_status(o->surface) != CAIRO_STATUS_SUCCESS)
		goto fail;
	o->cr = cairo_create(o->surface);
	if (o->cr == NULL || cairo_status(o->cr) != CAIRO_STATUS_SUCCESS) goto fail;
	return 0;

fail:
	tipsy_overlay_destroy_surface(o);
	return -1;
}

static int tipsy_overlay_refresh_visible_background(
	struct tipsy_focused_overlay *o, uint64_t version, int text_len) {
	if (o == NULL || !o->background_refresh_pending) return 0;
	// showKeyboard can precede the frame that paints the focused field. The
	// first subsequent nonempty text generation is the causal point at which
	// the focused view must already exist for the new glyph. Empty property
	// churn is deliberately ignored: it can still precede the focused-field
	// frame. No guessed delay controls correctness.
	if (text_len == 0 || version == o->version) return 0;
	if (o->mapped) {
		XUnmapWindow(o->dpy, o->child);
		o->mapped = 0;
	}
	XSync(o->dpy, False);
	if (tipsy_overlay_capture_visible_background(o) != 0) return -1;
	o->background_refresh_pending = 0;
	return 0;
}

static PangoFontDescription *tipsy_overlay_font_description(
	const char *file, double px, int font_id) {
	PangoFontDescription *desc = NULL;
	if (file != NULL && file[0] != 0) {
		FcConfig *config = FcConfigGetCurrent();
		if (config != NULL && FcConfigAppFontAddFile(config,
			(const FcChar8 *)file)) {
			PangoFontMap *map = pango_cairo_font_map_get_default();
			if (PANGO_IS_FC_FONT_MAP(map))
				pango_fc_font_map_config_changed(PANGO_FC_FONT_MAP(map));
		}
		int count = 0;
		FcPattern *pattern = FcFreeTypeQuery((const FcChar8 *)file,
			0, NULL, &count);
		if (pattern != NULL) {
			desc = pango_fc_font_description_from_pattern(pattern, FALSE);
			FcPatternDestroy(pattern);
		}
	}
	// A resolved APK file is authoritative. Silently substituting a host face
	// would reintroduce the legibility and metric drift this surface fixes.
	if (desc == NULL && file != NULL && file[0] != 0) return NULL;
	if (desc == NULL) {
		desc = pango_font_description_new();
		pango_font_description_set_family(desc, "Source Sans Pro");
		if (font_id == 4)
			pango_font_description_set_weight(desc, PANGO_WEIGHT_BOLD);
		else if (font_id == 5)
			pango_font_description_set_weight(desc, PANGO_WEIGHT_LIGHT);
		else
			pango_font_description_set_weight(desc, PANGO_WEIGHT_NORMAL);
	}
	if (desc != NULL)
		pango_font_description_set_absolute_size(desc, px * PANGO_SCALE);
	return desc;
}

static int tipsy_overlay_open_font(struct tipsy_focused_overlay *o, double px,
	int font_id, const char *font_file) {
	const char *requested = font_file == NULL ? "" : font_file;
	const char *current = o->font_file == NULL ? "" : o->font_file;
	if (o->font_desc != NULL && fabs(o->font_px - px) < 0.001 &&
		o->font_id == font_id && strcmp(current, requested) == 0) return 0;
	tipsy_overlay_close_font(o);
	o->font_desc = tipsy_overlay_font_description(requested, px, font_id);
	if (o->font_desc == NULL) return -1;
	o->font_file = strdup(requested);
	if (o->font_file == NULL) {
		tipsy_overlay_close_font(o);
		return -1;
	}
	o->font_px = px;
	o->font_id = font_id;
	return 0;
}

static unsigned long tipsy_overlay_channel(unsigned long pixel,
	unsigned long mask) {
	if (mask == 0) return 0;
	while ((mask & 1) == 0) {
		mask >>= 1;
		pixel >>= 1;
	}
	return (pixel & mask) * 255 / mask;
}

static unsigned long tipsy_overlay_solid_pixel(struct tipsy_focused_overlay *o,
	uint32_t argb) {
	unsigned long pixel = 0;
	unsigned long channels[3] = {
		(argb >> 16) & 0xff, (argb >> 8) & 0xff, argb & 0xff,
	};
	unsigned long masks[3] = {
		o->visual->red_mask, o->visual->green_mask, o->visual->blue_mask,
	};
	for (int i = 0; i < 3; ++i) {
		unsigned long mask = masks[i];
		if (mask == 0) continue;
		int shift = 0;
		while (((mask >> shift) & 1) == 0) shift++;
		unsigned long normalized = mask >> shift;
		pixel |= ((channels[i] * normalized + 127) / 255) << shift;
	}
	return pixel;
}

static int tipsy_overlay_pixel_diag_enabled(void) {
	static int ready;
	static int enabled;
	if (!ready) {
		const char *e = getenv("TIPSY_OVERLAY_PIXEL_DIAG");
		enabled = (e != NULL && e[0] == '1' && e[1] == '\0');
		ready = 1;
	}
	return enabled;
}

static int tipsy_overlay_measure_pixels(struct tipsy_focused_overlay *o,
	uint32_t argb) {
	XImage *bg = XGetImage(o->dpy, o->background, 0, 0,
		(unsigned)o->width, (unsigned)o->height, AllPlanes, ZPixmap);
	XImage *paint = XGetImage(o->dpy, o->backing, 0, 0,
		(unsigned)o->width, (unsigned)o->height, AllPlanes, ZPixmap);
	if (bg == NULL || paint == NULL) {
		if (bg != NULL) XDestroyImage(bg);
		if (paint != NULL) XDestroyImage(paint);
		return -1;
	}
	unsigned long solid = tipsy_overlay_solid_pixel(o, argb);
	o->glyph_pixels = 0;
	o->caret_pixels = 0;
	o->antialias_pixels = 0;
	o->bright_pixels = 0;
	o->background_pixels = 0;
	for (int y = 0; y < o->height; ++y) {
		for (int x = 0; x < o->width; ++x) {
			unsigned long before = XGetPixel(bg, x, y);
			unsigned long after = XGetPixel(paint, x, y);
			int caret = o->caret_valid && x == o->caret_x &&
				y >= o->caret_y && y < o->caret_y + o->caret_height;
			if (after == before) {
				o->background_pixels++;
				continue;
			}
			if (caret) {
				o->caret_pixels++;
				continue;
			}
			o->glyph_pixels++;
			if (after != solid) o->antialias_pixels++;
			unsigned long r = tipsy_overlay_channel(after, o->visual->red_mask);
			unsigned long g = tipsy_overlay_channel(after, o->visual->green_mask);
			unsigned long b = tipsy_overlay_channel(after, o->visual->blue_mask);
			if (r * 299 + g * 587 + b * 114 >= 192000) o->bright_pixels++;
		}
	}
	// Pixel images can encode glyph silhouettes. Wipe them immediately and
	// retain aggregate, content-free counts only.
	memset(bg->data, 0, (size_t)bg->bytes_per_line * (size_t)bg->height);
	memset(paint->data, 0, (size_t)paint->bytes_per_line * (size_t)paint->height);
	XDestroyImage(bg);
	XDestroyImage(paint);
	return 0;
}

static int tipsy_overlay_paint(struct tipsy_focused_overlay *o,
	const unsigned char *text, int text_len, int cursor_byte,
	uint32_t argb, int x_alignment, int y_alignment,
	int multiline, int wrapped, int editable, int cursor_visible,
	double letter_spacing, int pad_left, int pad_top,
	int pad_right, int pad_bottom, int include_font_padding) {
	int content_width = o->width - pad_left - pad_right;
	int content_height = o->height - pad_top - pad_bottom;
	if (content_width < 1 || content_height < 1) return -1;
	XCopyArea(o->dpy, o->background, o->backing, o->copy_gc,
		0, 0, (unsigned)o->width, (unsigned)o->height, 0, 0);
	cairo_surface_mark_dirty(o->surface);
	cairo_save(o->cr);
	cairo_reset_clip(o->cr);
	cairo_rectangle(o->cr, pad_left, pad_top, content_width, content_height);
	cairo_clip(o->cr);

	PangoLayout *layout = pango_cairo_create_layout(o->cr);
	if (layout == NULL) {
		cairo_restore(o->cr);
		return -1;
	}
	pango_layout_set_font_description(layout, o->font_desc);
	pango_layout_set_text(layout, (const char *)text, text_len);
	int multi = multiline || wrapped;
	if (multi) {
		pango_layout_set_width(layout, content_width * PANGO_SCALE);
		pango_layout_set_wrap(layout, PANGO_WRAP_WORD_CHAR);
		pango_layout_set_alignment(layout,
			x_alignment == 1 ? PANGO_ALIGN_RIGHT :
			(x_alignment == 2 ? PANGO_ALIGN_CENTER : PANGO_ALIGN_LEFT));
	} else {
		pango_layout_set_width(layout, -1);
		pango_layout_set_single_paragraph_mode(layout, TRUE);
	}
	if (fabs(letter_spacing) > 0.00001 && text_len > 0) {
		PangoAttrList *attrs = pango_attr_list_new();
		PangoAttribute *tracking = pango_attr_letter_spacing_new(
			(int)lrint(letter_spacing * o->font_px * PANGO_SCALE));
		tracking->start_index = 0;
		tracking->end_index = (guint)text_len;
		pango_attr_list_insert(attrs, tracking);
		pango_layout_set_attributes(layout, attrs);
		pango_attr_list_unref(attrs);
	}
	PangoRectangle ink, logical;
	pango_layout_get_pixel_extents(layout, &ink, &logical);
	PangoRectangle box = include_font_padding ? logical : ink;
	if (box.height < 1) box.height = (int)ceil(o->font_px);
	int box_top = pad_top;
	if (y_alignment == 1)
		box_top = pad_top + (content_height - box.height) / 2;
	else if (y_alignment == 2)
		box_top = o->height - pad_bottom - box.height;
	int origin_y = box_top - box.y;
	int origin_x = pad_left - box.x;
	if (!multi) {
		if (x_alignment == 1)
			origin_x = o->width - pad_right - box.width - box.x;
		else if (x_alignment == 2)
			origin_x = pad_left + (content_width - box.width) / 2 - box.x;
		PangoRectangle strong, weak;
		pango_layout_get_cursor_pos(layout, cursor_byte, &strong, &weak);
		int cursor_x = origin_x + PANGO_PIXELS(strong.x);
		if (cursor_x > o->width - pad_right)
			origin_x -= cursor_x - (o->width - pad_right);
		if (cursor_x < pad_left) origin_x += pad_left - cursor_x;
	}

	double a = ((argb >> 24) & 0xff) / 255.0;
	double r = ((argb >> 16) & 0xff) / 255.0;
	double g = ((argb >> 8) & 0xff) / 255.0;
	double b = (argb & 0xff) / 255.0;
	cairo_set_source_rgba(o->cr, r, g, b, a);
	cairo_move_to(o->cr, origin_x, origin_y);
	pango_cairo_show_layout(o->cr, layout);
	o->caret_valid = 0;
	if (editable && cursor_visible) {
		PangoRectangle strong, weak;
		pango_layout_get_cursor_pos(layout, cursor_byte, &strong, &weak);
		o->caret_valid = 1;
		o->caret_x = origin_x + PANGO_PIXELS(strong.x);
		o->caret_y = origin_y + PANGO_PIXELS(strong.y);
		o->caret_height = PANGO_PIXELS(strong.height);
		if (o->caret_height < 1) o->caret_height = box.height;
		cairo_rectangle(o->cr, o->caret_x, o->caret_y, 1,
			o->caret_height);
		cairo_fill(o->cr);
	}
	o->text_origin_x = origin_x;
	o->line_box_top = box_top;
	o->line_box_height = box.height;
	o->baseline_y = origin_y + PANGO_PIXELS(pango_layout_get_baseline(layout));
	g_object_unref(layout);
	cairo_restore(o->cr);
	cairo_surface_flush(o->surface);
	o->requested_argb = argb;
	// Full-field XGetImage + pixel scan is diagnostic-only. Default off so
	// ordinary focused-text paints stay composition-only (~4 Hz while a
	// textbox is focused). Enable with TIPSY_OVERLAY_PIXEL_DIAG=1.
	if (tipsy_overlay_pixel_diag_enabled() &&
		tipsy_overlay_measure_pixels(o, argb) != 0) return -1;
	XSetWindowBackgroundPixmap(o->dpy, o->child, o->backing);
	if (!o->mapped) {
		XMapRaised(o->dpy, o->child);
		o->mapped = 1;
	} else {
		XRaiseWindow(o->dpy, o->child);
	}
	// The same completed backing repaints Expose without retaining the text.
	XClearWindow(o->dpy, o->child);
	return 0;
}

uintptr_t tipsy_focused_overlay_new(uintptr_t dpy_ptr, unsigned long parent) {
	if (dpy_ptr == 0 || parent == 0) return 0;
	struct tipsy_focused_overlay *o = calloc(1, sizeof(*o));
	if (o == NULL) return 0;
	o->dpy = (Display *)dpy_ptr;
	o->parent = (Window)parent;
	return (uintptr_t)o;
}

int tipsy_focused_overlay_update(uintptr_t ptr, int visible,
	uint64_t version, int parent_width, int parent_height,
	int x, int y, int width, int height, double font_px, int font_id,
	const char *font_file, uint32_t argb, double letter_spacing,
	int pad_left, int pad_top, int pad_right, int pad_bottom,
	int include_font_padding, int x_alignment, int y_alignment,
	int multiline, int wrapped, int editable, int cursor_visible,
	const unsigned char *text, int text_len, int cursor_byte) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || o->dpy == NULL) return -1;
	if (!visible) {
		tipsy_overlay_destroy_surface(o);
		o->requested_argb = 0;
		o->glyph_pixels = o->caret_pixels = o->antialias_pixels = 0;
		o->bright_pixels = o->background_pixels = 0;
		o->version = 0;
		XFlush(o->dpy);
		return 0;
	}
	if (width <= 0 || height <= 0 || font_px <= 0 || text == NULL ||
		text_len < 0 || cursor_byte < 0 || cursor_byte > text_len ||
		pad_left < 0 || pad_top < 0 || pad_right < 0 || pad_bottom < 0)
		return -1;
	int recreate = o->child == None || o->x != x || o->y != y ||
		o->width != width || o->height != height ||
		o->parent_width != parent_width || o->parent_height != parent_height;
	if (recreate && tipsy_overlay_create_surface(o, x, y, width, height,
		parent_width, parent_height) != 0) return -1;
	if (tipsy_overlay_open_font(o, font_px, font_id, font_file) != 0) return -1;
	if (recreate) {
		// Never show the speculative pre-focus snapshot. A property/text/layout
		// generation after show is the proof that the real field frame exists.
		o->version = version;
		XFlush(o->dpy);
		return 0;
	}
	if (tipsy_overlay_refresh_visible_background(o, version, text_len) != 0)
		return -1;
	if (o->background_refresh_pending) {
		o->version = version;
		XFlush(o->dpy);
		return 0;
	}
	o->version = version;
	if (tipsy_overlay_paint(o, text, text_len, cursor_byte, argb,
		x_alignment, y_alignment, multiline, wrapped, editable,
		cursor_visible, letter_spacing, pad_left, pad_top, pad_right,
		pad_bottom, include_font_padding) != 0) return -1;
	XFlush(o->dpy);
	return 0;
}

void tipsy_focused_overlay_free(uintptr_t ptr) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL) return;
	tipsy_overlay_destroy_surface(o);
	XFlush(o->dpy);
	memset(o, 0, sizeof(*o));
	free(o);
}

int tipsy_focused_overlay_measure_for_test(uintptr_t ptr) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || o->dpy == NULL || o->child == None ||
		o->background == None || o->backing == None ||
		o->width <= 0 || o->height <= 0) return -1;
	return tipsy_overlay_measure_pixels(o, o->requested_argb);
}

int tipsy_focused_overlay_query(uintptr_t ptr, int *x, int *y,
	int *width, int *height, unsigned long *painted_pixels,
	int *background_preserved, uint32_t *requested_argb,
	unsigned long *glyph_pixels, unsigned long *caret_pixels,
	unsigned long *antialias_pixels, unsigned long *bright_pixels,
	unsigned long *background_pixels, int *text_origin_x,
	int *line_box_top, int *line_box_height, int *baseline_y,
	unsigned long *input_shape_pixels) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || o->dpy == NULL || o->child == None) return 0;
	XWindowAttributes attrs;
	if (!XGetWindowAttributes(o->dpy, o->child, &attrs)) return 0;
	if (x != NULL) *x = attrs.x;
	if (y != NULL) *y = attrs.y;
	if (width != NULL) *width = attrs.width;
	if (height != NULL) *height = attrs.height;
	if (requested_argb != NULL) *requested_argb = o->requested_argb;
	if (glyph_pixels != NULL) *glyph_pixels = o->glyph_pixels;
	if (caret_pixels != NULL) *caret_pixels = o->caret_pixels;
	if (antialias_pixels != NULL) *antialias_pixels = o->antialias_pixels;
	if (bright_pixels != NULL) *bright_pixels = o->bright_pixels;
	if (background_pixels != NULL) *background_pixels = o->background_pixels;
	if (painted_pixels != NULL) *painted_pixels = o->glyph_pixels + o->caret_pixels;
	if (background_preserved != NULL) *background_preserved = o->background_pixels > 0;
	if (text_origin_x != NULL) *text_origin_x = o->text_origin_x;
	if (line_box_top != NULL) *line_box_top = o->line_box_top;
	if (line_box_height != NULL) *line_box_height = o->line_box_height;
	if (baseline_y != NULL) *baseline_y = o->baseline_y;
	if (input_shape_pixels != NULL) {
		*input_shape_pixels = 0;
		int count = 0, ordering = 0;
		XRectangle *rects = XShapeGetRectangles(o->dpy, o->child,
			ShapeInput, &count, &ordering);
		if (rects != NULL) {
			for (int i = 0; i < count; ++i)
				*input_shape_pixels += (unsigned long)rects[i].width * rects[i].height;
			XFree(rects);
		}
	}
	return attrs.map_state == IsViewable ? 1 : 0;
}

static int tipsy_overlay_root_pixel(struct tipsy_focused_overlay *o,
	int parent_x, int parent_y, unsigned long *pixel) {
	if (o == NULL || o->dpy == NULL || o->parent == None || pixel == NULL) return -1;
	Window root = RootWindow(o->dpy, DefaultScreen(o->dpy));
	Window child = None;
	int root_x = 0, root_y = 0;
	if (!XTranslateCoordinates(o->dpy, o->parent, root,
		parent_x, parent_y, &root_x, &root_y, &child)) return -1;
	XImage *image = XGetImage(o->dpy, root, root_x, root_y, 1, 1,
		AllPlanes, ZPixmap);
	if (image == NULL) return -1;
	*pixel = XGetPixel(image, 0, 0);
	memset(image->data, 0, (size_t)image->bytes_per_line * image->height);
	XDestroyImage(image);
	return 0;
}

int tipsy_focused_overlay_test_fill_parent(uintptr_t ptr,
	int x, int y, int width, int height, unsigned long *pixel) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || width <= 0 || height <= 0) return -1;
	XColor color;
	memset(&color, 0, sizeof(color));
	color.red = 0x2345;
	color.green = 0x9876;
	color.blue = 0xcdef;
	color.flags = DoRed | DoGreen | DoBlue;
	if (!XAllocColor(o->dpy, DefaultColormap(o->dpy, DefaultScreen(o->dpy)),
		&color)) return -1;
	GC gc = XCreateGC(o->dpy, o->parent, 0, NULL);
	XSetForeground(o->dpy, gc, color.pixel);
	XFillRectangle(o->dpy, o->parent, gc, x, y,
		(unsigned)width, (unsigned)height);
	XFreeGC(o->dpy, gc);
	XSync(o->dpy, False);
	return tipsy_overlay_root_pixel(o, x + width - 1, y + height - 1, pixel);
}

int tipsy_focused_overlay_test_add_visible_underlay(uintptr_t ptr,
	int x, int y, int width, int height, unsigned long *pixel) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || width <= 0 || height <= 0 || pixel == NULL) return -1;
	XColor color;
	memset(&color, 0, sizeof(color));
	color.red = 0xc321;
	color.green = 0x6543;
	color.blue = 0x2345;
	color.flags = DoRed | DoGreen | DoBlue;
	if (!XAllocColor(o->dpy, DefaultColormap(o->dpy, o->screen), &color)) return -1;
	Window underlay = XCreateSimpleWindow(o->dpy, o->parent, x, y,
		(unsigned)width, (unsigned)height, 0, color.pixel, color.pixel);
	if (underlay == None) return -1;
	XMapWindow(o->dpy, underlay);
	XSync(o->dpy, False);
	return tipsy_overlay_root_pixel(o, x + width - 1, y + height - 1, pixel);
}

int tipsy_focused_overlay_test_change_visible_underlay(uintptr_t ptr,
	unsigned long *pixel) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || pixel == NULL) return -1;
	Window root = None, parent = None;
	Window *children = NULL;
	unsigned count = 0;
	if (!XQueryTree(o->dpy, o->parent, &root, &parent, &children, &count))
		return -1;
	Window underlay = None;
	for (unsigned i = 0; i < count; ++i) {
		if (children[i] != o->child) {
			underlay = children[i];
			break;
		}
	}
	if (children != NULL) XFree(children);
	if (underlay == None) return -1;
	XColor color;
	memset(&color, 0, sizeof(color));
	color.red = 0x3456;
	color.green = 0xcdef;
	color.blue = 0x789a;
	color.flags = DoRed | DoGreen | DoBlue;
	if (!XAllocColor(o->dpy, DefaultColormap(o->dpy, o->screen), &color)) return -1;
	XSetWindowBackground(o->dpy, underlay, color.pixel);
	XClearWindow(o->dpy, underlay);
	XSync(o->dpy, False);
	*pixel = color.pixel;
	return 0;
}

int tipsy_focused_overlay_test_root_pixel(uintptr_t ptr,
	int parent_x, int parent_y, unsigned long *pixel) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL) return -1;
	XSync(o->dpy, False);
	return tipsy_overlay_root_pixel(o, parent_x, parent_y, pixel);
}

int tipsy_focused_overlay_test_expose(uintptr_t ptr) {
	struct tipsy_focused_overlay *o = (struct tipsy_focused_overlay *)ptr;
	if (o == NULL || o->child == None || !o->mapped) return -1;
	XClearArea(o->dpy, o->child, 0, 0, 0, 0, True);
	XSync(o->dpy, False);
	return 0;
}

int tipsy_focused_overlay_test_window_open(int width, int height,
	uintptr_t *dpy_ptr, unsigned long *xid, unsigned long *focus_before) {
	if (width <= 0 || height <= 0 || dpy_ptr == NULL || xid == NULL) return -1;
	Display *dpy = XOpenDisplay(NULL);
	if (dpy == NULL) return -1;
	int screen = DefaultScreen(dpy);
	Window root = RootWindow(dpy, screen);
	if (focus_before != NULL) {
		Window focus = None;
		int revert = 0;
		XGetInputFocus(dpy, &focus, &revert);
		*focus_before = (unsigned long)focus;
	}
	XSetWindowAttributes attrs;
	memset(&attrs, 0, sizeof(attrs));
	attrs.override_redirect = True;
	attrs.background_pixel = BlackPixel(dpy, screen);
	attrs.border_pixel = 0;
	attrs.event_mask = 0;
	Window win = XCreateWindow(dpy, root, 8, 8,
		(unsigned)width, (unsigned)height, 0, CopyFromParent, InputOutput,
		CopyFromParent, CWOverrideRedirect | CWBackPixel | CWBorderPixel |
		CWEventMask, &attrs);
	if (win == None) {
		XCloseDisplay(dpy);
		return -1;
	}
	XMapWindow(dpy, win);
	XSync(dpy, False);
	*dpy_ptr = (uintptr_t)dpy;
	*xid = (unsigned long)win;
	return 0;
}

unsigned long tipsy_focused_overlay_test_focus(uintptr_t dpy_ptr) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL) return 0;
	Window focus = None;
	int revert = 0;
	XGetInputFocus(dpy, &focus, &revert);
	return (unsigned long)focus;
}

void tipsy_focused_overlay_test_window_close(uintptr_t dpy_ptr,
	unsigned long xid) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL) return;
	if (xid != 0) XDestroyWindow(dpy, (Window)xid);
	XCloseDisplay(dpy);
}
