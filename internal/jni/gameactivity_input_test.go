// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

// --- Capture state machine ---

// stubCursorHidden substitutes the engine cursor-hidden reader. The returned
// struct is the live reading; mutate it between events.
type cursorHiddenReading struct{ hidden, ok bool }

func stubCursorHidden(t *testing.T, hidden, ok bool) *cursorHiddenReading {
	t.Helper()
	r := &cursorHiddenReading{hidden: hidden, ok: ok}
	old := engineCursorHiddenSource
	engineCursorHiddenSource = func() (bool, bool) { return r.hidden, r.ok }
	t.Cleanup(func() { engineCursorHiddenSource = old })
	return r
}

// cameraFirstPersonReading is a mutable substitute for the debounced detector.
type cameraFirstPersonReading struct{ firstPerson bool }

func stubCameraFirstPerson(t *testing.T, firstPerson bool) *cameraFirstPersonReading {
	t.Helper()
	r := &cameraFirstPersonReading{firstPerson: firstPerson}
	old := engineCameraFirstPersonSource
	engineCameraFirstPersonSource = func() bool { return r.firstPerson }
	t.Cleanup(func() { engineCameraFirstPersonSource = old })
	return r
}

func lastVisible(t *testing.T, calls *captureCalls) bool {
	t.Helper()
	if len(calls.visible) == 0 {
		t.Fatal("no cursor visibility call recorded")
	}
	return calls.visible[len(calls.visible)-1]
}

// TestSelectCaptureStatePriorityEdges names every priority edge: the gates
// (Unfocused, Home, LeftAlt) beat everything, then Center, Anchor,
// CameraFirstPerson, RMB, Always, Free.
func TestSelectCaptureStatePriorityEdges(t *testing.T) {
	open := captureInputs{focused: true, experience: true}
	with := func(f func(*captureInputs)) captureInputs {
		in := open
		f(&in)
		return in
	}
	every := with(func(in *captureInputs) {
		in.center, in.anchor, in.cameraFirstPerson, in.rmb, in.always = true, true, true, true, true
	})
	cases := []struct {
		name string
		in   captureInputs
		want captureState
	}{
		{"nothing is Free", open, captureFree},
		{"center", with(func(in *captureInputs) { in.center = true }), captureEngineCenter},
		{"anchor", with(func(in *captureInputs) { in.anchor = true }), captureEngineAnchor},
		{"camera first person", with(func(in *captureInputs) { in.cameraFirstPerson = true }), captureCameraFirstPerson},
		{"rmb", with(func(in *captureInputs) { in.rmb = true }), captureRMBFallback},
		{"always", with(func(in *captureInputs) { in.always = true }), captureAlwaysOptIn},
		{"center outranks anchor camera rmb always", every, captureEngineCenter},
		{"anchor outranks camera rmb always", with(func(in *captureInputs) {
			*in = every
			in.center = false
		}), captureEngineAnchor},
		{"camera outranks rmb always", with(func(in *captureInputs) {
			*in = every
			in.center, in.anchor = false, false
		}), captureCameraFirstPerson},
		{"rmb outranks always", with(func(in *captureInputs) {
			*in = every
			in.center, in.anchor, in.cameraFirstPerson = false, false, false
		}), captureRMBFallback},
		{"always is the last capture", with(func(in *captureInputs) {
			*in = every
			in.center, in.anchor, in.cameraFirstPerson, in.rmb = false, false, false, false
		}), captureAlwaysOptIn},
		{"unfocused is Free", with(func(in *captureInputs) { *in = every; in.focused = false }), captureFree},
		{"Home is Free", with(func(in *captureInputs) { *in = every; in.experience = false }), captureFree},
		{"LeftAlt is Free", with(func(in *captureInputs) { *in = every; in.released = true }), captureFree},
	}
	for _, tc := range cases {
		if got := selectCaptureState(tc.in); got != tc.want {
			t.Errorf("%s: state %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSelectCaptureStateExhaustive checks all 256 input combinations against
// an independent statement of the priority order.
func TestSelectCaptureStateExhaustive(t *testing.T) {
	for bits := 0; bits < 256; bits++ {
		in := captureInputs{
			focused: bits&1 != 0, experience: bits&2 != 0, released: bits&4 != 0,
			center: bits&8 != 0, anchor: bits&16 != 0, rmb: bits&32 != 0,
			always: bits&64 != 0, cameraFirstPerson: bits&128 != 0,
		}
		want := captureFree
		if in.focused && in.experience && !in.released {
			switch {
			case in.center:
				want = captureEngineCenter
			case in.anchor:
				want = captureEngineAnchor
			case in.cameraFirstPerson:
				want = captureCameraFirstPerson
			case in.rmb:
				want = captureRMBFallback
			case in.always:
				want = captureAlwaysOptIn
			}
		}
		if got := selectCaptureState(in); got != want {
			t.Fatalf("inputs %+v: state %v, want %v", in, got, want)
		}
	}
}

func TestCapturePolicyValues(t *testing.T) {
	cases := []struct {
		value           string
		enabled, always bool
		name            string
	}{
		{"", true, false, "auto"},
		{"auto", true, false, "auto"},
		{"AUTO", true, false, "auto"},
		{"nonsense", true, false, "auto"},
		{"always", true, true, "always"},
		// The retired zoomlock value is just an unrecognised value: auto.
		{"zoomlock", true, false, "auto"},
		{" ZoomLock ", true, false, "auto"},
		{"off", false, false, "auto"},
		{"0", false, false, "auto"},
	}
	for _, tc := range cases {
		selectMouseCapture(t, tc.value)
		if pointerCapturePolicyEnabled() != tc.enabled || pointerCaptureAlways() != tc.always ||
			pointerCapturePolicyName() != tc.name {
			t.Errorf("TIPSY_MOUSE_CAPTURE=%q: enabled=%t always=%t name=%s", tc.value,
				pointerCapturePolicyEnabled(), pointerCaptureAlways(), pointerCapturePolicyName())
		}
	}
}

// With no engine lock, no RMB and no readable cursor-hidden state the pointer
// is Free: no grab is requested, the host cursor stays hidden and every wheel
// detent carries the real pointer position, never the viewport center.
func TestAutoCaptureStaysFreeWithoutEngineSignals(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, false, false)

	moveAbsolute(111, 193)
	for i := 0; i < 4; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 111, Y: 193, ScrollY: 1})
		if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 111 || y != 193 {
			t.Fatalf("detent %d wheel=(%v,%v), want the pointer (111,193)", i, x, y)
		}
	}
	// The real pointer is passed through untouched, even outside the viewport.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: -7, Y: 900, ScrollY: -1})
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != -7 || y != 900 {
		t.Fatalf("free wheel=(%v,%v), want the real pointer (-7,900)", x, y)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 111, Y: 193})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 1, X: 111, Y: 193})
	moveAbsolute(115, 190)
	if calls.grabs() != 0 {
		t.Fatalf("Free requested a grab: %+v", calls)
	}
	for _, visible := range calls.visible {
		if visible {
			t.Fatalf("cursor calls=%v, want the host cursor never shown while Free", calls.visible)
		}
	}
	if st := currentCaptureState(); st != captureFree {
		t.Fatalf("state=%v, want free", st)
	}
	if want := wantedCaptureState(observeEngineLock()); want != captureFree {
		t.Fatalf("wanted state=%v, want free", want)
	}
}

// CameraFirstPerson follows only the camera detector: capture in first person,
// free in third person, with wheel at the clamped logical point while captured
// and at the real pointer once Free.
func TestAutoCaptureCameraFirstPersonTransitions(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, true, true)
	camera := stubCameraFirstPerson(t, false)

	moveAbsolute(100, 200)
	if calls.grabs() != 0 || currentCaptureState() != captureFree {
		t.Fatalf("third-person camera captured: state=%v calls=%+v", currentCaptureState(), calls)
	}

	camera.firstPerson = true
	movesBefore := RobloxDirectInputStats().MoveDelivered
	moveAbsolute(111, 193)
	if len(calls.center) != 1 || !calls.center[0] || len(calls.cursor) != 0 {
		t.Fatalf("hidden cursor grab calls=%+v, want one centered SetPointerLock(true)", calls)
	}
	if fx, fy, ok := RobloxDirectFallbackPosition(); !ok || fx != 640 || fy != 360 {
		t.Fatalf("hidden cursor entry seeded logical=(%v,%v,%v), want center (640,360,true)", fx, fy, ok)
	}
	if currentCaptureState() != captureCameraFirstPerson || !persistentPointerCapture.Load() || lastVisible(t, calls) {
		t.Fatalf("state=%v persistent=%t, want cursor-hidden with the host cursor hidden",
			currentCaptureState(), persistentPointerCapture.Load())
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 1 {
		t.Fatalf("the acquire sample delivered %d moves, want one center announcement", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 0 || dy != 0 {
		t.Fatalf("center announcement=(%v,%v,%v,%v), want (640,360,0,0)", x, y, dx, dy)
	}
	lookRelative(1000, -1000)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 1279 || y != 0 {
		t.Fatalf("captured wheel=(%v,%v), want the clamped logical point (1279,0), never the center", x, y)
	}

	// The camera returns to third person: the next sample frees the pointer.
	camera.firstPerson = false
	movesBefore = RobloxDirectInputStats().MoveDelivered
	lookRelative(1, 1)
	if currentCaptureState() != captureFree || persistentPointerCapture.Load() {
		t.Fatalf("visible cursor kept the grab: state=%v", currentCaptureState())
	}
	if len(calls.center) != 2 || !calls.center[0] || calls.center[1] || lastVisible(t, calls) {
		t.Fatalf("release calls=%+v visible=%v, want the centered acquire then one ungrab and the host cursor still hidden", calls, calls.visible)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 0 {
		t.Fatalf("the release sample delivered %d moves, want 0 (consumed)", got)
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: -7, Y: 900, ScrollY: 1})
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != -7 || y != 900 {
		t.Fatalf("free wheel=(%v,%v), want the real pointer (-7,900)", x, y)
	}

	// A click in first person captures on the edge; returning to third person
	// frees the pointer on the next sample.
	camera.firstPerson = true
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 50, Y: 60})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 1, X: 50, Y: 60})
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("a click while hidden did not capture: state=%v", currentCaptureState())
	}
	camera.firstPerson = false
	lookRelative(2, 2)
	if currentCaptureState() != captureFree || persistentPointerCapture.Load() {
		t.Fatalf("an unavailable reading kept the grab: state=%v", currentCaptureState())
	}
}

