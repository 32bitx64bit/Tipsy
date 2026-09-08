/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_X11PROBE_H
#define TIPSY_X11PROBE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

int probe_open(void);
void probe_close(void);
int probe_focus(unsigned long xid, int gained);
int probe_set_focus(unsigned long xid);
unsigned long probe_query_focus(void);
int probe_key(unsigned long xid, unsigned long keysym, int pressed, unsigned long timestamp);
int probe_detectable_repeat(uintptr_t display, int enabled);
int probe_repeat_rate(unsigned int delay, unsigned int interval);
int probe_real_key(unsigned long keysym, int pressed);
void probe_sync(void);
int probe_button(unsigned long xid, int x, int y, unsigned int button, int pressed);
int probe_motion(unsigned long xid, int x, int y, unsigned int state);
int probe_warp_pointer(unsigned long xid, int x, int y);
int probe_relative_motion(int dx, int dy);
int probe_query_pointer(unsigned long xid, int *out_x, int *out_y);
int probe_window_root_origin(unsigned long xid, int *out_x, int *out_y);
int probe_grab_pointer(unsigned long xid);
void probe_ungrab_pointer(void);
int probe_move(unsigned long xid, int x, int y);
int probe_randr_output_property(void);
int probe_resize(unsigned long xid, unsigned int width, unsigned int height);
int probe_window_size(unsigned long xid, int *out_width, int *out_height);
int probe_window_min_size(unsigned long xid, int *out_width, int *out_height);
int probe_wm_delete(unsigned long xid);
int probe_is_viewable(unsigned long xid);
int probe_get_utf8_title(unsigned long xid, char *out, int cap);
int probe_get_icon_info(unsigned long xid, unsigned long *out_width,
	unsigned long *out_height, unsigned long *out_items);
int probe_has_fullscreen(unsigned long xid);
int probe_window_manager_present(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_X11PROBE_H */
