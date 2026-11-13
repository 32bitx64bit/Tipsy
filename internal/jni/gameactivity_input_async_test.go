// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

// restartInputBridgeWithDispatch stops any running bridge, installs dispatch as
// the worker's per-event seam and binds a fresh worker that captures it. The
// cleanup joins the worker before restoring the production seam, so the seam
// write is never concurrent with a worker read. Tests must release any blocking
// dispatch before the cleanup runs so the join is prompt.
func restartInputBridgeWithDispatch(t *testing.T, dispatch func(x11.InputEvent)) {
	t.Helper()
	stopX11InputBridge()
	old := inputBridgeDispatch
	inputBridgeDispatch = dispatch
	t.Cleanup(func() {
		stopX11InputBridge()
		inputBridgeDispatch = old
	})
	bindX11InputBridge()
}

func waitForInputBridge(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestInputBridgeOnInputReturnsWhileDispatchWedged is the core guarantee: the
// Window.OnInput callback (enqueueX11InputEvent, what win.Pump calls) returns
// promptly even while the delivery worker is blocked inside a GameActivity
// input callback. A synchronous bridge would wedge win.Pump here and never
// service WM_DELETE_WINDOW, focus-out unlock or ctx cancellation.
func TestInputBridgeOnInputReturnsWhileDispatchWedged(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	restartInputBridgeWithDispatch(t, func(ev x11.InputEvent) {
		once.Do(func() { close(started) })
		<-release
	})

	// The first event occupies the worker, which now blocks in the "engine".
	enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 29, ScanCode: 38})
	waitForInputBridge(t, "worker to reach the blocking dispatch", func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})

	// Every subsequent pump callback must return promptly despite the wedge.
	events := []x11.InputEvent{
		{Kind: x11.InputFocus, FocusGained: false},
		{Kind: x11.InputKey, KeyPressed: false, KeyCode: 29, ScanCode: 38},
		{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: 10, Y: 20},
		{Kind: x11.InputFocus, FocusGained: true},
	}
	for _, ev := range events {
		start := time.Now()
		enqueueX11InputEvent(ev)
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Fatalf("Window.OnInput callback took %v; it must not wait on a wedged engine dispatch", elapsed)
		}
	}

	close(release)
	stopX11InputBridge()
}

// TestInputBridgePreservesOrderAndCoalescesMotionWhenSaturated proves the
// bounded queue keeps key/button/focus ordering, never grows past its cap, and
// coalesces high-frequency motion only once saturated.
func TestInputBridgePreservesOrderAndCoalescesMotionWhenSaturated(t *testing.T) {
	var mu sync.Mutex
	var recorded []x11.InputEvent
	var first sync.Once
	gate := make(chan struct{})
	restartInputBridgeWithDispatch(t, func(ev x11.InputEvent) {
		mu.Lock()
		recorded = append(recorded, ev)
		mu.Unlock()
		// Block only the first delivery so the queue can fill behind it.
		first.Do(func() { <-gate })
	})

	// Occupy the worker with K0, then queue two keys while there is room.
	enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 100, ScanCode: 1})
	waitForInputBridge(t, "first event recorded", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(recorded) >= 1
	})
	enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 101, ScanCode: 2})
	enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 102, ScanCode: 3})

	// Flood motion far past the cap; trailing moves must coalesce, not grow.
	const moves = 400
	for i := 0; i < moves; i++ {
		enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputPointer, PointerAction: x11.PointerMove, X: float32(i), Y: float32(i)})
	}
	// A key arriving while the queue is saturated with motion is excess and may
	// be dropped; it must never reorder or displace the queued keys.
	enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 103, ScanCode: 4})

	inputBridge.mu.Lock()
	inst := inputBridge.inst
	inputBridge.mu.Unlock()
	if inst == nil {
		t.Fatal("bridge instance vanished")
	}
	inst.mu.Lock()
	qlen := len(inst.queue)
	inst.mu.Unlock()
	if qlen > inputBridgeQueueCap {
		t.Fatalf("queue length %d exceeds cap %d", qlen, inputBridgeQueueCap)
	}

	close(gate)
	// K0 + K1 + K2 + (cap-2) coalesced move slots.
	expected := inputBridgeQueueCap + 1
	waitForInputBridge(t, "queue to drain", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(recorded) >= expected
	})
	stopX11InputBridge()

	mu.Lock()
	defer mu.Unlock()
	if len(recorded) < 3 {
		t.Fatalf("recorded %d events, want at least the three keys", len(recorded))
	}
	for i, want := range []int32{100, 101, 102} {
		if recorded[i].Kind != x11.InputKey || recorded[i].KeyCode != want {
			t.Fatalf("recorded[%d] = %+v, want key %d in enqueue order", i, recorded[i], want)
		}
	}
	moveCount := 0
	for _, ev := range recorded {
		if ev.KeyCode == 103 {
			t.Fatal("saturated excess key was delivered; it must be dropped, not reorder the queue")
		}
		if ev.Kind == x11.InputPointer {
			moveCount++
		}
	}
	if moveCount == 0 || moveCount >= moves {
		t.Fatalf("recorded %d moves from %d enqueued; want coalescing (0 < n < %d)", moveCount, moves, moves)
	}
}

// TestInputBridgeStopBoundedWithWedgedDispatch proves teardown returns within a
// bounded deadline even when the single in-flight native call is wedged, rather
// than hanging shutdown forever.
func TestInputBridgeStopBoundedWithWedgedDispatch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	stopX11InputBridge()
	old := inputBridgeDispatch
	inputBridgeDispatch = func(ev x11.InputEvent) {
		once.Do(func() { close(started) })
		<-release
	}
	bindX11InputBridge()

	enqueueX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 29, ScanCode: 38})
	waitForInputBridge(t, "worker to wedge", func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})

	// Capture the worker's done channel so we can join the abandoned worker
	// after releasing it, keeping the seam restore race-free.
	inputBridge.mu.Lock()
	inst := inputBridge.inst
	inputBridge.mu.Unlock()
	if inst == nil {
		t.Fatal("bridge instance vanished")
	}
	done := inst.done

	start := time.Now()
	stopX11InputBridge()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("stop took %v; a wedged worker must be abandoned within a bounded deadline", elapsed)
	}
	if elapsed < inputBridgeStopTimeout/2 {
		t.Fatalf("stop returned in %v; expected to wait out the %v deadline for the wedged worker", elapsed, inputBridgeStopTimeout)
	}

	// Let the abandoned worker finish so it exits and the seam restore is safe.
	close(release)
	<-done
	inputBridgeDispatch = old
}