/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_FOCUSED_OVERLAY_H
#define TIPSY_FOCUSED_OVERLAY_H

#include <stdint.h>

uintptr_t tipsy_focused_overlay_new(void);
int tipsy_focused_overlay_update(uintptr_t ptr, int visible, uint64_t version,
	int x, int y, int width, int height, double font_px, int font_id,
	const char *font_file, uint32_t argb, double letter_spacing,
	int pad_left, int pad_top, int pad_right, int pad_bottom,
	int include_font_padding, int x_alignment, int y_alignment,
	int multiline, int wrapped, int editable, int cursor_visible,
	const unsigned char *text, int text_len, int cursor_byte);
void tipsy_focused_overlay_free(uintptr_t ptr);
struct tipsy_focused_overlay_metrics {
	int x, y, width, height;
	uint32_t requested_argb;
	unsigned long glyph_pixels, caret_pixels;
	unsigned long antialias_pixels, bright_pixels;
	int text_origin_x, line_box_top, line_box_height, baseline_y;
};
int tipsy_focused_overlay_query(uintptr_t ptr,
	struct tipsy_focused_overlay_metrics *out);
int tipsy_focused_overlay_test_foreground_alpha(uintptr_t ptr, int x, int y,
	unsigned long *alpha);

#endif /* TIPSY_FOCUSED_OVERLAY_H */