// A hidden engine cursor and a wheel zoom-in are not first-person evidence. The
// Red Lake with a weapon out stays Free until the camera detector says first
// person.
func TestHiddenCursorAndWheelDoNotCaptureThirdPersonCamera(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	stubEngineMouseBehavior(t, MouseBehaviorDefault)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, true, true)
	camera := stubCameraFirstPerson(t, false)

	moveAbsolute(900, 220)
	if state := currentCaptureState(); state != captureFree {
		t.Fatalf("hidden without a wheel detent state=%v, want Free", state)
	}
	if want := wantedCaptureState(observeEngineLock()); want != captureFree {
		t.Fatalf("hidden without a wheel detent wanted=%v, want Free", want)
	}
	if calls.grabs() != 0 || persistentPointerCapture.Load() {
		t.Fatalf("hidden without a wheel detent grabbed: calls=%+v persistent=%t",
			calls, persistentPointerCapture.Load())
	}

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	moveAbsolute(901, 221)
	if state := currentCaptureState(); state != captureFree || persistentPointerCapture.Load() {
		t.Fatalf("hidden third-person camera after a wheel zoom-in state=%v persistent=%t, want Free",
			state, persistentPointerCapture.Load())
	}
	camera.firstPerson = true
	moveAbsolute(902, 222)
	if state := currentCaptureState(); state != captureCameraFirstPerson || !persistentPointerCapture.Load() {
		t.Fatalf("first-person camera state=%v persistent=%t, want camera capture",
			state, persistentPointerCapture.Load())
	}
}

// TestAlwaysOptInEntryCentersPointer pins result 3 for the always policy: the
// entry warps the host grab and the logical pair to the window center once, and
// later wheel detents keep using the clamped logical point instead of snapping
// back to the center.
func TestAlwaysOptInEntryCentersPointer(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	moveAbsolute(100, 200)
	if len(calls.center) != 1 || !calls.center[0] || len(calls.cursor) != 0 {
		t.Fatalf("always entry grab calls=%+v, want one centered SetPointerLock(true)", calls)
	}
	if !persistentPointerCapture.Load() || currentCaptureState() != captureAlwaysOptIn {
		t.Fatalf("state=%v persistent=%t, want always", currentCaptureState(), persistentPointerCapture.Load())
	}
	if fx, fy, ok := RobloxDirectFallbackPosition(); !ok || fx != 640 || fy != 360 {
		t.Fatalf("always entry seeded logical=(%v,%v,%v), want center (640,360,true)", fx, fy, ok)
	}

	// Drift off-view, then scroll: the detent lands on the clamped logical
	// point, not the center, and the entry does not run again.
	lookRelative(1000, -1000)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 1279 || y != 0 {
		t.Fatalf("captured wheel=(%v,%v), want the clamped logical point (1279,0)", x, y)
	}
	if len(calls.center) != 1 {
		t.Fatalf("always entry re-centered after a wheel: %+v", calls)
	}
	if fx, fy, _ := RobloxDirectFallbackPosition(); fx != 1640 || fy != -640 {
		t.Fatalf("integrator after wheel=(%v,%v), want the unbounded logical pair (1640,-640)", fx, fy)
	}
}

func TestTipsyCapturePinsEngineMotion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		hidden bool
		state  captureState
	}{
		{"cursor-hidden", "auto", true, captureCameraFirstPerson},
		{"always", "always", false, captureAlwaysOptIn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TIPSY_TRACE_CURSOR_STATE", "1")
			selectPointerPath(t, "direct")
			selectMouseCapture(t, tc.policy)
			wireRecordingDirectTarget(t, 0x1234, 0x5678)
			stubCaptureSeams(t)
			enterExperienceForTest(t, newCaptureVM(t), 79966250354565)
			testDirectRecSetMouseLocked(false)
			stubCursorHidden(t, tc.hidden, true)
			if tc.state == captureCameraFirstPerson {
				stubCameraFirstPerson(t, true)
			}
			logs := captureLogs(t)
			moveAbsolute(100, 200)
			if got := currentCaptureState(); got != tc.state {
				t.Fatalf("capture state=%v, want %v", got, tc.state)
			}
			for _, delta := range [][2]float32{{5, 3}, {-12.5, 7.25}, {1000, -1000}, {0, -0.5}} {
				before := RobloxDirectInputStats().MoveDelivered
				lookRelative(delta[0], delta[1])
				if got := RobloxDirectInputStats().MoveDelivered - before; got != 1 {
					t.Fatalf("motion delivered %d moves, want 1", got)
				}
				if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != delta[0] || dy != delta[1] {
					t.Fatalf("motion=(%v,%v,%v,%v), want (640,360,%v,%v)", x, y, dx, dy, delta[0], delta[1])
				}
			}
			if log := logs.String(); !strings.Contains(log, "capture pinned motion") || !strings.Contains(log, "x=640") || !strings.Contains(log, "y=360") {
				t.Fatalf("missing pinned-position trace: %s", log)
			}
		})
	}
}

// TestEngineCenterEntrySeedsLogicalCenter pins result 3 for the engine's own
// LockCenter: the centered sticky host grab is also the point the logical pair
// starts from, so the first captured delta is continuous.
func TestEngineCenterEntrySeedsLogicalCenter(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)

	moveAbsolute(900, 220)
	if calls.grabs() != 0 || pointerLockSticky.Load() {
		t.Fatalf("third person grabbed: %+v", calls)
	}

	// The engine enters LockCenter; the transition move is consumed.
	testDirectRecSetMouseLocked(true)
	moveAbsolute(901, 221)
	if !pointerLockSticky.Load() {
		t.Fatal("LockCenter did not take the centered sticky grab")
	}
	if len(calls.center) != 1 || !calls.center[0] {
		t.Fatalf("center entry calls=%+v, want [true]", calls.center)
	}
	if lx, ly, ok := robloxDirectLastPosition(); !ok || lx != 640 || ly != 360 {
		t.Fatalf("LockCenter entry seeded logical=(%v,%v,%v), want center (640,360,true)", lx, ly, ok)
	}
	lookRelative(5, 3)
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 645 || y != 363 || dx != 5 || dy != 3 {
		t.Fatalf("centered motion=(%v,%v,%v,%v), want (645,363,5,3) from the center", x, y, dx, dy)
	}
}

// TestHeldRMBEntryKeepsPointerInPlace pins that the RMB entry is unchanged:
// only the persistent states and EngineCenter center, while a held RMB keeps
// the cursor-anchored grab at the click.
func TestHeldRMBEntryKeepsPointerInPlace(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	joinExperienceForTest(t)
	testDirectRecSetMouseLocked(false)

	moveAbsolute(20, 22)
	// Camera first person appears on the button edge; RMB must still win here.
	stubCameraFirstPerson(t, true)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 20, Y: 22})
	if len(calls.cursor) != 1 || !calls.cursor[0] || len(calls.center) != 0 {
		t.Fatalf("RMB entry calls=%+v, want one cursor-anchored grab and no centering", calls)
	}
	if fx, fy, ok := RobloxDirectFallbackPosition(); !ok || fx != 20 || fy != 22 {
		t.Fatalf("RMB entry seeded logical=(%v,%v,%v), want the click (20,22,true)", fx, fy, ok)
	}
}

// TestFreeKeepsHostCursorHidden pins result 3: ordinary Free never shows the
// host cursor, including after a capture release and after FocusIn into Free.
// Only the explicit LeftAlt release is allowed to show it.
func TestFreeKeepsHostCursorHidden(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, true, true)
	camera := stubCameraFirstPerson(t, true)

	moveAbsolute(100, 200)
	if currentCaptureState() != captureCameraFirstPerson || !persistentPointerCapture.Load() {
		t.Fatalf("setup: state=%v persistent=%t, want cursor-hidden",
			currentCaptureState(), persistentPointerCapture.Load())
	}

	// The camera leaves first person: releasePersistentCapture frees the grab,
	// but the host cursor must stay hidden.
	camera.firstPerson = false
	lookRelative(1, 1)
	if currentCaptureState() != captureFree || persistentPointerCapture.Load() {
		t.Fatalf("release: state=%v persistent=%t, want free",
			currentCaptureState(), persistentPointerCapture.Load())
	}
	for _, visible := range calls.visible {
		if visible {
			t.Fatalf("release to Free showed the host cursor: %v", calls.visible)
		}
	}

	// FocusIn into plain Free (no LeftAlt release) hides rather than shows.
	calls.visible = nil
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if currentCaptureState() != captureFree {
		t.Fatalf("FocusIn: state=%v, want free", currentCaptureState())
	}
	if len(calls.visible) == 0 || lastVisible(t, calls) {
		t.Fatalf("FocusIn into Free cursor calls=%v, want hidden", calls.visible)
	}
}

