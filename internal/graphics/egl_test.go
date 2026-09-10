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

func TestWindowRefreshRatesNoWindow(t *testing.T) {
	current, supported := WindowRefreshRates(0, 0)
	if current != 0 || len(supported) != 0 {
		t.Fatalf("WindowRefreshRates(0,0) = %v, %v; want zero rates", current, supported)
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

	// Exercise the EGL-owning-thread Swap primitive. Production's sentinel
	// path is StartSwapThread, which clears and presents its black frame
	// inside C instead of exposing a glClear wrapper.
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

func ensureDisplay(t *testing.T, extraArgs ...string) {
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
	args := append([]string{display, "-screen", "0", "128x128x24", "-nolisten", "tcp"}, extraArgs...)
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

func TestWindowRefreshRatesWithoutRandR(t *testing.T) {
	t.Setenv("DISPLAY", "")
	ensureDisplay(t, "-extension", "RANDR")
	w, err := x11.Open("refresh without RandR", 32, 32)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 10; i++ {
		current, supported := WindowRefreshRates(w.Display(), w.XID())
		if current != 0 || len(supported) != 0 {
			t.Fatalf("missing RandR returned invented rates: %v, %v", current, supported)
		}
	}
}
