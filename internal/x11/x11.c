/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "x11.h"

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <X11/XKBlib.h>
#include <X11/extensions/XInput2.h>
#include <X11/extensions/Xrandr.h>
#include <errno.h>
#include <fcntl.h>
#include <float.h>
#include <locale.h>
#include <math.h>
#include <poll.h>
#include <pthread.h>
#include <stdio.h>
#include <string.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>

_Static_assert(sizeof(struct tipsy_input_ev) <= 48, "tipsy_input_ev must stay a small pointer slot");

extern void GoX11_Notify(void);

static int tipsy_x_error_code;
static int tipsy_x_io_error;
static int tipsy_x_inited;
static XIM tipsy_xim;
static XIC tipsy_xic;
static int tipsy_f11_down;
// Roblox has a verified resize-storm crash below this floor. The values are
// provided by Go only for the Roblox window; ordinary X11 test windows retain
// their requested geometry.
static int tipsy_window_min_width = 1;
static int tipsy_window_min_height = 1;
// XI2 is optional at runtime. It is enabled only for an active pointer grab:
// selecting RawMotion on the root all the time would wake Tipsy for unrelated
// desktop movement while the client is idle or unfocused.
static int tipsy_xi_opcode;
static int tipsy_xi_available;
static int tipsy_xi_raw_selected;
// X11 core keycodes are bytes. Preserve the first physical down across XKB
// repeat presses; -1 means focus loss already released the key to Android.
static struct { int down; int repeats; int android_code; } tipsy_keys[256];

// Pointer/key/motion slots stay small. Committed IME text is rare and lives
// in a side ring indexed by the same head/tail as the event. Overflow drops
// wipe that slot so secrets do not linger after a discarded text event.
static struct tipsy_input_ev tipsy_input_ring[TIPSY_INPUT_RING];
static char tipsy_input_text[TIPSY_INPUT_RING][TIPSY_INPUT_TEXT_BYTES];
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

// Discover XI2 once per display. Raw motion selection itself is deliberately
// deferred until an active pointer grab so normal desktop mouse movement cannot
// wake this process or add any per-event work.
static void tipsy_x11_init_xi2(Display *dpy) {
	tipsy_xi_opcode = 0;
	tipsy_xi_available = 0;
	tipsy_xi_raw_selected = 0;
	if (dpy == NULL) {
		return;
	}
	int opcode = 0, event = 0, error = 0;
	if (!XQueryExtension(dpy, "XInputExtension", &opcode, &event, &error)) {
		return;
	}
	// Raw master-pointer motion while a core XGrabPointer is active requires
	// an XI 2.1-or-newer client negotiation on Xorg/Xwayland. Merely checking
	// that the server implements XI2 is insufficient: the server can alter
	// event delivery according to the version this client announced. Keep the
	// proven core MotionNotify/recenter route for an XI 2.0-only server rather
	// than selecting a raw route which might then receive no camera samples.
	int major = 2, minor = 1;
	if (XIQueryVersion(dpy, &major, &minor) != Success ||
		major < 2 || (major == 2 && minor < 1)) {
		return;
	}
	tipsy_xi_opcode = opcode;
	tipsy_xi_available = 1;
}

// XI2 raw events may only be selected on a root window. `valuators.values`
// retains the server's transformed/accelerated floating delta, while
// `raw_values` is the unscaled hardware delta. We use the former so the user’s
// existing desktop sensitivity remains intact, without core MotionNotify’s
// integer-coordinate quantization.
static int tipsy_x11_set_raw_motion(Display *dpy, int enabled) {
	if (dpy == NULL || !tipsy_xi_available) {
		return 0;
	}
	if (!!enabled == !!tipsy_xi_raw_selected) {
		return tipsy_xi_raw_selected;
	}
	unsigned char mask[XIMaskLen(XI_RawMotion)];
	memset(mask, 0, sizeof(mask));
	if (enabled) {
		XISetMask(mask, XI_RawMotion);
	}
	XIEventMask events;
	events.deviceid = XIAllMasterDevices;
	events.mask_len = sizeof(mask);
	events.mask = mask;
	if (XISelectEvents(dpy, DefaultRootWindow(dpy), &events, 1) != Success) {
		return 0;
	}
	tipsy_xi_raw_selected = enabled ? 1 : 0;
	return tipsy_xi_raw_selected;
}

