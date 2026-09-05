// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: x11 xrandr
#cgo LDFLAGS: -lX11 -pthread
#cgo CFLAGS: -D_GNU_SOURCE

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <X11/XKBlib.h>
#include <X11/extensions/Xrandr.h>
#include <errno.h>
#include <fcntl.h>
#include <locale.h>
#include <poll.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

extern void GoX11_Notify(void);

static int tipsy_x_error_code;
static int tipsy_x_io_error;
static int tipsy_x_inited;
static XIM tipsy_xim;
static XIC tipsy_xic;
static int tipsy_f11_down;
// X11 core keycodes are bytes. Preserve the first physical down across XKB
// repeat presses; -1 means focus loss already released the key to Android.
static struct { int down; int repeats; int android_code; } tipsy_keys[256];

// Input event capture. kinds: 0 focus, 1 key, 2 pointer, 3 resize, 4 text.
// focus: a = 1 gained / 0 lost.
// key:   a = 1 pressed / 0 released, b = Android physical keycode (0 only
//        for an unmapped KeySym), c = raw X11 core keycode. KeyPress and
//        KeyRelease are retained for the direct Roblox physical-key route;
//        this does not synthesize or log text.
// pointer: a = 0 down / 1 up / 2 move. PointerMotionMask is selected, so
//        moves arrive both with and without a pressed button. The direct
//        Roblox mouse path needs both forms; it computes real deltas from
//        this ordered stream.
//        a = 3 is captured motion: x/y remain at the stable grab anchor and
//        b/c carry real relative dx/dy. Adjacent captured moves sum deltas.
// scroll: a = horizontal detents, b = vertical detents. Core X11 encodes
//        wheel motion as Button4..7; only ButtonPress is one detent.
// resize:  b = width, c = height. ConfigureNotify lives in this stream so
//        the Android surface is resized before subsequent pointer events.
// text: committed UTF-8 from X11's input method. It follows the originating
//        physical KeyPress and is never logged. This preserves the host
//        keyboard layout instead of reconstructing text from a keycode.
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
	int repeat_count;
	int text_len;
	char text[TIPSY_INPUT_TEXT_BYTES];
};

static struct tipsy_input_ev tipsy_input_ring[TIPSY_INPUT_RING];
static int tipsy_input_head;
static int tipsy_input_tail;
static pthread_mutex_t tipsy_input_mu = PTHREAD_MUTEX_INITIALIZER;
// Serializes X event consumption (the sole XPending/XNextEvent reader while
// the background pump is running) with pointer-lock and other Xlib callers.
static pthread_mutex_t tipsy_event_mu = PTHREAD_MUTEX_INITIALIZER;
// Coalesced Go wakeup: one cgo notify while a token is already pending.
static atomic_int tipsy_go_wake_pending;
// Like the input ring, invalidation is process-wide: Tipsy owns one Roblox
// window. A second test window can only cause a conservative extra query.
static _Atomic uint64_t tipsy_refresh_version = 1;

uint64_t tipsy_x11_refresh_version(void) {
	return atomic_load_explicit(&tipsy_refresh_version, memory_order_relaxed);
}

// Write end of the background pump's wakeup pipe, or -1. Other Xlib
// callers nudge it so poll() cannot miss events already read into Xlib's
// queue as a side effect of a reply (grab, unmap, fullscreen).
static int tipsy_pump_nudge_fd = -1;
// A graphics query can nudge concurrently with pump teardown. Protect the
// descriptor through the nonblocking write and close to prevent writing to
// an unrelated descriptor if the OS reuses its number.
static pthread_mutex_t tipsy_pump_wake_mu = PTHREAD_MUTEX_INITIALIZER;

static void tipsy_wake_go(void) {
	if (atomic_exchange(&tipsy_go_wake_pending, 1) != 0) {
		return;
	}
	GoX11_Notify();
}

void tipsy_x11_wake_ack(void) {
	atomic_store(&tipsy_go_wake_pending, 0);
}

void tipsy_nudge_pump(void) {
	pthread_mutex_lock(&tipsy_pump_wake_mu);
	int fd = tipsy_pump_nudge_fd;
	if (fd >= 0) {
		char one = 1;
		(void)write(fd, &one, 1);
	}
	pthread_mutex_unlock(&tipsy_pump_wake_mu);
}

struct tipsy_pointer_capture {
	int active;
	int right_down;
	int have_last;
	int anchor_x;
	int anchor_y;
	int last_x;
	int last_y;
	int ignore_warps;
	int failed_status;
	Display *dpy;
	Window win;
};

static struct tipsy_pointer_capture tipsy_capture;

static void tipsy_input_push_repeat(int kind, int a, long b, long c, float x, float y, int repeats) {
	pthread_mutex_lock(&tipsy_input_mu);
	int was_empty = (tipsy_input_head == tipsy_input_tail);
	// Captured relative motion must sum every delta; replacing it would lose
	// camera travel when the engine drains more slowly than the X server.
	int last = (tipsy_input_head - 1 + TIPSY_INPUT_RING) % TIPSY_INPUT_RING;
	if (kind == TIPSY_INPUT_POINTER && a == 3 &&
		tipsy_input_head != tipsy_input_tail &&
		tipsy_input_ring[last].kind == TIPSY_INPUT_POINTER &&
		tipsy_input_ring[last].a == 3) {
		tipsy_input_ring[last].b += b;
		tipsy_input_ring[last].c += c;
		tipsy_input_ring[last].x = x;
		tipsy_input_ring[last].y = y;
		pthread_mutex_unlock(&tipsy_input_mu);
		return;
	}
	// Ordinary absolute motion coalesces to its newest real position.
	if (kind == TIPSY_INPUT_POINTER && a == 2 &&
		tipsy_input_head != tipsy_input_tail &&
		tipsy_input_ring[last].kind == TIPSY_INPUT_POINTER &&
		tipsy_input_ring[last].a == 2) {
		tipsy_input_ring[last].x = x;
		tipsy_input_ring[last].y = y;
		pthread_mutex_unlock(&tipsy_input_mu);
		return;
	}
	// Reparenting window managers may report the same final client geometry
	// more than once while processing one resize request. Keep one ordered
	// resize edge; the Android bridge is dimension-deduplicated as well.
	if (kind == TIPSY_INPUT_RESIZE &&
		tipsy_input_head != tipsy_input_tail &&
		tipsy_input_ring[last].kind == TIPSY_INPUT_RESIZE &&
		tipsy_input_ring[last].b == b && tipsy_input_ring[last].c == c) {
		pthread_mutex_unlock(&tipsy_input_mu);
		return;
	}
	int next = (tipsy_input_head + 1) % TIPSY_INPUT_RING;
	if (next == tipsy_input_tail) {
		// Ring full: drop the oldest event. Focus and key events are
		// small and rare; motion floods are coalesced above.
		tipsy_input_tail = (tipsy_input_tail + 1) % TIPSY_INPUT_RING;
	}
	tipsy_input_ring[tipsy_input_head].kind = kind;
	tipsy_input_ring[tipsy_input_head].a = a;
	tipsy_input_ring[tipsy_input_head].b = b;
	tipsy_input_ring[tipsy_input_head].c = c;
	tipsy_input_ring[tipsy_input_head].x = x;
	tipsy_input_ring[tipsy_input_head].y = y;
	tipsy_input_ring[tipsy_input_head].repeat_count = repeats;
	tipsy_input_ring[tipsy_input_head].text_len = 0;
	tipsy_input_head = next;
	pthread_mutex_unlock(&tipsy_input_mu);
	if (was_empty || kind == TIPSY_INPUT_CLOSE || kind == TIPSY_INPUT_RESIZE) {
		tipsy_wake_go();
	}
}

static void tipsy_input_push(int kind, int a, long b, long c, float x, float y) {
	tipsy_input_push_repeat(kind, a, b, c, x, y, 0);
}

static void tipsy_release_keys(void) {
	for (int code = 0; code < 256; ++code) {
		if (tipsy_keys[code].down == 1) {
			tipsy_input_push(TIPSY_INPUT_KEY, 0, tipsy_keys[code].android_code, code, 0, 0);
			tipsy_keys[code].down = -1;
			tipsy_keys[code].repeats = 0;
		}
	}
	tipsy_f11_down = 0;
}

static int tipsy_clamp_coord(int value, int extent) {
	if (extent <= 1) return 0;
	if (value < 0) return 0;
	if (value >= extent) return extent - 1;
	return value;
}

