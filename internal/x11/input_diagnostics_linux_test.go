// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"fmt"
	"strings"
	"testing"
)

func resetInputDrainDiagnostics(t *testing.T, enabled bool) {
	t.Helper()
	SetInputDrainDiagnostics(false)
	_ = InputDrainSnapshot(true)
	testClearInputRing()
	SetInputDrainDiagnostics(enabled)
	t.Cleanup(func() {
		SetInputDrainDiagnostics(false)
		_ = InputDrainSnapshot(true)
		testClearInputRing()
	})
}

func drainForInputDiagnostics(t *testing.T, w *Window) {
	t.Helper()
	w.mu.Lock()
	evs, _ := w.drainInputLocked()
	w.mu.Unlock()
	// Match Pump's post-subscriber wipe so the diagnostic test never retains
	// its synthetic text sentinel through the reusable event backing slice.
	clear(evs)
}

func TestInputDrainDiagnosticsDefaultOffSkipsCounters(t *testing.T) {
	resetInputDrainDiagnostics(t, false)
	w := &Window{}
	drainForInputDiagnostics(t, w)
	testPushPointer(PointerDown, 1, 4, 5)
	drainForInputDiagnostics(t, w)
	if got := InputDrainSnapshot(false); got != (InputDrainStats{}) {
		t.Fatalf("disabled diagnostics recorded %+v", got)
	}
}

func TestInputDrainDiagnosticsCountsBatchesWithoutContent(t *testing.T) {
	resetInputDrainDiagnostics(t, true)
	if !InputDrainDiagnosticsEnabled() {
		t.Fatal("diagnostics did not enable")
	}
	w := &Window{}
	drainForInputDiagnostics(t, w) // 0
	testPushPointer(PointerDown, 1, 4, 5)
	drainForInputDiagnostics(t, w) // 1
	for i := 0; i < 3; i++ {
		testPushPointer(PointerDown, 1, float32(i), 0)
	}
	drainForInputDiagnostics(t, w) // 3
	for i := 0; i < 5; i++ {
		testPushPointer(PointerDown, 1, float32(i), 0)
	}
	drainForInputDiagnostics(t, w) // 5
	const sentinel = "input-content-must-not-escape"
	testPushText(sentinel)
	drainForInputDiagnostics(t, w) // 1 text, still only one aggregate event

	got := InputDrainSnapshot(true)
	if got.DrainCalls != 5 || got.EmptyDrains != 1 || got.NonEmptyDrains != 4 || got.Events != 10 {
		t.Fatalf("drain aggregates = %+v", got)
	}
	wantBatches := [InputDrainBatchBuckets]uint64{1, 2, 1, 1}
	if got.BatchBuckets != wantBatches {
		t.Fatalf("batch buckets = %v, want %v", got.BatchBuckets, wantBatches)
	}
	if got.CDrain.Samples == 0 || got.InputRingLockWait.Samples == 0 || got.GoDrain.Samples != 5 {
		t.Fatalf("missing enabled timing aggregates: c=%+v lock=%+v go=%+v", got.CDrain, got.InputRingLockWait, got.GoDrain)
	}
	if rendered := fmt.Sprintf("%+v", got); strings.Contains(rendered, sentinel) {
		t.Fatal("aggregate diagnostics retained input content")
	}
	if next := InputDrainSnapshot(false); next != (InputDrainStats{}) {
		t.Fatalf("reset snapshot retained diagnostics %+v", next)
	}
}

func TestInputDrainDiagnosticsCountsRingDrops(t *testing.T) {
	resetInputDrainDiagnostics(t, true)
	// The 256-slot ring holds 255 queued events; every push beyond that
	// discards the oldest queued event. Button edges never coalesce, so the
	// drop count is exact: pushes - 255.
	const extra = 9
	for i := 0; i < TIPSYInputRingLen+extra; i++ {
		testPushPointer(PointerDown, 1, float32(i), 0)
	}
	got := InputDrainSnapshot(true)
	if got.RingDrops != extra+1 {
		t.Fatalf("ring drops = %d, want %d", got.RingDrops, extra+1)
	}
	if next := InputDrainSnapshot(false); next != (InputDrainStats{}) {
		t.Fatalf("reset snapshot retained diagnostics %+v", next)
	}
}

func TestInputDrainDiagnosticsResetRaceDoesNotLoseDrainCalls(t *testing.T) {
	resetInputDrainDiagnostics(t, true)
	const drains = 2000
	w := &Window{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < drains; i++ {
			drainForInputDiagnostics(t, w)
		}
	}()

	var observed uint64
	for {
		select {
		case <-done:
			observed += InputDrainSnapshot(true).DrainCalls
			if observed != drains {
				t.Fatalf("observed drain calls = %d, want %d", observed, drains)
			}
			return
		default:
			observed += InputDrainSnapshot(true).DrainCalls
		}
	}
}

func TestInputDrainDiagnosticsRequiresExactStutterOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", "2"} {
		value := value
		if inputDrainDiagnosticsRequested(func(string) string { return value }) {
			t.Errorf("value %q enabled diagnostics", value)
		}
	}
	if !inputDrainDiagnosticsRequested(func(name string) string {
		if name != "TIPSY_STUTTER_DIAG" {
			t.Fatalf("queried unexpected environment key %q", name)
		}
		return "1"
	}) {
		t.Fatal("exact opt-in did not enable diagnostics")
	}
	if inputDrainDiagnosticsRequested(nil) {
		t.Fatal("nil environment reader enabled diagnostics")
	}
}

func TestInputDrainLatencyBucketsAreBounded(t *testing.T) {
	if got := inputDrainLatencyBucket(0); got != 0 {
		t.Fatalf("zero bucket = %d, want 0", got)
	}
	if got := inputDrainLatencyBucket(2_000); got != 1 {
		t.Fatalf("2us bucket = %d, want 1", got)
	}
	if got := inputDrainLatencyBucket(1 << 62); got != InputDrainLatencyBuckets-1 {
		t.Fatalf("large duration bucket = %d, want saturated %d", got, InputDrainLatencyBuckets-1)
	}
}

func BenchmarkInputDrainDiagnosticsDisabled(b *testing.B) {
	SetInputDrainDiagnostics(false)
	defer SetInputDrainDiagnostics(false)
	_ = InputDrainSnapshot(true)
	w := &Window{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.mu.Lock()
		_, _ = w.drainInputLocked()
		w.mu.Unlock()
	}
}

func BenchmarkInputDrainDiagnosticsEnabled(b *testing.B) {
	SetInputDrainDiagnostics(true)
	defer SetInputDrainDiagnostics(false)
	_ = InputDrainSnapshot(true)
	w := &Window{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.mu.Lock()
		_, _ = w.drainInputLocked()
		w.mu.Unlock()
	}
}