struct tipsy_pointer_capture {
	int active;
	int sticky;
	int center;
	int right_down;
	int have_last;
	int anchor_x;
	int anchor_y;
	int last_x;
	int last_y;
	int ignore_warps;
	int failed_status;
	int raw_motion;
	int raw_warp_pending;
	Time last_raw_motion_time;
	Display *dpy;
	Window win;
};

// Test hooks: raw samples accepted and tipsy_warp_to calls during the last
// tipsy_x11_pump pass. Production does not read these.
static int tipsy_in_pump;
static int tipsy_test_last_pump_raw_samples;
static int tipsy_test_last_pump_warps;
static int tipsy_warp_dry_run;

static struct tipsy_pointer_capture tipsy_capture;
// Keyboard focus for the sole client window. Pointer-lock acquisition is
// refused while this is 0 so a Roblox getter that stays true after Alt-Tab
// cannot re-grab the desktop pointer.
static int tipsy_have_keyboard_focus;
static Atom tipsy_atom_net_active_window;

static void tipsy_input_drop_oldest_locked(void) {
	if (tipsy_input_ring[tipsy_input_tail].kind == TIPSY_INPUT_TEXT &&
		tipsy_input_ring[tipsy_input_tail].text_len > 0) {
		memset(tipsy_input_text[tipsy_input_tail], 0, TIPSY_INPUT_TEXT_BYTES);
	}
	tipsy_input_tail = (tipsy_input_tail + 1) % TIPSY_INPUT_RING;
}

static void tipsy_input_push_repeat(int kind, int a, long b, long c, float x, float y, int repeats) {
	pthread_mutex_lock(&tipsy_input_mu);
	int was_empty = (tipsy_input_head == tipsy_input_tail);
	int last = (tipsy_input_head - 1 + TIPSY_INPUT_RING) % TIPSY_INPUT_RING;
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
		// Ring full: this generic path retains its existing bounded behavior.
		// Captured-pointer overflow is handled separately below so it keeps
		// camera travel without sacrificing a low-frequency input edge.
		tipsy_input_drop_oldest_locked();
	}
	tipsy_input_ring[tipsy_input_head].kind = kind;
	tipsy_input_ring[tipsy_input_head].a = a;
	tipsy_input_ring[tipsy_input_head].b = b;
	tipsy_input_ring[tipsy_input_head].c = c;
	tipsy_input_ring[tipsy_input_head].x = x;
	tipsy_input_ring[tipsy_input_head].y = y;
	tipsy_input_ring[tipsy_input_head].dx = 0;
	tipsy_input_ring[tipsy_input_head].dy = 0;
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

