// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#cgo pkg-config: x11 xrandr xi
#cgo LDFLAGS: -lX11 -pthread
#cgo CFLAGS: -D_GNU_SOURCE
#include "x11.h"
*/
import "C"

import "unsafe"

// SetInputDrainDiagnostics enables the bounded X11 input-drain observer. It
// is disabled by default. Normal paths retain one enabled-gate load but do
// not take clocks, mutate counters, allocate, or retain input data. Production
// enables it only through exact TIPSY_STUTTER_DIAG=1 before window creation.
func SetInputDrainDiagnostics(enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_x11_input_diagnostics_set_enabled(v)
	inputDrainDiagnosticsOn.Store(enabled)
}

// InputDrainDiagnosticsEnabled reports whether the process-wide observer is
// active. Tipsy owns one Roblox X11 window per process.
func InputDrainDiagnosticsEnabled() bool {
	return inputDrainDiagnosticsEnabled()
}

func inputDrainDurationFromC(raw C.tipsy_x11_input_drain_duration_stats) InputDrainDurationStats {
	stats := InputDrainDurationStats{
		Samples: uint64(raw.samples),
		TotalNS: uint64(raw.total_ns),
		MaxNS:   uint64(raw.max_ns),
	}
	buckets := (*[InputDrainLatencyBuckets]C.uint64_t)(unsafe.Pointer(&raw.buckets[0]))
	for i := range stats.Buckets {
		stats.Buckets[i] = uint64(buckets[i])
	}
	return stats
}

// InputDrainSnapshot returns aggregate-only counters. With reset true, each
// counter is atomically exchanged, so a live pump can continue recording and
// a caller can use adjacent bounded intervals without storing raw input.
func InputDrainSnapshot(reset bool) InputDrainStats {
	var raw C.tipsy_x11_input_drain_stats
	r := C.int(0)
	if reset {
		r = 1
	}
	C.tipsy_x11_input_diagnostics_snapshot(&raw, r)
	windowLockWait, goDrain := inputDrainGoSnapshot(reset)
	stats := InputDrainStats{
		DrainCalls:        uint64(raw.drain_calls),
		EmptyDrains:       uint64(raw.empty_drains),
		NonEmptyDrains:    uint64(raw.nonempty_drains),
		Events:            uint64(raw.events),
		InputRingLockWait: inputDrainDurationFromC(raw.input_lock_wait),
		CDrain:            inputDrainDurationFromC(raw.c_drain),
		PumpWait:          inputDrainDurationFromC(raw.pump_wait),
		PumpWaitCalls:     uint64(raw.pump_wait_calls),
		PumpReady:         uint64(raw.pump_pending_ready),
		PumpPipeWakes:     uint64(raw.pump_pipe_wakes),
		PumpErrors:        uint64(raw.pump_errors),
		RingDrops:         uint64(raw.ring_drops),
		GoWindowLockWait:  windowLockWait,
		GoDrain:           goDrain,
	}
	batches := (*[InputDrainBatchBuckets]C.uint64_t)(unsafe.Pointer(&raw.batch_buckets[0]))
	for i := range stats.BatchBuckets {
		stats.BatchBuckets[i] = uint64(batches[i])
	}
	return stats
}
