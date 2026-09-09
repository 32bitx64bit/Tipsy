/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_X11_H
#define TIPSY_X11_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Input event capture. kinds: 0 focus, 1 key, 2 pointer, 3 resize, 4 text.
 * focus: a = 1 gained / 0 lost.
 * key:   a = 1 pressed / 0 released, b = Android physical keycode (0 only
 *        for an unmapped KeySym), c = raw X11 core keycode. KeyPress and
 *        KeyRelease are retained for the direct Roblox physical-key route;
 *        this does not synthesize or log text.
 * pointer: a = 0 down / 1 up / 2 move. PointerMotionMask is selected, so
 *        moves arrive both with and without a pressed button. The direct
 *        Roblox mouse path needs both forms; it computes real deltas from
 *        this ordered stream.
 *        a = 3 is captured motion: x/y remain at the window-center grab
 *        anchor and dx/dy carry the real relative delta at float precision.
 * scroll: a = horizontal detents, b = vertical detents. Core X11 encodes
 *        wheel motion as Button4..7; only ButtonPress is one detent.
 * resize:  b = width, c = height. ConfigureNotify lives in this stream so
 *        the Android surface is resized before subsequent pointer events.
 * text: committed UTF-8 from X11's input method. It follows the originating
 *        physical KeyPress and is never logged. This preserves the host
 *        keyboard layout instead of reconstructing text from a keycode.
 *        UTF-8 lives in the parallel tipsy_input_text side ring, not in
 *        this slot, so pointer/key copies stay a small ABI. */
#define TIPSY_INPUT_FOCUS 0
#define TIPSY_INPUT_KEY 1
#define TIPSY_INPUT_POINTER 2
#define TIPSY_INPUT_SCROLL 3
#define TIPSY_INPUT_RESIZE 4
#define TIPSY_INPUT_TEXT 5
#define TIPSY_INPUT_CLOSE 6
#define TIPSY_INPUT_CAPTURE 7
#define TIPSY_INPUT_RING 256
#define TIPSY_INPUT_TEXT_BYTES 256

struct tipsy_input_ev {
	int kind;
	int a;
	long b;
	long c;
	float x;
	float y;
	float dx;
	float dy;
	int repeat_count;
	int text_len;
};

typedef struct {
	char name[128];
	int x;
	int y;
	int width;
	int height;
	int primary;
} tipsy_xrr_output;

uint64_t tipsy_x11_refresh_version(void);
void tipsy_x11_wake_ack(void);
void tipsy_nudge_pump(void);
int tipsy_x11_set_pointer_lock(uintptr_t dpy_ptr, unsigned long xid,
	int locked, int *out_x, int *out_y, int *out_status);
int tipsy_x11_input_drain(struct tipsy_input_ev *out, char *text_out, int max);
void tipsy_x11_input_test_clear(void);
void tipsy_x11_input_test_push(int kind, int a, long b, long c, float x, float y);
void tipsy_x11_input_test_push_text(const char *text, int len);
int tipsy_x11_input_test_text_slots_clean(void);
int tipsy_x11_input_ev_size(void);
int tipsy_x11_io_error(void);
int tipsy_x11_request_fullscreen(uintptr_t dpy_ptr, unsigned long win, int enabled);
int tipsy_x11_list_outputs(tipsy_xrr_output *out, int max);
int tipsy_x11_open(const char *title, int width, int height,
	int min_width, int min_height,
	int place_x, int place_y, int use_position,
	const unsigned long *icon, int icon_len,
	uintptr_t *out_dpy, unsigned long *out_xid, unsigned long *out_delete,
	int *out_randr_event_base);
unsigned long tipsy_x11_hide_cursor(uintptr_t dpy_ptr, unsigned long xid);
void tipsy_x11_restore_cursor(uintptr_t dpy_ptr, unsigned long xid, unsigned long cursor);
int tipsy_x11_pump(uintptr_t dpy_ptr, unsigned long xid, unsigned long wm_delete,
	int randr_event_base, int *inout_w, int *inout_h, int *out_closed);
uintptr_t tipsy_x11_pump_thread_start(uintptr_t dpy, unsigned long xid,
	unsigned long del, int randr_event_base, int w, int h);
void tipsy_x11_pump_thread_stop(uintptr_t ptr, int *out_w, int *out_h, int *out_closed);
void tipsy_x11_close(uintptr_t dpy_ptr, unsigned long xid);
int tipsy_x11_unmap(uintptr_t dpy_ptr, unsigned long xid);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_X11_H */