// Every physical captured sample needs its own JNI call: summing samples in
// the C ring preserved total travel but visibly lowered camera cadence. Keep
// the common path lossless. If a stalled consumer fills the bounded ring,
// retain the remaining travel in its final captured event rather than dropping
// it or allocating/unboundedly growing during a mouse flood.
static void tipsy_input_push_relative(float dx, float dy, float anchor_x, float anchor_y) {
	if (dx == 0 && dy == 0) {
		return;
	}
	pthread_mutex_lock(&tipsy_input_mu);
	int was_empty = (tipsy_input_head == tipsy_input_tail);
	int last = (tipsy_input_head - 1 + TIPSY_INPUT_RING) % TIPSY_INPUT_RING;
	int next = (tipsy_input_head + 1) % TIPSY_INPUT_RING;
	if (next == tipsy_input_tail) {
		// Overflow is an exceptional stalled-consumer condition. Preserve the
		// full remaining displacement in one event; never overwrite a button,
		// focus, resize, or key edge just to retain a finer mouse cadence.
		if (tipsy_input_ring[last].kind == TIPSY_INPUT_POINTER &&
			tipsy_input_ring[last].a == 3) {
			tipsy_input_ring[last].dx += dx;
			tipsy_input_ring[last].dy += dy;
			pthread_mutex_unlock(&tipsy_input_mu);
			return;
		}
		tipsy_input_drop_oldest_locked();
	}
	struct tipsy_input_ev *slot = &tipsy_input_ring[tipsy_input_head];
	slot->kind = TIPSY_INPUT_POINTER;
	slot->a = 3;
	slot->b = 0;
	slot->c = 0;
	slot->x = anchor_x;
	slot->y = anchor_y;
	slot->dx = dx;
	slot->dy = dy;
	slot->repeat_count = 0;
	slot->text_len = 0;
	tipsy_input_head = next;
	pthread_mutex_unlock(&tipsy_input_mu);
	if (was_empty) {
		tipsy_wake_go();
	}
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

static void tipsy_capture_center(int width, int height, int *x, int *y) {
	*x = tipsy_clamp_coord(width / 2, width);
	*y = tipsy_clamp_coord(height / 2, height);
}

// Held-RMB camera look anchors at the live cursor. Prefer the last client
// coordinate Tipsy already delivered (the click, or latest hover) so the
// grab cannot teleport relative to the button edge. Query, then window
// center, only when no pointer sample exists yet.
static void tipsy_capture_set_cursor_anchor(Display *dpy, Window win,
	int width, int height) {
	if (tipsy_capture.have_last) {
		tipsy_capture.anchor_x = tipsy_clamp_coord(tipsy_capture.last_x, width);
		tipsy_capture.anchor_y = tipsy_clamp_coord(tipsy_capture.last_y, height);
		return;
	}
	Window root = 0, child = 0;
	int root_x = 0, root_y = 0, x = 0, y = 0;
	unsigned int mask = 0;
	if (dpy != NULL && win != 0 &&
		XQueryPointer(dpy, win, &root, &child, &root_x, &root_y,
			&x, &y, &mask)) {
		tipsy_capture.anchor_x = tipsy_clamp_coord(x, width);
		tipsy_capture.anchor_y = tipsy_clamp_coord(y, height);
		return;
	}
	tipsy_capture_center(width, height, &tipsy_capture.anchor_x,
		&tipsy_capture.anchor_y);
}

static int tipsy_window_is_related(Display *dpy, Window win, Window other) {
	if (dpy == NULL || win == 0 || other == 0) {
		return 0;
	}
	if (win == other) {
		return 1;
	}
	Window root = 0, parent = 0, *children = NULL;
	unsigned int n = 0;
	Window current = other;
	for (int hop = 0; hop < 8; hop++) {
		if (current == None || current == DefaultRootWindow(dpy)) {
			break;
		}
		if (current == win) {
			return 1;
		}
		if (!XQueryTree(dpy, current, &root, &parent, &children, &n)) {
			return 0;
		}
		if (children != NULL) {
			XFree(children);
		}
		if (parent == None || parent == current) {
			break;
		}
		current = parent;
	}
	current = win;
	for (int hop = 0; hop < 8; hop++) {
		if (current == None || current == DefaultRootWindow(dpy)) {
			break;
		}
		if (current == other) {
			return 1;
		}
		if (!XQueryTree(dpy, current, &root, &parent, &children, &n)) {
			return 0;
		}
		if (children != NULL) {
			XFree(children);
		}
		if (parent == None || parent == current) {
			break;
		}
		current = parent;
	}
	return 0;
}

static Window tipsy_ewmh_active_window(Display *dpy) {
	if (dpy == NULL || tipsy_atom_net_active_window == None) {
		return None;
	}
	Atom actual = None;
	int format = 0;
	unsigned long n = 0, after = 0;
	unsigned char *raw = NULL;
	Window active = None;
	if (XGetWindowProperty(dpy, DefaultRootWindow(dpy),
		tipsy_atom_net_active_window, 0, 1, False, AnyPropertyType,
		&actual, &format, &n, &after, &raw) == Success && raw != NULL) {
		if (n >= 1 && format == 32) {
			active = *(Window *)raw;
		}
		XFree(raw);
	}
	return active;
}

static void tipsy_discard_queued_motion(Display *dpy, Window win) {
	XEvent junk;
	while (XCheckTypedWindowEvent(dpy, win, MotionNotify, &junk)) {
	}
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

static void tipsy_warp_to(Display *dpy, Window win, int x, int y) {
	if (tipsy_in_pump) {
		tipsy_test_last_pump_warps++;
	}
	if (tipsy_warp_dry_run) {
		return;
	}
	Window root = DefaultRootWindow(dpy);
	int root_x = 0, root_y = 0;
	Window child = None;
	if (XTranslateCoordinates(dpy, win, root, x, y, &root_x, &root_y, &child)) {
		XWarpPointer(dpy, None, root, 0, 0, 0, 0, root_x, root_y);
	} else {
		XWarpPointer(dpy, None, win, 0, 0, 0, 0, x, y);
	}
	if (tipsy_xi_available) {
		(void)XIWarpPointer(dpy, XIAllMasterDevices, None, win,
			0, 0, 0, 0, (double)x, (double)y);
	}
}

// Raw XI2 motion has already established that the physical pointer moved, so
// avoid the XQueryPointer round trip in the core fallback helper. The next
// core recenter event is harmless because raw capture does not select core
// PointerMotionMask on its grab.
static void tipsy_warp_to_anchor_force(Display *dpy, Window win) {
	tipsy_capture.raw_warp_pending = 0;
	tipsy_warp_to(dpy, win, tipsy_capture.anchor_x, tipsy_capture.anchor_y);
	tipsy_capture.ignore_warps = 1;
	tipsy_capture.last_x = tipsy_capture.anchor_x;
	tipsy_capture.last_y = tipsy_capture.anchor_y;
}

// One recenter for a pump pass of captured XI2 samples. Per-sample warps
// stay queued in Xlib until the later XFlush, so they do not pull the
// pointer back before later events in this pass; they only multiply X
// requests. Lock/resize/unlock and the quiet-raw core fallback still warp
// immediately through tipsy_warp_to_anchor / tipsy_warp_to_anchor_force.
static void tipsy_flush_pending_raw_warp(Display *dpy, Window win) {
	if (!tipsy_capture.raw_warp_pending) {
		return;
	}
	tipsy_capture.raw_warp_pending = 0;
	if (!tipsy_capture.active || !tipsy_capture.raw_motion ||
		tipsy_capture.dpy != dpy || tipsy_capture.win != win) {
		return;
	}
	tipsy_warp_to_anchor_force(dpy, win);
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
	tipsy_warp_to_anchor_force(dpy, win);
}

static void tipsy_handle_raw_motion(Display *dpy, Window win,
	const XIRawEvent *raw) {
	if (!tipsy_capture.active || !tipsy_capture.raw_motion ||
		tipsy_capture.dpy != dpy || tipsy_capture.win != win ||
		raw == NULL || raw->valuators.mask == NULL ||
		raw->valuators.values == NULL || raw->valuators.mask_len <= 0) {
		return;
	}
	double dx = 0, dy = 0;
	int have_x = 0, have_y = 0;
	const double *values = raw->valuators.values;
	for (int axis = 0; axis < raw->valuators.mask_len * 8; ++axis) {
		if (!XIMaskIsSet(raw->valuators.mask, axis)) {
			continue;
		}
		double value = *values++;
		if (axis == 0) {
			dx = value;
			have_x = 1;
		} else if (axis == 1) {
			dy = value;
			have_y = 1;
		}
	}
	if ((!have_x && !have_y) || !isfinite(dx) || !isfinite(dy) ||
		fabs(dx) > FLT_MAX || fabs(dy) > FLT_MAX) {
		return;
	}
	tipsy_input_push_relative((float)dx, (float)dy,
		(float)tipsy_capture.anchor_x, (float)tipsy_capture.anchor_y);
	// A normal core MotionNotify produced by this same physical sample has the
	// same server timestamp. Keep it selected as an active-capture fallback,
	// but use this marker to avoid delivering the coarse coordinate delta in
	// addition to the precise XI2 value. Recenter is deferred to the end of
	// this pump pass: one XWarpPointer, not one per raw sample.
	tipsy_capture.last_raw_motion_time = raw->time;
	tipsy_capture.last_x = tipsy_capture.anchor_x;
	tipsy_capture.last_y = tipsy_capture.anchor_y;
	tipsy_capture.raw_warp_pending = 1;
	if (tipsy_in_pump) {
		tipsy_test_last_pump_raw_samples++;
	}
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
	// Leave the desktop pointer at the grab anchor. First-person look uses
	// the window center; held-RMB look uses the click. Warping to a different
	// restore point made unlock appear to teleport.
	tipsy_capture.ignore_warps = 0;
	tipsy_capture.raw_warp_pending = 0;
	if (tipsy_capture.raw_motion) {
		tipsy_capture.raw_motion = 0;
		(void)tipsy_x11_set_raw_motion(dpy, 0);
	}
	tipsy_discard_queued_motion(dpy, win);
	tipsy_warp_to(dpy, win, tipsy_capture.anchor_x, tipsy_capture.anchor_y);
	tipsy_capture.last_x = tipsy_capture.anchor_x;
	tipsy_capture.last_y = tipsy_capture.anchor_y;
	XUngrabPointer(dpy, CurrentTime);
	tipsy_capture.active = 0;
	XSync(dpy, False);
	tipsy_discard_queued_motion(dpy, win);
	if (notify) tipsy_capture_event(0, GrabSuccess);
	return 1;
}

// Grab/recenter. Caller holds tipsy_event_mu. center!=0 is first-person
// lock (window center, sticky tab-back). center==0 is held-RMB camera look
// (live cursor, no sticky recapture). Returns 1 for a new grab, 0 for
// unchanged (including an already-active first-person grab that was
// re-centered), and -1 for a rejected grab.
static int tipsy_pointer_lock_apply(Display *dpy, Window win, int center,
	int *out_status) {
	if (dpy == NULL || win == 0 || tipsy_x_io_error) {
		return -1;
	}
	if (!tipsy_have_keyboard_focus) {
		return 0;
	}
	XWindowAttributes attr;
	if (XGetWindowAttributes(dpy, win, &attr) == 0 || attr.map_state != IsViewable) {
		tipsy_capture.failed_status = GrabNotViewable + 1;
		if (out_status != NULL) {
			*out_status = GrabNotViewable;
		}
		tipsy_capture_event(2, GrabNotViewable);
		return -1;
	}
	int center_x = 0, center_y = 0;
	tipsy_capture_center(attr.width, attr.height, &center_x, &center_y);
	if (tipsy_capture.active && tipsy_capture.dpy == dpy &&
		tipsy_capture.win == win) {
		if (center) {
			tipsy_capture.center = 1;
			tipsy_capture.sticky = 1;
			tipsy_capture.anchor_x = center_x;
			tipsy_capture.anchor_y = center_y;
			tipsy_capture.last_x = center_x;
			tipsy_capture.last_y = center_y;
			tipsy_warp_to_anchor_force(dpy, win);
		}
		return 0;
	}
	tipsy_capture.dpy = dpy;
	tipsy_capture.win = win;
	tipsy_capture.center = center ? 1 : 0;
	if (tipsy_capture.center) {
		tipsy_capture.anchor_x = center_x;
		tipsy_capture.anchor_y = center_y;
	} else {
		tipsy_capture_set_cursor_anchor(dpy, win, attr.width, attr.height);
	}
	tipsy_capture.last_x = tipsy_capture.anchor_x;
	tipsy_capture.last_y = tipsy_capture.anchor_y;
	tipsy_capture.have_last = 1;
	// First-person: warp to the center before confine so an edge cursor
	// cannot stay pinned against the grab rectangle. Held-RMB: warp is a
	// no-op at the live cursor so look does not teleport to mid-window.
	tipsy_warp_to_anchor_force(dpy, win);
	XFlush(dpy);
	int raw_motion = tipsy_x11_set_raw_motion(dpy, 1);
	unsigned long event_mask = ButtonPressMask | ButtonReleaseMask |
		PointerMotionMask;
	int status = XGrabPointer(dpy, win, False, event_mask,
		GrabModeAsync, GrabModeAsync, win, None, CurrentTime);
	if (status != GrabSuccess) {
		if (raw_motion) {
			(void)tipsy_x11_set_raw_motion(dpy, 0);
		}
		tipsy_capture.failed_status = status + 1;
		if (out_status != NULL) {
			*out_status = status;
		}
		tipsy_capture_event(2, status);
		return -1;
	}
	tipsy_capture.active = 1;
	tipsy_capture.sticky = tipsy_capture.center;
	tipsy_capture.raw_motion = raw_motion;
	tipsy_capture.last_raw_motion_time = CurrentTime;
	tipsy_warp_to_anchor_force(dpy, win);
	tipsy_capture_event(1, GrabSuccess);
	return 1;
}

static void tipsy_apply_host_focus(Display *dpy, Window win, int gained) {
	if (gained) {
		if (tipsy_have_keyboard_focus) {
			if (tipsy_capture.sticky) {
				(void)tipsy_pointer_lock_apply(dpy, win, tipsy_capture.center, NULL);
			}
			return;
		}
		tipsy_have_keyboard_focus = 1;
		tipsy_capture.failed_status = 0;
		if (tipsy_xic != NULL) {
			XSetICFocus(tipsy_xic);
		}
		if (tipsy_capture.sticky) {
			(void)tipsy_pointer_lock_apply(dpy, win, tipsy_capture.center, NULL);
		}
		tipsy_input_push(TIPSY_INPUT_FOCUS, 1, 0, 0, 0, 0);
		return;
	}
	if (!tipsy_have_keyboard_focus && !tipsy_capture.active) {
		return;
	}
	tipsy_have_keyboard_focus = 0;
	tipsy_release_keys();
	tipsy_pointer_unlock(dpy, win, 1, 1);
	if (tipsy_xic != NULL) {
		XUnsetICFocus(tipsy_xic);
	}
	tipsy_input_push(TIPSY_INPUT_FOCUS, 0, 0, 0, 0, 0);
}

// Apply the state read from the APK-proven native getter. Return 1 for
// acquired, 2 for released, 0 for unchanged, -1 for grab rejection, and -2
// for a closed display.
int tipsy_x11_set_pointer_lock(uintptr_t dpy_ptr, unsigned long xid,
	int locked, int center, int *out_x, int *out_y, int *out_status) {
	Display *dpy = (Display *)dpy_ptr;
	Window win = (Window)xid;
	if (dpy == NULL || win == 0 || tipsy_x_io_error) return -2;
	pthread_mutex_lock(&tipsy_event_mu);
	int result = 0;
	if (!locked) {
		tipsy_capture.failed_status = 0;
		tipsy_capture.sticky = 0;
		tipsy_capture.center = 0;
		result = tipsy_pointer_unlock(dpy, win, 1, 0) ? 2 : 0;
		goto done;
	}
	if (tipsy_capture.failed_status != 0) goto done;
	result = tipsy_pointer_lock_apply(dpy, win, center, out_status);

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
		tipsy_input_drop_oldest_locked();
	}
	struct tipsy_input_ev *slot = &tipsy_input_ring[tipsy_input_head];
	slot->kind = TIPSY_INPUT_TEXT;
	slot->a = 0;
	slot->b = 0;
	slot->c = 0;
	slot->x = 0;
	slot->y = 0;
	slot->dx = 0;
	slot->dy = 0;
	slot->repeat_count = 0;
	slot->text_len = len;
	memcpy(tipsy_input_text[tipsy_input_head], text, (size_t)len);
	if (len < TIPSY_INPUT_TEXT_BYTES) {
		memset(tipsy_input_text[tipsy_input_head] + len, 0,
			(size_t)(TIPSY_INPUT_TEXT_BYTES - len));
	}
	tipsy_input_head = next;
	pthread_mutex_unlock(&tipsy_input_mu);
	if (was_empty) {
		tipsy_wake_go();
	}
}

int tipsy_x11_input_drain(struct tipsy_input_ev *out, char *text_out, int max) {
	if (out == NULL || max <= 0) {
		return 0;
	}
	pthread_mutex_lock(&tipsy_input_mu);
	int n = 0;
	while (tipsy_input_tail != tipsy_input_head && n < max) {
		out[n] = tipsy_input_ring[tipsy_input_tail];
		if (out[n].kind == TIPSY_INPUT_TEXT && out[n].text_len > 0) {
			int len = out[n].text_len;
			if (len > TIPSY_INPUT_TEXT_BYTES) {
				len = TIPSY_INPUT_TEXT_BYTES;
			}
			if (text_out != NULL) {
				memcpy(text_out + (size_t)n * TIPSY_INPUT_TEXT_BYTES,
					tipsy_input_text[tipsy_input_tail], (size_t)len);
			}
			memset(tipsy_input_text[tipsy_input_tail], 0, TIPSY_INPUT_TEXT_BYTES);
		}
		tipsy_input_tail = (tipsy_input_tail + 1) % TIPSY_INPUT_RING;
		n++;
	}
	pthread_mutex_unlock(&tipsy_input_mu);
	return n;
}

void tipsy_x11_input_test_clear(void) {
	pthread_mutex_lock(&tipsy_input_mu);
	memset(tipsy_input_ring, 0, sizeof(tipsy_input_ring));
	memset(tipsy_input_text, 0, sizeof(tipsy_input_text));
	tipsy_input_head = 0;
	tipsy_input_tail = 0;
	pthread_mutex_unlock(&tipsy_input_mu);
}

void tipsy_x11_input_test_push(int kind, int a, long b, long c, float x, float y) {
	tipsy_input_push(kind, a, b, c, x, y);
}

void tipsy_x11_input_test_push_text(const char *text, int len) {
	tipsy_input_push_text(text, len);
}

int tipsy_x11_test_last_pump_raw_samples(void) {
	return tipsy_test_last_pump_raw_samples;
}

int tipsy_x11_test_last_pump_warps(void) {
	return tipsy_test_last_pump_warps;
}

// Simulate one pump pass of N valid captured XI2 samples without a real X
// grab or XWarpPointer. Used when the desktop already holds the pointer.
int tipsy_x11_test_coalesce_raw_pump(int n) {
	if (n < 2 || n >= TIPSY_INPUT_RING) {
		return -1;
	}
	pthread_mutex_lock(&tipsy_event_mu);
	struct tipsy_pointer_capture saved = tipsy_capture;
	Display *fake = (Display *)(uintptr_t)1;
	Window win = 1;
	memset(&tipsy_capture, 0, sizeof(tipsy_capture));
	tipsy_capture.active = 1;
	tipsy_capture.raw_motion = 1;
	tipsy_capture.dpy = fake;
	tipsy_capture.win = win;
	tipsy_capture.anchor_x = 160;
	tipsy_capture.anchor_y = 90;
	tipsy_capture.last_x = 160;
	tipsy_capture.last_y = 90;
	tipsy_capture.have_last = 1;

	tipsy_x11_input_test_clear();

	unsigned char mask[4];
	memset(mask, 0, sizeof(mask));
	XISetMask(mask, 0);
	XISetMask(mask, 1);
	double values[2] = {1.0, 0.0};
	XIRawEvent raw;
	memset(&raw, 0, sizeof(raw));
	raw.valuators.mask = mask;
	raw.valuators.mask_len = 1;
	raw.valuators.values = values;

	tipsy_warp_dry_run = 1;
	tipsy_test_last_pump_raw_samples = 0;
	tipsy_test_last_pump_warps = 0;
	tipsy_in_pump = 1;
	for (int i = 0; i < n; i++) {
		raw.time = (Time)(i + 1);
		tipsy_handle_raw_motion(fake, win, &raw);
	}
	tipsy_flush_pending_raw_warp(fake, win);
	tipsy_in_pump = 0;
	tipsy_warp_dry_run = 0;

	tipsy_capture = saved;
	pthread_mutex_unlock(&tipsy_event_mu);
	return 0;
}

int tipsy_x11_input_test_text_slots_clean(void) {
	int clean = 1;
	pthread_mutex_lock(&tipsy_input_mu);
	for (int i = 0; i < TIPSY_INPUT_RING && clean; i++) {
		for (int j = 0; j < TIPSY_INPUT_TEXT_BYTES; j++) {
			if (tipsy_input_text[i][j] != 0) {
				clean = 0;
				break;
			}
		}
	}
	pthread_mutex_unlock(&tipsy_input_mu);
	return clean;
}

int tipsy_x11_input_ev_size(void) {
	return (int)sizeof(struct tipsy_input_ev);
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

int tipsy_x11_request_fullscreen(uintptr_t dpy_ptr, unsigned long win, int enabled) {
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
	int min_width, int min_height,
	int place_x, int place_y, int use_position,
	const unsigned long *icon, int icon_len,
	uintptr_t *out_dpy, unsigned long *out_xid, unsigned long *out_delete,
	int *out_randr_event_base) {
	tipsy_x11_once();
	tipsy_x_error_code = 0;
	tipsy_x_io_error = 0;
	tipsy_f11_down = 0;
	tipsy_window_min_width = min_width > 0 ? min_width : 1;
	tipsy_window_min_height = min_height > 0 ? min_height : 1;
	memset(tipsy_keys, 0, sizeof(tipsy_keys));
	memset(&tipsy_capture, 0, sizeof(tipsy_capture));
	tipsy_have_keyboard_focus = 0;

	Display *dpy = XOpenDisplay(NULL);
	if (dpy == NULL) {
		return -1;
	}
	XSetIOErrorExitHandler(dpy, tipsy_xioexit, NULL);
	tipsy_x11_init_xi2(dpy);
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
	tipsy_atom_net_active_window = XInternAtom(dpy, "_NET_ACTIVE_WINDOW", False);
	// EWMH active-window changes are the XWayland path where compositor
	// focus moves on but X11 FocusOut never arrives for a grabbed pointer.
	XSelectInput(dpy, root, PropertyChangeMask);
	atomic_fetch_add_explicit(&tipsy_refresh_version, 1, memory_order_relaxed);

	XSetWindowAttributes swa;
	memset(&swa, 0, sizeof(swa));
	swa.background_pixel = black;
	swa.border_pixel = black;
	swa.colormap = DefaultColormap(dpy, screen);
	// WM_NORMAL_HINTS below is the X11 standard governing interactive title-bar
	// resizes. Direct foreign XResizeWindow requests are intentionally not
	// redirected here: ResizeRedirectMask also blocks the window manager's
	// legitimate fullscreen, restore, and ordinary valid-resize requests.
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
		sh->min_width = tipsy_window_min_width;
		sh->min_height = tipsy_window_min_height;
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
	tipsy_test_last_pump_raw_samples = 0;
	tipsy_test_last_pump_warps = 0;
	tipsy_in_pump = 1;
	while (XPending(dpy) > 0) {
		XEvent ev;
		XNextEvent(dpy, &ev);
		if (ev.type == GenericEvent && ev.xcookie.extension == tipsy_xi_opcode &&
			tipsy_xi_opcode != 0 && XGetEventData(dpy, &ev.xcookie)) {
			if (ev.xcookie.evtype == XI_RawMotion) {
				tipsy_handle_raw_motion(dpy, win,
					(const XIRawEvent *)ev.xcookie.data);
			}
			XFreeEventData(dpy, &ev.xcookie);
			continue;
		}
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
			if (ev.xfocus.window == win &&
				ev.xfocus.detail != NotifyPointer &&
				ev.xfocus.detail != NotifyPointerRoot &&
				ev.xfocus.detail != NotifyInferior) {
				if (ev.type == FocusOut) {
					tipsy_have_keyboard_focus = 0;
					tipsy_release_keys();
					tipsy_pointer_unlock(dpy, win, 1, 1);
					if (tipsy_xic != NULL) {
						XUnsetICFocus(tipsy_xic);
					}
				} else {
					// Re-apply a sticky first-person grab on the same FocusIn.
					tipsy_have_keyboard_focus = 1;
					tipsy_capture.failed_status = 0;
					if (tipsy_xic != NULL) {
						XSetICFocus(tipsy_xic);
					}
					if (tipsy_capture.sticky) {
						(void)tipsy_pointer_lock_apply(dpy, win, tipsy_capture.center, NULL);
					}
				}
				tipsy_input_push(TIPSY_INPUT_FOCUS,
					ev.type == FocusIn ? 1 : 0, 0, 0, 0, 0);
			}
			break;
		case PropertyNotify:
			if (ev.xproperty.atom == tipsy_atom_net_active_window &&
				ev.xproperty.window == DefaultRootWindow(dpy)) {
				if (ev.xproperty.state == PropertyDelete) {
					tipsy_apply_host_focus(dpy, win, 0);
					break;
				}
				Window active = tipsy_ewmh_active_window(dpy);
				tipsy_apply_host_focus(dpy, win,
					active != None && tipsy_window_is_related(dpy, win, active));
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
						// Remember the click for a following held-RMB grab.
						// Do not steal an active first-person center anchor.
						if (!(tipsy_capture.active && tipsy_capture.center)) {
							tipsy_capture.anchor_x = ev.xbutton.x;
							tipsy_capture.anchor_y = ev.xbutton.y;
						}
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
					// RawMotion and the corresponding core motion have one server
					// timestamp. Prefer the XI2 float valuators for that sample;
					// otherwise this core motion is the necessary compatibility
					// fallback when an enabled raw stream is quiet or unusable.
					if (tipsy_capture.raw_motion &&
						tipsy_capture.last_raw_motion_time != CurrentTime &&
						ev.xmotion.time == tipsy_capture.last_raw_motion_time) {
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
						tipsy_input_push_relative((float)dx, (float)dy,
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
				// A compliant window manager honors WM_NORMAL_HINTS before this
				// point. Retain this filter for direct X11 configuration requests:
				// even if a foreign client transiently creates an unsafe drawable,
				// never forward it into Android/GameActivity.
				if (ev.xconfigure.width < tipsy_window_min_width ||
					ev.xconfigure.height < tipsy_window_min_height) {
					int width = ev.xconfigure.width < tipsy_window_min_width ?
						tipsy_window_min_width : ev.xconfigure.width;
					int height = ev.xconfigure.height < tipsy_window_min_height ?
						tipsy_window_min_height : ev.xconfigure.height;
					XResizeWindow(dpy, win, (unsigned)width, (unsigned)height);
					XFlush(dpy);
					break;
				}
				atomic_fetch_add_explicit(&tipsy_refresh_version, 1, memory_order_relaxed);
				if (tipsy_capture.active) {
					int ax = 0, ay = 0;
					if (tipsy_capture.center) {
						tipsy_capture_center(ev.xconfigure.width, ev.xconfigure.height,
							&ax, &ay);
					} else {
						ax = tipsy_clamp_coord(tipsy_capture.anchor_x,
							ev.xconfigure.width);
						ay = tipsy_clamp_coord(tipsy_capture.anchor_y,
							ev.xconfigure.height);
					}
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
	tipsy_flush_pending_raw_warp(dpy, win);
	tipsy_in_pump = 0;
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
	tipsy_have_keyboard_focus = 0;
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