static void tipsy_capture_event(int action, int status) {
	tipsy_input_push(TIPSY_INPUT_CAPTURE, action, (long)status,
		(long)tipsy_capture.win, (float)tipsy_capture.anchor_x,
		(float)tipsy_capture.anchor_y);
}

// Recenter MotionNotify can land a pixel or two off the requested anchor
// (compositor rounding, scaled buffers). Matching only the exact coordinate
// lets that snap-back through as camera motion and cancels the look.
static int tipsy_is_recenter_motion(int x, int y) {
	int dx = x - tipsy_capture.anchor_x;
	int dy = y - tipsy_capture.anchor_y;
	if (dx < 0) dx = -dx;
	if (dy < 0) dy = -dy;
	return dx <= 2 && dy <= 2;
}

static void tipsy_warp_to_anchor(Display *dpy, Window win) {
	Window root = 0, child = 0;
	int root_x = 0, root_y = 0, x = 0, y = 0;
	unsigned int mask = 0;
	if (XQueryPointer(dpy, win, &root, &child, &root_x, &root_y,
		&x, &y, &mask) && x == tipsy_capture.anchor_x &&
		y == tipsy_capture.anchor_y) {
		tipsy_capture.last_x = tipsy_capture.anchor_x;
		tipsy_capture.last_y = tipsy_capture.anchor_y;
		return;
	}
	XWarpPointer(dpy, None, win, 0, 0, 0, 0,
		tipsy_capture.anchor_x, tipsy_capture.anchor_y);
	tipsy_capture.ignore_warps = 1;
	tipsy_capture.last_x = tipsy_capture.anchor_x;
	tipsy_capture.last_y = tipsy_capture.anchor_y;
}

// End an acquired grab exactly once. A focus/close cancellation also emits
// one secondary-button release when its physical release can no longer be
// trusted to arrive, preventing a stuck Roblox button state.
static int tipsy_pointer_unlock(Display *dpy, Window win, int notify,
	int cancel_right_button) {
	if (!tipsy_capture.active || tipsy_capture.dpy != dpy ||
		tipsy_capture.win != win) {
		if (cancel_right_button) tipsy_capture.right_down = 0;
		return 0;
	}
	if (cancel_right_button && tipsy_capture.right_down) {
		tipsy_input_push(TIPSY_INPUT_POINTER, 1, Button3, 0,
			(float)tipsy_capture.anchor_x, (float)tipsy_capture.anchor_y);
		tipsy_capture.right_down = 0;
	}
	// This warp precedes the ungrab on the same X connection, leaving the
	// desktop pointer at the stable anchor after an ordinary RMB release.
	tipsy_warp_to_anchor(dpy, win);
	// A recenter queued while capture was active may not be read until after
	// this release. It is no longer safe to suppress an anchor-coordinate
	// MotionNotify once the grab has ended: that same coordinate can be the
	// user's first or second real desktop movement. Any leftover recenter event
	// is harmless as a normal zero-delta absolute move, whereas retaining the
	// token silently drops a real move and desynchronizes the next delta.
	tipsy_capture.ignore_warps = 0;
	XUngrabPointer(dpy, CurrentTime);
	tipsy_capture.active = 0;
	if (notify) tipsy_capture_event(0, GrabSuccess);
	return 1;
}

// Apply the state read from the APK-proven native getter. Return 1 for
// acquired, 2 for released, 0 for unchanged, -1 for grab rejection, and -2
// for a closed display.
static int tipsy_x11_set_pointer_lock(uintptr_t dpy_ptr, unsigned long xid,
	int locked, int *out_x, int *out_y, int *out_status) {
	Display *dpy = (Display *)dpy_ptr;
	Window win = (Window)xid;
	if (dpy == NULL || win == 0 || tipsy_x_io_error) return -2;
	pthread_mutex_lock(&tipsy_event_mu);
	int result = 0;
	if (!locked) {
		tipsy_capture.failed_status = 0;
		result = tipsy_pointer_unlock(dpy, win, 1, 0) ? 2 : 0;
		goto done;
	}
	if (tipsy_capture.active) goto done;
	if (tipsy_capture.failed_status != 0) goto done;
	tipsy_capture.dpy = dpy;
	tipsy_capture.win = win;

	XWindowAttributes attr;
	if (XGetWindowAttributes(dpy, win, &attr) == 0 || attr.map_state != IsViewable) {
		tipsy_capture.failed_status = GrabNotViewable + 1;
		if (out_status != NULL) *out_status = GrabNotViewable;
		tipsy_capture_event(2, GrabNotViewable);
		result = -1;
		goto done;
	}
	int anchor_x = tipsy_capture.right_down ? tipsy_capture.anchor_x : tipsy_capture.last_x;
	int anchor_y = tipsy_capture.right_down ? tipsy_capture.anchor_y : tipsy_capture.last_y;
	if (!tipsy_capture.have_last) {
		anchor_x = attr.width / 2;
		anchor_y = attr.height / 2;
	}
	anchor_x = tipsy_clamp_coord(anchor_x, attr.width);
	anchor_y = tipsy_clamp_coord(anchor_y, attr.height);
	int status = XGrabPointer(dpy, win, False,
		ButtonPressMask | ButtonReleaseMask | PointerMotionMask,
		GrabModeAsync, GrabModeAsync, win, None, CurrentTime);
	// XGrabPointer replaces an implicit or explicit grab already owned by this
	// X client. AlreadyGrabbed therefore remains an honest competing-client
	// failure and must not be treated as capture success.
	if (status != GrabSuccess) {
		tipsy_capture.failed_status = status + 1;
		if (out_status != NULL) *out_status = status;
		tipsy_capture_event(2, status);
		result = -1;
		goto done;
	}
	tipsy_capture.active = 1;
	tipsy_capture.dpy = dpy;
	tipsy_capture.win = win;
	tipsy_capture.anchor_x = anchor_x;
	tipsy_capture.anchor_y = anchor_y;
	tipsy_capture.last_x = anchor_x;
	tipsy_capture.last_y = anchor_y;
	tipsy_capture.have_last = 1;
	tipsy_warp_to_anchor(dpy, win);
	tipsy_capture_event(1, GrabSuccess);
	result = 1;

done:
	if (out_x != NULL) *out_x = tipsy_capture.anchor_x;
	if (out_y != NULL) *out_y = tipsy_capture.anchor_y;
	pthread_mutex_unlock(&tipsy_event_mu);
	XFlush(dpy);
	tipsy_nudge_pump();
	return result;
}

static void tipsy_input_push_text(const char *text, int len) {
	if (text == NULL || len <= 0) {
		return;
	}
	if (len > TIPSY_INPUT_TEXT_BYTES) {
		len = TIPSY_INPUT_TEXT_BYTES;
	}
	pthread_mutex_lock(&tipsy_input_mu);
	int was_empty = (tipsy_input_head == tipsy_input_tail);
	int next = (tipsy_input_head + 1) % TIPSY_INPUT_RING;
	if (next == tipsy_input_tail) {
		tipsy_input_tail = (tipsy_input_tail + 1) % TIPSY_INPUT_RING;
	}
	struct tipsy_input_ev *slot = &tipsy_input_ring[tipsy_input_head];
	memset(slot, 0, sizeof(*slot));
	slot->kind = TIPSY_INPUT_TEXT;
	memcpy(slot->text, text, (size_t)len);
	slot->text_len = len;
	tipsy_input_head = next;
	pthread_mutex_unlock(&tipsy_input_mu);
	if (was_empty) {
		tipsy_wake_go();
	}
}

int tipsy_x11_input_drain(struct tipsy_input_ev *out, int max) {
	if (out == NULL || max <= 0) {
		return 0;
	}
	pthread_mutex_lock(&tipsy_input_mu);
	int n = 0;
	while (tipsy_input_tail != tipsy_input_head && n < max) {
		out[n] = tipsy_input_ring[tipsy_input_tail];
		tipsy_input_tail = (tipsy_input_tail + 1) % TIPSY_INPUT_RING;
		n++;
	}
	pthread_mutex_unlock(&tipsy_input_mu);
	return n;
}

