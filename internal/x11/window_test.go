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
	if err := w.StartBackgroundPump(); err != nil {
		t.Fatalf("StartBackgroundPump: %v", err)
	}
	if err := x11probe.WMDelete(w.XID()); err != nil {
		t.Fatalf("WM_DELETE_WINDOW: %v", err)
	}
	deadline := time.Now().Add(750 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := w.Pump(); errors.Is(err, ErrClosed) {
			if x11probe.Viewable(w.XID()) {
				t.Fatal("WM_DELETE acknowledged but the client window stayed viewable")
			}
			return
		} else if err != nil {
			t.Fatalf("Pump: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background pump consumed WM_DELETE_WINDOW without notifying Go")
}

func ensureDisplay(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") != "" {
		return
	}
	startXvfb(t)
}

func startXvfb(t *testing.T) {
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
	cmd := exec.Command(xvfb, display, "-screen", "0", "128x128x24", "-nolisten", "tcp")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Skipf("Xvfb start failed: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
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
	for i := 0; i < 40; i++ {
		n := 80 + (pid+i)%40
		lock := fmt.Sprintf("/tmp/.X%d-lock", n)
		if _, err := os.Stat(lock); err == nil {
			continue
		}
		return n, nil
	}
	return 0, errors.New("no free display in :80-:119")
}
