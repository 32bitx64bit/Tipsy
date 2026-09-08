/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_FOCUSED_OVERLAY_H
#define TIPSY_FOCUSED_OVERLAY_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

uintptr_t tipsy_focused_overlay_new(uintptr_t dpy_ptr, unsigned long parent);
int tipsy_focused_overlay_update(uintptr_t ptr, int visible,
	uint64_t version, int parent_width, int parent_height,
	int x, int y, int width, int height, double font_px, int font_id,
	const char *font_file, uint32_t argb, double letter_spacing,
	int pad_left, int pad_top, int pad_right, int pad_bottom,
	int include_font_padding, int x_alignment, int y_alignment,
	int multiline, int wrapped, int editable, int cursor_visible,
	const unsigned char *text, int text_len, int cursor_byte);
void tipsy_focused_overlay_free(uintptr_t ptr);
int tipsy_focused_overlay_query(uintptr_t ptr, int *x, int *y,
	int *width, int *height, unsigned long *painted_pixels,
	int *background_preserved, uint32_t *requested_argb,
	unsigned long *glyph_pixels, unsigned long *caret_pixels,
	unsigned long *antialias_pixels, unsigned long *bright_pixels,
	unsigned long *background_pixels, int *text_origin_x,
	int *line_box_top, int *line_box_height, int *baseline_y,
	unsigned long *input_shape_pixels);
int tipsy_focused_overlay_test_fill_parent(uintptr_t ptr,
	int x, int y, int width, int height, unsigned long *pixel);
int tipsy_focused_overlay_test_add_visible_underlay(uintptr_t ptr,
	int x, int y, int width, int height, unsigned long *pixel);
int tipsy_focused_overlay_test_change_visible_underlay(uintptr_t ptr,
	unsigned long *pixel);
int tipsy_focused_overlay_test_root_pixel(uintptr_t ptr,
	int parent_x, int parent_y, unsigned long *pixel);
int tipsy_focused_overlay_test_expose(uintptr_t ptr);
int tipsy_focused_overlay_test_window_open(int width, int height,
	uintptr_t *dpy_ptr, unsigned long *xid, unsigned long *focus_before);
unsigned long tipsy_focused_overlay_test_focus(uintptr_t dpy_ptr);
void tipsy_focused_overlay_test_window_close(uintptr_t dpy_ptr,
	unsigned long xid);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_FOCUSED_OVERLAY_H */
