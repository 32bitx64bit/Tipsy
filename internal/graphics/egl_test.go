// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/graphics/flickercap"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

func TestBindEGLNilWindow(t *testing.T) {
	t.Parallel()
	_, err := BindEGL(nil)
	if err == nil {
		t.Fatal("BindEGL(nil) succeeded")
	}
}

func TestFirstFrame(t *testing.T) {
	ensureDisplay(t)

	w, err := x11.Open("Tipsy EGL test", 64, 64)
	if err != nil {
		if errors.Is(err, x11.ErrUnavailable) || errors.Is(err, x11.ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("x11.Open: %v", err)
	}
	defer w.Close()
	if err := w.Pump(); err != nil && !errors.Is(err, x11.ErrClosed) {
		t.Fatalf("Pump: %v", err)
	}

	e, err := BindEGL(w)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skip(err)
		}
		t.Skipf("EGL not usable on this display: %v", err)
	}
	defer e.Close()

	if err := e.clearRGBA(0.05, 0.05, 0.08, 1); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := e.Swap(); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSwapThread(t *testing.T) {
	ensureDisplay(t)
	w, err := x11.Open("Tipsy swap thread", 64, 64)
	if err != nil {
		if errors.Is(err, x11.ErrUnavailable) || errors.Is(err, x11.ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("x11.Open: %v", err)
	}
	defer w.Close()
	e, err := BindEGL(w)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skip(err)
		}
		t.Skipf("EGL not usable on this display: %v", err)
	}
	defer e.Close()
	if err := e.ReleaseCurrent(); err != nil {
		t.Fatalf("ReleaseCurrent: %v", err)
	}
	if err := e.StartSwapThread(); err != nil {
		t.Fatalf("StartSwapThread: %v", err)
	}
	// A lone sentinel presenter must never claim a handoff: Tipsy's own
	// black sentinel is the only frame ever on this window.
	time.Sleep(400 * time.Millisecond)
	if e.SwapHandedOff() {
		t.Fatal("SwapHandedOff = true without a second presenter")
	}
	if err := e.StopSwapThread(); err != nil {
		t.Fatalf("StopSwapThread: %v", err)
	}
}

// TestSwapThreadRetiresOnSecondPresenter verifies the single-presenter
// handoff: when another EGL client presents non-black frames on the same
// window, the Tipsy swap thread must detect it and never present again.
func TestSwapThreadRetiresOnSecondPresenter(t *testing.T) {
	ensureDisplay(t)
	w, err := x11.Open("Tipsy handoff test", 64, 64)
	if err != nil {
		if errors.Is(err, x11.ErrUnavailable) || errors.Is(err, x11.ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("x11.Open: %v", err)
	}
	defer w.Close()
	victim, err := BindEGL(w)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skip(err)
		}
		t.Skipf("EGL not usable on this display: %v", err)
	}
	defer victim.Close()
	if err := victim.ReleaseCurrent(); err != nil {
		t.Fatalf("victim ReleaseCurrent: %v", err)
	}
	if err := victim.StartSwapThread(); err != nil {
		t.Fatalf("victim StartSwapThread: %v", err)
	}

	// Second presenter through its own X connection: painting white content
	// on the same window is exactly what the probe must observe, the way the
	// Roblox RenderJob's frames (via its own EGLDisplay/connection) appear.
	client, err := flickercap.Attach(w.XID())
	if err != nil {
		t.Skipf("second connection unusable: %v", err)
	}
	defer client.Close()
	// A real presenter (the Roblox RenderJob) presents continuously; paint
	// repeatedly so a present that races the sentinel cannot erase the
	// foreign content before a probe observes it.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err := client.PaintWhite(); err != nil {
			t.Fatalf("client paint: %v", err)
		}
		if victim.SwapHandedOff() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("swap thread did not retire within 8s of a second presenter")
}

func ensureDisplay(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") != "" {
		return
	}
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
		w, err := x11.Open("tipsy-egl-xvfb-probe", 64, 64)
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