// The gates beat the engine: LeftAlt frees the pointer and shows the cursor;
// focus loss and Home free it with the cursor hidden. The state is re-read on
// LeftAlt re-arm and on FocusIn.
func TestAutoCaptureLeftAltFocusAndHomeRelease(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, true, true)
	camera := stubCameraFirstPerson(t, true)

	moveAbsolute(100, 200)
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("setup: state=%v, want cursor-hidden", currentCaptureState())
	}

	altDown := x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: leftAltKeyCode, ScanCode: 64}
	altUp := x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: leftAltKeyCode, ScanCode: 64}
	handleX11InputEvent(altDown)
	handleX11InputEvent(altUp)
	if currentCaptureState() != captureFree || !pointerCaptureReleased.Load() || !lastVisible(t, calls) {
		t.Fatalf("LeftAlt: state=%v released=%t visible=%v, want free and visible",
			currentCaptureState(), pointerCaptureReleased.Load(), calls.visible)
	}
	grabs := calls.grabs()
	moveAbsolute(120, 210)
	if calls.grabs() != grabs {
		t.Fatalf("a released stream grabbed: %+v", calls)
	}
	handleX11InputEvent(altDown)
	handleX11InputEvent(altUp)
	if pointerCaptureReleased.Load() || lastVisible(t, calls) {
		t.Fatalf("LeftAlt re-arm: released=%t visible=%v, want armed and hidden for the hidden state",
			pointerCaptureReleased.Load(), calls.visible)
	}
	moveAbsolute(130, 220)
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("re-arm did not re-evaluate to cursor-hidden: state=%v", currentCaptureState())
	}

	// Focus loss frees the pointer; FocusIn re-reads the engine and does not grab.
	// Neither is a LeftAlt release, so the host cursor stays hidden.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	if currentCaptureState() != captureFree || lastVisible(t, calls) ||
		wantedCaptureState(observeEngineLock()) != captureFree {
		t.Fatalf("focus loss: state=%v visible=%v, want free and hidden", currentCaptureState(), calls.visible)
	}
	grabs = calls.grabs()
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if calls.grabs() != grabs || currentCaptureState() != captureFree {
		t.Fatalf("FocusIn grabbed on its own: state=%v calls=%+v", currentCaptureState(), calls)
	}
	if lastVisible(t, calls) {
		t.Fatalf("FocusIn with a hidden engine cursor left the host cursor visible: %v", calls.visible)
	}
	moveAbsolute(140, 230)
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("motion after FocusIn did not re-acquire: state=%v", currentCaptureState())
	}
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	camera.firstPerson = false
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	if currentCaptureState() != captureFree || lastVisible(t, calls) {
		t.Fatalf("FocusIn with a third-person camera: state=%v visible=%v, want free and hidden",
			currentCaptureState(), calls.visible)
	}

	// Home: leaving the experience frees a held grab on the next sample.
	camera.firstPerson = true
	moveAbsolute(150, 240)
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("setup: state=%v, want cursor-hidden", currentCaptureState())
	}
	if _, handled := vm.dispatch(jnull(), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("leave onGameLoaded not handled")
	}
	lookRelative(3, 3)
	if currentCaptureState() != captureFree || persistentPointerCapture.Load() {
		t.Fatalf("Home kept the grab: state=%v", currentCaptureState())
	}
}

// An engine lock outranks the cursor-hidden signal and hands the pointer back
// to it when the lock ends.
func TestEngineLockOutranksCursorHidden(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	script := stubEngineMouseBehavior(t,
		MouseBehaviorDefault, MouseBehaviorLockCurrentPosition, MouseBehaviorDefault)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, true, true)
	stubCameraFirstPerson(t, true)

	moveAbsolute(900, 220)
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("setup: state=%v, want cursor-hidden", currentCaptureState())
	}
	script.next()
	lookRelative(5, 3)
	if currentCaptureState() != captureEngineAnchor || persistentPointerCapture.Load() {
		t.Fatalf("LockCurrentPosition did not outrank cursor-hidden: state=%v", currentCaptureState())
	}
	script.next()
	lookRelative(1, 1)
	if currentCaptureState() != captureCameraFirstPerson {
		t.Fatalf("the engine lock ended but cursor-hidden did not take the pointer back: state=%v", currentCaptureState())
	}
	if fx, fy, ok := RobloxDirectFallbackPosition(); !ok || fx != 640 || fy != 360 {
		t.Fatalf("anchor handoff logical=(%v,%v,%v), want center", fx, fy, ok)
	}
	// The zoom-in lock adopts the cursor-hidden grab in place, and the handoff
	// back reuses it: one centered SetPointerLock(true) for the hidden entry and
	// one for the handoff, with no free/regrab in between.
	if len(calls.center) != 2 || !calls.center[0] || !calls.center[1] {
		t.Fatalf("hidden anchor entry/handoff did not center the host: %+v", calls)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 0 || dy != 0 {
		t.Fatalf("handoff announcement=(%v,%v,%v,%v), want center and zero delta", x, y, dx, dy)
	}
}

// Project 12 combines LockCurrentPosition with a hidden engine cursor. That
// anchor is centered on entry, retains relative-delta delivery, and returns to
// Free with one absolute center announcement after MouseBehavior reads Default.
func TestHiddenEngineAnchorCentersAndReleasesAtCenter(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "1")
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	script := stubEngineMouseBehavior(t,
		MouseBehaviorDefault, MouseBehaviorLockCurrentPosition, MouseBehaviorDefault)
	testDirectRecSetMouseLocked(false)
	reading := stubCursorHidden(t, false, true)
	logs := captureLogs(t)

	moveAbsolute(900, 220)
	script.next()
	reading.hidden = true
	movesBefore := RobloxDirectInputStats().MoveDelivered
	moveAbsolute(901, 221)
	if currentCaptureState() != captureEngineAnchor || !engineAnchorCentered() {
		t.Fatalf("hidden anchor state=%v centered=%t, want EngineAnchor(hidden)",
			currentCaptureState(), engineAnchorCentered())
	}
	if len(calls.center) != 1 || !calls.center[0] || len(calls.cursor) != 0 {
		t.Fatalf("hidden anchor entry calls=%+v, want one centered grab and no cursor grab", calls)
	}
	if x, y, ok := RobloxDirectFallbackPosition(); !ok || x != 640 || y != 360 {
		t.Fatalf("hidden anchor seed=(%v,%v,%v), want center (640,360,true)", x, y, ok)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 1 {
		t.Fatalf("hidden anchor entry delivered %d moves, want one center announcement", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 0 || dy != 0 {
		t.Fatalf("hidden anchor announcement=(%v,%v,%v,%v), want (640,360,0,0)", x, y, dx, dy)
	}
	var entryTrace string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "free -> EngineAnchor(hidden)") {
			entryTrace = line
		}
	}
	if entryTrace == "" || !strings.Contains(entryTrace, "warp=center") {
		t.Fatalf("hidden anchor entry trace=%q, want EngineAnchor(hidden) with warp=center; logs=%s", entryTrace, logs.String())
	}

	// LockCurrentPosition still consumes relative deltas; centering changes the
	// origin, not the anchor-style delivery function.
	lookRelative(5, 3)
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 645 || y != 363 || dx != 5 || dy != 3 {
		t.Fatalf("hidden anchor motion=(%v,%v,%v,%v), want (645,363,5,3)", x, y, dx, dy)
	}
	if len(calls.center) != 1 {
		t.Fatalf("relative motion re-centered the hidden anchor: %+v", calls)
	}

	// The visibility and behavior edges can arrive together. The latch for the
	// lock actually held selects a center release even though hidden is now false.
	script.next()
	reading.hidden = false
	movesBefore = RobloxDirectInputStats().MoveDelivered
	lookRelative(1, 1)
	if currentCaptureState() != captureFree || engineAnchorActive() {
		t.Fatalf("Default + visible state=%v anchor=%t, want Free", currentCaptureState(), engineAnchorActive())
	}
	if len(calls.zoom) != 2 || !calls.zoom[0] || calls.zoom[1] {
		t.Fatalf("hidden anchor release calls=%+v, want center re-anchor then ungrab", calls)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 1 {
		t.Fatalf("hidden anchor release delivered %d moves, want one absolute center move", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 0 || dy != 0 {
		t.Fatalf("hidden anchor release move=(%v,%v,%v,%v), want (640,360,0,0)", x, y, dx, dy)
	}
}

func TestHiddenEngineAnchorVisibilityChangeReleasesAtCenter(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	stubEngineMouseBehavior(t, MouseBehaviorLockCurrentPosition)
	testDirectRecSetMouseLocked(false)
	reading := stubCursorHidden(t, true, true)
	moveAbsolute(900, 220)
	if !engineAnchorCentered() {
		t.Fatal("setup: hidden anchor was not centered")
	}

	reading.hidden = false
	movesBefore := RobloxDirectInputStats().MoveDelivered
	lookRelative(1, 1)
	if currentCaptureState() != captureEngineAnchor || engineAnchorCentered() {
		t.Fatalf("visible anchor state=%v centered=%t, want lock-in-place", currentCaptureState(), engineAnchorCentered())
	}
	if len(calls.zoom) != 2 || !calls.zoom[0] || calls.zoom[1] || len(calls.cursor) != 1 || !calls.cursor[0] {
		t.Fatalf("hidden->visible calls=%+v, want center release then cursor-anchored grab", calls)
	}
	if x, y, ok := engineAnchorFrozenOrigin(); !ok || x != 640 || y != 360 {
		t.Fatalf("visible anchor origin=(%v,%v,%v), want center", x, y, ok)
	}
	// MouseBehavior is still LockCurrentPosition: no absolute center release
	// announcement is allowed, only the real relative sample goes to the engine.
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 1 {
		t.Fatalf("hidden->visible delivered %d moves, want only the relative sample", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 641 || y != 361 || dx != 1 || dy != 1 {
		t.Fatalf("hidden->visible motion=(%v,%v,%v,%v), want (641,361,1,1)", x, y, dx, dy)
	}
}

func TestHiddenEngineAnchorReleaseGates(t *testing.T) {
	for _, gate := range []string{"LeftAlt", "focus", "Home"} {
		t.Run(gate, func(t *testing.T) {
			selectPointerPath(t, "direct")
			selectMouseCapture(t, "auto")
			wireRecordingDirectTarget(t, 0x1234, 0x5678)
			calls := stubCaptureSeams(t)
			vm := newCaptureVM(t)
			enterExperienceForTest(t, vm, 79966250354565)
			stubEngineMouseBehavior(t, MouseBehaviorLockCurrentPosition)
			testDirectRecSetMouseLocked(false)
			stubCursorHidden(t, true, true)
			moveAbsolute(900, 220)
			if currentCaptureState() != captureEngineAnchor || !engineAnchorCentered() {
				t.Fatal("setup: hidden EngineAnchor did not acquire the centered grab")
			}

			switch gate {
			case "LeftAlt":
				handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: leftAltKeyCode, ScanCode: 64})
				handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: false, KeyCode: leftAltKeyCode, ScanCode: 64})
				if !pointerCaptureReleased.Load() || !lastVisible(t, calls) {
					t.Fatal("LeftAlt did not leave the released, host-cursor-visible state")
				}
			case "focus":
				handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
			case "Home":
				if _, handled := vm.dispatch(jnull(), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
					t.Fatal("leave onGameLoaded not handled")
				}
				lookRelative(1, 1)
			}
			if currentCaptureState() != captureFree {
				t.Fatalf("%s left state=%v, want Free", gate, currentCaptureState())
			}
		})
	}
}

func TestCameraFirstPersonOutranksHeldRMBAfterEntry(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	// X11 returns changed=false while re-centering an already-held grab.
	pointerLockSetter = func(locked bool) (bool, error) {
		calls.center = append(calls.center, locked)
		return false, nil
	}
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, false, true)
	camera := stubCameraFirstPerson(t, false)
	moveAbsolute(900, 220)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 900, Y: 220})
	if currentCaptureState() != captureRMBFallback {
		t.Fatal("RMB did not acquire")
	}
	camera.firstPerson = true
	before := RobloxDirectInputStats().MoveDelivered
	lookRelative(5, 3)
	if currentCaptureState() != captureCameraFirstPerson || len(calls.center) != 1 || !calls.center[0] {
		t.Fatalf("hidden did not take centered grab: state=%v calls=%+v", currentCaptureState(), calls)
	}
	if rmbPointerFallback.Load() || !persistentPointerCapture.Load() {
		t.Fatalf("RMB->cursor-hidden conversion: rmb=%t persistent=%t, want only persistent",
			rmbPointerFallback.Load(), persistentPointerCapture.Load())
	}
	if got := RobloxDirectInputStats().MoveDelivered - before; got != 2 {
		t.Fatalf("got %d moves, want center announcement then relative motion", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 5 || dy != 3 {
		t.Fatalf("relative motion=(%v,%v,%v,%v), want (640,360,5,3)", x, y, dx, dy)
	}
}

func TestCameraFirstPersonToAlwaysRecentersHeldGrab(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "always")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, true, true)
	camera := stubCameraFirstPerson(t, true)
	moveAbsolute(900, 220)
	lookRelative(50, 30)
	camera.firstPerson = false
	lookRelative(2, 3)
	if currentCaptureState() != captureAlwaysOptIn || len(calls.center) != 2 {
		t.Fatalf("hidden-to-always state=%v calls=%+v", currentCaptureState(), calls)
	}
	if x, y, ok := RobloxDirectFallbackPosition(); !ok || x != 642 || y != 363 {
		t.Fatalf("hidden-to-always logical=(%v,%v,%v), want (642,363,true)", x, y, ok)
	}
}

