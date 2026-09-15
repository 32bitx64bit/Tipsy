// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"runtime"
	"sync/atomic"
	"testing"

	qt "github.com/mappu/miqt/qt6"
	"golang.org/x/sys/unix"
)

func TestOwnerThreadNotifierEOFClosesWithoutDispatch(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()
	parent := qt.NewQWidget2()
	defer parent.Delete()
	metrics := &guiRuntimeMetrics{}
	var dispatched atomic.Int32
	notifier, err := newOwnerThreadNotifier(parent.QObject, func() { dispatched.Add(1) }, metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer notifier.close()

	// EOF is not a state edge. It must retire the pipe rather than spin or
	// present a callback that is no longer backed by a writer.
	notifier.mu.Lock()
	writer := notifier.writeFD
	notifier.writeFD = -1
	notifier.mu.Unlock()
	if err := unix.Close(writer); err != nil {
		t.Fatal(err)
	}
	notifier.drain()
	if dispatched.Load() != 0 || metrics.snapshot().OwnerDispatched != 0 {
		t.Fatalf("EOF dispatched a stale owner callback: dispatched=%d metrics=%+v", dispatched.Load(), metrics.snapshot())
	}
	notifier.mu.Lock()
	closed := notifier.closed
	notifier.mu.Unlock()
	if !closed {
		t.Fatal("EOF left the owner notification transport usable")
	}
}

func TestOwnerThreadNotifierConcurrentCloseDropsLateWakeups(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()
	parent := qt.NewQWidget2()
	defer parent.Delete()
	metrics := &guiRuntimeMetrics{}
	var dispatched atomic.Int32
	notifier, err := newOwnerThreadNotifier(parent.QObject, func() { dispatched.Add(1) }, metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer notifier.close()

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		for i := 0; i < 1024; i++ {
			notifier.notify()
			runtime.Gosched()
		}
		close(done)
	}()
	<-started
	notifier.close()
	<-done

	before := metrics.snapshot()
	notifier.notify()
	qt.QCoreApplication_ProcessEvents()
	if got := metrics.snapshot(); got != before || dispatched.Load() != 0 {
		t.Fatalf("late wakeup escaped closed notifier: before=%+v after=%+v dispatched=%d", before, got, dispatched.Load())
	}
}
