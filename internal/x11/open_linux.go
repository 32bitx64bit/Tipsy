// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: x11
#cgo LDFLAGS: -lX11 -pthread

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <locale.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

static int tipsy_x_error_code;
static int tipsy_x_io_error;
static int tipsy_x_inited;
static XIM tipsy_xim;
static XIC tipsy_xic;

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
// resize:  b = width, c = height. ConfigureNotify lives in this stream so
//        the Android surface is resized before subsequent pointer events.
// text: committed UTF-8 from X11's input method. It follows the originating
//        physical KeyPress and is never logged. This preserves the host
//        keyboard layout instead of reconstructing text from a keycode.
#define TIPSY_INPUT_FOCUS 0
#define TIPSY_INPUT_KEY 1
#define TIPSY_INPUT_POINTER 2
#define TIPSY_INPUT_RESIZE 3
#define TIPSY_INPUT_TEXT 4
#define TIPSY_INPUT_RING 256
#define TIPSY_INPUT_TEXT_BYTES 256
#define TIPSY_X11_BACKGROUND_POLL_USEC 2000

struct tipsy_input_ev {
	int kind;
	int a;
	long b;
	long c;
	float x;
	float y;
	int text_len;
	char text[TIPSY_INPUT_TEXT_BYTES];
};

static struct tipsy_input_ev tipsy_input_ring[TIPSY_INPUT_RING];
static int tipsy_input_head;
static int tipsy_input_tail;
static pthread_mutex_t tipsy_input_mu = PTHREAD_MUTEX_INITIALIZER;

