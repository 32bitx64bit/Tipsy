// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package x11probe sends synthetic X11 input events to a window for tests.
// It exists because cgo is forbidden in x11 package _test.go files (the
// same constraint that created internal/graphics/flickercap). XSendEvent
// marks events send_event=true; the x11 decoder accepts both synthetic and
// real events. Test and diagnostics use only.
package x11probe

/*
#cgo pkg-config: x11
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <stdlib.h>
#include <string.h>

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
	XFlush(probe_dpy);
	return 0;
}

unsigned long probe_query_focus(void) {
	if (probe_dpy == NULL) return 0;
	Window focus = 0;
	int revert = 0;
	XGetInputFocus(probe_dpy, &focus, &revert);
	return (unsigned long)focus;
}

int probe_key(unsigned long xid, unsigned long keysym, int pressed) {
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
	ev.xkey.time = CurrentTime;
	ev.xkey.x = ev.xkey.y = 1;
	ev.xkey.x_root = ev.xkey.y_root = 1;
	ev.xkey.keycode = kc;
	ev.xkey.state = 0;
	ev.xkey.same_screen = True;
	return probe_send(&ev, pressed ? KeyPressMask : KeyReleaseMask);
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

int probe_resize(unsigned long xid, unsigned int width, unsigned int height) {
	if (probe_dpy == NULL || width == 0 || height == 0) return -1;
	XResizeWindow(probe_dpy, (Window)xid, width, height);
	XSync(probe_dpy, False);
	return 0;
}
*/
import "C"

import "errors"

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
	if err := mustOpen(); err != nil {
		return err
	}
	p := C.int(0)
	if pressed {
		p = 1
	}
	rc := C.probe_key(C.ulong(xid), C.ulong(keysym), p)
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
