// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package x11probe sends synthetic X11 input events to a window for tests.
// It exists because cgo is forbidden in x11 package _test.go files (the
// same constraint that created internal/graphics/flickercap). XSendEvent
// marks events send_event=true; the x11 decoder accepts both synthetic and
// real events. Test and diagnostics use only.
package x11probe

/*
#cgo pkg-config: x11 xrandr xtst
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
*/
import "C"

import (
	"errors"
	"time"
	"unsafe"
)

// ErrProbe is returned when the probe connection is unavailable.
var ErrProbe = errors.New("x11probe: unavailable")

func mustOpen() error {
	if C.probe_open() != 0 {
		return ErrProbe
	}
	return nil
}

// Open connects the probe's private X display.
func Open() error { return mustOpen() }

// Close disconnects the probe display.
func Close() { C.probe_close() }

// Focus sends a synthetic FocusIn/FocusOut for xid.
func Focus(xid uintptr, gained bool) error {
	if err := mustOpen(); err != nil {
		return err
	}
	g := C.int(0)
	if gained {
		g = 1
	}
	if C.probe_focus(C.ulong(xid), g) != 0 {
		return ErrProbe
	}
	return nil
}

// SetFocus performs a real XSetInputFocus from the probe connection.
func SetFocus(xid uintptr) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_set_focus(C.ulong(xid)) != 0 {
		return ErrProbe
	}
	return nil
}

// QueryFocus returns the server's current input-focus window.
func QueryFocus() uintptr {
	if err := mustOpen(); err != nil {
		return 0
	}
	return uintptr(C.probe_query_focus())
}

// Key sends a synthetic key press/release for keysym.
func Key(xid uintptr, keysym uint64, pressed bool) error {
	return KeyAt(xid, keysym, pressed, 0)
}

// KeyAt sends an event with an explicit server timestamp for legacy repeat tests.
func KeyAt(xid uintptr, keysym uint64, pressed bool, timestamp uint32) error {
	if err := mustOpen(); err != nil {
		return err
	}
	p := C.int(0)
	if pressed {
		p = 1
	}
	rc := C.probe_key(C.ulong(xid), C.ulong(keysym), p, C.ulong(timestamp))
	if rc == -2 {
		return errors.New("x11probe: keysym not in server keymap")
	}
	if rc != 0 {
		return ErrProbe
	}
	return nil
}

// Button sends a synthetic button press/release at (x, y).
func Button(xid uintptr, x, y int, button uint64, pressed bool) error {
	if err := mustOpen(); err != nil {
		return err
	}
	p := C.int(0)
	if pressed {
		p = 1
	}
	if C.probe_button(C.ulong(xid), C.int(x), C.int(y), C.uint(button), p) != 0 {
		return ErrProbe
	}
	return nil
}

// Motion sends a synthetic motion event with the given button state
// (e.g. 0x100 = Button1Mask) so the decoder treats it as drag motion.
func Motion(xid uintptr, x, y int, state uint64) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_motion(C.ulong(xid), C.int(x), C.int(y), C.uint(state)) != 0 {
		return ErrProbe
	}
	return nil
}

// WarpPointer moves the real server pointer to window-relative coordinates.
// Pointer-lock tests use it to exercise grab, relative delta, and recentering
// behavior without reading or generating user input.
func WarpPointer(xid uintptr, x, y int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_warp_pointer(C.ulong(xid), C.int(x), C.int(y)) != 0 {
		return ErrProbe
	}
	return nil
}

// RelativeMotion emits one real relative pointer movement through XTEST.
// It is limited to X11 integration tests and never inspects user input.
func RelativeMotion(dx, dy int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_relative_motion(C.int(dx), C.int(dy)) != 0 {
		return ErrProbe
	}
	return nil
}

// PointerPosition returns the real server pointer in window coordinates.
func PointerPosition(xid uintptr) (x, y int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var px, py C.int
	if C.probe_query_pointer(C.ulong(xid), &px, &py) != 0 {
		return 0, 0, ErrProbe
	}
	return int(px), int(py), nil
}

// WindowRootOrigin returns the window's top-left corner in root coordinates.
func WindowRootOrigin(xid uintptr) (x, y int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var px, py C.int
	if C.probe_window_root_origin(C.ulong(xid), &px, &py) != 0 {
		return 0, 0, ErrProbe
	}
	return int(px), int(py), nil
}

// GrabPointer acquires a competing grab from the probe connection. Tests use
// it to verify that Tipsy reports grab rejection without swallowing edges.
func GrabPointer(xid uintptr) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_grab_pointer(C.ulong(xid)) != 0 {
		return ErrProbe
	}
	return nil
}

// UngrabPointer releases a probe-owned grab.
func UngrabPointer() { C.probe_ungrab_pointer() }