static void tipsy_input_push(int kind, int a, long b, long c, float x, float y) {
	pthread_mutex_lock(&tipsy_input_mu);
	// Coalesce: a queued pending move is replaced by the newest move.
	int last = (tipsy_input_head - 1 + TIPSY_INPUT_RING) % TIPSY_INPUT_RING;
	if (kind == TIPSY_INPUT_POINTER && a == 2 &&
		tipsy_input_head != tipsy_input_tail &&
		tipsy_input_ring[last].kind == TIPSY_INPUT_POINTER &&
		tipsy_input_ring[last].a == 2) {
		tipsy_input_ring[last].x = x;
		tipsy_input_ring[last].y = y;
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
	tipsy_input_ring[tipsy_input_head].text_len = 0;
	tipsy_input_head = next;
	pthread_mutex_unlock(&tipsy_input_mu);
}

static void tipsy_input_push_text(const char *text, int len) {
	if (text == NULL || len <= 0) {
		return;
	}
	if (len > TIPSY_INPUT_TEXT_BYTES) {
		len = TIPSY_INPUT_TEXT_BYTES;
	}
	pthread_mutex_lock(&tipsy_input_mu);
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

int tipsy_x11_open(const char *title, int width, int height,
	uintptr_t *out_dpy, unsigned long *out_xid, unsigned long *out_delete) {
	tipsy_x11_once();
	tipsy_x_error_code = 0;
	tipsy_x_io_error = 0;

	Display *dpy = XOpenDisplay(NULL);
	if (dpy == NULL) {
		return -1;
	}
	XSetIOErrorExitHandler(dpy, tipsy_xioexit, NULL);

	int screen = DefaultScreen(dpy);
	Window root = RootWindow(dpy, screen);
	unsigned long black = BlackPixel(dpy, screen);

	XSetWindowAttributes swa;
	memset(&swa, 0, sizeof(swa));
	swa.background_pixel = black;
	swa.border_pixel = black;
	swa.colormap = DefaultColormap(dpy, screen);
	swa.event_mask = ExposureMask | StructureNotifyMask |
		FocusChangeMask | KeyPressMask | KeyReleaseMask |
		ButtonPressMask | ButtonReleaseMask | PointerMotionMask;

	Window win = XCreateWindow(dpy, root,
		0, 0, (unsigned)width, (unsigned)height, 0,
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

	if (title == NULL) {
		title = "";
	}
	XStoreName(dpy, win, title);

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
	int *inout_w, int *inout_h, int *out_closed) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || tipsy_x_io_error) {
		return -1;
	}
	Window win = (Window)xid;
	*out_closed = 0;
	while (XPending(dpy) > 0) {
		XEvent ev;
		XNextEvent(dpy, &ev);
		Bool filtered = XFilterEvent(&ev, win);
		switch (ev.type) {
		case Expose:
			break;
		case FocusIn:
		case FocusOut:
			if (ev.xfocus.window == win) {
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
				tipsy_input_push(TIPSY_INPUT_KEY,
					ev.type == KeyPress ? 1 : 0,
					tipsy_android_keycode(ks),
					(long)ev.xkey.keycode, 0, 0);
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
			if (ev.xbutton.window == win &&
				(ev.xbutton.button == Button1 ||
				 ev.xbutton.button == Button3)) {
				tipsy_input_push(TIPSY_INPUT_POINTER,
					ev.type == ButtonPress ? 0 : 1,
					(long)ev.xbutton.button, 0,
					(float)ev.xbutton.x, (float)ev.xbutton.y);
			}
			break;
		case MotionNotify:
			// PointerMotionMask selected at creation delivers ordinary hover
			// and button-held motion. Keep both: the direct Roblox listener is
			// a mouse contract, not the older GameActivity touch contract.
			if (ev.xmotion.window == win) {
				tipsy_input_push(TIPSY_INPUT_POINTER, 2, 0, 0,
					(float)ev.xmotion.x, (float)ev.xmotion.y);
			}
			break;
		case ConfigureNotify:
			if (ev.xconfigure.window == win) {
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
				(Atom)ev.xclient.data.l[0] == (Atom)wm_delete) {
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
				*out_closed = 1;
			}
			break;
		case MapNotify:
			break;
		default:
			break;
		}
	}
	XFlush(dpy);
	if (tipsy_x_io_error) {
		return -1;
	}
	return 0;
}

struct tipsy_pump {
	uintptr_t dpy;
	unsigned long xid;
	unsigned long del;
	int w;
	int h;
	volatile int run;
	volatile int closed;
	pthread_t thr;
};

static void *tipsy_pump_main(void *arg) {
	struct tipsy_pump *p = (struct tipsy_pump *)arg;
	while (p->run && !p->closed) {
		int closed = 0;
		if (tipsy_x11_pump(p->dpy, p->xid, p->del, &p->w, &p->h, &closed) != 0 || closed) {
			p->closed = 1;
			break;
		}
		usleep(TIPSY_X11_BACKGROUND_POLL_USEC);
	}
	return NULL;
}

uintptr_t tipsy_x11_pump_thread_start(uintptr_t dpy, unsigned long xid,
	unsigned long del, int w, int h) {
	struct tipsy_pump *p = (struct tipsy_pump *)calloc(1, sizeof(*p));
	if (p == NULL) {
		return 0;
	}
	p->dpy = dpy;
	p->xid = xid;
	p->del = del;
	p->w = w;
	p->h = h;
	p->run = 1;
	if (pthread_create(&p->thr, NULL, tipsy_pump_main, p) != 0) {
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
	pthread_join(p->thr, NULL);
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
}
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// TIPSYInputRingLen mirrors the C input ring capacity. The ring is
// process-wide; Tipsy owns one Roblox window per process.
const TIPSYInputRingLen = 256

// Open creates a mapped InputOutput window on the native X11 display.
func Open(title string, width, height int) (*Window, error) {
	if width < 1 || height < 1 {
		return nil, ErrInvalidSize
	}
	ctitle := C.CString(title)
	defer C.free(unsafe.Pointer(ctitle))

	var dpy C.uintptr_t
	var xid, del C.ulong
	rc := C.tipsy_x11_open(ctitle, C.int(width), C.int(height), &dpy, &xid, &del)
	if rc != 0 || dpy == 0 || xid == 0 {
		return nil, fmt.Errorf("%w (DISPLAY=%q)", ErrNoDisplay, os.Getenv("DISPLAY"))
	}

	w := &Window{
		display:  uintptr(dpy),
		xid:      uintptr(xid),
		wmDelete: uintptr(del),
		width:    width,
		height:   height,
	}
	// Roblox renders its own cursor. This transparent cursor is scoped to the
	// client window: leaving it returns to the host cursor automatically.
	w.cursor = uintptr(C.tipsy_x11_hide_cursor(C.uintptr_t(w.display), C.ulong(w.xid)))
	if w.cursor == 0 {
		logging.Logger(logging.CatX11).Info("X11 cursor hide unavailable")
	}
	_ = w.Pump()
	logging.Logger(logging.CatX11).Info("opened X11 window",
		"title", title, "width", w.width, "height", w.height, "xid", w.xid)
	return w, nil
}

// Pump processes pending X events without blocking.
func (w *Window) Pump() error {
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	if w.closed || w.display == 0 {
		w.mu.Unlock()
		return ErrClosed
	}
	if C.tipsy_x11_io_error() != 0 {
		w.closed = true
		w.mu.Unlock()
		return ErrClosed
	}

	cw := C.int(w.width)
	ch := C.int(w.height)
	var closed C.int
	rc := C.tipsy_x11_pump(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.wmDelete), &cw, &ch, &closed)
	w.width = int(cw)
	w.height = int(ch)
	if rc != 0 {
		w.closed = true
		w.mu.Unlock()
		return ErrClosed
	}
	evs := w.drainInputLocked()
	if closed != 0 {
		w.closed = true
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
func (w *Window) drainInputLocked() []InputEvent {
	var raw [TIPSYInputRingLen]C.struct_tipsy_input_ev
	n := int(C.tipsy_x11_input_drain(&raw[0], C.int(len(raw))))
	if n == 0 {
		return nil
	}
	evs := make([]InputEvent, 0, n)
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
			evs = append(evs, InputEvent{Kind: InputKey, KeyPressed: r.a != 0, KeyCode: int32(r.b), ScanCode: int32(r.c)})
		case C.TIPSY_INPUT_POINTER:
			action := PointerDown
			if r.a == 1 {
				action = PointerUp
			} else if r.a == 2 {
				action = PointerMove
			}
			evs = append(evs, InputEvent{Kind: InputPointer, PointerAction: action, Button: int32(r.b), X: float32(r.x), Y: float32(r.y)})
		case C.TIPSY_INPUT_RESIZE:
			width, height := int(r.b), int(r.c)
			if width <= 0 || height <= 0 {
				continue
			}
			w.width, w.height = width, height
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
		}
	}
	return evs
}

// StartBackgroundPump runs Pump on a C pthread so V2Start can block on
// C Main while the window still drains X events. Do not Swap/EGL here.
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
	p := C.tipsy_x11_pump_thread_start(C.uintptr_t(w.display), C.ulong(w.xid), C.ulong(w.wmDelete), C.int(w.width), C.int(w.height))
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
	return nil
}