// The GameActivity-only path delivers relative motion as absolute ev.X/ev.Y,
// so it must never take a host grab: a grab would freeze camera look. The
// direct path owns every centered entry.
func TestGameActivityOnlyPathNeverGrabs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		hidden bool
		anchor bool
	}{
		{name: "cursor-hidden", policy: "auto", hidden: true},
		{name: "hidden-engine-anchor", policy: "auto", hidden: true, anchor: true},
		{name: "always", policy: "always"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectPointerPath(t, "gameactivity")
			selectMouseCapture(t, tc.policy)
			calls := stubCaptureSeams(t)
			vm := inputTestVM(t, map[string]uintptr{
				methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
			})
			SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
			enterExperienceForTest(t, vm, 79966250354565)
			SetPointerClampViewport(1280, 720)
			t.Cleanup(ResetPointerClampViewportForTest)
			stubCursorHidden(t, tc.hidden, true)
			stubCameraFirstPerson(t, tc.hidden)
			testDirectRecSetMouseLocked(false)
			if tc.anchor {
				stubEngineMouseBehavior(t, MouseBehaviorLockCurrentPosition)
			}
			// The RMB down edge below mutates the package-global pointerButtons
			// state. Release it even when an assertion fails first, so the
			// following tests still see a clear button set.
			t.Cleanup(func() {
				handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 900, Y: 220})
				if got := pointerButtonState(0, false); got != 0 {
					t.Errorf("button state after RMB release = %d, want 0", got)
				}
			})

			moveAbsolute(900, 220)
			// A held RMB is another capture state this path must not honour.
			handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 900, Y: 220})

			if calls.grabs() != 0 {
				t.Fatalf("GameActivity-only path took a host grab: %+v", calls)
			}
			if persistentPointerCapture.Load() || pointerLockSticky.Load() ||
				engineAnchorActive() || rmbPointerFallback.Load() {
				t.Fatalf("GameActivity-only capture flags: persistent=%t sticky=%t anchor=%t rmb=%t",
					persistentPointerCapture.Load(), pointerLockSticky.Load(),
					engineAnchorActive(), rmbPointerFallback.Load())
			}
			if state := currentCaptureState(); state != captureFree {
				t.Fatalf("GameActivity-only state=%v, want free", state)
			}
		})
	}
}

// The GameActivity-only stream must not turn an engine lock re-read on FocusIn
// into a host grab either: every state stays free on this path.
func TestGameActivityOnlyFocusInDoesNotGrab(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	stubEngineMouseBehavior(t, MouseBehaviorLockCenter)
	testDirectRecSetMouseLocked(true)

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})

	if calls.grabs() != 0 || pointerLockSticky.Load() || engineAnchorActive() {
		t.Fatalf("FocusIn on GameActivity-only grabbed: calls=%+v sticky=%t anchor=%t",
			calls, pointerLockSticky.Load(), engineAnchorActive())
	}
	if state := currentCaptureState(); state != captureFree {
		t.Fatalf("GameActivity-only FocusIn state=%v, want free", state)
	}
}

// The GameActivity-only bridge must keep delivering the real absolute pointer,
// never a capture anchor: this is the camera-look path that a grab froze.
func TestGameActivityOnlyPathDeliversRealAbsolutePosition(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	selectMouseCapture(t, "auto")
	calls := stubCaptureSeams(t)
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	enterExperienceForTest(t, vm, 79966250354565)
	SetPointerClampViewport(1280, 720)
	t.Cleanup(ResetPointerClampViewportForTest)
	stubCursorHidden(t, true, true)
	moveAbsolute(900, 220)

	if calls.grabs() != 0 {
		t.Fatalf("GameActivity-only path took a host grab: %+v", calls)
	}
	o := vm.get(int64(testRecTouchID(3)))
	if o == nil || o.fields["x"] != float32(900) || o.fields["y"] != float32(220) {
		t.Fatalf("GameActivity delivery=%+v, want the real absolute (900,220)", o)
	}
}

// The default policy releases LockCurrentPosition at the frozen origin on a
// wheel detent and never re-centers.
func TestAutoAnchorWheelReleaseRestoresTheFrozenOrigin(t *testing.T) {
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	vm := newCaptureVM(t)
	enterExperienceForTest(t, vm, 79966250354565)
	script := stubEngineMouseBehavior(t,
		MouseBehaviorDefault, MouseBehaviorLockCurrentPosition, MouseBehaviorDefault)
	testDirectRecSetMouseLocked(false)
	stubCursorHidden(t, false, true)

	moveAbsolute(900, 220)
	script.next()
	lookRelative(5, 3)
	lookRelative(400, 100)
	if currentCaptureState() != captureEngineAnchor {
		t.Fatalf("setup: state=%v, want engine-anchor", currentCaptureState())
	}
	script.next()
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: -1})
	if x, y := testDirectRecWheelFloat(0), testDirectRecWheelFloat(1); x != 100 || y != 200 {
		t.Fatalf("anchored wheel=(%v,%v), want the detent point (100,200), never the center", x, y)
	}
	if engineAnchorActive() || len(calls.zoom) != 0 {
		t.Fatalf("anchor=%t zoom calls=%v, want released at the origin without a center warp", engineAnchorActive(), calls.zoom)
	}
	if lx, ly, ok := robloxDirectLastPosition(); !ok || lx != 900 || ly != 220 {
		t.Fatalf("last origin=(%v,%v,%v), want the frozen origin (900,220,true)", lx, ly, ok)
	}
}

// cameraAnchorLockSetup joins Project 12 under the default policy with the enum
// authority scripted Default -> LockCurrentPosition -> Default, a visible cursor
// and a third-person camera.
func cameraAnchorLockSetup(t *testing.T) (*captureCalls, *scriptedEngineMouseBehavior, *cursorHiddenReading, *cameraFirstPersonReading) {
	t.Helper()
	selectPointerPath(t, "direct")
	selectMouseCapture(t, "auto")
	wireRecordingDirectTarget(t, 0x1234, 0x5678)
	calls := stubCaptureSeams(t)
	enterExperienceForTest(t, newCaptureVM(t), 79966250354565)
	script := stubEngineMouseBehavior(t,
		MouseBehaviorDefault, MouseBehaviorLockCurrentPosition, MouseBehaviorDefault)
	testDirectRecSetMouseLocked(false)
	reading := stubCursorHidden(t, false, true)
	camera := stubCameraFirstPerson(t, false)
	pointerButtons.mu.Lock()
	pointerButtons.state = 0
	pointerButtons.mu.Unlock()
	return calls, script, reading, camera
}

