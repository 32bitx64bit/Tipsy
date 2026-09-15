// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"sync/atomic"
	"time"
)

const (
	// InputDrainBatchBuckets is the number of fixed aggregate batch-size
	// buckets. Their bounds are 0, 1, 2-3, 4-7, 8-15, 16-31, 32-63, and 64+.
	InputDrainBatchBuckets = 8
	// InputDrainLatencyBuckets is the number of fixed aggregate latency
	// buckets. They are logarithmic from one microsecond through 32768+ us.
	InputDrainLatencyBuckets = 16
)

// InputDrainDurationStats is a content-free aggregate duration distribution.
// Total and Max are in monotonic nanoseconds. Buckets never retain individual
// samples or timestamps.
type InputDrainDurationStats struct {
	Samples uint64
	TotalNS uint64
	MaxNS   uint64
	Buckets [InputDrainLatencyBuckets]uint64
}

// InputDrainStats is a bounded aggregate observer for the X11 input-drain
// boundary. It contains no input event payload, coordinates, key identity,
// text, timestamps, window identifiers, URL, or account data.
//
// DrainCalls/EmptyDrains/NonEmptyDrains/Events and BatchBuckets are exact for
// an enabled interval. Duration fields are sampled once per 64 C-side calls
// to bound diagnostic perturbation. GoWindowLockWait and GoDrain include all
// enabled calls because they are already on the Go launch-loop boundary.
type InputDrainStats struct {
	DrainCalls     uint64
	EmptyDrains    uint64
	NonEmptyDrains uint64
	Events         uint64
	BatchBuckets   [InputDrainBatchBuckets]uint64

	// InputRingLockWait measures only waiting for the C input-ring mutex.
	InputRingLockWait InputDrainDurationStats
	// CDrain spans C input-ring lock, copy, and unlock.
	CDrain InputDrainDurationStats
	// PumpWait spans the background X-pump wait boundary.
	PumpWait      InputDrainDurationStats
	PumpWaitCalls uint64
	PumpReady     uint64
	PumpPipeWakes uint64
	PumpErrors    uint64
	// RingDrops counts oldest-queued events discarded on ring saturation
	// during an enabled interval. It is the telemetry half of the overflow
	// task; a resynchronization protocol for dropped edges is still open.
	RingDrops uint64

	// GoWindowLockWait covers waiting to enter Window.Pump's Window mutex.
	GoWindowLockWait InputDrainDurationStats
	// GoDrain covers the cgo drain, event decode, and privacy wipe before
	// subscribers are invoked. It excludes subscriber/JNI dispatch.
	GoDrain InputDrainDurationStats
}

// inputDrainDiagnosticsRequested shares the exact stutter-capture policy.
// Values such as "true" and "yes" stay disabled so captures are deliberate
// and normal launches retain their existing behavior.
func inputDrainDiagnosticsRequested(getenv func(string) string) bool {
	return getenv != nil && getenv("TIPSY_STUTTER_DIAG") == "1"
}

var inputDrainDiagnosticsOn atomic.Bool

func inputDrainDiagnosticsEnabled() bool { return inputDrainDiagnosticsOn.Load() }

type inputDrainGoDuration struct {
	samples atomic.Uint64
	totalNS atomic.Uint64
	maxNS   atomic.Uint64
	buckets [InputDrainLatencyBuckets]atomic.Uint64
}

func inputDrainLatencyBucket(ns uint64) int {
	// 0 = <=1us; buckets thereafter double through 32768us, then saturate.
	if ns <= uint64(time.Microsecond) {
		return 0
	}
	unit := uint64(time.Microsecond)
	us := ns / unit
	if ns%unit != 0 {
		us++
	}
	bucket := 0
	for us > 1 && bucket < InputDrainLatencyBuckets-1 {
		us = (us + 1) >> 1
		bucket++
	}
	return bucket
}

func (d *inputDrainGoDuration) record(elapsed time.Duration) {
	if elapsed < 0 {
		return
	}
	ns := uint64(elapsed)
	d.samples.Add(1)
	d.totalNS.Add(ns)
	for old := d.maxNS.Load(); old < ns && !d.maxNS.CompareAndSwap(old, ns); old = d.maxNS.Load() {
	}
	d.buckets[inputDrainLatencyBucket(ns)].Add(1)
}

func (d *inputDrainGoDuration) snapshot(reset bool) InputDrainDurationStats {
	read := func(v *atomic.Uint64) uint64 {
		if reset {
			return v.Swap(0)
		}
		return v.Load()
	}
	stats := InputDrainDurationStats{
		Samples: read(&d.samples),
		TotalNS: read(&d.totalNS),
		MaxNS:   read(&d.maxNS),
	}
	for i := range d.buckets {
		stats.Buckets[i] = read(&d.buckets[i])
	}
	return stats
}

var inputDrainGoStats struct {
	windowLockWait inputDrainGoDuration
	drain          inputDrainGoDuration
}

func recordInputDrainWindowLockWait(elapsed time.Duration) {
	inputDrainGoStats.windowLockWait.record(elapsed)
}

func recordInputDrainDuration(elapsed time.Duration) {
	inputDrainGoStats.drain.record(elapsed)
}

func inputDrainGoSnapshot(reset bool) (windowLockWait, drain InputDrainDurationStats) {
	return inputDrainGoStats.windowLockWait.snapshot(reset), inputDrainGoStats.drain.snapshot(reset)
}
