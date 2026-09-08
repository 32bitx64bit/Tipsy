/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "probe.h"

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <X11/XKBlib.h>
#include <X11/extensions/XTest.h>
#include <X11/extensions/Xrandr.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static Display *probe_dpy = NULL;

int probe_open(void) {
	if (probe_dpy != NULL) {
		return 0;
	}
	probe_dpy = XOpenDisplay(NULL);
	return probe_dpy == NULL ? -1 : 0;
}

void probe_close(void) {
	if (probe_dpy != NULL) {
		XUngrabPointer(probe_dpy, CurrentTime);
		XCloseDisplay(probe_dpy);
		probe_dpy = NULL;
	}
}

static int probe_send(XEvent *ev, long mask) {
	int rc = XSendEvent(probe_dpy, ev->xfocus.window, False, mask, ev);
	// No event loop runs on this connection: flush the queued request.
	XFlush(probe_dpy);
	return rc == 0 ? -1 : 0;
}

int probe_focus(unsigned long xid, int gained) {
	if (probe_dpy == NULL) return -1;
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xfocus.type = gained ? FocusIn : FocusOut;
	ev.xfocus.display = probe_dpy;
	ev.xfocus.window = (Window)xid;
	ev.xfocus.mode = NotifyNormal;
	ev.xfocus.detail = NotifyDetailNone;
	return probe_send(&ev, FocusChangeMask);
}

// probe_set_focus performs a REAL focus change (XSetInputFocus from the
// probe connection), which makes the server generate a genuine FocusIn.
int probe_set_focus(unsigned long xid) {
	if (probe_dpy == NULL) return -1;
	XSetInputFocus(probe_dpy, (Window)xid, RevertToParent, CurrentTime);
	XSync(probe_dpy, False);
	return 0;
}

unsigned long probe_query_focus(void) {
	if (probe_dpy == NULL) return 0;
	Window focus = 0;
	int revert = 0;
	XGetInputFocus(probe_dpy, &focus, &revert);
	return (unsigned long)focus;
}

int probe_key(unsigned long xid, unsigned long keysym, int pressed, unsigned long timestamp) {
	if (probe_dpy == NULL) return -1;
	KeyCode kc = XKeysymToKeycode(probe_dpy, (KeySym)keysym);
	if (kc == 0) return -2;
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xkey.type = pressed ? KeyPress : KeyRelease;
	ev.xkey.display = probe_dpy;
	ev.xkey.window = (Window)xid;
	ev.xkey.root = RootWindow(probe_dpy, DefaultScreen(probe_dpy));
	ev.xkey.subwindow = 0;
	ev.xkey.time = timestamp;
	ev.xkey.x = ev.xkey.y = 1;
	ev.xkey.x_root = ev.xkey.y_root = 1;
	ev.xkey.keycode = kc;
	ev.xkey.state = 0;
	ev.xkey.same_screen = True;
	return probe_send(&ev, pressed ? KeyPressMask : KeyReleaseMask);
}

// Tests call this on the window connection before starting its pump.
int probe_detectable_repeat(uintptr_t display, int enabled) {
	Bool supported = False;
	Bool state = XkbSetDetectableAutoRepeat((Display *)display, enabled, &supported);
	return supported && state == enabled ? 0 : -1;
}

int probe_repeat_rate(unsigned int delay, unsigned int interval) {
	if (probe_dpy == NULL) return -1;
	if (!XkbSetAutoRepeatRate(probe_dpy, XkbUseCoreKbd, delay, interval)) return -1;
	XAutoRepeatOn(probe_dpy);
	XSync(probe_dpy, False);
	return 0;
}

int probe_real_key(unsigned long keysym, int pressed) {
	if (probe_dpy == NULL) return -1;
	KeyCode code = XKeysymToKeycode(probe_dpy, (KeySym)keysym);
	if (code == 0 || !XTestFakeKeyEvent(probe_dpy, code, pressed, CurrentTime)) return -1;
	XSync(probe_dpy, False);
	return 0;
}