// tipsy_android_keycode maps the physical keys the desktop client can
// honestly observe to public Android KeyEvent constants. This is a
// physical-key bridge only: it never creates text, unicode characters, or
// IME input. XLookupKeysym level zero provides the unshifted key identity,
// so Shift+A remains Android KEYCODE_A with a distinct real Shift edge.
static long tipsy_android_keycode(KeySym ks) {
	if (ks >= XK_a && ks <= XK_z) return 29 + (ks - XK_a); // KEYCODE_A..Z
	if (ks >= XK_A && ks <= XK_Z) return 29 + (ks - XK_A);
	if (ks >= XK_0 && ks <= XK_9) return 7 + (ks - XK_0); // KEYCODE_0..9
	if (ks >= XK_KP_0 && ks <= XK_KP_9) return 144 + (ks - XK_KP_0);
	switch (ks) {
	case XK_BackSpace: return 67;
	case XK_Tab: return 61;
	case XK_Return: return 66;
	case XK_Escape: return 111;
	case XK_space: return 62;
	case XK_comma: return 55;
	case XK_period: return 56;
	case XK_grave: return 68;
	case XK_minus: return 69;
	case XK_equal: return 70;
	case XK_bracketleft: return 71;
	case XK_bracketright: return 72;
	case XK_backslash: return 73;
	case XK_semicolon: return 74;
	case XK_apostrophe: return 75;
	case XK_slash: return 76;
	case XK_at: return 77;
	case XK_plus: return 81;
	case XK_Shift_L: return 59;
	case XK_Shift_R: return 60;
	case XK_Control_L: return 129;
	case XK_Control_R: return 130;
	case XK_Alt_L: return 57;
	case XK_Alt_R: return 58;
	case XK_ISO_Level3_Shift: return 58;
	case XK_Caps_Lock: return 115;
	case XK_Num_Lock: return 143;
	case XK_Scroll_Lock: return 116;
	case XK_Left: return 21;
	case XK_Up: return 19;
	case XK_Right: return 22;
	case XK_Down: return 20;
	case XK_Page_Up: return 92;
	case XK_Page_Down: return 93;
	case XK_Home: return 122;
	case XK_End: return 123;
	case XK_Insert: return 124;
	case XK_Delete: return 112;
	case XK_Menu: return 82;
	case XK_Print: return 120;
	case XK_Pause: return 121;
	case XK_KP_Divide: return 154;
	case XK_KP_Multiply: return 155;
	case XK_KP_Subtract: return 156;
	case XK_KP_Add: return 157;
	case XK_KP_Decimal: return 158;
	case XK_KP_Separator: return 159;
	case XK_KP_Enter: return 160;
	case XK_KP_Equal: return 161;
	case XK_F1: return 131;
	case XK_F2: return 132;
	case XK_F3: return 133;
	case XK_F4: return 134;
	case XK_F5: return 135;
	case XK_F6: return 136;
	case XK_F7: return 137;
	case XK_F8: return 138;
	case XK_F9: return 139;
	case XK_F10: return 140;
	case XK_F11: return 141;
	case XK_F12: return 142;
	default: return 0;
	}
}

static int tipsy_xerr(Display *dpy, XErrorEvent *ev) {
	(void)dpy;
	tipsy_x_error_code = ev->error_code;
	return 0;
}

static int tipsy_xioerr(Display *dpy) {
	(void)dpy;
	tipsy_x_io_error = 1;
	return 0;
}

static void tipsy_xioexit(Display *dpy, void *data) {
	(void)dpy;
	(void)data;
	tipsy_x_io_error = 1;
}

static void tipsy_x11_once(void) {
	if (tipsy_x_inited) {
		return;
	}
	// Let Xlib select the host locale/input method. Failure is harmless:
	// the physical-key path remains live and text lookup falls back to
	// XLookupString below.
	setlocale(LC_CTYPE, "");
	XSetLocaleModifiers("");
	XInitThreads();
	XSetErrorHandler(tipsy_xerr);
	XSetIOErrorHandler(tipsy_xioerr);
	tipsy_x_inited = 1;
}

int tipsy_x11_io_error(void) {
	return tipsy_x_io_error;
}

static void tipsy_x11_set_title(Display *dpy, Window win, const char *title) {
	if (title == NULL) {
		title = "";
	}
	// ICCCM properties keep older window managers working. EWMH UTF-8
	// properties are what modern desktops use for the title bar/task switcher.
	XStoreName(dpy, win, title);
	XSetIconName(dpy, win, title);
	Atom utf8 = XInternAtom(dpy, "UTF8_STRING", False);
	Atom net_name = XInternAtom(dpy, "_NET_WM_NAME", False);
	Atom net_icon_name = XInternAtom(dpy, "_NET_WM_ICON_NAME", False);
	int len = (int)strlen(title);
	XChangeProperty(dpy, win, net_name, utf8, 8, PropModeReplace,
		(const unsigned char *)title, len);
	XChangeProperty(dpy, win, net_icon_name, utf8, 8, PropModeReplace,
		(const unsigned char *)title, len);
}

static void tipsy_x11_set_icon(Display *dpy, Window win,
	const unsigned long *icon, int icon_len) {
	if (icon == NULL || icon_len < 3) {
		return;
	}
	Atom net_icon = XInternAtom(dpy, "_NET_WM_ICON", False);
	// Xlib requires native unsigned longs for format=32 on LP64 even though
	// each property item contains exactly 32 significant bits.
	XChangeProperty(dpy, win, net_icon, XA_CARDINAL, 32, PropModeReplace,
		(const unsigned char *)icon, icon_len);
}

#define TIPSY_NET_WM_STATE_REMOVE 0
#define TIPSY_NET_WM_STATE_ADD 1

static int tipsy_x11_has_fullscreen(Display *dpy, Window win) {
	Atom state = XInternAtom(dpy, "_NET_WM_STATE", False);
	Atom fullscreen = XInternAtom(dpy, "_NET_WM_STATE_FULLSCREEN", False);
	Atom actual = None;
	int format = 0;
	unsigned long count = 0;
	unsigned long remaining = 0;
	unsigned char *raw = NULL;
	int found = 0;
	if (XGetWindowProperty(dpy, win, state, 0, 1024, False, XA_ATOM,
		&actual, &format, &count, &remaining, &raw) == Success &&
		actual == XA_ATOM && format == 32 && raw != NULL) {
		Atom *atoms = (Atom *)raw;
		for (unsigned long i = 0; i < count; i++) {
			if (atoms[i] == fullscreen) {
				found = 1;
				break;
			}
		}
	}
	if (raw != NULL) {
		XFree(raw);
	}
	return found;
}

static int tipsy_x11_request_fullscreen(uintptr_t dpy_ptr, Window win, int enabled) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || win == 0 || tipsy_x_io_error) {
		return -1;
	}
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xclient.type = ClientMessage;
	ev.xclient.display = dpy;
	ev.xclient.window = win;
	ev.xclient.message_type = XInternAtom(dpy, "_NET_WM_STATE", False);
	ev.xclient.format = 32;
	ev.xclient.data.l[0] = enabled ? TIPSY_NET_WM_STATE_ADD : TIPSY_NET_WM_STATE_REMOVE;
	ev.xclient.data.l[1] = (long)XInternAtom(dpy, "_NET_WM_STATE_FULLSCREEN", False);
	ev.xclient.data.l[2] = 0;
	ev.xclient.data.l[3] = 1; // EWMH source indication: normal application.
	ev.xclient.data.l[4] = 0;
	Window root = RootWindow(dpy, DefaultScreen(dpy));
	if (XSendEvent(dpy, root, False,
		SubstructureRedirectMask | SubstructureNotifyMask, &ev) == 0) {
		return -2;
	}
	XFlush(dpy);
	tipsy_nudge_pump();
	return 0;
}

static void tipsy_x11_place_mapped(Display *dpy, Window win, int x, int y) {
	XMoveWindow(dpy, win, x, y);
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xclient.type = ClientMessage;
	ev.xclient.display = dpy;
	ev.xclient.window = win;
	ev.xclient.message_type = XInternAtom(dpy, "_NET_MOVERESIZE_WINDOW", False);
	ev.xclient.format = 32;
	// NorthWest gravity, x and y present, application source.
	ev.xclient.data.l[0] = NorthWestGravity | (1 << 8) | (1 << 9) | (1 << 12);
	ev.xclient.data.l[1] = x;
	ev.xclient.data.l[2] = y;
	ev.xclient.data.l[3] = 0;
	ev.xclient.data.l[4] = 0;
	XSendEvent(dpy, DefaultRootWindow(dpy), False,
		SubstructureRedirectMask | SubstructureNotifyMask, &ev);
	XFlush(dpy);
}

