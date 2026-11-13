// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"testing"
	"time"
)

// TestShutdownWatchdogCoversEveryTeardownStep proves the watchdog is armed
// before the first step and stays armed while Dismiss, StopBackgroundPump,
// presenter.stop, and closeLifecycle each run. A wedge at any of those positions
// must still trigger the force-exit hook instead of leaving the process alive.
func TestShutdownWatchdogCoversEveryTeardownStep(t *testing.T) {
	names := []string{"dismiss", "stop-background-pump", "presenter-stop", "close-lifecycle"}
	for blockAt := range names {
		blockAt := blockAt
		t.Run(names[blockAt], func(t *testing.T) {
			entered := make(chan string, 1)
			release := make(chan struct{})
			forced := make(chan int, 1)
			ran := make([]string, 0, len(names))
			steps := make([]func(), 0, len(names))
			for i, name := range names {
				name := name
				if i != blockAt {
					steps = append(steps, func() { ran = append(ran, name) })
					continue
				}
				steps = append(steps, func() {
					ran = append(ran, name)
					entered <- name
					<-release
				})
			}

			done := make(chan struct{})
			go func() {
				runBoundedShutdown(20*time.Millisecond, func(code int) { forced <- code }, "test-wedge", steps...)
				close(done)
			}()

			select {
			case got := <-entered:
				if got != names[blockAt] {
					t.Fatalf("blocking step = %q, want %q", got, names[blockAt])
				}
			case <-time.After(time.Second):
				t.Fatalf("teardown never reached %q", names[blockAt])
			}
			select {
			case code := <-forced:
				if code != 0 {
					t.Fatalf("force-exit code = %d, want 0", code)
				}
			case <-time.After(time.Second):
				t.Fatalf("watchdog did not fire while %q blocked", names[blockAt])
			}
			for i := 0; i <= blockAt; i++ {
				if ran[i] != names[i] {
					t.Fatalf("steps before wedge = %v, want prefix %v", ran, names[:blockAt+1])
				}
			}

			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("shutdown did not return after the blocked step released")
			}
		})
	}
}

func TestShutdownWatchdogStoppedAfterCleanTeardown(t *testing.T) {
	forced := make(chan int, 1)
	runBoundedShutdown(20*time.Millisecond, func(code int) { forced <- code }, "test-clean",
		func() {}, func() {}, func() {}, func() {})
	select {
	case code := <-forced:
		t.Fatalf("clean shutdown fired force-exit with code %d", code)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestGameActivityShutdownStopsX11InputBridgeBeforeTerminate pins that the
// async X11->GameActivity input worker is stopped with the rest of the session
// teardown, before terminateNativeCode joins and destroys the native app. The
// deferred ClearGameActivityInputTarget runs only after Launch returns, which
// is too late for queued input to be safe.
func TestGameActivityShutdownStopsX11InputBridgeBeforeTerminate(t *testing.T) {
	prev := gameActivityInputBridgeStop
	t.Cleanup(func() { gameActivityInputBridgeStop = prev })

	var order []string
	gameActivityInputBridgeStop = func() {
		order = append(order, "stop-x11-input-bridge")
	}
	s := &gameActivitySession{call: func(name, sig string, extra ...uintptr) {
		order = append(order, name)
	}}
	s.shutdown("test-input-bridge")

	stopAt, terminateAt := -1, -1
	for i, name := range order {
		switch name {
		case "stop-x11-input-bridge":
			stopAt = i
		case "terminateNativeCode":
			terminateAt = i
		}
	}
	if stopAt < 0 {
		t.Fatalf("shutdown did not stop the X11 input bridge: %v", order)
	}
	if terminateAt < 0 {
		t.Fatalf("shutdown did not call terminateNativeCode: %v", order)
	}
	if stopAt > terminateAt {
		t.Fatalf("X11 input bridge stopped at %d, after terminateNativeCode at %d: %v",
			stopAt, terminateAt, order)
	}
}
