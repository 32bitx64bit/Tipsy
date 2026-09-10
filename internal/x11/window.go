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
	// ErrPointerGrab is returned when the X server rejects a Roblox-requested
	// window-scoped pointer lock. Button and motion delivery remains live.
	ErrPointerGrab = errors.New("x11: pointer grab failed")
)

// RobloxWindowTitle is the desktop title of the official Roblox client when
// it is hosted by Tipsy.
const RobloxWindowTitle = "Roblox - Tipsy"

// RobloxMinimumWidth and RobloxMinimumHeight are the minimum logical X11
// client pixels accepted for the official Android client. A real resize storm
// below this floor re-enters its native presenter and can crash it; these are
// X11 client pixels because the desktop Android contract uses density 1.
const (
	RobloxMinimumWidth  = 1280
	RobloxMinimumHeight = 720
)

// Window is a mapped native X11 InputOutput window.
type Window struct {
	mu              sync.Mutex
	display         uintptr // Display*
	xid             uintptr // X11 Window
	wmDelete        uintptr // Atom WM_DELETE_WINDOW
	randrEventBase  int     // RandR event range on this connection; 0 = absent
	width           int
	height          int
	closed          bool
	dismissed       bool
	focused         bool
	pump            uintptr // C tipsy_pump* background thread, or 0
	cursor          uintptr // transparent X cursor owned by this client window, or 0
	pointerCaptured bool
	pointerAnchorX  int
	pointerAnchorY  int
	// inputScratch is a reused C drain buffer (*inputDrainScratch on
	// linux+cgo). It lives on Window so escape analysis cannot allocate a
	// 256-wide event array on every InputReady wake.
	inputScratch any
	// inputEvents is the reused Go drain target. Pump is the sole consumer
	// for a window (the launch loop, or the single-window Open probe), so the
	// backing array is overwritten only after notifyInput returns. Pump clears
	// it afterwards so committed input text is not retained. Direct
	// drainInputLocked callers must finish with the slice before draining
	// again.
	inputEvents []InputEvent
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
	// InputPointerCapture reports a host pointer-lock transition requested by
	// Roblox's native lock getter. Capture failures carry CaptureStatus and do
	// not replace or swallow the original button edge.
	InputPointerCapture
)

// Pointer action values carried in InputEvent.PointerAction.
const (
	PointerDown int32 = 0
	PointerUp   int32 = 1
	PointerMove int32 = 2
)

// decodePointerRingAction maps the C input ring's pointer `a` field.
// 0 is ButtonPress, 1 is ButtonRelease, 2 is ordinary absolute MotionNotify,
// and 3 is captured relative motion while the host grab is active.
func decodePointerRingAction(rawA int32) (action int32, relative bool) {
	switch rawA {
	case 1:
		return PointerUp, false
	case 2:
		return PointerMove, false
	case 3:
		return PointerMove, true
	default:
		return PointerDown, false
	}
}

// InputEvent is one captured real X11 input event, translated toward the
// Android surface the engine expects.
type InputEvent struct {
	Kind           InputKind
	FocusGained    bool    // InputFocus
	KeyPressed     bool    // InputKey
	KeyCode        int32   // Android keycode; 0 = unmapped (dropped)
	ScanCode       int32   // raw X11 keycode (InputKey), 0 otherwise
	RepeatCount    int32   // InputKey: 0 for physical down/up, >0 for repeated down
	Text           string  // committed UTF-8 (InputText); never log
	PointerAction  int32   // PointerDown/Up/Move
	Button         int32   // 1 left, 3 right (InputPointer down/up)
	X, Y           float32 // pointer position in window pixels
	ScrollX        float32 // horizontal wheel detents (InputScroll)
	ScrollY        float32 // vertical wheel detents (InputScroll)
	Width, Height  int     // new client dimensions (InputResize)
	Relative       bool    // captured InputPointer move carries explicit deltas
	DeltaX, DeltaY float32 // captured relative motion in window pixels
	Captured       bool    // InputPointerCapture acquired/released state
	CaptureFailed  bool    // InputPointerCapture grab failure
	CaptureStatus  int32   // XGrabPointer status for a failed acquisition
}

var (
	inputMu    sync.RWMutex
	inputSubs  = map[int]func(InputEvent){}
	inputFns   []func(InputEvent) // snapshot, sorted by subscribe id
	inputSeq   int
	inputDrops uint64 // keys with no Android physical mapping, counted not logged
)

var activeWindow struct {
	sync.Mutex
	w *Window
}

func setActiveWindow(w *Window) {
	activeWindow.Lock()
	activeWindow.w = w
	activeWindow.Unlock()
}

func clearActiveWindow(w *Window) {
	activeWindow.Lock()
	if activeWindow.w == w {
		activeWindow.w = nil
	}
	activeWindow.Unlock()
}

// SetPointerLock applies Roblox's current native mouse-lock request to the
// sole client window. First-person / shift-lock grabs warp to the window
// center so look has travel room in every direction. Release leaves the
// desktop pointer at that same center rather than teleporting back to the
// pre-lock coordinate. Acquisition is refused while the window is unfocused
// so Alt-Tab cannot leave a background grab in place.
func SetPointerLock(locked bool) (bool, error) {
	return setPointerLock(locked, true)
}

// SetPointerLockAtCursor is the held-RMB camera-look grab. It confines the
// pointer at its current client position instead of warping to the window
// center, so releasing RMB leaves the desktop cursor where the user clicked.
func SetPointerLockAtCursor(locked bool) (bool, error) {
	return setPointerLock(locked, false)
}

func setPointerLock(locked, center bool) (bool, error) {
	activeWindow.Lock()
	w := activeWindow.w
	activeWindow.Unlock()
	if w == nil {
		return false, ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 || w.xid == 0 {
		return false, ErrClosed
	}
	return setPointerLockLocked(w, locked, center)
}

// OnInput subscribes fn to captured input events of every open window.
// Tipsy owns a single Roblox window per process. fn runs on the caller of
// Pump (the launch loop after InputReady) and must not block or re-enter
// Pump. The returned cancel func removes the subscription.
func OnInput(fn func(InputEvent)) (cancel func()) {
	if fn == nil {
		return func() {}
	}
	inputMu.Lock()
	defer inputMu.Unlock()
	id := inputSeq
	inputSeq++
	inputSubs[id] = fn
	rebuildInputFnsLocked()
	return func() {
		inputMu.Lock()
		defer inputMu.Unlock()
		delete(inputSubs, id)
		rebuildInputFnsLocked()
	}
}

func rebuildInputFnsLocked() {
	ids := make([]int, 0, len(inputSubs))
	for id := range inputSubs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	fns := make([]func(InputEvent), len(ids))
	for i, id := range ids {
		fns[i] = inputSubs[id]
	}
	inputFns = fns
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
	fns := inputFns
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

// PointerCapture reports the current host capture state and stable window
// anchor. It is intended for diagnostics and tests; Roblox's native getter is
// the authority that changes this state.
func (w *Window) PointerCapture() (captured bool, anchorX, anchorY int) {
	if w == nil {
		return false, 0, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pointerCaptured, w.pointerAnchorX, w.pointerAnchorY
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
