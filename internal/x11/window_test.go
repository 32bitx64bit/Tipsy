// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11/x11probe"
)

func TestDecodePointerRingAction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw      int32
		action   int32
		relative bool
	}{
		{0, PointerDown, false},
		{1, PointerUp, false},
		{2, PointerMove, false},
		{3, PointerMove, true},
	}
	for _, tc := range cases {
		action, relative := decodePointerRingAction(tc.raw)
		if action != tc.action || relative != tc.relative {
			t.Fatalf("decodePointerRingAction(%d) = (%d,%t), want (%d,%t)",
				tc.raw, action, relative, tc.action, tc.relative)
		}
	}
}

func TestSetFullscreenRejectsClosedWindow(t *testing.T) {
	t.Parallel()
	w := &Window{}
	if err := w.SetFullscreen(true); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetFullscreen = %v, want ErrClosed", err)
	}
}

func TestEmbeddedWindowIcon(t *testing.T) {
	t.Parallel()
	icon, err := windowIconARGB()
	if err != nil {
		t.Fatalf("windowIconARGB: %v", err)
	}
	if len(icon) < 3 {
		t.Fatalf("icon item count = %d, want width + height + pixels", len(icon))
	}
	width, height := int(icon[0]), int(icon[1])
	if width != 512 || height != 512 {
		t.Fatalf("icon dimensions = %dx%d, want canonical 512x512 branding", width, height)
	}
	if got, want := len(icon), 2+width*height; got != want {
		t.Fatalf("icon item count = %d, want %d", got, want)
	}
}

func TestOpenPlacesOnPrimaryAndLeavesPointerUnforced(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	outputs, err := ListOutputs()
	if err != nil || len(outputs) == 0 {
		t.Fatalf("ListOutputs: %v %#v", err, outputs)
	}
	const width, height = 64, 48
	wantX, wantY, force := ResolvePlacement(DisplayPrimary, width, height, outputs)
	if !force {
		t.Fatal("primary placement was not forced on a live display")
	}

	primary, err := OpenOnDisplay("placement-primary", width, height, DisplayPrimary)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("OpenOnDisplay primary: %v", err)
	}
	defer primary.Close()
	target, ok := pickOutput(DisplayPrimary, outputs)
	if !ok {
		t.Fatal("no primary output")
	}
	x, y, err := waitRootOriginOnOutput(t, primary, target)
	if err != nil {
		t.Fatalf("primary origin: %v", err)
	}
	if x11probe.WindowManagerPresent() {
		if x < target.X || y < target.Y || x >= target.X+target.Width || y >= target.Y+target.Height {
			t.Fatalf("primary origin = (%d,%d) is outside %+v", x, y, target)
		}
	} else if x != wantX || y != wantY {
		t.Fatalf("primary origin = (%d,%d), want (%d,%d) on %+v", x, y, wantX, wantY, outputs)
	}

	pointer, err := OpenOnDisplay("placement-pointer", width, height, DisplayPointer)
	if err != nil {
		t.Fatalf("OpenOnDisplay pointer: %v", err)
	}
	defer pointer.Close()
	x, y, err = waitRootOrigin(t, pointer)
	if err != nil {
		t.Fatalf("pointer origin: %v", err)
	}
	if !x11probe.WindowManagerPresent() && (x != 0 || y != 0) {
		t.Fatalf("pointer origin = (%d,%d), want unforced (0,0) on a bare X server", x, y)
	}
}

func waitRootOrigin(t *testing.T, w *Window) (int, int, error) {
	t.Helper()
	return waitRootOriginOnOutput(t, w, Output{})
}

