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
)

func TestSetFullscreenUnimplemented(t *testing.T) {
	t.Parallel()
	w := &Window{}
	if err := w.SetFullscreen(true); !errors.Is(err, ErrFullscreen) {
		t.Fatalf("SetFullscreen = %v, want ErrFullscreen", err)
	}
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
