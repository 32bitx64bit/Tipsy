// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "frmstats.h"
*/
import "C"

// FrameTimeRingCapacity is the native frame-time ring length.
const FrameTimeRingCapacity = int(C.TIPSY_FRMSTATS_CAPACITY)

// SetFrameTimeArmed arms or disarms the native frame-time ring fed by the
// Vulkan present-return path. Arming starts a fresh capture. Returns the
// lifetime sequence cursor.
func SetFrameTimeArmed(armed bool) uint64 {
	v := C.int(0)
	if armed {
		v = 1
	}
	return uint64(C.tipsy_frmstats_set_armed(v))
}

// FrameTimeSnapshot copies retained present-return stamps newer than after.
// Overwritten is the capture's cumulative, increase-only lost-sample count:
// presents dropped for a newer slot owner plus samples the ring wrapped past
// before the oldest retained one. Fold in only the increase over the previous
// call.
func FrameTimeSnapshot(after uint64) (cursor, overwritten uint64, samples []uint64) {
	var raw [FrameTimeRingCapacity]C.uint64_t
	var c, o C.uint64_t
	n := int(C.tipsy_frmstats_snapshot(C.uint64_t(after), &raw[0],
		C.uint32_t(len(raw)), &c, &o))
	cursor, overwritten = uint64(c), uint64(o)
	if n == 0 {
		return cursor, overwritten, nil
	}
	samples = make([]uint64, n)
	for i := range samples {
		samples[i] = uint64(raw[i])
	}
	return cursor, overwritten, samples
}

// RecordFrameTimePresent records one present return at nowNS. Present-path
// tests use it; production presents call the native entry point directly.
func RecordFrameTimePresent(nowNS uint64) {
	C.tipsy_frmstats_present(C.uint64_t(nowNS))
}

// ResetFrameTimeRing disarms and clears the ring for tests.
func ResetFrameTimeRing() {
	C.tipsy_frmstats_reset()
}

// FrameTimeSequence returns the published sample count of the native ring.
func FrameTimeSequence() uint64 {
	return uint64(C.tipsy_frmstats_sequence())
}

// FrameTimeContended returns the number of armed presentations dropped because
// neither a supplied stamp nor the host clock was available.
func FrameTimeContended() uint64 {
	return uint64(C.tipsy_frmstats_contended())
}

// FrameTimeAttempts returns the number of armed presentations reaching the
// ring, including dropped presentations, since it was armed.
func FrameTimeAttempts() uint64 {
	return uint64(C.tipsy_frmstats_attempts())
}