void probe_sync(void) {
	if (probe_dpy != NULL) XSync(probe_dpy, False);
}

int probe_button(unsigned long xid, int x, int y, unsigned int button, int pressed) {
	if (probe_dpy == NULL) return -1;
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xbutton.type = pressed ? ButtonPress : ButtonRelease;
	ev.xbutton.display = probe_dpy;
	ev.xbutton.window = (Window)xid;
	ev.xbutton.root = RootWindow(probe_dpy, DefaultScreen(probe_dpy));
	ev.xbutton.time = CurrentTime;
	ev.xbutton.x = x;
	ev.xbutton.y = y;
	ev.xbutton.x_root = x;
	ev.xbutton.y_root = y;
	ev.xbutton.button = button;
	ev.xbutton.state = pressed ? 0 : button;
	ev.xbutton.same_screen = True;
	return probe_send(&ev, pressed ? ButtonPressMask : ButtonReleaseMask);
}

int probe_motion(unsigned long xid, int x, int y, unsigned int state) {
	if (probe_dpy == NULL) return -1;
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xmotion.type = MotionNotify;
	ev.xmotion.display = probe_dpy;
	ev.xmotion.window = (Window)xid;
	ev.xmotion.root = RootWindow(probe_dpy, DefaultScreen(probe_dpy));
	ev.xmotion.time = CurrentTime;
	ev.xmotion.x = x;
	ev.xmotion.y = y;
	ev.xmotion.x_root = x;
	ev.xmotion.y_root = y;
	ev.xmotion.state = state;
	ev.xmotion.is_hint = False;
	ev.xmotion.same_screen = True;
	return probe_send(&ev, PointerMotionMask);
}

int probe_warp_pointer(unsigned long xid, int x, int y) {
	if (probe_dpy == NULL) return -1;
	XWarpPointer(probe_dpy, None, (Window)xid, 0, 0, 0, 0, x, y);
	XSync(probe_dpy, False);
	return 0;
}

// probe_relative_motion emits a real relative XTEST motion edge. It exercises
// the XI2 RawMotion route used by pointer lock, unlike XSendEvent MotionNotify
// (which is intentionally only a core-event decoder test).
int probe_relative_motion(int dx, int dy) {
	if (probe_dpy == NULL) return -1;
	if (!XTestFakeRelativeMotionEvent(probe_dpy, dx, dy, CurrentTime)) return -1;
	XSync(probe_dpy, False);
	return 0;
}

int probe_query_pointer(unsigned long xid, int *out_x, int *out_y) {
	if (probe_dpy == NULL) return -1;
	Window root = 0, child = 0;
	int root_x = 0, root_y = 0, x = 0, y = 0;
	unsigned int mask = 0;
	if (!XQueryPointer(probe_dpy, (Window)xid, &root, &child,
		&root_x, &root_y, &x, &y, &mask)) return -2;
	if (out_x != NULL) *out_x = x;
	if (out_y != NULL) *out_y = y;
	return 0;
}

int probe_window_root_origin(unsigned long xid, int *out_x, int *out_y) {
	if (probe_dpy == NULL) return -1;
	Window child = 0;
	int x = 0, y = 0;
	if (!XTranslateCoordinates(probe_dpy, (Window)xid,
		DefaultRootWindow(probe_dpy), 0, 0, &x, &y, &child)) {
		return -2;
	}
	if (out_x != NULL) *out_x = x;
	if (out_y != NULL) *out_y = y;
	return 0;
}

int probe_grab_pointer(unsigned long xid) {
	if (probe_dpy == NULL) return -1;
	int status = XGrabPointer(probe_dpy, (Window)xid, False,
		ButtonPressMask | ButtonReleaseMask | PointerMotionMask,
		GrabModeAsync, GrabModeAsync, (Window)xid, None, CurrentTime);
	XSync(probe_dpy, False);
	return status;
}