static int tipsy_x11_toggle_fullscreen(Display *dpy, Window win) {
	return tipsy_x11_request_fullscreen((uintptr_t)dpy, win,
		!tipsy_x11_has_fullscreen(dpy, win));
}

typedef struct {
	char name[128];
	int x;
	int y;
	int width;
	int height;
	int primary;
} tipsy_xrr_output;

int tipsy_x11_list_outputs(tipsy_xrr_output *out, int max) {
	if (out == NULL || max <= 0) {
		return -1;
	}
	Display *dpy = XOpenDisplay(NULL);
	if (dpy == NULL) {
		return -1;
	}
	Window root = DefaultRootWindow(dpy);
	int count = 0;
	int event_base = 0, error_base = 0;
	if (!XRRQueryExtension(dpy, &event_base, &error_base)) {
		Screen *scr = DefaultScreenOfDisplay(dpy);
		snprintf(out[0].name, sizeof(out[0].name), "screen");
		out[0].x = 0;
		out[0].y = 0;
		out[0].width = WidthOfScreen(scr);
		out[0].height = HeightOfScreen(scr);
		out[0].primary = 1;
		XCloseDisplay(dpy);
		return 1;
	}
	XRRScreenResources *res = XRRGetScreenResourcesCurrent(dpy, root);
	if (res == NULL) {
		XCloseDisplay(dpy);
		return 0;
	}
	RROutput primary = XRRGetOutputPrimary(dpy, root);
	for (int i = 0; i < res->noutput && count < max; i++) {
		XRROutputInfo *oi = XRRGetOutputInfo(dpy, res, res->outputs[i]);
		if (oi == NULL) {
			continue;
		}
		if (oi->connection != RR_Connected || oi->crtc == None) {
			XRRFreeOutputInfo(oi);
			continue;
		}
		XRRCrtcInfo *ci = XRRGetCrtcInfo(dpy, res, oi->crtc);
		if (ci == NULL || ci->mode == None || ci->width == 0 || ci->height == 0) {
			if (ci != NULL) {
				XRRFreeCrtcInfo(ci);
			}
			XRRFreeOutputInfo(oi);
			continue;
		}
		const char *name = oi->name != NULL ? oi->name : "output";
		snprintf(out[count].name, sizeof(out[count].name), "%s", name);
		out[count].x = ci->x;
		out[count].y = ci->y;
		out[count].width = (int)ci->width;
		out[count].height = (int)ci->height;
		out[count].primary = res->outputs[i] == primary ? 1 : 0;
		count++;
		XRRFreeCrtcInfo(ci);
		XRRFreeOutputInfo(oi);
	}
	XRRFreeScreenResources(res);
	if (count == 0) {
		Screen *scr = DefaultScreenOfDisplay(dpy);
		snprintf(out[0].name, sizeof(out[0].name), "screen");
		out[0].x = 0;
		out[0].y = 0;
		out[0].width = WidthOfScreen(scr);
		out[0].height = HeightOfScreen(scr);
		out[0].primary = 1;
		count = 1;
	}
	XCloseDisplay(dpy);
	return count;
}

int tipsy_x11_open(const char *title, int width, int height,
	int place_x, int place_y, int use_position,
	const unsigned long *icon, int icon_len,
	uintptr_t *out_dpy, unsigned long *out_xid, unsigned long *out_delete,
	int *out_randr_event_base) {
	tipsy_x11_once();
	tipsy_x_error_code = 0;
	tipsy_x_io_error = 0;
	tipsy_f11_down = 0;
	memset(tipsy_keys, 0, sizeof(tipsy_keys));
	memset(&tipsy_capture, 0, sizeof(tipsy_capture));

	Display *dpy = XOpenDisplay(NULL);
	if (dpy == NULL) {
		return -1;
	}
	XSetIOErrorExitHandler(dpy, tipsy_xioexit, NULL);
	// Per-client XKB option: a held key produces repeated KeyPress events
	// and one physical KeyRelease. Servers without it use the pair fallback
	// in the pump; this does not change the desktop's autorepeat settings.
	Bool repeat_supported = False;
	XkbSetDetectableAutoRepeat(dpy, True, &repeat_supported);

	int screen = DefaultScreen(dpy);
	Window root = RootWindow(dpy, screen);
	unsigned long black = BlackPixel(dpy, screen);
	*out_randr_event_base = 0;
	int randr_error_base = 0;
	if (XRRQueryExtension(dpy, out_randr_event_base, &randr_error_base)) {
		// Root events cover mode/rate changes, CRTC reassignment, hotplug,
		// and output properties; ConfigureNotify below covers window moves.
		int mask = RRScreenChangeNotifyMask | RRCrtcChangeNotifyMask |
			RROutputChangeNotifyMask | RROutputPropertyNotifyMask;
#ifdef RRResourceChangeNotifyMask
		mask |= RRResourceChangeNotifyMask;
#endif
		XRRSelectInput(dpy, root, mask);
	}
	atomic_fetch_add_explicit(&tipsy_refresh_version, 1, memory_order_relaxed);

	XSetWindowAttributes swa;
	memset(&swa, 0, sizeof(swa));
	swa.background_pixel = black;
	swa.border_pixel = black;
	swa.colormap = DefaultColormap(dpy, screen);
	swa.event_mask = ExposureMask | StructureNotifyMask |
		FocusChangeMask | KeyPressMask | KeyReleaseMask |
		ButtonPressMask | ButtonReleaseMask | PointerMotionMask;

	int create_x = 0, create_y = 0;
	if (use_position) {
		create_x = place_x;
		create_y = place_y;
	}
	Window win = XCreateWindow(dpy, root,
		create_x, create_y, (unsigned)width, (unsigned)height, 0,
		CopyFromParent, InputOutput, CopyFromParent,
		CWBackPixel | CWBorderPixel | CWColormap | CWEventMask, &swa);
	if (win == 0) {
		XCloseDisplay(dpy);
		return -2;
	}

	// XIM supplies committed UTF-8 (layout, Shift, compose/dead-key state)
	// for the desktop editor adapter. It is optional: XLookupString remains
	// a safe ASCII/legacy-layout fallback and physical keys are independent.
	tipsy_xim = XOpenIM(dpy, NULL, NULL, NULL);
	if (tipsy_xim != NULL) {
		tipsy_xic = XCreateIC(tipsy_xim,
			XNInputStyle, XIMPreeditNothing | XIMStatusNothing,
			XNClientWindow, win,
			XNFocusWindow, win,
			NULL);
	}

	tipsy_x11_set_title(dpy, win, title);
	tipsy_x11_set_icon(dpy, win, icon, icon_len);

	XClassHint hint;
	hint.res_name = "Tipsy";
	hint.res_class = "roblox";
	XSetClassHint(dpy, win, &hint);

	XSizeHints *sh = XAllocSizeHints();
	if (sh != NULL) {
		sh->flags = PSize | PMinSize;
		sh->width = width;
		sh->height = height;
		sh->min_width = 1;
		sh->min_height = 1;
		if (use_position) {
			sh->flags |= USPosition | PPosition;
			sh->x = create_x;
			sh->y = create_y;
		}
		XSetWMNormalHints(dpy, win, sh);
		XFree(sh);
	}

	Atom wm_delete = XInternAtom(dpy, "WM_DELETE_WINDOW", False);
	Atom wm_take_focus = XInternAtom(dpy, "WM_TAKE_FOCUS", False);
	Atom protocols[2] = {wm_delete, wm_take_focus};
	XSetWMProtocols(dpy, win, protocols, 2);

	// ICCCM: declare this a focus-loving window so window managers offer
	// it input focus (FocusIn) or deliver WM_TAKE_FOCUS ClientMessages.
	XWMHints *wmh = XAllocWMHints();
	if (wmh != NULL) {
		wmh->flags = InputHint;
		wmh->input = True;
		XSetWMHints(dpy, win, wmh);
		XFree(wmh);
	}

	XMapWindow(dpy, win);
	if (use_position) {
		XEvent mapped;
		memset(&mapped, 0, sizeof(mapped));
		for (int i = 0; i < 50; i++) {
			if (XCheckTypedWindowEvent(dpy, win, MapNotify, &mapped)) {
				break;
			}
			XSync(dpy, False);
			usleep(10000);
		}
		tipsy_x11_place_mapped(dpy, win, create_x, create_y);
	}
	// Take focus immediately (bare X servers have no WM to route it);
	// failures (not yet viewable) are swallowed by the error handler.
	XSetInputFocus(dpy, win, RevertToParent, CurrentTime);
	XSync(dpy, False);

	*out_dpy = (uintptr_t)dpy;
	*out_xid = (unsigned long)win;
	*out_delete = (unsigned long)wm_delete;
	return 0;
}

