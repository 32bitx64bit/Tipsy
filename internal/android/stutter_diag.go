// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

import (
	"time"
	"unsafe"
)

const (
	StutterWaitCond = iota
	StutterWaitTimedCond
	StutterWaitFutexPump
	stutterWaitPaths
)

// StutterWaitPathStats is an interval aggregate for one Tipsy-owned wait path.
// Duration is sampled once per 64 entries; Calls and Slices are exact.
type StutterWaitPathStats struct {
	Calls           uint64
	Slices          uint64
	Samples         uint64
	SampledDuration time.Duration
	MaxDuration     time.Duration
}

// StutterWaitStats contains renderer-independent wait telemetry.
type StutterWaitStats struct {
	Cond      StutterWaitPathStats
	TimedCond StutterWaitPathStats
	FutexPump StutterWaitPathStats
}

// SetStutterWaitDiagnostics enables the opt-in C-side counters. The disabled
// path takes no clock timestamps and does not mutate a counter.
func SetStutterWaitDiagnostics(enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_stutter_wait_set_enabled(v)
}

func stutterWaitPath(raw C.TipsyStutterWaitPathStats) StutterWaitPathStats {
	return StutterWaitPathStats{
		Calls:           uint64(raw.calls),
		Slices:          uint64(raw.slices),
		Samples:         uint64(raw.samples),
		SampledDuration: time.Duration(raw.sampled_ns),
		MaxDuration:     time.Duration(raw.max_ns),
	}
}

// StutterWaitSnapshot returns current aggregates. When reset is true each
// atomic field is exchanged with zero, allowing interval snapshots while
// worker threads continue recording without losing count increments.
func StutterWaitSnapshot(reset bool) StutterWaitStats {
	var raw C.TipsyStutterWaitStats
	r := C.int(0)
	if reset {
		r = 1
	}
	C.tipsy_stutter_wait_snapshot(&raw, r)
	paths := (*[stutterWaitPaths]C.TipsyStutterWaitPathStats)(unsafe.Pointer(&raw.path[0]))
	return StutterWaitStats{
		Cond:      stutterWaitPath(paths[StutterWaitCond]),
		TimedCond: stutterWaitPath(paths[StutterWaitTimedCond]),
		FutexPump: stutterWaitPath(paths[StutterWaitFutexPump]),
	}
}

func testStutterWaitClockCalls() uint64 {
	return uint64(C.tipsy_test_stutter_wait_clock_calls())
}

func testStutterWaitRecord(path int, durationNS, slices uint64) {
	C.tipsy_test_stutter_wait_record(C.int(path), C.uint64_t(durationNS), C.uint64_t(slices))
}

func testStutterWaitResetTLS() {
	C.tipsy_test_stutter_wait_reset_tls()
}