void probe_ungrab_pointer(void) {
	if (probe_dpy == NULL) return;
	XUngrabPointer(probe_dpy, CurrentTime);
	XSync(probe_dpy, False);
}

int probe_move(unsigned long xid, int x, int y) {
	if (probe_dpy == NULL) return -1;
	XMoveWindow(probe_dpy, (Window)xid, x, y);
	XSync(probe_dpy, False);
	return 0;
}

// Emit a real output-property RandR notification on an isolated test server.
// No mode or connected-output state is changed. Creation and deletion both
// notify clients that selected RROutputPropertyNotifyMask on the root.
int probe_randr_output_property(void) {
	if (probe_dpy == NULL) return -1;
	int event_base, error_base;
	if (!XRRQueryExtension(probe_dpy, &event_base, &error_base)) return -2;
	XRRScreenResources *res = XRRGetScreenResourcesCurrent(probe_dpy,
		DefaultRootWindow(probe_dpy));
	if (res == NULL) return -2;
	if (res->noutput == 0) {
		XRRFreeScreenResources(res);
		return -2;
	}
	Atom prop = XInternAtom(probe_dpy, "_TIPSY_TEST_REFRESH_INVALIDATION", False);
	unsigned char value = 1;
	XRRChangeOutputProperty(probe_dpy, res->outputs[0], prop, XA_INTEGER,
		8, PropModeReplace, &value, 1);
	XRRDeleteOutputProperty(probe_dpy, res->outputs[0], prop);
	XRRFreeScreenResources(res);
	XSync(probe_dpy, False);
	return 0;
}

int probe_resize(unsigned long xid, unsigned int width, unsigned int height) {
	if (probe_dpy == NULL || width == 0 || height == 0) return -1;
	XResizeWindow(probe_dpy, (Window)xid, width, height);
	XSync(probe_dpy, False);
	// A reparenting WM handles the resize request asynchronously. Do not send
	// the test's following synthetic pointer until the real client geometry
	// has changed; a physical pointer cannot report coordinates in a future
	// geometry either.
	for (int i = 0; i < 200; i++) {
		XWindowAttributes attr;
		if (XGetWindowAttributes(probe_dpy, (Window)xid, &attr) != 0 &&
			(unsigned int)attr.width == width &&
			(unsigned int)attr.height == height) {
			return 0;
		}
		XSync(probe_dpy, False);
		usleep(10000);
	}
	return -2;
}

int probe_window_size(unsigned long xid, int *out_width, int *out_height) {
	if (probe_dpy == NULL || out_width == NULL || out_height == NULL) return -1;
	XWindowAttributes attr;
	if (XGetWindowAttributes(probe_dpy, (Window)xid, &attr) == 0) return -2;
	*out_width = attr.width;
	*out_height = attr.height;
	return 0;
}

int probe_window_min_size(unsigned long xid, int *out_width, int *out_height) {
	if (probe_dpy == NULL || out_width == NULL || out_height == NULL) return -1;
	XSizeHints hints;
	long supplied = 0;
	if (!XGetWMNormalHints(probe_dpy, (Window)xid, &hints, &supplied) ||
		!(hints.flags & PMinSize)) return -2;
	*out_width = hints.min_width;
	*out_height = hints.min_height;
	return 0;
}

int probe_wm_delete(unsigned long xid) {
	if (probe_dpy == NULL) return -1;
	XEvent ev;
	memset(&ev, 0, sizeof(ev));
	ev.xclient.type = ClientMessage;
	ev.xclient.display = probe_dpy;
	ev.xclient.window = (Window)xid;
	ev.xclient.message_type = XInternAtom(probe_dpy, "WM_PROTOCOLS", False);
	ev.xclient.format = 32;
	ev.xclient.data.l[0] = (long)XInternAtom(probe_dpy, "WM_DELETE_WINDOW", False);
	ev.xclient.data.l[1] = (long)CurrentTime;
	return probe_send(&ev, NoEventMask);
}