// Camera first person in a game without LockCenter centers the pointer and then
// locks it there, whatever the legacy cursor-hidden reader says.
func TestCameraFirstPersonLockCentersThenLocks(t *testing.T) {
	calls, script, reading, camera := cameraAnchorLockSetup(t)

	moveAbsolute(900, 220)
	for i := 0; i < 3; i++ {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	}
	if calls.grabs() != 0 {
		t.Fatalf("wheel detents grabbed before the engine locked: %+v", calls)
	}
	camera.firstPerson = true
	script.next()
	movesBefore := RobloxDirectInputStats().MoveDelivered
	moveAbsolute(905, 223)
	if currentCaptureState() != captureEngineAnchor || !engineAnchorZoomCentered() {
		t.Fatalf("state=%v zoom=%t, want the centered zoom-in lock", currentCaptureState(), engineAnchorZoomCentered())
	}
	if len(calls.center) != 1 || !calls.center[0] || len(calls.cursor) != 0 || len(calls.zoom) != 0 {
		t.Fatalf("lock calls=%+v, want one centered SetPointerLock(true) and no lock-in-place grab", calls)
	}
	if got := RobloxDirectInputStats().MoveDelivered - movesBefore; got != 1 {
		t.Fatalf("entry delivered %d moves, want one center announcement", got)
	}
	if x, y, dx, dy := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1), testDirectRecMoveFloat(2), testDirectRecMoveFloat(3); x != 640 || y != 360 || dx != 0 || dy != 0 {
		t.Fatalf("entry announcement=(%v,%v,%v,%v), want (640,360,0,0)", x, y, dx, dy)
	}

	// Look integrates from the center; visibility edges never reshape the lock.
	lookRelative(5, 3)
	reading.hidden = true
	lookRelative(1, 1)
	reading.hidden, reading.ok = false, false
	lookRelative(1, 1)
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 647 || y != 365 {
		t.Fatalf("look=(%v,%v), want (647,365) from the center", x, y)
	}
	if len(calls.center) != 1 || len(calls.cursor) != 0 || len(calls.zoom) != 0 {
		t.Fatalf("visibility edges touched the grab: %+v", calls)
	}

	// Leaving camera first person lets MouseBehavior Default end the lock at the center.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: -1})
	camera.firstPerson = false
	script.next()
	lookRelative(1, 1)
	if currentCaptureState() != captureFree || engineAnchorActive() {
		t.Fatalf("zoom-out state=%v anchor=%t, want Free", currentCaptureState(), engineAnchorActive())
	}
	if len(calls.zoom) != 2 || !calls.zoom[0] || calls.zoom[1] {
		t.Fatalf("release calls=%+v, want center re-anchor then ungrab", calls)
	}
	if x, y := testDirectRecMoveFloat(0), testDirectRecMoveFloat(1); x != 640 || y != 360 {
		t.Fatalf("release move=(%v,%v), want the center", x, y)
	}
}

// RMB camera-look still locks where the cursor is, even in camera first person.
func TestCameraFirstPersonLeavesHeldRMBLookInPlace(t *testing.T) {
	calls, script, _, camera := cameraAnchorLockSetup(t)

	moveAbsolute(900, 220)
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputScroll, X: 100, Y: 200, ScrollY: 1})
	camera.firstPerson = true
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 900, Y: 220})
	t.Cleanup(func() {
		pointerButtons.mu.Lock()
		pointerButtons.state = 0
		pointerButtons.mu.Unlock()
	})
	script.next()
	lookRelative(4, 2)
	if currentCaptureState() != captureEngineAnchor || engineAnchorCentered() {
		t.Fatalf("state=%v centered=%t, want lock-in-place", currentCaptureState(), engineAnchorCentered())
	}
	if len(calls.center) != 0 {
		t.Fatalf("RMB look centered the pointer: %+v", calls)
	}
	if x, y, ok := engineAnchorFrozenOrigin(); !ok || x != 900 || y != 220 {
		t.Fatalf("frozen origin=(%v,%v,%v), want the click (900,220)", x, y, ok)
	}
}

// A LockCurrentPosition lock that is already active when RMB goes down is a
// held-RMB camera look on the direct arm too. Even with camera first person and
// a visible cursor, the press must anchor at the click rather than center:
// pointerButtons never sees direct-arm edges, so the guard reads the mirrored
// secondary state.
func TestDirectPathHeldRMBKeepsEngineAnchorAtClick(t *testing.T) {
	calls, script, _, camera := cameraAnchorLockSetup(t)

	moveAbsolute(900, 220)
	// The engine is already in LockCurrentPosition before the press, so the
	// down edge itself selects the engine-anchor shape.
	script.next()
	camera.firstPerson = true
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 900, Y: 220})
	t.Cleanup(func() {
		handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 3, X: 900, Y: 220})
	})

	if currentCaptureState() != captureEngineAnchor || engineAnchorCentered() || engineAnchorZoomCentered() {
		t.Fatalf("state=%v centered=%t zoomCentered=%t, want lock-in-place",
			currentCaptureState(), engineAnchorCentered(), engineAnchorZoomCentered())
	}
	if len(calls.center) != 0 || len(calls.cursor) != 1 || !calls.cursor[0] {
		t.Fatalf("lock calls=%+v, want one cursor-anchored grab and no centering", calls)
	}
	if x, y, ok := engineAnchorFrozenOrigin(); !ok || x != 900 || y != 220 {
		t.Fatalf("frozen origin=(%v,%v,%v), want the click (900,220)", x, y, ok)
	}

	// The held gesture keeps looking from the click for its whole life.
	lookRelative(4, 2)
	if currentCaptureState() != captureEngineAnchor || engineAnchorCentered() {
		t.Fatalf("look state=%v centered=%t, want lock-in-place", currentCaptureState(), engineAnchorCentered())
	}
}

// The tests drive the production dispatch and delivery paths directly;
// cgo is unsupported in this package's test files, so the fake engine
// natives are the recording C functions exposed via test hooks in
// gameactivity_input.go.

func TestDispatchInputMotionEventGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	vm.mu.Unlock()

	probe := func(t *testing.T, name, sig string, intArg int32) uintptr {
		t.Helper()
		v, handled := testEventGetter(vm, objID, motionEventClass, name, sig, intArg)
		if !handled {
			t.Fatalf("%s%s not handled", name, sig)
		}
		return v
	}

	if got := probe(t, "getAction", "()I", -1); got != uintptr(uint32(motionActionDown)) {
		t.Fatalf("getAction = %d, want %d", got, motionActionDown)
	}
	if got := probe(t, "getPointerCount", "()I", -1); got != 1 {
		t.Fatalf("getPointerCount = %d, want 1", got)
	}
	if got := probe(t, "getPointerId", "(I)I", 0); got != 0 {
		t.Fatalf("getPointerId(0) = %d, want 0", got)
	}
	if got := int32(probe(t, "getPointerId", "(I)I", 1)); got != -1 {
		t.Fatalf("getPointerId(1) = %d, want -1", got)
	}
	// The MotionEvent carries the selected device identity (see
	// pointerSource/pointerTool): touch (default) or the A/B mouse gate.
	if got := int32(probe(t, "getSource", "()I", -1)); got != pointerSource() {
		t.Fatalf("getSource = %#x, want %#x", got, pointerSource())
	}
	if got := probe(t, "getToolType", "(I)I", 0); got != uintptr(uint32(pointerTool())) {
		t.Fatalf("getToolType(0) = %d, want %d", got, pointerTool())
	}
	// Android order: getAxisValue(axis, pointerIndex) — axis first.
	// x=3.5 and y=7.25 are distinct on purpose: if the axis slot were
	// misread, AXIS_Y would return the X value and fail this assertion.
	{
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", motionAxisX, 0)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		if got := math.Float32frombits(uint32(v)); got != 3.5 {
			t.Fatalf("getAxisValue(AXIS_X,0) = %v, want 3.5", got)
		}
	}
	{
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", motionAxisY, 0)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		if got := math.Float32frombits(uint32(v)); got != 7.25 {
			t.Fatalf("getAxisValue(AXIS_Y,0) = %v, want 7.25", got)
		}
	}
	if got := int64(probe(t, "getEventTime", "()J", -1)); got != 250 {
		t.Fatalf("getEventTime = %d, want 250", got)
	}
	if got := int64(probe(t, "getDownTime", "()J", -1)); got != 100 {
		t.Fatalf("getDownTime = %d, want 100", got)
	}
	if got := probe(t, "getHistorySize", "()I", -1); got != 0 {
		t.Fatalf("getHistorySize = %d, want 0", got)
	}
}

