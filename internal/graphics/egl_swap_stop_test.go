// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package graphics

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/cgo"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

// swapThreadCount counts the live C sentinel pthreads by their kernel thread
// name, so a stop that silently times out instead of joining is observable.
func swapThreadCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Skipf("cannot read /proc/self/task: %v", err)
	}
	n := 0
	for _, entry := range entries {
		comm, err := os.ReadFile(filepath.Join("/proc/self/task", entry.Name(), "comm"))
		if err != nil {
			continue // thread exited while listing
		}
		if strings.TrimSpace(string(comm)) == "tip.eglswap" {
			n++
		}
	}
	return n
}

func waitSwapThreadCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n := swapThreadCount(t); n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("swap thread count never reached %d (now %d)", want, swapThreadCount(t))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func openSwapTestEGL(t *testing.T) *EGL {
	t.Helper()
	ensureDisplay(t)
	w, err := x11.Open("Tipsy swap stop", 64, 64)
	if err != nil {
		if errors.Is(err, x11.ErrUnavailable) || errors.Is(err, x11.ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("x11.Open: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	e, err := BindEGL(w)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skip(err)
		}
		t.Skipf("EGL not usable on this display: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.ReleaseCurrent(); err != nil {
		t.Fatalf("ReleaseCurrent: %v", err)
	}
	return e
}

// TestSwapThreadStopJoinsHealthyThread pins that a healthy StopSwapThread
// actually joins the sentinel pthread instead of silently taking the bounded
// timeout branch. A 300ms test stall (shorter than the 500ms join deadline)
// wedges the thread just before stop: a real join waits out the stall and
// returns with the thread gone, while a stop that instantly "times out"
// returns with the thread deterministically still stalled and live.
func TestSwapThreadStopJoinsHealthyThread(t *testing.T) {
	e := openSwapTestEGL(t)
	baseline := swapThreadCount(t)
	if err := e.StartSwapThread(); err != nil {
		t.Fatalf("StartSwapThread: %v", err)
	}
	waitSwapThreadCount(t, baseline+1)
	armSwapStallForTest(300 * time.Millisecond)
	// The fallback loop cycles every 125ms and self-retires only after 8
	// probes (~1s), so the stall is consumed long before exhaustion.
	deadline := time.Now().Add(3 * time.Second)
	for !swapStallEnteredForTest() {
		if time.Now().After(deadline) {
			t.Fatal("sentinel never entered the test stall")
		}
		time.Sleep(time.Millisecond)
	}
	if err := e.StopSwapThread(); err != nil {
		t.Fatalf("StopSwapThread: %v", err)
	}
	if n := swapThreadCount(t); n != baseline {
		t.Fatalf("StopSwapThread returned with %d swap thread(s) still live (baseline %d); stop timed out instead of joining", n, baseline)
	}
}

// TestSwapThreadStopTimeoutConsumesHandoffOnce wedges the sentinel past the
// 500ms join deadline and pins the timed-out stop contract: stop returns
// bounded, consumes the handoff exactly once before Go deletes the cgo.Handle,
// and the late-finishing thread neither re-hands-off nor touches freed state
// (the stop deliberately leaks the swap struct while the thread is live).
func TestSwapThreadStopTimeoutConsumesHandoffOnce(t *testing.T) {
	e := openSwapTestEGL(t)
	baseline := swapThreadCount(t)
	armSwapStallForTest(900 * time.Millisecond)
	wake := make(chan struct{}, 1)
	handle := cgo.NewHandle(wake)
	p := startSwapThreadForTest(e, uintptr(handle))
	if p == 0 {
		handle.Delete()
		t.Fatal("swap thread start failed")
	}
	// Wait until the thread is wedged in the stall (it holds no mutex there).
	deadline := time.Now().Add(3 * time.Second)
	for !swapStallEnteredForTest() {
		if time.Now().After(deadline) {
			t.Fatal("sentinel never entered the test stall")
		}
		time.Sleep(5 * time.Millisecond)
	}
	start := time.Now()
	fail := stopSwapThreadForTest(p)
	elapsed := time.Since(start)
	if fail != 0 {
		t.Fatalf("wedged stop fail = %d; want success", fail)
	}
	if elapsed < 450*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("bounded stop took %v; want the ~500ms join timeout", elapsed)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("timed-out stop did not consume the handoff token")
	}
	// Mirror stopSwapThreadLocked: Go deletes the handle right after stop.
	handle.Delete()
	// The stall outlasts the join deadline, so the thread finishes late with
	// go_handle already consumed. It must exit cleanly (the leaked struct and
	// its sync objects stay valid) and never hand off a second time.
	waitSwapThreadCount(t, baseline)
	select {
	case <-wake:
		t.Fatal("late finish called GoEGLHandoff after the handle was consumed and deleted")
	case <-time.After(200 * time.Millisecond):
	}
	if !swapThreadFinishedForTest(p) {
		t.Fatal("wedged thread never finished after the stall")
	}
}