int probe_is_viewable(unsigned long xid) {
	if (probe_dpy == NULL) return 0;
	XWindowAttributes attr;
	if (XGetWindowAttributes(probe_dpy, (Window)xid, &attr) == 0) return 0;
	return attr.map_state == IsViewable;
}

int probe_get_utf8_title(unsigned long xid, char *out, int cap) {
	if (probe_dpy == NULL || out == NULL || cap < 1) return -1;
	Atom prop = XInternAtom(probe_dpy, "_NET_WM_NAME", False);
	Atom actual = None;
	int format = 0;
	unsigned long count = 0, remaining = 0;
	unsigned char *raw = NULL;
	int rc = XGetWindowProperty(probe_dpy, (Window)xid, prop, 0, 1024,
		False, AnyPropertyType, &actual, &format, &count, &remaining, &raw);
	if (rc != Success || format != 8 || raw == NULL) {
		if (raw != NULL) XFree(raw);
		return -1;
	}
	int n = (int)count;
	if (n >= cap) n = cap - 1;
	memcpy(out, raw, (size_t)n);
	out[n] = '\0';
	XFree(raw);
	return n;
}

int probe_get_icon_info(unsigned long xid, unsigned long *out_width,
	unsigned long *out_height, unsigned long *out_items) {
	if (probe_dpy == NULL) return -1;
	Atom prop = XInternAtom(probe_dpy, "_NET_WM_ICON", False);
	Atom actual = None;
	int format = 0;
	unsigned long count = 0, remaining = 0;
	unsigned char *raw = NULL;
	int rc = XGetWindowProperty(probe_dpy, (Window)xid, prop, 0, 2,
		False, XA_CARDINAL, &actual, &format, &count, &remaining, &raw);
	if (rc != Success || actual != XA_CARDINAL || format != 32 ||
		count < 2 || raw == NULL) {
		if (raw != NULL) XFree(raw);
		return -1;
	}
	unsigned long *items = (unsigned long *)raw;
	if (out_width != NULL) *out_width = items[0];
	if (out_height != NULL) *out_height = items[1];
	if (out_items != NULL) *out_items = count + remaining / 4;
	XFree(raw);
	return 0;
}

int probe_has_fullscreen(unsigned long xid) {
	if (probe_dpy == NULL) return 0;
	Atom state = XInternAtom(probe_dpy, "_NET_WM_STATE", False);
	Atom fullscreen = XInternAtom(probe_dpy, "_NET_WM_STATE_FULLSCREEN", False);
	Atom actual = None;
	int format = 0;
	unsigned long count = 0, remaining = 0;
	unsigned char *raw = NULL;
	int found = 0;
	if (XGetWindowProperty(probe_dpy, (Window)xid, state, 0, 1024,
		False, XA_ATOM, &actual, &format, &count, &remaining, &raw) == Success &&
		actual == XA_ATOM && format == 32 && raw != NULL) {
		Atom *items = (Atom *)raw;
		for (unsigned long i = 0; i < count; i++) {
			if (items[i] == fullscreen) {
				found = 1;
				break;
			}
		}
	}
	if (raw != NULL) XFree(raw);
	return found;
}

int probe_window_manager_present(void) {
	if (probe_dpy == NULL) return 0;
	Window root = RootWindow(probe_dpy, DefaultScreen(probe_dpy));
	Atom check = XInternAtom(probe_dpy, "_NET_SUPPORTING_WM_CHECK", False);
	Atom actual = None;
	int format = 0;
	unsigned long count = 0, remaining = 0;
	unsigned char *raw = NULL;
	int present = 0;
	if (XGetWindowProperty(probe_dpy, root, check, 0, 1, False, XA_WINDOW,
		&actual, &format, &count, &remaining, &raw) == Success &&
		actual == XA_WINDOW && format == 32 && count == 1 && raw != NULL) {
		present = (*(unsigned long *)raw) != 0;
	}
	if (raw != NULL) XFree(raw);
	return present;
}