func waitRootOriginOnOutput(t *testing.T, w *Window, out Output) (int, int, error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastX, lastY int
	var last error
	have := false
	for time.Now().Before(deadline) {
		x, y, err := x11probe.WindowRootOrigin(w.XID())
		if err != nil {
			last = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		lastX, lastY, last, have = x, y, nil, true
		if out.Width <= 0 || out.Height <= 0 ||
			(x >= out.X && y >= out.Y && x < out.X+out.Width && y < out.Y+out.Height) {
			return x, y, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !have {
		return 0, 0, last
	}
	return lastX, lastY, nil
}

func TestRobloxWindowBrandingProperties(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	w, err := Open("Roblox", 96, 80)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	title, err := x11probe.WindowTitle(w.XID())
	if err != nil {
		t.Fatalf("read _NET_WM_NAME: %v", err)
	}
	if title != RobloxWindowTitle {
		t.Fatalf("_NET_WM_NAME = %q, want %q", title, RobloxWindowTitle)
	}
	width, height, items, err := x11probe.WindowIconInfo(w.XID())
	if err != nil {
		t.Fatalf("read _NET_WM_ICON: %v", err)
	}
	if width != 512 || height != 512 || items != 2+width*height {
		t.Fatalf("_NET_WM_ICON = %dx%d, %d items; want 512x512, %d items",
			width, height, items, 2+uint64(512*512))
	}
}

// TestRobloxWindowMinimumGeometry applies the floor at the actual X11
// boundary. WM_NORMAL_HINTS is the protocol consulted by the window manager
// for the user's title-bar drag. The valid resize storm confirms the hint does
// not turn this production window into a fixed-size one.
func TestRobloxWindowMinimumGeometry(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	w, err := Open("Roblox", 96, 80)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	if width, height := w.Size(); width != RobloxMinimumWidth || height != RobloxMinimumHeight {
		t.Fatalf("initial Roblox size = %dx%d, want %dx%d", width, height, RobloxMinimumWidth, RobloxMinimumHeight)
	}
	if width, height, err := x11probe.WindowMinimumSize(w.XID()); err != nil {
		t.Fatalf("WM_NORMAL_HINTS: %v", err)
	} else if width != RobloxMinimumWidth || height != RobloxMinimumHeight {
		t.Fatalf("WM_NORMAL_HINTS min = %dx%d, want %dx%d", width, height, RobloxMinimumWidth, RobloxMinimumHeight)
	}

	for _, size := range [][2]int{{1280, 720}, {1366, 768}, {1440, 810}, {1600, 900}} {
		if err := x11probe.Resize(w.XID(), size[0], size[1]); err != nil {
			t.Fatalf("valid resize %dx%d: %v", size[0], size[1], err)
		}
		if err := w.Pump(); err != nil {
			t.Fatalf("Pump valid resize %dx%d: %v", size[0], size[1], err)
		}
	}
	waitWindowSize(t, w, 1600, 900)
}

func waitWindowSize(t *testing.T, w *Window, wantWidth, wantHeight int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var gotWidth, gotHeight int
	for time.Now().Before(deadline) {
		if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
			t.Fatalf("Pump waiting for %dx%d: %v", wantWidth, wantHeight, err)
		}
		width, height, err := x11probe.WindowSize(w.XID())
		if err == nil {
			gotWidth, gotHeight = width, height
			if width == wantWidth && height == wantHeight {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server geometry = %dx%d, want %dx%d", gotWidth, gotHeight, wantWidth, wantHeight)
}

func TestFullscreenRoundTripWithEWMHWindowManager(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	if !x11probe.WindowManagerPresent() {
		t.Skip("display has no EWMH window manager")
	}
	w, err := Open("Tipsy fullscreen test", 320, 180)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	if err := w.SetFullscreen(true); err != nil {
		t.Fatalf("SetFullscreen(true): %v", err)
	}
	if !x11probe.WaitFullscreen(w.XID(), true, 3*time.Second) {
		t.Fatal("window manager did not add _NET_WM_STATE_FULLSCREEN")
	}
	if err := w.SetFullscreen(false); err != nil {
		t.Fatalf("SetFullscreen(false): %v", err)
	}
	if !x11probe.WaitFullscreen(w.XID(), false, 3*time.Second) {
		t.Fatal("window manager did not remove _NET_WM_STATE_FULLSCREEN")
	}
}

func TestF11TogglesFullscreenWithEWMHWindowManager(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	if !x11probe.WindowManagerPresent() {
		t.Skip("display has no EWMH window manager")
	}
	w, err := Open("Tipsy F11 test", 320, 180)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	toggle := func(enabled bool, repeatedPress bool) {
		t.Helper()
		if err := x11probe.Key(w.XID(), xkF11, true); err != nil {
			t.Fatalf("F11 press: %v", err)
		}
		if repeatedPress {
			if err := x11probe.Key(w.XID(), xkF11, true); err != nil {
				t.Fatalf("F11 repeated press: %v", err)
			}
		}
		if err := x11probe.Key(w.XID(), xkF11, false); err != nil {
			t.Fatalf("F11 release: %v", err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			_ = w.Pump()
			if x11probe.Fullscreen(w.XID()) == enabled {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("F11 did not set fullscreen=%t", enabled)
	}

	// A held/repeated F11 must produce one state transition, not a fullscreen
	// flap for every keyboard auto-repeat event.
	toggle(true, true)
	toggle(false, false)
}

func TestOpenRejectsInvalidSize(t *testing.T) {
	t.Parallel()
	if _, err := Open("x", 0, 64); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("Open(0,64) = %v, want ErrInvalidSize", err)
	}
	if _, err := Open("x", 64, -1); !errors.Is(err, ErrInvalidSize) {
		t.Fatalf("Open(64,-1) = %v, want ErrInvalidSize", err)
	}
}

func TestOpenPumpClose(t *testing.T) {
	ensureDisplay(t)

	w, err := Open("Tipsy test", 64, 64)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if w.Display() == 0 {
		t.Fatal("Display() is nil")
	}
	if w.XID() == 0 {
		t.Fatal("XID() is 0")
	}
	if !w.CursorHidden() {
		t.Fatal("host cursor is visible over the Roblox client window")
	}
	width, height := w.Size()
	if width < 1 || height < 1 {
		t.Fatalf("Size() = %dx%d, want positive", width, height)
	}

	if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("Pump: %v", err)
	}

	// Idempotent close is checked after defer Close via a second call below.
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if w.CursorHidden() {
		t.Fatal("cursor remained defined after Close")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := w.Pump(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Pump after Close = %v, want ErrClosed", err)
	}
}

func TestBackgroundPump(t *testing.T) {
	ensureDisplay(t)
	w, err := Open("Tipsy pump thread", 64, 64)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump again: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := w.StopBackgroundPump(); err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("StopBackgroundPump: %v", err)
	}
	if err := w.StopBackgroundPump(); err != nil {
		t.Fatalf("StopBackgroundPump again: %v", err)
	}
}

func TestBackgroundPumpPreservesWMDeleteForGoLoop(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	w, err := Open("Tipsy background close", 64, 64)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	drainInputReady(w)
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	if err := x11probe.WMDelete(w.XID()); err != nil {
		t.Fatalf("WM_DELETE_WINDOW: %v", err)
	}
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-w.InputReady():
			if err := w.Pump(); errors.Is(err, ErrClosed) {
				if x11probe.Viewable(w.XID()) {
					t.Fatal("WM_DELETE acknowledged but the client window stayed viewable")
				}
				return
			} else if err != nil {
				t.Fatalf("Pump: %v", err)
			}
		case <-timer.C:
			t.Fatal("background pump consumed WM_DELETE_WINDOW without notifying Go")
		}
	}
}

func TestStopBackgroundPumpJoinsPromptly(t *testing.T) {
	ensureDisplay(t)
	w, err := Open("Tipsy pump join", 64, 64)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	if err := w.StopBackgroundPump(); err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("StopBackgroundPump: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("StopBackgroundPump took %s, want a prompt poll wakeup", elapsed)
	}
}

func TestInputReadyWakesOnKeyWithBackgroundPump(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	w, err := Open("Tipsy input wake", 64, 64)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	c := collectInput(t)
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	defer w.StopBackgroundPump()
	settleBackgroundInput(t, w, c)

	if err := x11probe.Key(w.XID(), xkA, true); err != nil {
		t.Fatalf("key press: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkA, false); err != nil {
		t.Fatalf("key release: %v", err)
	}
	select {
	case <-w.InputReady():
	case <-time.After(2 * time.Second):
		t.Fatal("InputReady did not wake on key")
	}
	if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("Pump: %v", err)
	}
	ev := c.next(t, w)
	if ev.Kind != InputKey || !ev.KeyPressed {
		t.Fatalf("first event = %+v, want key press", ev)
	}
}

func TestInputReadyCoalescesWhileGoIsBehind(t *testing.T) {
	ensureDisplay(t)
	requireProbe(t)
	w, err := Open("Tipsy wake coalesce", 64, 64)
	if err != nil {
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	defer w.StopBackgroundPump()
	settleBackgroundInput(t, w, nil)

	if err := x11probe.Key(w.XID(), xkA, true); err != nil {
		t.Fatalf("key press: %v", err)
	}
	select {
	case <-w.InputReady():
	case <-time.After(2 * time.Second):
		t.Fatal("InputReady did not wake on first key")
	}
	if err := x11probe.Key(w.XID(), xkA, false); err != nil {
		t.Fatalf("key release: %v", err)
	}
	if err := x11probe.Key(w.XID(), xkEscape, true); err != nil {
		t.Fatalf("escape press: %v", err)
	}
	select {
	case <-w.InputReady():
		t.Fatal("second wake before Pump ack; coalescing failed")
	case <-time.After(80 * time.Millisecond):
	}
	if err := w.Pump(); err != nil && !errors.Is(err, ErrClosed) {
		t.Fatalf("Pump: %v", err)
	}
}

func drainInputReady(w *Window) {
	if w == nil {
		return
	}
	for {
		select {
		case <-w.InputReady():
			// Consuming the token also needs Pump's C-side acknowledgement;
			// otherwise the next background event remains coalesced away.
			_ = w.Pump()
		default:
			return
		}
	}
}

func settleBackgroundInput(t *testing.T, w *Window, c *inputCollector) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-w.InputReady():
			_ = w.Pump()
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	drainInputReady(w)
	if c != nil {
		c.clear()
	}
}

func ensureDisplay(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") != "" {
		return
	}
	startXvfb(t)
}

func startXvfb(t *testing.T, extraArgs ...string) {
	t.Helper()
	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("DISPLAY unset and Xvfb not found")
	}

	n, err := unusedDisplay()
	if err != nil {
		t.Skipf("DISPLAY unset; no free Xvfb display: %v", err)
	}
	display := ":" + strconv.Itoa(n)
	args := append([]string{display, "-screen", "0", "1280x720x24", "-nolisten", "tcp"}, extraArgs...)
	cmd := exec.Command(xvfb, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Skipf("Xvfb start failed: %v", err)
	}
	t.Cleanup(func() {
		// Let Xvfb remove its socket/lock. SIGKILL alone leaves stale
		// display locks and eventually turns repeated suites into skips.
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	t.Setenv("DISPLAY", display)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w, err := Open("tipsy-xvfb-probe", 64, 64)
		if err == nil {
			_ = w.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("Xvfb started on %s but XOpenDisplay still failing", display)
}

func unusedDisplay() (int, error) {
	pid := os.Getpid()
	for i := 0; i < 920; i++ {
		n := 80 + (pid+i)%920
		lock := fmt.Sprintf("/tmp/.X%d-lock", n)
		if _, err := os.Stat(lock); err == nil {
			continue
		}
		return n, nil
	}
	return 0, errors.New("no free display in :80-:999")
}

func TestRefreshVersionInvalidation(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			// Output-property injection must never touch the user's desktop.
			startXvfb(t)
			requireProbe(t)
			w, err := Open("refresh invalidation", 32, 32)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if background {
				if err := w.StartBackgroundPump(); err != nil {
					t.Fatal(err)
				}
			}
			for _, change := range []struct {
				name string
				fn   func() error
			}{
				{"move", func() error { return x11probe.Move(w.XID(), 20, 30) }},
				{"resize", func() error { return x11probe.Resize(w.XID(), 48, 48) }},
				{"randr", x11probe.RandROutputProperty},
			} {
				if err := w.Pump(); err != nil {
					t.Fatal(err)
				}
				drainInputReady(w)
				before := w.RefreshVersion()
				if before == 0 {
					t.Fatal("open window has no refresh version")
				}
				if err := change.fn(); err != nil {
					t.Fatalf("%s: %v", change.name, err)
				}
				deadline := time.After(time.Second)
				for w.RefreshVersion() == before {
					if background {
						select {
						case <-w.InputReady():
						case <-deadline:
							t.Fatalf("%s did not wake/invalidate the background reader", change.name)
						}
					}
					if err := w.Pump(); err != nil {
						t.Fatal(err)
					}
					select {
					case <-deadline:
						t.Fatalf("%s did not invalidate refresh", change.name)
					default:
					}
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if w.RefreshVersion() != 0 || (*Window)(nil).RefreshVersion() != 0 {
				t.Fatal("closed/nil window has a refresh version")
			}
		})
	}
}

func TestRefreshVersionStableWithoutRandR(t *testing.T) {
	startXvfb(t, "-extension", "RANDR")
	requireProbe(t)
	w, err := Open("refresh without RandR", 32, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.randrEventBase != 0 {
		t.Fatal("test server unexpectedly has RandR")
	}
	if err := x11probe.Move(w.XID(), 25, 25); err != nil {
		t.Fatal(err)
	}
	before := w.RefreshVersion()
	if err := w.Pump(); err != nil {
		t.Fatal(err)
	}
	if w.RefreshVersion() == before {
		t.Fatal("move must invalidate even without RandR")
	}
	stable := w.RefreshVersion()
	for i := 0; i < 50; i++ {
		if err := w.Pump(); err != nil {
			t.Fatal(err)
		}
		if got := w.RefreshVersion(); got != stable {
			t.Fatalf("unchanged window version=%d, want %d", got, stable)
		}
	}
}

func TestWakeEventPumpDuringStop(t *testing.T) {
	startXvfb(t)
	w, err := Open("refresh wake teardown", 32, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 20; i++ {
		if err := w.StartBackgroundPump(); err != nil {
			t.Fatal(err)
		}
		started, done := make(chan struct{}), make(chan struct{})
		go func() {
			close(started)
			for j := 0; j < 1000; j++ {
				WakeEventPump()
			}
			close(done)
		}()
		<-started
		if err := w.StopBackgroundPump(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("refresh wake blocked during pump teardown")
		}
	}
}

// Real server autorepeat must produce one physical down, repeated downs and
// one physical up, with committed text preserved on every down. Exercise both
// XKB detectable repeat and the server's legacy release/press representation.
func TestKeyRepeatRealHold(t *testing.T) {
	for _, detectable := range []bool{true, false} {
		t.Run(fmt.Sprintf("detectable=%t", detectable), func(t *testing.T) {
			startXvfb(t) // XTest and repeat settings must never touch a desktop.
			requireProbe(t)
			w := openInputWindow(t)
			c := collectInput(t)
			if err := x11probe.DetectableRepeat(w.Display(), detectable); err != nil {
				t.Fatal(err)
			}
			if err := x11probe.RepeatRate(100, 30); err != nil {
				t.Fatal(err)
			}
			if err := x11probe.SetFocus(w.XID()); err != nil {
				t.Fatal(err)
			}
			c.clearAndSettle(t, w)
			if err := w.StartBackgroundPump(); err != nil {
				t.Fatal(err)
			}
			const key = uint64('w')
			if err := x11probe.RealKey(key, true); err != nil {
				t.Fatal(err)
			}
			defer x11probe.RealKey(key, false)
			down, up, texts := 0, 0, 0
			released := false
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				ev := c.next(t, w)
				switch ev.Kind {
				case InputKey:
					if ev.KeyCode != 51 || ev.ScanCode != 25 {
						t.Fatalf("W translation = %+v", ev)
					}
					if ev.KeyPressed {
						if ev.RepeatCount != int32(down) {
							t.Fatalf("down %d repeat=%d", down, ev.RepeatCount)
						}
						down++
						if down == 4 {
							if err := x11probe.RealKey(key, false); err != nil {
								t.Fatal(err)
							}
							released = true
						}
					} else {
						up++
						if !released || ev.RepeatCount != 0 {
							t.Fatalf("synthetic release escaped before real release: %+v", ev)
						}
					}
				case InputText:
					if ev.Text != "w" {
						t.Fatal("repeated text was changed")
					}
					texts++
				}
				if up == 1 {
					break
				}
			}
			if down < 4 || up != 1 || texts != down {
				t.Fatalf("down/up/text = %d/%d/%d", down, up, texts)
			}
		})
	}
}

func TestKeyRepeatLegacyPairsAndFocusRelease(t *testing.T) {
	startXvfb(t)
	requireProbe(t)
	w := openInputWindow(t)
	c := collectInput(t)
	c.clearAndSettle(t, w)
	key := func(sym uint64, down bool, at uint32) {
		t.Helper()
		if err := x11probe.KeyAt(w.XID(), sym, down, at); err != nil {
			t.Fatal(err)
		}
	}
	// Equal-time adjacent pairs are repeats. Different-time pairs remain
	// genuine releases/new presses, even when drained in the same batch.
	key('w', true, 100)
	key('w', false, 200)
	key('w', true, 200)
	key('w', false, 300)
	key('w', true, 301)
	key(0xffe1, true, 302) // Shift stays independent from W.
	if err := x11probe.Focus(w.XID(), false); err != nil {
		t.Fatal(err)
	}
	key('w', false, 400) // focus already released both: no duplicate up
	key(0xffe1, false, 401)
	if err := x11probe.Focus(w.XID(), true); err != nil {
		t.Fatal(err)
	}
	key('w', true, 500)
	key('w', false, 600)
	x11probe.Sync()
	if err := w.Pump(); err != nil {
		t.Fatal(err)
	}
	var keys []InputEvent
	texts, beforeFocusReleases := 0, 0
	focusLost := false
	for len(c.ch) > 0 {
		ev := <-c.ch
		switch ev.Kind {
		case InputKey:
			keys = append(keys, ev)
			if !focusLost && !ev.KeyPressed {
				beforeFocusReleases++
			}
		case InputText:
			texts++
		case InputFocus:
			if !ev.FocusGained {
				focusLost = true
				if beforeFocusReleases != 3 {
					t.Fatalf("releases before focus loss = %d, want W + held W/Shift", beforeFocusReleases)
				}
			}
		}
	}
	want := []struct {
		code    int32
		down    bool
		repeats int32
	}{
		{51, true, 0}, {51, true, 1}, {51, false, 0}, {51, true, 0}, {59, true, 0},
		{51, false, 0}, {59, false, 0}, {51, true, 0}, {51, false, 0},
	}
	if len(keys) != len(want) {
		t.Fatalf("key count=%d, want %d: %+v", len(keys), len(want), keys)
	}
	for i, expected := range want {
		got := keys[i]
		if got.KeyCode != expected.code || got.KeyPressed != expected.down || got.RepeatCount != expected.repeats {
			t.Fatalf("key %d=%+v want %+v", i, got, expected)
		}
	}
	if texts != 4 || !focusLost {
		t.Fatalf("text count=%d, focusLost=%t", texts, focusLost)
	}
}