// TestGetAxisValueAndroidArgumentOrder pins the Android MotionEvent
// argument order — getAxisValue(axis, pointerIndex): the AXIS request is
// args[0], the pointer index args[1]. Regression for the swapped-slot bug
// that answered every AXIS_Y read with the X coordinate (single-pointer
// events carry pointerIndex 0, which matched AXIS_X under the swapped
// reading). x=3.5 and y=7.25 are distinct so the X/Y mix-up cannot pass.
func TestGetAxisValueAndroidArgumentOrder(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	vm.mu.Unlock()

	axis := func(t *testing.T, a, b int32) float32 {
		t.Helper()
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", a, b)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}

	if x, y := float32(3.5), float32(7.25); x == y {
		t.Fatalf("test fixture degenerate: x == y == %v", x)
	}
	if got := axis(t, motionAxisX, 0); got != 3.5 {
		t.Fatalf("getAxisValue(AXIS_X, pointer 0) = %v, want 3.5", got)
	}
	if got := axis(t, motionAxisY, 0); got != 7.25 {
		t.Fatalf("getAxisValue(AXIS_Y, pointer 0) = %v, want 7.25 (Android: args[0] is the axis)", got)
	}
	// The pointer index is the second slot and is ignored for this
	// single-pointer event: it must never be read as an axis (old swapped
	// reading turned AXIS_Y+0 into "axis 0" = X for both calls).
	if got := axis(t, motionAxisX, 1); got != 3.5 {
		t.Fatalf("getAxisValue(AXIS_X, pointer 1) = %v, want 3.5 (pointer index is not the axis)", got)
	}
	// Unknown axis answers 0, per Android.
	if got := axis(t, 2, 0); got != 0 {
		t.Fatalf("getAxisValue(axis 2, pointer 0) = %v, want 0", got)
	}

	// getHistoricalAxisValue(axis, pointerIndex, pos) — same axis-first
	// order on the three-int form.
	hist := func(t *testing.T, a, b, c int32) float32 {
		t.Helper()
		v, handled := testEventGetterIII(vm, objID, motionEventClass, "getHistoricalAxisValue", "(III)F", a, b, c)
		if !handled {
			t.Fatal("getHistoricalAxisValue(III)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}
	if got := hist(t, motionAxisX, 0, 0); got != 3.5 {
		t.Fatalf("getHistoricalAxisValue(AXIS_X, 0, 0) = %v, want 3.5", got)
	}
	if got := hist(t, motionAxisY, 0, 0); got != 7.25 {
		t.Fatalf("getHistoricalAxisValue(AXIS_Y, 0, 0) = %v, want 7.25 (axis is args[0])", got)
	}
	if got := hist(t, 2, 0, 0); got != 0 {
		t.Fatalf("getHistoricalAxisValue(axis 2, 0, 0) = %v, want 0", got)
	}
}

// TestX11BridgeKeepsXYDistinct drives the production x11→GameActivity
// handler with distinct coordinates and verifies the delivered MotionEvent
// answers getAxisValue(axis, pointerIndex) with X and Y unmixed end to end.
// Regression seam for the axis-slot bug class: the getter slot itself is
// pinned by TestGetAxisValueAndroidArgumentOrder; this pins the bridge hop
// in front of it (ev.X→x, ev.Y→y must not swap) and the unmapped-key guard.
func TestX11BridgeKeepsXYDistinct(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	// InputStats are package-global and shared across tests: assert deltas.
	consumedBefore := InputDeliveryStats().PointerConsumed

	const cx, cy = float32(640), float32(383)
	handleX11InputEvent(x11.InputEvent{
		Kind:          x11.InputPointer,
		PointerAction: x11.PointerDown,
		Button:        1,
		X:             cx,
		Y:             cy,
	})
	objID := int64(testRecTouchID(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	if o.fields["action"] != int32(x11.PointerDown) {
		t.Fatalf("bridged action = %v, want %d", o.fields["action"], x11.PointerDown)
	}
	if o.fields["x"] != cx || o.fields["y"] != cy {
		t.Fatalf("bridged position = x=%v y=%v, want x=%v y=%v", o.fields["x"], o.fields["y"], cx, cy)
	}
	axis := func(t *testing.T, a, b int32) float32 {
		t.Helper()
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", a, b)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}
	if got := axis(t, motionAxisX, 0); got != cx {
		t.Fatalf("bridged getAxisValue(AXIS_X,0) = %v, want %v", got, cx)
	}
	if got := axis(t, motionAxisY, 0); got != cy {
		t.Fatalf("bridged getAxisValue(AXIS_Y,0) = %v, want %v (X and Y must stay distinct through the bridge)", got, cy)
	}
	if got := InputDeliveryStats().PointerConsumed; got != consumedBefore+1 {
		t.Fatalf("PointerConsumed delta = %d, want 1", got-consumedBefore)
	}

	// An unmapped key (keycode 0) must be dropped by the bridge guard,
	// never dispatched as an invalid key event.
	before := InputDeliveryStats().KeyDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyCode: 0, KeyPressed: true})
	if got := InputDeliveryStats().KeyDelivered; got != before {
		t.Fatalf("unmapped key dispatched through the bridge")
	}
}

func TestDispatchInputKeyEventGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newKeyEventLocked(111, true, 50, 51, 9)
	objID := o.id
	vm.mu.Unlock()

	probe := func(t *testing.T, name, sig string, intArg int32) uintptr {
		t.Helper()
		v, handled := testEventGetter(vm, objID, keyEventClass, name, sig, intArg)
		if !handled {
			t.Fatalf("%s%s not handled", name, sig)
		}
		return v
	}

	if got := probe(t, "getAction", "()I", -1); got != uintptr(uint32(keyEventActionDown)) {
		t.Fatalf("getAction = %d, want %d", got, keyEventActionDown)
	}
	if got := probe(t, "getKeyCode", "()I", -1); got != 111 {
		t.Fatalf("getKeyCode = %d, want 111", got)
	}
	if got := probe(t, "getScanCode", "()I", -1); got != 9 {
		t.Fatalf("getScanCode = %d, want 9", got)
	}
	if got := probe(t, "getUnicodeChar", "()I", -1); got != 0 {
		t.Fatalf("getUnicodeChar = %d, want 0 (no text mapping this pass)", got)
	}
	if got := probe(t, "getRepeatCount", "()I", -1); got != 0 {
		t.Fatalf("getRepeatCount = %d, want 0", got)
	}
	if got := probe(t, "getSource", "()I", -1); got != uintptr(uint32(sourceKeyboard)) {
		t.Fatalf("getSource = %#x, want %#x", got, sourceKeyboard)
	}
}

func TestDispatchInputOnlyEventClasses(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	cls := vm.classes["java/io/File"]
	o := vm.newObjectLocked(cls)
	objID := o.id
	vm.mu.Unlock()

	if _, handled := testEventGetter(vm, objID, "java/io/File", "getAction", "()I", -1); handled {
		t.Fatal("non-event class handled by input dispatch")
	}
}

func inputTestVM(t *testing.T, natives map[string]uintptr) *VM {
	t.Helper()
	testRecReset()
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	for k, v := range natives {
		vm.natives[k] = v
	}
	vm.mu.Unlock()
	t.Cleanup(ClearGameActivityInputTarget)
	return vm
}

func TestDispatchGameActivityFocus(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onWindowFocusChangedNative", "(JZ)V"): testRecordFocusFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	if !DispatchGameActivityFocus(true) {
		t.Fatal("focus dispatch failed")
	}
	if got := testRec4(0); got != vm.Env().Raw() {
		t.Fatalf("recorded env = %#x, want %#x", got, vm.Env().Raw())
	}
	if got := testRec4(1); got != 42 {
		t.Fatalf("recorded activity = %d, want 42", got)
	}
	if got := testRec4(2); got != 77 {
		t.Fatalf("recorded handle = %d, want 77", got)
	}
	if got := testRec4(3); got != 1 {
		t.Fatalf("recorded gained = %d, want 1", got)
	}
	if got := InputDeliveryStats().FocusDelivered; got != 1 {
		t.Fatalf("FocusDelivered = %d, want 1", got)
	}
}

func TestDispatchGameActivityKey(t *testing.T) {
	// The engine registers onKeyDownNative/onKeyUpNative with a leading J
	// handle: (JLandroid/view/KeyEvent;)Z (RegisterNatives batch, launch
	// logs). Dispatch must resolve exactly that identity and pass
	// (env, activity, handle, keyEvent) in that order.
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"): testRecordKeyFn(),
		methodLogName(gameActivityClass, "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"):   testRecordKeyFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	consumedBefore := InputDeliveryStats().KeyConsumed

	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key down dispatch failed (engine verdict false)")
	}
	if got := testRec4(2); got != 77 {
		t.Fatalf("recorded leading J (handle) = %d, want 77", got)
	}
	objID := int64(testRec4(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != keyEventClass {
		t.Fatalf("delivered object = %+v, want KeyEvent", o)
	}
	if o.fields["keyCode"] != int32(111) || o.fields["scanCode"] != int32(9) || o.fields["action"] != keyEventActionDown {
		t.Fatalf("KeyEvent fields = %+v", o.fields)
	}
	if got := InputDeliveryStats().KeyConsumed - consumedBefore; got != 1 {
		t.Fatalf("KeyConsumed delta = %d, want 1", got)
	}

	if !DispatchGameActivityKey(111, 9, false) {
		t.Fatal("key up dispatch failed")
	}
	o2 := vm.get(int64(testRec4(3)))
	if o2 == nil || o2.fields["action"] != keyEventActionUp {
		t.Fatalf("key up event fields = %+v", o2.fields)
	}
	if down, _ := o.fields["downTime"].(int64); o2.fields["downTime"] != down {
		t.Fatalf("key UP downTime = %v, want the matching DOWN's %d", o2.fields["downTime"], down)
	}
}

func TestDispatchGameActivityPointerMarshaling(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	// InputStats are package-global and shared across tests: assert deltas.
	consumedBefore := InputDeliveryStats().PointerConsumed

	if !DispatchGameActivityPointer(motionActionDown, 3.5, 7.25, 1) {
		t.Fatal("pointer dispatch failed")
	}
	// Proven packing (APK DEX, GameActivity.N1):
	// i1..i5 = pointerCount, historySize, deviceId, source, action.
	if got := testRecTouchInt(0); got != 1 {
		t.Fatalf("primitive i1 (pointerCount) = %d, want 1", got)
	}
	if got := testRecTouchInt(1); got != 0 {
		t.Fatalf("primitive i2 (historySize) = %d, want 0", got)
	}
	if got := testRecTouchInt(2); got != 0 {
		t.Fatalf("primitive i3 (deviceId) = %d, want 0", got)
	}
	if got := testRecTouchInt(3); got != uintptr(uint32(pointerSource())) {
		t.Fatalf("primitive i4 (source) = %#x, want %#x", got, pointerSource())
	}
	if got := testRecTouchInt(4); got != uintptr(uint32(motionActionDown)) {
		t.Fatalf("primitive i5 (action) = %d, want %d", got, motionActionDown)
	}
	// j1, j2 = eventTime, downTime (DEX order — eventTime first).
	if got := testRecTouchLong(0); got < 0 || got != testRecTouchLong(1) {
		t.Fatalf("primitive j1/j2 (eventTime/downTime) = %d/%d, want equal monotonic millis for DOWN", testRecTouchLong(0), testRecTouchLong(1))
	}
	// k1..k6 = flags, metaState, actionButton, buttonState, classification, edgeFlags.
	// Touch mode: buttonState is 0 (Android touchscreen events carry no
	// mouse buttons) regardless of the caller argument; mouse mode keeps
	// the derived BUTTON_PRIMARY press state.
	wantButton := int32(0)
	if !pointerDeviceIsTouch() {
		wantButton = 1
	}
	if got := int32(testRecTouchInt(8)); got != wantButton {
		t.Fatalf("primitive k4 (buttonState) = %d, want %d", got, wantButton)
	}
	if got := testRecTouchFloat(0); got != 1 {
		t.Fatalf("primitive f1 (xPrecision) = %v, want 1", got)
	}
	objID := int64(testRecTouchID(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	if o.fields["x"] != float32(3.5) || o.fields["y"] != float32(7.25) {
		t.Fatalf("MotionEvent position = %+v", o.fields)
	}
	if got := InputDeliveryStats().PointerConsumed; got != consumedBefore+1 {
		t.Fatalf("PointerConsumed delta = %d, want 1", got-consumedBefore)
	}
}

// TestGameActivityEventObjectsAreBounded pins the per-edge allocation
// contract: the GameActivity dispatchers reuse one MotionEvent and one
// KeyEvent object per VM. A stream of input edges must neither grow the VM
// object table nor leave per-edge JNI local references behind.
func TestGameActivityEventObjectsAreBounded(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"):                      testRecordKeyFn(),
		methodLogName(gameActivityClass, "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"):                        testRecordKeyFn(),
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	liveEvents := func(class string) (int, int32) {
		vm.mu.RLock()
		defer vm.mu.RUnlock()
		n := 0
		var refs int32
		for _, o := range vm.objects {
			if o.class != nil && o.class.name == class {
				n++
				refs += o.localRefs.Load()
			}
		}
		return n, refs
	}

	for i := 0; i < 64; i++ {
		if !DispatchGameActivityKey(int32(29+i%10), int32(10+i), i%2 == 0) {
			t.Fatal("key dispatch failed")
		}
		if !DispatchGameActivityPointer(int32(i%3), float32(i), float32(i*2), 1) {
			t.Fatal("pointer dispatch failed")
		}
	}
	if n, refs := liveEvents(keyEventClass); n != 1 || refs != 0 {
		t.Fatalf("KeyEvent objects=%d localRefs=%d, want one pooled object with no local reference", n, refs)
	}
	if n, refs := liveEvents(motionEventClass); n != 1 || refs != 0 {
		t.Fatalf("MotionEvent objects=%d localRefs=%d, want one pooled object with no local reference", n, refs)
	}
}

func TestInputDroppedWithoutTarget(t *testing.T) {
	ClearGameActivityInputTarget()
	testRecReset()
	// Target stays unwired: everything drops, nothing is queued or sent.
	before := InputDeliveryStats().Dropped
	if DispatchGameActivityFocus(true) {
		t.Fatal("focus delivered without target")
	}
	if DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key delivered without target")
	}
	if DispatchGameActivityPointer(motionActionDown, 0, 0, 1) {
		t.Fatal("pointer delivered without target")
	}
	after := InputDeliveryStats().Dropped
	if after != before+3 {
		t.Fatalf("Dropped = %d, want %d", after, before+3)
	}
	if testRec4(2) != 0 {
		t.Fatal("recording native invoked without target")
	}
}

// lastTouch reads the (downTime, eventTime, action) of the most recently
// delivered MotionEvent object.
func lastTouch(t *testing.T, vm *VM) (down, event int64, action int32) {
	t.Helper()
	o := vm.get(int64(testRecTouchID(3)))
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	down, _ = o.fields["downTime"].(int64)
	event, _ = o.fields["eventTime"].(int64)
	action, _ = o.fields["action"].(int32)
	return down, event, action
}

// TestPointerGestureDownTimeSemantics proves the Android MotionEvent
// gesture contract on the delivered events: the MOVEs and the ACTION_UP of
// one gesture carry the initiating ACTION_DOWN's downTime, eventTime
// advances, the primitive j1/j2 slots mirror the object, and a new gesture
// starts a fresh downTime.
func TestPointerGestureDownTimeSemantics(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	if !DispatchGameActivityPointer(motionActionDown, 10, 10, 1) {
		t.Fatal("pointer down dispatch failed")
	}
	downDown, downAt, act := lastTouch(t, vm)
	if act != motionActionDown || downDown != downAt {
		t.Fatalf("DOWN times = (%d, %d) action=%d, want downTime==eventTime", downDown, downAt, act)
	}

	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionMove, 20, 25, 1) {
		t.Fatal("pointer move dispatch failed")
	}
	moveDown, moveAt, act := lastTouch(t, vm)
	if act != motionActionMove {
		t.Fatalf("action = %d, want MOVE", act)
	}
	if moveDown != downDown {
		t.Fatalf("MOVE downTime = %d, want the DOWN's %d", moveDown, downDown)
	}
	if moveAt <= moveDown {
		t.Fatalf("MOVE eventTime = %d, want > downTime %d", moveAt, moveDown)
	}
	// j1 = eventTime, j2 = downTime (DEX order) on a non-DOWN event.
	if got := testRecTouchLong(0); got != moveAt {
		t.Fatalf("primitive j1 (eventTime) = %d, want %d", got, moveAt)
	}
	if got := testRecTouchLong(1); got != moveDown {
		t.Fatalf("primitive j2 (downTime) = %d, want %d", got, moveDown)
	}

	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionUp, 20, 25, 0) {
		t.Fatal("pointer up dispatch failed")
	}
	upDown, upAt, act := lastTouch(t, vm)
	if act != motionActionUp {
		t.Fatalf("action = %d, want UP", act)
	}
	if upDown != downDown {
		t.Fatalf("UP downTime = %d, want the matching DOWN's %d (Android gesture contract)", upDown, downDown)
	}
	if upAt <= moveAt {
		t.Fatalf("UP eventTime = %d, want > MOVE eventTime %d", upAt, moveAt)
	}
	if got := testRecTouchLong(0); got != upAt {
		t.Fatalf("primitive j1 (eventTime) = %d, want %d (must mirror the object)", got, upAt)
	}
	if got := testRecTouchLong(1); got != upDown {
		t.Fatalf("primitive j2 (downTime) = %d, want %d (must mirror the object)", got, upDown)
	}

	// A new gesture starts a fresh downTime, and a stray move with no open
	// gesture self-anchors (downTime == eventTime) instead of inventing one.
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionDown, 5, 5, 1) {
		t.Fatal("second pointer down dispatch failed")
	}
	down2, down2At, act := lastTouch(t, vm)
	if act != motionActionDown || down2 != down2At {
		t.Fatalf("second DOWN times = (%d, %d), want fresh downTime==eventTime", down2, down2At)
	}
	if down2 <= upAt {
		t.Fatalf("second DOWN downTime = %d, want > previous gesture UP time %d", down2, upAt)
	}
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionUp, 5, 5, 0) {
		t.Fatal("second pointer up dispatch failed")
	}
	if !DispatchGameActivityPointer(motionActionMove, 1, 1, 0) {
		t.Fatal("stray pointer move dispatch failed")
	}
	strayDown, strayAt, _ := lastTouch(t, vm)
	if strayDown != strayAt {
		t.Fatalf("stray MOVE times = (%d, %d), want self-anchored downTime==eventTime", strayDown, strayAt)
	}
}

// TestPointerButtonState proves the Android button-state derivation from
// real X11 button press/release pairs: left=BUTTON_PRIMARY(1),
// right=BUTTON_SECONDARY(2), moves report the held set, and the state is
// clear after both releases.
func TestPointerButtonState(t *testing.T) {
	if got := pointerButtonState(1, true); got != 1 {
		t.Fatalf("left down state = %d, want 1", got)
	}
	if got := pointerButtonState(0, false); got != 1 {
		t.Fatalf("move during left drag state = %d, want 1", got)
	}
	if got := pointerButtonState(3, true); got != 3 {
		t.Fatalf("right added state = %d, want 3", got)
	}
	if got := pointerButtonState(1, false); got != 2 {
		t.Fatalf("left released state = %d, want 2", got)
	}
	if got := pointerButtonState(3, false); got != 0 {
		t.Fatalf("right released state = %d, want 0", got)
	}
}

// TestKeyGestureDownTimeSemantics proves the Android KeyEvent contract on
// the delivered events: a key UP carries the matching DOWN's downTime and
// a new press starts a fresh one. The key stream is independent of the
// pointer stream (they must not share gesture state).
func TestKeyGestureDownTimeSemantics(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"):                      testRecordKeyFn(),
		methodLogName(gameActivityClass, "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"):                        testRecordKeyFn(),
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)

	lastKey := func(t *testing.T) (down, event int64) {
		t.Helper()
		o := vm.get(int64(testRec4(3)))
		if o == nil || o.class == nil || o.class.name != keyEventClass {
			t.Fatalf("delivered object = %+v, want KeyEvent", o)
		}
		down, _ = o.fields["downTime"].(int64)
		event, _ = o.fields["eventTime"].(int64)
		return down, event
	}

	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key down dispatch failed")
	}
	kDown, kAt := lastKey(t)
	if kDown != kAt {
		t.Fatalf("key DOWN times = (%d, %d), want downTime==eventTime", kDown, kAt)
	}

	// An unrelated pointer gesture must not disturb the key gesture clock.
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityPointer(motionActionDown, 0, 0, 1) || !DispatchGameActivityPointer(motionActionUp, 0, 0, 0) {
		t.Fatal("pointer gesture dispatch failed")
	}
	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityKey(111, 9, false) {
		t.Fatal("key up dispatch failed")
	}
	kUpDown, kUpAt := lastKey(t)
	if kUpDown != kDown {
		t.Fatalf("key UP downTime = %d, want the matching DOWN's %d", kUpDown, kDown)
	}
	if kUpAt <= kUpDown {
		t.Fatalf("key UP eventTime = %d, want > downTime %d", kUpAt, kUpDown)
	}

	time.Sleep(3 * time.Millisecond)
	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("second key down dispatch failed")
	}
	kDown2, kAt2 := lastKey(t)
	if kDown2 != kAt2 || kDown2 <= kUpAt {
		t.Fatalf("second key DOWN times = (%d, %d), want fresh downTime > %d", kDown2, kAt2, kUpAt)
	}
}

