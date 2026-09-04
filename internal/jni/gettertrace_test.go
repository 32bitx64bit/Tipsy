// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

// TestGetterTracePerEventAttribution proves the observation-only getter
// trace: identities are attributed exactly to the event object native
// read them on, in first-call order, with counts; the drain releases the
// entry; and totals accumulate across events. Only known method
// identities and counts are recorded — no event payload.
func TestGetterTracePerEventAttribution(t *testing.T) {
	resetGetterTrace()
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newMotionEventLocked(motionActionDown, 3.5, 7.25, 100, 250)
	objID := o.id
	vm.mu.Unlock()

	// Native-driven getter consumption through the production dispatch.
	if _, handled := testEventGetter(vm, objID, motionEventClass, "getAction", "()I", -1); !handled {
		t.Fatal("getAction not handled")
	}
	if _, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", motionAxisX, 0); !handled {
		t.Fatal("getAxisValue not handled")
	}
	if _, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", motionAxisY, 0); !handled {
		t.Fatal("getAxisValue (second) not handled")
	}
	if _, handled := testEventGetter(vm, objID, motionEventClass, "getAction", "()I", -1); !handled {
		t.Fatal("getAction (second) not handled")
	}

	hits := drainEventGetterTrace(objID)
	want := []string{"getAction()I:2", "getAxisValue(II)F:2"}
	if len(hits) != len(want) {
		t.Fatalf("hits = %v, want %v", hits, want)
	}
	for i := range want {
		if hits[i] != want[i] {
			t.Fatalf("hits[%d] = %q, want %q (first-call order, counts merged)", i, hits[i], want[i])
		}
	}
	if again := drainEventGetterTrace(objID); again != nil {
		t.Fatalf("second drain = %v, want empty (entry released)", again)
	}

	totals := GetterConsumptionTotals()
	if totals["getAction()I"] != 2 || totals["getAxisValue(II)F"] != 2 {
		t.Fatalf("totals = %v, want getAction()I:2 getAxisValue(II)F:2", totals)
	}

	// An untouched object has no trace; a foreign identity never appears.
	if hits := drainEventGetterTrace(objID + 1); hits != nil {
		t.Fatalf("foreign object trace = %v, want none", hits)
	}
	if _, present := totals["getX()F"]; present {
		t.Fatal("getX recorded without a call")
	}
	resetGetterTrace()
}

// TestGetterTraceKeyDispatchLine proves the per-delivery observation line
// for a key event carries the consumed getter identities and never event
// data such as keycodes or coordinates.
func TestGetterTraceKeyDispatchLine(t *testing.T) {
	resetGetterTrace()
	vm := inputTestVM(t, map[string]uintptr{
		methodLogName(gameActivityClass, "onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"): testRecordKeyFn(),
	})
	SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	buf := captureLogs(t)

	if !DispatchGameActivityKey(111, 9, true) {
		t.Fatal("key dispatch failed")
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] input getters") || !strings.Contains(out, "kind=key") || !strings.Contains(out, "action=down") {
		t.Fatalf("missing input-getters observation line: %s", out)
	}
	// The recording fake native consumes no getters; the line must say so
	// honestly rather than inventing consumption (identity recording is
	// proven by TestGetterTracePerEventAttribution).
	if !strings.Contains(out, "getters=none") {
		t.Fatalf("fake native consumption invented: %s", out)
	}
	resetGetterTrace()
}

// TestGetterTraceRingBound proves the per-event trace ring stays bounded
// (never grows with event count).
func TestGetterTraceRingBound(t *testing.T) {
	resetGetterTrace()
	for i := int64(1); i <= getterTraceCap+10; i++ {
		noteGetterCall(i, "getAction", "()I")
	}
	if len(getterTraceState.ring) != getterTraceCap {
		t.Fatalf("ring size = %d, want %d", len(getterTraceState.ring), getterTraceCap)
	}
	// The oldest events were evicted; the newest are still attributable.
	if hits := drainEventGetterTrace(1); hits != nil {
		t.Fatalf("evicted event still traced: %v", hits)
	}
	if hits := drainEventGetterTrace(getterTraceCap + 10); len(hits) != 1 {
		t.Fatalf("newest event trace = %v, want one identity", hits)
	}
	resetGetterTrace()
}