// Define a transparent cursor on this client window only. X cursor
// inheritance restores the desktop cursor outside the window; no pointer
// warping or global cursor state is involved.
unsigned long tipsy_x11_hide_cursor(uintptr_t dpy_ptr, unsigned long xid) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || xid == 0 || tipsy_x_io_error) {
		return 0;
	}
	char empty = 0;
	Pixmap bitmap = XCreateBitmapFromData(dpy, (Window)xid, &empty, 1, 1);
	if (bitmap == None) {
		return 0;
	}
	XColor color;
	memset(&color, 0, sizeof(color));
	Cursor cursor = XCreatePixmapCursor(dpy, bitmap, bitmap, &color, &color, 0, 0);
	XFreePixmap(dpy, bitmap);
	if (cursor == None) {
		return 0;
	}
	XDefineCursor(dpy, (Window)xid, cursor);
	XFlush(dpy);
	return (unsigned long)cursor;
}

void tipsy_x11_restore_cursor(uintptr_t dpy_ptr, unsigned long xid, unsigned long cursor) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || tipsy_x_io_error) {
		return;
	}
	if (xid != 0) {
		XUndefineCursor(dpy, (Window)xid);
	}
	if (cursor != None) {
		XFreeCursor(dpy, (Cursor)cursor);
	}
	XFlush(dpy);
}

int tipsy_x11_pump(uintptr_t dpy_ptr, unsigned long xid, unsigned long wm_delete,
	int randr_event_base, int *inout_w, int *inout_h, int *out_closed) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || tipsy_x_io_error) {
		return -1;
	}
	Window win = (Window)xid;
	*out_closed = 0;
	pthread_mutex_lock(&tipsy_event_mu);
	while (XPending(dpy) > 0) {
		XEvent ev;
		XNextEvent(dpy, &ev);
		if (randr_event_base != 0 &&
			(ev.type == randr_event_base + RRScreenChangeNotify ||
			 ev.type == randr_event_base + RRNotify)) {
			if (ev.type == randr_event_base + RRScreenChangeNotify) {
				XRRUpdateConfiguration(&ev);
			}
			atomic_fetch_add_explicit(&tipsy_refresh_version, 1, memory_order_relaxed);
			tipsy_wake_go();
			continue;
		}
		// Legacy X11 autorepeat emits an adjacent release/press with identical
		// keycode and server timestamp. Consume only the synthetic release,
		// before XIM sees it. Also support sources that send legacy pairs even
		// when this connection has detectable repeat enabled. CurrentTime=0
		// is a request sentinel, not a server event timestamp.
		if (ev.type == KeyRelease && ev.xkey.window == win &&
			ev.xkey.time != CurrentTime && XEventsQueued(dpy, QueuedAfterReading) > 0) {
			XEvent next;
			XPeekEvent(dpy, &next);
			if (next.type == KeyPress && next.xkey.window == ev.xkey.window &&
				next.xkey.keycode == ev.xkey.keycode && next.xkey.time == ev.xkey.time) {
				continue;
			}
		}
		Bool filtered = XFilterEvent(&ev, win);
		switch (ev.type) {
		case Expose:
			break;
		case FocusIn:
		case FocusOut:
			if (ev.xfocus.window == win) {
				if (ev.type == FocusOut) {
					tipsy_release_keys();
					tipsy_pointer_unlock(dpy, win, 1, 1);
				} else {
					tipsy_capture.failed_status = 0;
				}
				if (tipsy_xic != NULL) {
					if (ev.type == FocusIn) {
						XSetICFocus(tipsy_xic);
					} else {
						XUnsetICFocus(tipsy_xic);
					}
				}
				tipsy_input_push(TIPSY_INPUT_FOCUS,
					ev.type == FocusIn ? 1 : 0, 0, 0, 0, 0);
			}
			break;
		case KeyPress:
		case KeyRelease:
			if (ev.xkey.window == win) {
				KeySym ks = XLookupKeysym(&ev.xkey, 0);
				// F11 is the normal desktop fullscreen affordance. It is consumed
				// here as a window-manager command rather than also forwarding it
				// to the Android client, which could otherwise toggle twice.
				if (ks == XK_F11) {
					if (ev.type == KeyPress && !tipsy_f11_down) {
						tipsy_f11_down = 1;
						tipsy_x11_toggle_fullscreen(dpy, win);
					} else if (ev.type == KeyRelease) {
						tipsy_f11_down = 0;
					}
					break;
				}
				unsigned int code = ev.xkey.keycode;
				if (code >= 256) break;
				int android_code = tipsy_android_keycode(ks);
				int repeats = 0;
				if (ev.type == KeyPress) {
					if (tipsy_keys[code].down == 1) {
						if (tipsy_keys[code].repeats < INT32_MAX) ++tipsy_keys[code].repeats;
						repeats = tipsy_keys[code].repeats;
						android_code = tipsy_keys[code].android_code;
					} else {
						tipsy_keys[code].down = 1;
						tipsy_keys[code].repeats = 0;
						tipsy_keys[code].android_code = android_code;
					}
				} else {
					int was_down = tipsy_keys[code].down;
					tipsy_keys[code].down = 0;
					tipsy_keys[code].repeats = 0;
					if (was_down == -1) break; // already released on focus loss
					if (was_down == 1) android_code = tipsy_keys[code].android_code;
				}
				tipsy_input_push_repeat(TIPSY_INPUT_KEY,
					ev.type == KeyPress ? 1 : 0, android_code, (long)code, 0, 0, repeats);
				if (ev.type == KeyPress && !filtered) {
					char text[TIPSY_INPUT_TEXT_BYTES];
					KeySym text_ks = NoSymbol;
					int n = 0;
					if (tipsy_xic != NULL) {
						Status status = XLookupNone;
						n = Xutf8LookupString(tipsy_xic, &ev.xkey, text,
							TIPSY_INPUT_TEXT_BYTES, &text_ks, &status);
						if (status != XLookupChars && status != XLookupBoth) {
							n = 0;
						}
					} else {
						n = XLookupString(&ev.xkey, text,
							TIPSY_INPUT_TEXT_BYTES, &text_ks, NULL);
					}
					// Return, Tab, Backspace, Escape, and other control
					// bytes remain editing/physical keys, never text.
					if (n > 0 && !((unsigned char)text[0] < 0x20 ||
						(unsigned char)text[0] == 0x7f)) {
						tipsy_input_push_text(text, n);
					}
				}
			}
			break;
		case ButtonPress:
		case ButtonRelease:
			if (ev.xbutton.window != win) {
				break;
			}
			if (ev.xbutton.button == Button1 || ev.xbutton.button == Button3) {
				float px = (float)ev.xbutton.x;
				float py = (float)ev.xbutton.y;
				if (tipsy_capture.active) {
					px = (float)tipsy_capture.anchor_x;
					py = (float)tipsy_capture.anchor_y;
				}
				if (ev.xbutton.button == Button3) {
					// A focus/close cancellation already emitted the one matching
					// release. Suppress a stale physical release after that edge.
					if (ev.type == ButtonRelease && !tipsy_capture.right_down) {
						break;
					}
					tipsy_capture.right_down = ev.type == ButtonPress;
					if (ev.type == ButtonPress) {
						tipsy_capture.anchor_x = ev.xbutton.x;
						tipsy_capture.anchor_y = ev.xbutton.y;
						tipsy_capture.failed_status = 0;
					}
				}
				tipsy_capture.last_x = (int)px;
				tipsy_capture.last_y = (int)py;
				tipsy_capture.have_last = 1;
				tipsy_input_push(TIPSY_INPUT_POINTER,
					ev.type == ButtonPress ? 0 : 1,
					(long)ev.xbutton.button, 0,
					px, py);
			} else if (ev.type == ButtonPress &&
				(ev.xbutton.button == Button4 || ev.xbutton.button == Button5 ||
				 ev.xbutton.button == 6 || ev.xbutton.button == 7)) {
				int dx = 0;
				long dy = 0;
				if (ev.xbutton.button == Button4) dy = 1;
				if (ev.xbutton.button == Button5) dy = -1;
				if (ev.xbutton.button == 6) dx = -1;
				if (ev.xbutton.button == 7) dx = 1;
				tipsy_input_push(TIPSY_INPUT_SCROLL, dx, dy, 0,
					(float)ev.xbutton.x, (float)ev.xbutton.y);
			}
			break;
		case MotionNotify:
			// PointerMotionMask selected at creation delivers ordinary hover
			// and button-held motion. Keep both: the direct Roblox listener is
			// a mouse contract, not the older GameActivity touch contract.
			if (ev.xmotion.window == win) {
				if (tipsy_capture.active) {
					// A captured event at the lock point is the host recenter,
					// not camera travel. Matching only while ignore_warps > 0
					// still let a later queued snap-back replace the physical
					// coordinate and cancel the look (net zero dx/dy).
					if (tipsy_is_recenter_motion(ev.xmotion.x, ev.xmotion.y)) {
						if (tipsy_capture.ignore_warps > 0) {
							tipsy_capture.ignore_warps--;
						}
						tipsy_capture.last_x = tipsy_capture.anchor_x;
						tipsy_capture.last_y = tipsy_capture.anchor_y;
						break;
					}
					int move_x = ev.xmotion.x;
					int move_y = ev.xmotion.y;
					XEvent newer;
					while (XCheckTypedWindowEvent(dpy, win, MotionNotify, &newer)) {
						if (tipsy_is_recenter_motion(newer.xmotion.x, newer.xmotion.y)) {
							if (tipsy_capture.ignore_warps > 0) {
								tipsy_capture.ignore_warps--;
							}
							continue;
						}
						move_x = newer.xmotion.x;
						move_y = newer.xmotion.y;
					}
					if (tipsy_is_recenter_motion(move_x, move_y)) {
						tipsy_capture.last_x = tipsy_capture.anchor_x;
						tipsy_capture.last_y = tipsy_capture.anchor_y;
						break;
					}
					int dx = move_x - tipsy_capture.last_x;
					int dy = move_y - tipsy_capture.last_y;
					if (dx != 0 || dy != 0) {
						tipsy_input_push(TIPSY_INPUT_POINTER, 3,
							(long)dx, (long)dy,
							(float)tipsy_capture.anchor_x,
							(float)tipsy_capture.anchor_y);
						tipsy_warp_to_anchor(dpy, win);
					}
				} else {
					tipsy_capture.last_x = ev.xmotion.x;
					tipsy_capture.last_y = ev.xmotion.y;
					tipsy_capture.have_last = 1;
					tipsy_input_push(TIPSY_INPUT_POINTER, 2, 0, 0,
						(float)ev.xmotion.x, (float)ev.xmotion.y);
				}
			}
			break;
		case ConfigureNotify:
			if (ev.xconfigure.window == win) {
				atomic_fetch_add_explicit(&tipsy_refresh_version, 1, memory_order_relaxed);
				if (tipsy_capture.active) {
					int ax = tipsy_clamp_coord(tipsy_capture.anchor_x, ev.xconfigure.width);
					int ay = tipsy_clamp_coord(tipsy_capture.anchor_y, ev.xconfigure.height);
					if (ax != tipsy_capture.anchor_x || ay != tipsy_capture.anchor_y) {
						tipsy_capture.anchor_x = ax;
						tipsy_capture.anchor_y = ay;
						tipsy_warp_to_anchor(dpy, win);
					}
				}
				if (inout_w != NULL) {
					*inout_w = ev.xconfigure.width;
				}
				if (inout_h != NULL) {
					*inout_h = ev.xconfigure.height;
				}
				// The background pump has its own size copy. Queue this real
				// ConfigureNotify too, so Go can update Android geometry in X
				// event order before any following pointer coordinate is used.
				tipsy_input_push(TIPSY_INPUT_RESIZE, 0,
					(long)ev.xconfigure.width, (long)ev.xconfigure.height, 0, 0);
			}
			break;
		case ClientMessage:
			if (ev.xclient.window == win &&
				ev.xclient.message_type == XInternAtom(dpy, "WM_PROTOCOLS", False) &&
				(Atom)ev.xclient.data.l[0] == (Atom)wm_delete) {
				tipsy_pointer_unlock(dpy, win, 1, 1);
				// The blocking-start background pump may be the Xlib caller that
				// consumes this ClientMessage. Preserve the close edge in the
				// shared ordered ring so the Go launch loop cannot miss it.
				tipsy_input_push(TIPSY_INPUT_CLOSE, 0, (long)win, 0, 0, 0);
				*out_closed = 1;
			} else if (ev.xclient.window == win &&
				(Atom)ev.xclient.data.l[0] == XInternAtom(dpy, "WM_TAKE_FOCUS", False)) {
				// ICCCM: the WM offered focus; claim it. The resulting
				// FocusIn is the real focus event.
				XSetInputFocus(dpy, win, RevertToParent,
					(Time)ev.xclient.data.l[1]);
			}
			break;
		case DestroyNotify:
			if (ev.xdestroywindow.window == win) {
				tipsy_pointer_unlock(dpy, win, 1, 1);
				tipsy_input_push(TIPSY_INPUT_CLOSE, 0, (long)win, 0, 0, 0);
				*out_closed = 1;
			}
			break;
		case MapNotify:
		case ReparentNotify:
			if (ev.xany.window == win) {
				atomic_fetch_add_explicit(&tipsy_refresh_version, 1, memory_order_relaxed);
				tipsy_wake_go();
			}
			break;
		default:
			break;
		}
	}
	XFlush(dpy);
	pthread_mutex_unlock(&tipsy_event_mu);
	if (tipsy_x_io_error) {
		tipsy_wake_go();
		return -1;
	}
	return 0;
}