// TestPointerDeviceTouchIdentity pins the explicit coherent touch identity
// against the public Android constants: SOURCE_TOUCHSCREEN (0x1002) +
// TOOL_TYPE_FINGER (1) on every MotionEvent, with the KeyEvent stream
// untouched (SOURCE_KEYBOARD).
func TestPointerDeviceTouchIdentity(t *testing.T) {
	t.Setenv("TIPSY_INPUT_DEVICE", "touch")
	t.Cleanup(ResetPointerDeviceMode)
	ResetPointerDeviceMode()
	if !PointerDeviceIsTouch() {
		t.Fatal("TIPSY_INPUT_DEVICE=touch did not select the touch identity")
	}
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	k := vm.newKeyEventLocked(111, true, 100, 101, 9)
	keyID := k.id
	vm.mu.Unlock()

	if got, _ := testEventGetter(vm, objID, motionEventClass, "getSource", "()I", -1); int32(uint32(got)) != sourceTouchscreen {
		t.Fatalf("MotionEvent getSource = %#x, want %#x (SOURCE_TOUCHSCREEN)", int32(uint32(got)), sourceTouchscreen)
	}
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getToolType", "(I)I", 0); int32(uint32(got)) != toolTypeFinger {
		t.Fatalf("getToolType(0) = %d, want %d (TOOL_TYPE_FINGER)", int32(uint32(got)), toolTypeFinger)
	}
	if got, _ := testEventGetter(vm, keyID, keyEventClass, "getSource", "()I", -1); int32(uint32(got)) != sourceKeyboard {
		t.Fatalf("KeyEvent getSource = %#x, want %#x (keys are unaffected by the pointer identity)", int32(uint32(got)), sourceKeyboard)
	}
}

