// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import "testing"

func resetStartupMeasurementForTest(t *testing.T) {
	t.Helper()
	testStartupMeasurementClear()
	t.Cleanup(testStartupMeasurementClear)
}

func deliverStartupMeasurementEdges(w *Window) {
	w.mu.Lock()
	callbacks := w.collectStartupMeasurementEdgesLocked()
	w.mu.Unlock()
	notifyStartupMeasurementEdges(callbacks)
}

func TestStartupMeasurementMapSubscriptionIsOwnedComposableAndTornDown(t *testing.T) {
	resetStartupMeasurementForTest(t)
	w := &Window{display: 41, xid: 73}
	var got []string
	stopFirst, availability := w.OnStartupMeasurementEdges(StartupMeasurementEdges{
		MapNotify: func() { got = append(got, "first") },
	})
	if availability.PostScrollDrawableUpdate {
		t.Fatal("map-only subscription unexpectedly enabled drawable observation")
	}
	stopSecond, _ := w.OnStartupMeasurementEdges(StartupMeasurementEdges{
		MapNotify: func() { got = append(got, "second") },
	})

	// A real-looking MapNotify for another XID must not satisfy this Window.
	testStartupMeasurementMap(41, 74)
	deliverStartupMeasurementEdges(w)
	if len(got) != 0 {
		t.Fatalf("foreign MapNotify delivered %v", got)
	}

	testStartupMeasurementMap(41, 73)
	deliverStartupMeasurementEdges(w)
	if want := []string{"first", "second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("owned MapNotify order = %v, want %v", got, want)
	}

	stopFirst()
	testStartupMeasurementClear()
	testStartupMeasurementMap(41, 73)
	// The surviving subscription has already received the only MapNotify, so
	// a second real map cannot duplicate it; cancelled first never returns.
	deliverStartupMeasurementEdges(w)
	if want := []string{"first", "second"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("teardown or once-only delivery changed callbacks: %v", got)
	}
	stopSecond()
	if w.startupMeasurementMapEnabled {
		t.Fatal("last cancellation left the native MapNotify selector enabled")
	}
}

func TestStartupMeasurementEdgesOrderDrawableOnlyAfterDeclaredArm(t *testing.T) {
	resetStartupMeasurementForTest(t)
	w := &Window{display: 91, xid: 92}
	var got []string
	w.mu.Lock()
	w.startupMeasurementMapEnabled = true
	w.startupMeasurementDrawableEnabled = true
	w.addStartupMeasurementObserverLocked(StartupMeasurementEdges{
		MapNotify:                func() { got = append(got, "map") },
		PostScrollDrawableUpdate: func() { got = append(got, "drawable") },
	})
	w.mu.Unlock()

	// A drawable update before a declared scroll arm is ignored rather than
	// being reclassified as an input response.
	testStartupMeasurementDrawable(91, 92)
	deliverStartupMeasurementEdges(w)
	if len(got) != 0 {
		t.Fatalf("pre-arm drawable delivered %v", got)
	}

	// The native arm clears stale damage before setting its one-shot bit. Model
	// that boundary here, then provide the first later owned XDamage fact.
	resetStartupMeasurementForTest(t)
	w.mu.Lock()
	w.startupMeasurementMapEnabled = true
	w.startupMeasurementDrawableArmed = true
	w.mu.Unlock()
	testStartupMeasurementMap(91, 92)
	testStartupMeasurementDrawable(91, 92)
	deliverStartupMeasurementEdges(w)
	if want := []string{"map", "drawable"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("edge order = %v, want %v", got, want)
	}
	w.mu.Lock()
	if w.startupMeasurementDrawableArmed {
		w.mu.Unlock()
		t.Fatal("one-shot drawable arm remained set after delivery")
	}
	w.mu.Unlock()
}

func TestStartupMeasurementMapObservedBeforeSubscriptionReplaysExactFact(t *testing.T) {
	resetStartupMeasurementForTest(t)
	testStartupMeasurementMap(17, 18)
	w := &Window{display: 17, xid: 18}
	called := 0
	stop, _ := w.OnStartupMeasurementEdges(StartupMeasurementEdges{
		MapNotify: func() { called++ },
	})
	defer stop()
	if called != 1 {
		t.Fatalf("latched owned MapNotify callbacks = %d, want 1", called)
	}
}
