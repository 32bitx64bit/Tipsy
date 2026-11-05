// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !amd64

package android

// FrameTimeRingCapacity is the native frame-time ring length.
const FrameTimeRingCapacity = 8192

// SetFrameTimeArmed reports that this build has no Vulkan presenter ring.
func SetFrameTimeArmed(armed bool) uint64 { return 0 }

// FrameTimeSnapshot reports that this build has no retained samples.
func FrameTimeSnapshot(after uint64) (cursor, overwritten uint64, samples []uint64) {
	return after, 0, nil
}

// RecordFrameTimePresent is a no-op without the native ring.
func RecordFrameTimePresent(nowNS uint64) {}

// FrameTimeSequence reports an empty ring on a build without one.
func FrameTimeSequence() uint64 { return 0 }

// FrameTimeContended reports no dropped presentations without a native ring.
func FrameTimeContended() uint64 { return 0 }

// FrameTimeAttempts reports no presentations without a native ring.
func FrameTimeAttempts() uint64 { return 0 }

// ResetFrameTimeRing is a no-op without the native ring.
func ResetFrameTimeRing() {}
