// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

// OpenSLQueueOwnership contains fixed-size, content-free queue ownership
// counters from a bounded fake-player fixture. Allocations and copies are C
// queue-storage events, not Go allocations, CPU, RSS, OS wakeups, audio
// latency, or FPS measurements.
type OpenSLQueueOwnership struct {
	Capacity                    uint32
	NodesOwned                  uint32
	NodesFree                   uint32
	Queued                      uint32
	Inflight                    uint32
	Callbacks                   uint32
	NodeAllocations             uint64
	PayloadAllocations          uint64
	PayloadBytesAllocated       uint64
	CopyOperations              uint64
	CopyBytes                   uint64
	NodeReclamations            uint64
	PayloadReclamations         uint64
	CapacityRejections          uint64
	CallerBufferCopiesPreserved bool
}

// OpenSLQueueOwnershipFixture runs a bounded two-buffer fake-player sequence:
// a deliberately full enqueue, two callback re-enqueues, then stop and Clear.
// It never opens a device or carries PCM out of C, and exists solely to make
// queue lifetime/capacity policy independently measurable by internal/perf.
func OpenSLQueueOwnershipFixture() (OpenSLQueueOwnership, bool) {
	var result C.tipsy_audio_queue_ownership_result
	rc := C.tipsy_audio_test_queue_ownership(&result)
	return OpenSLQueueOwnership{
		Capacity:                    uint32(result.capacity),
		NodesOwned:                  uint32(result.nodes_owned),
		NodesFree:                   uint32(result.nodes_free),
		Queued:                      uint32(result.queued),
		Inflight:                    uint32(result.inflight),
		Callbacks:                   uint32(result.callbacks),
		NodeAllocations:             uint64(result.node_allocations),
		PayloadAllocations:          uint64(result.payload_allocations),
		PayloadBytesAllocated:       uint64(result.payload_bytes_allocated),
		CopyOperations:              uint64(result.copy_operations),
		CopyBytes:                   uint64(result.copy_bytes),
		NodeReclamations:            uint64(result.node_reclamations),
		PayloadReclamations:         uint64(result.payload_reclamations),
		CapacityRejections:          uint64(result.capacity_rejections),
		CallerBufferCopiesPreserved: result.caller_buffer_copies_preserved != 0,
	}, int(rc) == 0
}

// OpenSLRetryBackoff contains content-free worker-state metadata from a fake
// repeated-failure fixture. It demonstrates interruption and error-log
// limiting; it is not a device timing, CPU, RSS, wakeup, or latency result.
type OpenSLRetryBackoff struct {
	Callbacks            uint32
	Queued               uint32
	Inflight             uint32
	RetryWaits           uint64
	RetryInterruptions   uint64
	ErrorLogEmissions    uint64
	ErrorLogSuppressions uint64
}

func openSLRetryBackoffFixture() (OpenSLRetryBackoff, bool) {
	var result C.tipsy_audio_retry_backoff_result
	rc := C.tipsy_audio_test_retry_backoff(&result)
	return OpenSLRetryBackoff{
		Callbacks:            uint32(result.callbacks),
		Queued:               uint32(result.queued),
		Inflight:             uint32(result.inflight),
		RetryWaits:           uint64(result.retry_waits),
		RetryInterruptions:   uint64(result.retry_interruptions),
		ErrorLogEmissions:    uint64(result.error_log_emissions),
		ErrorLogSuppressions: uint64(result.error_log_suppressions),
	}, int(rc) == 0
}

// MutedCaptureCadence contains only fixed-size cadence timing metadata
// from the fake OpenSL backend. It deliberately carries no PCM, device,
// address, client, or microphone information.
type MutedCaptureCadence struct {
	Callbacks              uint32
	CallbackIntervalP50NS  uint64
	CallbackIntervalP95NS  uint64
	CallbackIntervalP99NS  uint64
	CallbackToRequeueP50NS uint64
	CallbackToRequeueP95NS uint64
	CallbackToRequeueP99NS uint64
	ScheduledIntervalNS    uint64
	DeadlineWaits          uint32
	MissedDeadlineClamps   uint32
}

// MutedCaptureCadenceFixture runs a bounded fake-host re-enqueue fixture.
// It is the Android-owned measurement seam for internal/perf. It does not
// measure CPU, native allocation, RSS, OS wakeups, or end-to-end latency.
func MutedCaptureCadenceFixture() (MutedCaptureCadence, bool) {
	var result C.tipsy_audio_muted_cadence_result
	rc := C.tipsy_audio_test_muted_cadence(&result)
	return MutedCaptureCadence{
		Callbacks:              uint32(result.callbacks),
		CallbackIntervalP50NS:  uint64(result.callback_interval_p50_ns),
		CallbackIntervalP95NS:  uint64(result.callback_interval_p95_ns),
		CallbackIntervalP99NS:  uint64(result.callback_interval_p99_ns),
		CallbackToRequeueP50NS: uint64(result.callback_to_requeue_p50_ns),
		CallbackToRequeueP95NS: uint64(result.callback_to_requeue_p95_ns),
		CallbackToRequeueP99NS: uint64(result.callback_to_requeue_p99_ns),
		ScheduledIntervalNS:    uint64(result.scheduled_interval_ns),
		DeadlineWaits:          uint32(result.deadline_waits),
		MissedDeadlineClamps:   uint32(result.missed_deadline_clamps),
	}, int(rc) == 0
}

func audioTestMutedDeadlineMath() (uint64, uint32, int) {
	var interval C.uint64_t
	var clamps C.uint32_t
	rc := C.tipsy_audio_test_muted_deadline_math(&interval, &clamps)
	return uint64(interval), uint32(clamps), int(rc)
}

func audioTestMutedCaptureInterrupts() (uint32, int) {
	var failed C.uint32_t
	rc := C.tipsy_audio_test_muted_capture_interrupts(&failed)
	return uint32(failed), int(rc)
}

// audioTestCaptureUnmuteRace deterministically flips mute after a recorder
// committed a silent-buffer decision. It is a fake-host safety seam: the
// buffer must stay silent and no unopened backend may be read.
func audioTestCaptureUnmuteRace() int {
	return int(C.tipsy_audio_test_capture_unmute_race())
}