// TestTouchModeCoherentDelivery drives the production x11→GameActivity
// bridge in explicit touch mode: a real left DOWN/UP and its drag MOVE
// arrive as SOURCE_TOUCHSCREEN/TOOL_TYPE_FINGER with buttonState 0 (touch
// events carry no mouse buttons) and gesture-paired downTimes; a real
// right-button press has no honest touch identity and is dropped; a MOVE
// without an open gesture is dropped.
func TestTouchModeCoherentDelivery(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	t.Setenv("TIPSY_INPUT_DEVICE", "touch")
	t.Cleanup(ResetPointerDeviceMode)
	ResetPointerDeviceMode()
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	// Close any gesture a previous test left open, so this test starts
	// from a closed gesture stream like a fresh launch.
	DispatchGameActivityPointer(motionActionUp, 0, 0, 0)
	deliveredBefore := InputDeliveryStats().PointerDelivered
	droppedBefore := InputDeliveryStats().Dropped

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 640, Y: 383})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Button: 0, X: 650, Y: 385})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerUp, Button: 1, X: 650, Y: 385})
	// Right button: a real X11 event, but not presentable as touch.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 650, Y: 385})
	// Stray MOVE with no open gesture (a right-drag would look like this).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, Button: 0, X: 660, Y: 390})

	if got := InputDeliveryStats().PointerDelivered; got != deliveredBefore+3 {
		t.Fatalf("PointerDelivered delta = %d, want 3 (down, move, up)", got-deliveredBefore)
	}
	if got := InputDeliveryStats().Dropped; got != droppedBefore+2 {
		t.Fatalf("Dropped delta = %d, want 2 (right button + stray move)", got-droppedBefore)
	}
	objID := int64(testRecTouchID(3))
	o := vm.get(objID)
	if o == nil || o.class == nil || o.class.name != motionEventClass {
		t.Fatalf("delivered object = %+v, want MotionEvent", o)
	}
	if o.fields["action"] != int32(x11.PointerUp) {
		t.Fatalf("last bridged action = %v, want UP", o.fields["action"])
	}
	if o.fields["x"] != float32(650) || o.fields["y"] != float32(385) {
		t.Fatalf("bridged position = %v/%v, want 650/385", o.fields["x"], o.fields["y"])
	}
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getSource", "()I", -1); int32(uint32(got)) != sourceTouchscreen {
		t.Fatalf("bridged getSource = %#x, want %#x", int32(uint32(got)), sourceTouchscreen)
	}
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getToolType", "(I)I", 0); int32(uint32(got)) != toolTypeFinger {
		t.Fatalf("bridged getToolType = %d, want %d", int32(uint32(got)), toolTypeFinger)
	}
	if got := int32(testRecTouchInt(3)); got != sourceTouchscreen {
		t.Fatalf("bridged primitive i4 (source) = %#x, want %#x", got, sourceTouchscreen)
	}
	if got := int32(testRecTouchInt(8)); got != 0 {
		t.Fatalf("bridged primitive k4 (buttonState) = %d, want 0 in touch mode", got)
	}
	// The UP's primitive longs must mirror the delivered object.
	if dt, _ := o.fields["downTime"].(int64); testRecTouchLong(1) != dt {
		t.Fatalf("UP primitive j2 (downTime) = %d, want object %d (must mirror)", testRecTouchLong(1), dt)
	}
}

// TestPointerDeviceMouseIdentity pins the default desktop identity end to end
// — SOURCE_MOUSE (0x2002) +
// TOOL_TYPE_MOUSE (3), right-button delivery, and BUTTON_PRIMARY-derived
// buttonState on the primitives.
func TestPointerDeviceMouseIdentity(t *testing.T) {
	selectPointerPath(t, "gameactivity")
	t.Setenv("TIPSY_INPUT_DEVICE", "")
	t.Cleanup(ResetPointerDeviceMode)
	ResetPointerDeviceMode()
	if PointerDeviceIsTouch() {
		t.Fatal("default device mode is not mouse")
	}
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"): testRecordTouchFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	DispatchGameActivityPointer(motionActionUp, 0, 0, 0) // close any open gesture
	deliveredBefore := InputDeliveryStats().PointerDelivered

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 1, X: 640, Y: 383})
	if got := InputDeliveryStats().PointerDelivered; got != deliveredBefore+1 {
		t.Fatalf("PointerDelivered delta = %d, want 1", got-deliveredBefore)
	}
	if got := int32(testRecTouchInt(3)); got != sourceMouse {
		t.Fatalf("primitive i4 (source) = %#x, want %#x", got, sourceMouse)
	}
	if got := int32(testRecTouchInt(8)); got != 1 {
		t.Fatalf("primitive k4 (buttonState) = %d, want 1 (BUTTON_PRIMARY)", got)
	}
	objID := int64(testRecTouchID(3))
	if got, _ := testEventGetter(vm, objID, motionEventClass, "getToolType", "(I)I", 0); int32(uint32(got)) != toolTypeMouse {
		t.Fatalf("getToolType(0) = %d, want %d (TOOL_TYPE_MOUSE)", int32(uint32(got)), toolTypeMouse)
	}
	// Right button is delivered in mouse mode, as before this pass.
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerDown, Button: 3, X: 1, Y: 2})
	if got := InputDeliveryStats().PointerDelivered; got != deliveredBefore+2 {
		t.Fatalf("right button dropped in mouse mode: delta = %d, want 2", got-deliveredBefore)
	}
}

func TestKeyRepeatGestureTimesAndGetter(t *testing.T) {
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"): testRecordKeyFn(),
		methodLogName(gameActivityClass, "onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"):   testRecordKeyFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	var first int64
	for i, edge := range []struct {
		code, scan int32
		down       bool
		repeat     int32
	}{
		{51, 25, true, 0}, {59, 50, true, 0}, {51, 25, true, 1}, {59, 50, false, 0}, {51, 25, true, 2}, {51, 25, false, 0},
	} {
		if !dispatchGameActivityKey(edge.code, edge.scan, edge.down, edge.repeat) {
			t.Fatal("dispatch failed")
		}
		id := int64(testRec4(3))
		o := vm.get(id)
		dt := o.fields["downTime"].(int64)
		at := o.fields["eventTime"].(int64)
		if i == 0 {
			first = dt
		}
		if edge.code == 51 && dt != first {
			t.Fatalf("edge %d reset held W downTime %d to %d", i, first, dt)
		}
		if at < dt {
			t.Fatalf("eventTime=%d before downTime=%d", at, dt)
		}
		if got, _ := testEventGetter(vm, id, keyEventClass, "getRepeatCount", "()I", -1); int32(got) != edge.repeat {
			t.Fatalf("repeat getter=%d, want %d", got, edge.repeat)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Deterministic clock checks catch a zero downTime and independent keys
	// without relying on the resolution of the real event clock.
	if keyGestureTime(0, 51, true, 0) != 0 || keyGestureTime(5, 59, true, 0) != 5 || keyGestureTime(10, 51, true, 1) != 0 || keyGestureTime(15, 59, false, 0) != 5 || keyGestureTime(20, 51, false, 0) != 0 || keyGestureTime(25, 51, true, 0) != 25 || keyGestureTime(30, 51, false, 0) != 25 {
		t.Fatal("interleaved key clocks violated Android gesture semantics")
	}
}