struct tipsy_pump {
	uintptr_t dpy;
	unsigned long xid;
	unsigned long del;
	int randr_event_base;
	int w;
	int h;
	volatile int run;
	volatile int closed;
	pthread_t thr;
	int wake_r;
	int wake_w;
};

// Block until the X connection has bytes, Xlib already has queued events, or
// the stop/nudge pipe is readable. Returns 1 when the caller should pump,
// 0 when the thread should exit, and -1 on I/O failure.
static int tipsy_wait_x11(Display *dpy, int wake_fd) {
	if (dpy == NULL || wake_fd < 0) {
		return -1;
	}
	struct pollfd fds[2];
	fds[0].fd = ConnectionNumber(dpy);
	fds[0].events = POLLIN;
	fds[1].fd = wake_fd;
	fds[1].events = POLLIN;
	for (;;) {
		if (tipsy_x_io_error) {
			return -1;
		}
		pthread_mutex_lock(&tipsy_event_mu);
		int pending = !tipsy_x_io_error && XPending(dpy) > 0;
		pthread_mutex_unlock(&tipsy_event_mu);
		if (pending) {
			return 1;
		}
		int n = poll(fds, 2, -1);
		if (n < 0) {
			if (errno == EINTR) {
				continue;
			}
			return -1;
		}
		if (fds[1].revents & (POLLIN | POLLHUP | POLLERR | POLLNVAL)) {
			char buf[8];
			while (read(wake_fd, buf, sizeof buf) > 0) {
			}
			return 0;
		}
		if (fds[0].revents & (POLLHUP | POLLERR | POLLNVAL)) {
			return -1;
		}
	}
}

static void *tipsy_pump_main(void *arg) {
	struct tipsy_pump *p = (struct tipsy_pump *)arg;
	while (p->run && !p->closed) {
		int st = tipsy_wait_x11((Display *)p->dpy, p->wake_r);
		if (!p->run) {
			break;
		}
		if (st < 0 || tipsy_x_io_error) {
			p->closed = 1;
			tipsy_wake_go();
			break;
		}
		int closed = 0;
		if (tipsy_x11_pump(p->dpy, p->xid, p->del, p->randr_event_base, &p->w, &p->h, &closed) != 0 || closed) {
			p->closed = 1;
			tipsy_wake_go();
			break;
		}
	}
	return NULL;
}