// Resize asks X11 to resize the client window. Tests use the resulting real
// ConfigureNotify to assert resize/pointer ordering in the capture bridge.
func Resize(xid uintptr, width, height int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if width <= 0 || height <= 0 || C.probe_resize(C.ulong(xid), C.uint(width), C.uint(height)) != 0 {
		return ErrProbe
	}
	return nil
}

// WindowSize reports the current server-side client geometry in X11 pixels.
func WindowSize(xid uintptr) (width, height int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var w, h C.int
	if C.probe_window_size(C.ulong(xid), &w, &h) != 0 {
		return 0, 0, ErrProbe
	}
	return int(w), int(h), nil
}

// WindowMinimumSize reports the WM_NORMAL_HINTS minimum client geometry.
func WindowMinimumSize(xid uintptr) (width, height int, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, err
	}
	var w, h C.int
	if C.probe_window_min_size(C.ulong(xid), &w, &h) != 0 {
		return 0, 0, ErrProbe
	}
	return int(w), int(h), nil
}

// WMDelete sends the standards-defined ICCCM request used by a window
// manager's title-bar close button.
func WMDelete(xid uintptr) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_wm_delete(C.ulong(xid)) != 0 {
		return ErrProbe
	}
	return nil
}

// Viewable reports whether the server currently considers xid mapped and
// viewable.
func Viewable(xid uintptr) bool {
	if err := mustOpen(); err != nil {
		return false
	}
	return C.probe_is_viewable(C.ulong(xid)) != 0
}

// WindowTitle returns the modern EWMH UTF-8 title for xid.
func WindowTitle(xid uintptr) (string, error) {
	if err := mustOpen(); err != nil {
		return "", err
	}
	var buf [1024]C.char
	n := C.probe_get_utf8_title(C.ulong(xid), &buf[0], C.int(len(buf)))
	if n < 0 {
		return "", ErrProbe
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(&buf[0])), n), nil
}

// WindowIconInfo returns the first EWMH icon dimensions and total 32-bit
// property item count.
func WindowIconInfo(xid uintptr) (width, height, items uint64, err error) {
	if err = mustOpen(); err != nil {
		return 0, 0, 0, err
	}
	var w, h, n C.ulong
	if C.probe_get_icon_info(C.ulong(xid), &w, &h, &n) != 0 {
		return 0, 0, 0, ErrProbe
	}
	return uint64(w), uint64(h), uint64(n), nil
}

// WindowManagerPresent reports whether the root window advertises an EWMH WM.
func WindowManagerPresent() bool {
	if err := mustOpen(); err != nil {
		return false
	}
	return C.probe_window_manager_present() != 0
}

// Fullscreen reports whether _NET_WM_STATE currently contains FULLSCREEN.
func Fullscreen(xid uintptr) bool {
	if err := mustOpen(); err != nil {
		return false
	}
	return C.probe_has_fullscreen(C.ulong(xid)) != 0
}

// WaitFullscreen waits for the asynchronous EWMH state transition.
func WaitFullscreen(xid uintptr, enabled bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if Fullscreen(xid) == enabled {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return Fullscreen(xid) == enabled
}

// Move asks X11 to move the client window and emit ConfigureNotify.
func Move(xid uintptr, x, y int) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_move(C.ulong(xid), C.int(x), C.int(y)) != 0 {
		return ErrProbe
	}
	return nil
}

// RandROutputProperty emits a real RandR output-property notification. Only
// call on a private test display: this briefly creates and deletes a test
// property on an output, without changing the output mode or connection.
func RandROutputProperty() error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_randr_output_property() != 0 {
		return ErrProbe
	}
	return nil
}

// DetectableRepeat changes only the given client connection. Its pump must be stopped.
func DetectableRepeat(display uintptr, enabled bool) error {
	flag := C.int(0)
	if enabled {
		flag = 1
	}
	if C.probe_detectable_repeat(C.uintptr_t(display), flag) != 0 {
		return ErrProbe
	}
	return nil
}

// RepeatRate changes the server's repeat settings. Use only on a private test server.
func RepeatRate(delay, interval uint32) error {
	if err := mustOpen(); err != nil {
		return err
	}
	if C.probe_repeat_rate(C.uint(delay), C.uint(interval)) != 0 {
		return ErrProbe
	}
	return nil
}

// RealKey injects a physical transition through XTest; the server generates repeats.
// Use only on a private test server and always release a pressed key.
func RealKey(keysym uint64, pressed bool) error {
	if err := mustOpen(); err != nil {
		return err
	}
	flag := C.int(0)
	if pressed {
		flag = 1
	}
	if C.probe_real_key(C.ulong(keysym), flag) != 0 {
		return ErrProbe
	}
	return nil
}

// Sync waits for the server to process all preceding probe requests.
func Sync() { C.probe_sync() }
