// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func resetJNIStutterTest(t *testing.T, enabled bool) {
	t.Helper()
	SetStutterDiagnostics(false)
	_ = StutterSnapshot(true)
	testJNIResetTLS()
	SetStutterDiagnostics(enabled)
	t.Cleanup(func() {
		SetStutterDiagnostics(false)
		_ = StutterSnapshot(true)
	})
}

func TestJNIStutterDiagnosticsDefaultOffSkipsClassificationAndCounters(t *testing.T) {
	resetJNIStutterTest(t, false)
	before := testJNIThreadNameLookups()
	testJNIRecord(JNICALLInstance)
	after := testJNIThreadNameLookups()
	if after != before {
		t.Fatalf("disabled thread-name lookups changed: before=%d after=%d", before, after)
	}
	if got := StutterSnapshot(false); got != (JNIStutterStats{}) {
		t.Fatalf("disabled counters changed: %+v", got)
	}
}

func TestJNIStutterThreadNameClassification(t *testing.T) {
	cases := map[string]int{
		"RBX Worker":   JNIThreadRBXWorker,
		"RBX Worker 7": JNIThreadRBXWorker,
		"Main":         JNIThreadMain,
		"RenderJob":    JNIThreadOther,
		"":             JNIThreadOther,
	}
	for name, want := range cases {
		if got := testJNIThreadClass(name); got != want {
			t.Errorf("classify %q=%d, want %d", name, got, want)
		}
	}
}

func TestJNIStutterCountsByNativeThreadAndCallFamily(t *testing.T) {
	resetJNIStutterTest(t, true)
	if rc := testJNIRecordOnNamedThread("RBX Worker", JNICALLInstance, 7); rc != 0 {
		t.Fatalf("RBX Worker recorder: %d", rc)
	}
	if rc := testJNIRecordOnNamedThread("Main", JNICALLStatic, 5); rc != 0 {
		t.Fatalf("Main recorder: %d", rc)
	}
	if rc := testJNIRecordOnNamedThread("RenderJob", JNICALLNonvirtual, 3); rc != 0 {
		t.Fatalf("other recorder: %d", rc)
	}
	got := StutterSnapshot(true)
	if got.Calls[JNIThreadRBXWorker][JNICALLInstance] != 7 ||
		got.Calls[JNIThreadMain][JNICALLStatic] != 5 ||
		got.Calls[JNIThreadOther][JNICALLNonvirtual] != 3 {
		t.Fatalf("unexpected thread/family matrix: %+v", got.Calls)
	}
	if got := StutterSnapshot(false); got != (JNIStutterStats{}) {
		t.Fatalf("reset snapshot retained values: %+v", got)
	}
}

func TestJNIStutterSnapshotRaceDoesNotLoseCalls(t *testing.T) {
	resetJNIStutterTest(t, true)
	const workers = 6
	const perWorker = 1000
	var done atomic.Bool
	var observed uint64
	var snapWG sync.WaitGroup
	snapWG.Add(1)
	go func() {
		defer snapWG.Done()
		for !done.Load() {
			got := StutterSnapshot(true)
			for threadClass := 0; threadClass < jniThreadClasses; threadClass++ {
				observed += got.Calls[threadClass][JNICALLInstance]
			}
			runtime.Gosched()
		}
	}()
	var workersWG sync.WaitGroup
	workersWG.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer workersWG.Done()
			for n := 0; n < perWorker; n++ {
				testJNIRecord(JNICALLInstance)
			}
		}()
	}
	workersWG.Wait()
	done.Store(true)
	snapWG.Wait()
	got := StutterSnapshot(true)
	for threadClass := 0; threadClass < jniThreadClasses; threadClass++ {
		observed += got.Calls[threadClass][JNICALLInstance]
	}
	if observed != workers*perWorker {
		t.Fatalf("observed calls=%d, want %d", observed, workers*perWorker)
	}
}