uintptr_t tipsy_x11_pump_thread_start(uintptr_t dpy, unsigned long xid,
	unsigned long del, int randr_event_base, int w, int h) {
	struct tipsy_pump *p = (struct tipsy_pump *)calloc(1, sizeof(*p));
	if (p == NULL) {
		return 0;
	}
	p->dpy = dpy;
	p->xid = xid;
	p->del = del;
	p->randr_event_base = randr_event_base;
	p->w = w;
	p->h = h;
	p->run = 1;
	p->wake_r = -1;
	p->wake_w = -1;
	int fds[2];
	if (pipe(fds) != 0) {
		free(p);
		return 0;
	}
	for (int i = 0; i < 2; i++) {
		int fl = fcntl(fds[i], F_GETFL, 0);
		if (fl < 0 || fcntl(fds[i], F_SETFL, fl | O_NONBLOCK) < 0 ||
			fcntl(fds[i], F_SETFD, FD_CLOEXEC) < 0) {
			close(fds[0]);
			close(fds[1]);
			free(p);
			return 0;
		}
	}
	p->wake_r = fds[0];
	p->wake_w = fds[1];
	pthread_mutex_lock(&tipsy_pump_wake_mu);
	tipsy_pump_nudge_fd = p->wake_w;
	pthread_mutex_unlock(&tipsy_pump_wake_mu);
	if (pthread_create(&p->thr, NULL, tipsy_pump_main, p) != 0) {
		pthread_mutex_lock(&tipsy_pump_wake_mu);
		tipsy_pump_nudge_fd = -1;
		close(p->wake_r);
		close(p->wake_w);
		pthread_mutex_unlock(&tipsy_pump_wake_mu);
		free(p);
		return 0;
	}
	return (uintptr_t)p;
}

void tipsy_x11_pump_thread_stop(uintptr_t ptr, int *out_w, int *out_h, int *out_closed) {
	struct tipsy_pump *p = (struct tipsy_pump *)ptr;
	if (p == NULL) {
		return;
	}
	p->run = 0;
	if (p->wake_w >= 0) {
		char one = 1;
		(void)write(p->wake_w, &one, 1);
	}
	pthread_join(p->thr, NULL);
	pthread_mutex_lock(&tipsy_pump_wake_mu);
	tipsy_pump_nudge_fd = -1;
	if (p->wake_r >= 0) {
		close(p->wake_r);
	}
	if (p->wake_w >= 0) {
		close(p->wake_w);
	}
	pthread_mutex_unlock(&tipsy_pump_wake_mu);
	if (out_w != NULL) {
		*out_w = p->w;
	}
	if (out_h != NULL) {
		*out_h = p->h;
	}
	if (out_closed != NULL) {
		*out_closed = p->closed;
	}
	free(p);
}

void tipsy_x11_close(uintptr_t dpy_ptr, unsigned long xid) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL) {
		return;
	}
	pthread_mutex_lock(&tipsy_event_mu);
	tipsy_pointer_unlock(dpy, (Window)xid, 0, 0);
	if (tipsy_xic != NULL) {
		XDestroyIC(tipsy_xic);
		tipsy_xic = NULL;
	}
	if (tipsy_xim != NULL) {
		XCloseIM(tipsy_xim);
		tipsy_xim = NULL;
	}
	if (xid != 0 && !tipsy_x_io_error) {
		XDestroyWindow(dpy, (Window)xid);
	}
	XCloseDisplay(dpy);
	memset(&tipsy_capture, 0, sizeof(tipsy_capture));
	pthread_mutex_unlock(&tipsy_event_mu);
}

int tipsy_x11_unmap(uintptr_t dpy_ptr, unsigned long xid) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || xid == 0 || tipsy_x_io_error) {
		return -1;
	}
	pthread_mutex_lock(&tipsy_event_mu);
	tipsy_pointer_unlock(dpy, (Window)xid, 0, 0);
	XUnmapWindow(dpy, (Window)xid);
	XFlush(dpy);
	pthread_mutex_unlock(&tipsy_event_mu);
	tipsy_nudge_pump();
	return tipsy_x_io_error ? -1 : 0;
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const maxListedOutputs = 32

// TIPSYInputRingLen mirrors the C input ring capacity. The ring is
// process-wide; Tipsy owns one Roblox window per process.
const TIPSYInputRingLen = 256

// ListOutputs reports connected XRandR outputs on the current DISPLAY.
func ListOutputs() ([]Output, error) {
	var raw [maxListedOutputs]C.tipsy_xrr_output
	n := int(C.tipsy_x11_list_outputs(&raw[0], C.int(len(raw))))
	if n < 0 {
		return nil, fmt.Errorf("%w (DISPLAY=%q)", ErrNoDisplay, os.Getenv("DISPLAY"))
	}
	out := make([]Output, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Output{
			Name:    C.GoString(&raw[i].name[0]),
			X:       int(raw[i].x),
			Y:       int(raw[i].y),
			Width:   int(raw[i].width),
			Height:  int(raw[i].height),
			Primary: raw[i].primary != 0,
		})
	}
	return out, nil
}

// Open creates a mapped InputOutput window on the native X11 display.
// Placement is left to the window manager (typically the pointer) unless
// OpenOnDisplay is used with a target monitor.
func Open(title string, width, height int) (*Window, error) {
	return OpenOnDisplay(title, width, height, DisplayPointer)
}

// OpenOnDisplay creates a mapped InputOutput window. display is "primary"
// (default), "pointer" for window-manager mouse placement, or an output name.
func OpenOnDisplay(title string, width, height int, display string) (*Window, error) {
	if width < 1 || height < 1 {
		return nil, ErrInvalidSize
	}
	if title == "Roblox" {
		title = RobloxWindowTitle
	}
	placeX, placeY, usePosition := 0, 0, 0
	outputs, err := ListOutputs()
	if err == nil {
		x, y, force := ResolvePlacement(display, width, height, outputs)
		if force {
			placeX, placeY, usePosition = x, y, 1
		}
	}
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))
	icon32, err := windowIconARGB()
	if err != nil {
		return nil, err
	}
	icon := make([]C.ulong, len(icon32))
	for i, value := range icon32 {
		icon[i] = C.ulong(value)
	}
	var iconPtr *C.ulong
	if len(icon) != 0 {
		iconPtr = &icon[0]
	}

	var dpy C.uintptr_t
	var xid, del C.ulong
	var randrEventBase C.int
	rc := C.tipsy_x11_open(ctitle, C.int(width), C.int(height),
		C.int(placeX), C.int(placeY), C.int(usePosition),
		iconPtr, C.int(len(icon)), &dpy, &xid, &del, &randrEventBase)
	if rc != 0 || dpy == 0 || xid == 0 {
		return nil, fmt.Errorf("%w (DISPLAY=%q)", ErrNoDisplay, os.Getenv("DISPLAY"))
	}

	w := &Window{
		display:        uintptr(dpy),
		xid:            uintptr(xid),
		wmDelete:       uintptr(del),
		randrEventBase: int(randrEventBase),
		width:          width,
		height:         height,
	}
	setActiveWindow(w)
	// Roblox renders its own cursor. This transparent cursor is scoped to the
	// client window: leaving it returns to the host cursor automatically.
	w.cursor = uintptr(C.tipsy_x11_hide_cursor(C.uintptr_t(w.display), C.ulong(w.xid)))
	if w.cursor == 0 {
		logging.Logger(logging.CatX11).Info("X11 cursor hide unavailable")
	}
	_ = w.Pump()
	logging.Logger(logging.CatX11).Info("opened X11 window",
		"title", title, "width", w.width, "height", w.height, "xid", w.xid,
		"display", normalizeDisplay(display), "placeX", placeX, "placeY", placeY,
		"forced", usePosition != 0)
	return w, nil
}

// setFullscreenLocked sends the EWMH state request while the Window mutex is
// held. The WM applies it asynchronously and reports the resulting geometry
// through ConfigureNotify.
func setFullscreenLocked(w *Window, enabled bool) error {
	value := C.int(0)
	if enabled {
		value = 1
	}
	if C.tipsy_x11_request_fullscreen(C.uintptr_t(w.display), C.ulong(w.xid), value) != 0 {
		return ErrFullscreen
	}
	return nil
}

func dismissLocked(w *Window) error {
	if C.tipsy_x11_unmap(C.uintptr_t(w.display), C.ulong(w.xid)) != 0 {
		return ErrClosed
	}
	w.dismissed = true
	logging.Logger(logging.CatX11).Info("dismissed X11 window", "xid", w.xid)
	return nil
}

