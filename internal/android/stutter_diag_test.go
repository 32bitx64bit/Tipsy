// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func resetStutterWaitTest(t *testing.T, enabled bool) {
	t.Helper()
	SetStutterWaitDiagnostics(false)
	_ = StutterWaitSnapshot(true)
	testStutterWaitResetTLS()
	SetStutterWaitDiagnostics(enabled)
	t.Cleanup(func() {
		SetStutterWaitDiagnostics(false)
		_ = StutterWaitSnapshot(true)
	})
}

func TestStutterWaitDiagnosticsDefaultOffSkipsClockAndCounters(t *testing.T) {
	resetStutterWaitTest(t, false)
	before := testStutterWaitClockCalls()
	testStutterWaitRecord(StutterWaitCond, 1234, 3)
	after := testStutterWaitClockCalls()
	if after != before {
		t.Fatalf("disabled clock calls changed: before=%d after=%d", before, after)
	}
	if got := StutterWaitSnapshot(false).Cond; got != (StutterWaitPathStats{}) {
		t.Fatalf("disabled counters changed: %+v", got)
	}
}

func TestStutterWaitDiagnosticsCountsAndSample(t *testing.T) {
	resetStutterWaitTest(t, true)
	testStutterWaitRecord(StutterWaitCond, 1234, 3)
	got := StutterWaitSnapshot(true).Cond
	if got.Calls != 1 || got.Slices != 3 || got.Samples != 1 ||
		got.SampledDuration != 1234 || got.MaxDuration != 1234 {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
	if got := StutterWaitSnapshot(false).Cond; got != (StutterWaitPathStats{}) {
		t.Fatalf("reset snapshot retained values: %+v", got)
	}
}

func TestStutterWaitSnapshotRaceDoesNotLoseCalls(t *testing.T) {
	resetStutterWaitTest(t, true)
	const workers = 6
	const perWorker = 1000
	var done atomic.Bool
	var observed uint64
	var snapWG sync.WaitGroup
	snapWG.Add(1)
	go func() {
		defer snapWG.Done()
		for !done.Load() {
			observed += StutterWaitSnapshot(true).TimedCond.Calls
			runtime.Gosched()
		}
	}()
	var workersWG sync.WaitGroup
	workersWG.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer workersWG.Done()
			for n := 0; n < perWorker; n++ {
				testStutterWaitRecord(StutterWaitTimedCond, 1, 0)
			}
		}()
	}
	workersWG.Wait()
	done.Store(true)
	snapWG.Wait()
	observed += StutterWaitSnapshot(true).TimedCond.Calls
	if observed != workers*perWorker {
		t.Fatalf("observed calls=%d, want %d", observed, workers*perWorker)
	}
}
