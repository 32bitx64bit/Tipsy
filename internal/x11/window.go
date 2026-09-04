// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"errors"
	"sort"
	"sync"
)

var (
	// ErrUnavailable is returned when this build has no native X11 (non-Linux or CGO disabled).
	ErrUnavailable = errors.New("x11: native X11 unavailable")
	// ErrNoDisplay is returned when XOpenDisplay fails (DISPLAY unset or server unreachable).
	ErrNoDisplay = errors.New("x11: XOpenDisplay failed")
	// ErrClosed is returned by Pump after WM_DELETE_WINDOW or Close.
	ErrClosed = errors.New("x11: window closed")
	// ErrFullscreen is returned when the window manager rejects an EWMH
	// fullscreen request.
	ErrFullscreen = errors.New("x11: fullscreen request failed")
	// ErrInvalidSize is returned when Open is given a non-positive size.
	ErrInvalidSize = errors.New("x11: width and height must be positive")
)

// RobloxWindowTitle is the desktop title of the official Roblox client when
// it is hosted by Tipsy.
const RobloxWindowTitle = "Roblox - Tipsy"

// Window is a mapped native X11 InputOutput window.
type Window struct {
	mu        sync.Mutex
	display   uintptr // Display*
	xid       uintptr // X11 Window
	wmDelete  uintptr // Atom WM_DELETE_WINDOW
	width     int
	height    int
	closed    bool
	dismissed bool
	focused   bool
	pump      uintptr // C tipsy_pump* background thread, or 0
	cursor    uintptr // transparent X cursor owned by this client window, or 0
}

// InputKind classifies a captured window input event.
type InputKind uint8

// Captured input kinds.
const (
	// InputFocus is a real FocusIn/FocusOut on the window.
	InputFocus InputKind = iota
	// InputKey is a key press/release. Non-text keys carry an Android
	// KeyCode; printable physical keys also carry their Android KeyCode for
	// the direct Roblox key route. A printable KeyPress is followed by a
	// separate InputText containing X11's actual committed UTF-8 text.
	// KeyCode 0 is retained only for an unrecognized KeySym.
	InputKey
	// InputPointer is a button press/release or window-relative pointer
	// motion. Both ordinary hover and button-held motion are delivered;
	// the direct Roblox mouse path needs their continuous positions.
	InputPointer
	// InputScroll is one real core-X11 wheel detent. Vertical wheel buttons
	// 4/5 carry ScrollY +1/-1; horizontal buttons 6/7 carry ScrollX -1/+1.
	// Only ButtonPress creates a detent because X11 wheel releases do not
	// represent a second scroll step.
	InputScroll
	// InputResize is a real ConfigureNotify for the Roblox client window.
	// It shares the input stream so consumers can update surface geometry
	// before a later pointer event uses the new window coordinates.
	InputResize
	// InputText is text genuinely committed by the X11 input method for one
	// KeyPress. It is separate from InputKey because Android/Roblox likewise
	// separates physical key edges from RbxKeyboard/EditText text changes.
	// Consumers must never log Text: it may contain credentials.
	InputText
)

// Pointer action values carried in InputEvent.PointerAction.
const (
	PointerDown int32 = 0
	PointerUp   int32 = 1
	PointerMove int32 = 2
)

// InputEvent is one captured real X11 input event, translated toward the
// Android surface the engine expects.
type InputEvent struct {
	Kind          InputKind
	FocusGained   bool    // InputFocus
	KeyPressed    bool    // InputKey
	KeyCode       int32   // Android keycode; 0 = unmapped (dropped)
	ScanCode      int32   // raw X11 keycode (InputKey), 0 otherwise
	Text          string  // committed UTF-8 (InputText); never log
	PointerAction int32   // PointerDown/Up/Move
	Button        int32   // 1 left, 3 right (InputPointer down/up)
	X, Y          float32 // pointer position in window pixels
	ScrollX       float32 // horizontal wheel detents (InputScroll)
	ScrollY       float32 // vertical wheel detents (InputScroll)
	Width, Height int     // new client dimensions (InputResize)
}

var (
	inputMu    sync.RWMutex
	inputSubs  = map[int]func(InputEvent){}
	inputSeq   int
	inputDrops uint64 // keys with no Android physical mapping, counted not logged
)

// OnInput subscribes fn to captured input events of every open window.
// Tipsy owns a single Roblox window per process. fn runs on the caller of
// Pump (the runtime ticker) and must not block or re-enter Pump. The
// returned cancel func removes the subscription.
func OnInput(fn func(InputEvent)) (cancel func()) {
	if fn == nil {
		return func() {}
	}
	inputMu.Lock()
	defer inputMu.Unlock()
	id := inputSeq
	inputSeq++
	inputSubs[id] = fn
	return func() {
		inputMu.Lock()
		defer inputMu.Unlock()
		delete(inputSubs, id)
	}
}

// InputDroppedKeys reports how many real key events lacked an Android
// physical-key mapping. Counts only; the key identity is never recorded.
func InputDroppedKeys() uint64 {
	inputMu.RLock()
	defer inputMu.RUnlock()
	return inputDrops
}

// Focused reports whether the last observed focus state is focused.
func (w *Window) Focused() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.focused
}

// notifyInput delivers events to subscribers outside every package lock.
// Event order is deliberately outermost: each subscriber sees a resize
// before any pointer event that X11 delivered after that ConfigureNotify.
// This prevents direct pointer coordinates from outrunning the Android
// surface geometry after a user resizes the window.
func notifyInput(evs []InputEvent) {
	if len(evs) == 0 {
		return
	}
	inputMu.RLock()
	fns := make([]func(InputEvent), 0, len(inputSubs))
	ids := make([]int, 0, len(inputSubs))
	for id := range inputSubs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		fns = append(fns, inputSubs[id])
	}
	inputMu.RUnlock()
	for _, ev := range evs {
		for _, fn := range fns {
			fn(ev)
		}
	}
}

// Display returns the Xlib Display* as an opaque uintptr.
func (w *Window) Display() uintptr {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.display
}

// XID returns the X11 Window XID as an opaque uintptr.
func (w *Window) XID() uintptr {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.xid
}

// Size returns the last known width and height in pixels.
func (w *Window) Size() (int, int) {
	if w == nil {
		return 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.width, w.height
}

// CursorHidden reports whether this window owns an invisible X cursor.
// X cursor definitions are window-scoped, so the host cursor is restored
// automatically as soon as it leaves the Roblox client window.
func (w *Window) CursorHidden() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cursor != 0
}

// Dismiss immediately removes the client window from the desktop while its
// X11 connection remains valid for orderly EGL/GameActivity teardown. It is
// idempotent and may be called after Pump reports ErrClosed.
func (w *Window) Dismiss() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.display == 0 || w.xid == 0 || w.dismissed {
		return nil
	}
	return dismissLocked(w)
}

// SetFullscreen asks the EWMH window manager to add or remove
// _NET_WM_STATE_FULLSCREEN. The request is asynchronous; ConfigureNotify
// events report the resulting client size through the normal resize stream.
func (w *Window) SetFullscreen(enabled bool) error {
	if w == nil {
		return ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 || w.xid == 0 {
		return ErrClosed
	}
	return setFullscreenLocked(w, enabled)
}