func setPointerLockLocked(w *Window, locked bool) (bool, error) {
	value := C.int(0)
	if locked {
		value = 1
	}
	var anchorX, anchorY, status C.int
	rc := int(C.tipsy_x11_set_pointer_lock(C.uintptr_t(w.display), C.ulong(w.xid),
		value, &anchorX, &anchorY, &status))
	switch rc {
	case 1:
		w.pointerCaptured = true
		w.pointerAnchorX = int(anchorX)
		w.pointerAnchorY = int(anchorY)
		logging.Logger(logging.CatX11).Info("X11 pointer lock acquired",
			"xid", w.xid, "anchorX", w.pointerAnchorX, "anchorY", w.pointerAnchorY)
		return true, nil
	case 2:
		w.pointerCaptured = false
		logging.Logger(logging.CatX11).Info("X11 pointer lock released", "xid", w.xid)
		return true, nil
	case -1:
		logging.Logger(logging.CatX11).Error("X11 pointer lock rejected",
			"xid", w.xid, "grabStatus", int(status))
		return false, fmt.Errorf("%w (status=%d)", ErrPointerGrab, int(status))
	case -2:
		return false, ErrClosed
	default:
		return false, nil
	}
}

// Pump drains the input ring into Go subscribers. While StartBackgroundPump
// is running, the C thread is the only XPending/XNextEvent reader; Pump
// does not call tipsy_x11_pump. Without a background pump (unit tests), Pump
// still consumes X events itself so tests stay single-consumer.
func (w *Window) Pump() error {
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	if w.closed || w.display == 0 {
		w.mu.Unlock()
		return ErrClosed
	}
	C.tipsy_x11_wake_ack()
	if C.tipsy_x11_io_error() != 0 {
		w.closed = true
		w.mu.Unlock()
		return ErrClosed
	}

	var closed C.int
	if w.pump == 0 {
		cw := C.int(w.width)
		ch := C.int(w.height)
		rc := C.tipsy_x11_pump(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.wmDelete), C.int(w.randrEventBase), &cw, &ch, &closed)
		w.width = int(cw)
		w.height = int(ch)
		if rc != 0 {
			w.closed = true
			w.mu.Unlock()
			return ErrClosed
		}
	}
	evs, closeRequested := w.drainInputLocked()
	if closed != 0 || closeRequested {
		w.closed = true
		_ = dismissLocked(w)
		w.mu.Unlock()
		notifyInput(evs)
		return ErrClosed
	}
	w.mu.Unlock()
	notifyInput(evs)
	return nil
}

// drainInputLocked moves captured events from the C ring into Go events
// and applies focus state. Called with w.mu held.
func (w *Window) drainInputLocked() ([]InputEvent, bool) {
	var raw [TIPSYInputRingLen]C.struct_tipsy_input_ev
	n := int(C.tipsy_x11_input_drain(&raw[0], C.int(len(raw))))
	if n == 0 {
		return nil, false
	}
	evs := make([]InputEvent, 0, n)
	closeRequested := false
	for i := 0; i < n; i++ {
		r := &raw[i]
		switch r.kind {
		case C.TIPSY_INPUT_FOCUS:
			gained := r.a != 0
			w.focused = gained
			evs = append(evs, InputEvent{Kind: InputFocus, FocusGained: gained})
			logging.Logger(logging.CatX11).Info("window focus", "gained", gained)
		case C.TIPSY_INPUT_KEY:
			if r.b == 0 {
				// Keep the actual X11 edge for diagnostics/subscribers, but
				// account for the absence of an Android physical key code.
				// The direct JNI route rejects KeyCode 0 rather than inventing
				// text or a guessed mapping.
				inputMu.Lock()
				inputDrops++
				inputMu.Unlock()
			}
			evs = append(evs, InputEvent{Kind: InputKey, KeyPressed: r.a != 0, KeyCode: int32(r.b), ScanCode: int32(r.c), RepeatCount: int32(r.repeat_count)})
		case C.TIPSY_INPUT_POINTER:
			action, relative := decodePointerRingAction(int32(r.a))
			ev := InputEvent{Kind: InputPointer, PointerAction: action, Button: int32(r.b), X: float32(r.x), Y: float32(r.y), Relative: relative}
			if relative {
				ev.Button = 0
				ev.DeltaX = float32(r.b)
				ev.DeltaY = float32(r.c)
			}
			evs = append(evs, ev)
		case C.TIPSY_INPUT_SCROLL:
			evs = append(evs, InputEvent{Kind: InputScroll, X: float32(r.x), Y: float32(r.y), ScrollX: float32(r.a), ScrollY: float32(r.b)})
		case C.TIPSY_INPUT_RESIZE:
			width, height := int(r.b), int(r.c)
			if width <= 0 || height <= 0 {
				continue
			}
			w.width, w.height = width, height
			if w.pointerCaptured {
				if w.pointerAnchorX >= width {
					w.pointerAnchorX = width - 1
				}
				if w.pointerAnchorY >= height {
					w.pointerAnchorY = height - 1
				}
			}
			evs = append(evs, InputEvent{Kind: InputResize, Width: width, Height: height})
		case C.TIPSY_INPUT_TEXT:
			n := int(r.text_len)
			if n <= 0 || n > C.TIPSY_INPUT_TEXT_BYTES {
				continue
			}
			text := C.GoStringN((*C.char)(unsafe.Pointer(&r.text[0])), C.int(n))
			if text == "" {
				continue
			}
			// Text can contain credentials. Preserve it for the subscribed
			// editor adapter, but never log it here.
			evs = append(evs, InputEvent{Kind: InputText, Text: text})
		case C.TIPSY_INPUT_CLOSE:
			// The C ring is process-global because Tipsy hosts one Roblox
			// window. Still match the XID so a late close from an already
			// destroyed test window cannot close a later one.
			if uintptr(r.b) == w.xid {
				closeRequested = true
			}
		case C.TIPSY_INPUT_CAPTURE:
			if uintptr(r.c) != w.xid {
				continue
			}
			ev := InputEvent{Kind: InputPointerCapture, X: float32(r.x), Y: float32(r.y)}
			switch r.a {
			case 1:
				ev.Captured = true
				w.pointerCaptured = true
				w.pointerAnchorX, w.pointerAnchorY = int(r.x), int(r.y)
			case 2:
				ev.CaptureFailed = true
				ev.CaptureStatus = int32(r.b)
			default:
				w.pointerCaptured = false
			}
			evs = append(evs, ev)
		}
	}
	return evs, closeRequested
}

// StartBackgroundPump starts the exclusive C X-event reader so V2Start can
// block on C Main while Go only drains the input ring. Do not Swap/EGL here.
func (w *Window) StartBackgroundPump() error {
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 {
		return ErrClosed
	}
	if w.pump != 0 {
		return nil
	}
	p := C.tipsy_x11_pump_thread_start(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.wmDelete), C.int(w.randrEventBase), C.int(w.width), C.int(w.height))
	if p == 0 {
		return fmt.Errorf("x11: background pump thread")
	}
	w.pump = uintptr(p)
	return nil
}

// StopBackgroundPump joins the C pump thread started by StartBackgroundPump.
func (w *Window) StopBackgroundPump() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopBackgroundPumpLocked()
}

func (w *Window) stopBackgroundPumpLocked() error {
	if w.pump == 0 {
		return nil
	}
	cw, ch := C.int(w.width), C.int(w.height)
	var closed C.int
	C.tipsy_x11_pump_thread_stop(C.uintptr_t(w.pump), &cw, &ch, &closed)
	w.pump = 0
	w.width = int(cw)
	w.height = int(ch)
	if closed != 0 {
		w.closed = true
		return ErrClosed
	}
	return nil
}

// Close destroys the window and closes the X display connection.
func (w *Window) Close() error {
	if w == nil {
		return nil
	}
	clearActiveWindow(w)
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.stopBackgroundPumpLocked()
	if w.display == 0 {
		w.closed = true
		return nil
	}
	if w.cursor != 0 {
		C.tipsy_x11_restore_cursor(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.cursor))
		w.cursor = 0
	}
	C.tipsy_x11_close(C.uintptr_t(w.display), C.ulong(w.xid))
	logging.Logger(logging.CatX11).Info("closed X11 window", "xid", w.xid)
	w.display = 0
	w.xid = 0
	w.closed = true
	w.dismissed = true
	w.pointerCaptured = false
	return nil
}

// RefreshVersion changes after the event reader observes a client move/resize,
// reparent/map, or RandR display-configuration event. Reading it performs no
// X-server query. Call Pump first when no background event reader is running.
// A zero version means the window is closed.
func (w *Window) RefreshVersion() uint64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 {
		return 0
	}
	return uint64(C.tipsy_x11_refresh_version())
}

// WakeEventPump wakes the event reader after another shared-display Xlib
// caller may have buffered events while waiting for a server reply.
func WakeEventPump() {
	C.tipsy_nudge_pump()
}
