// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && tipsy_frmstats_testhooks

package android

/*
#cgo CFLAGS: -DTIPSY_FRMSTATS_TEST_HOOKS
#cgo LDFLAGS: -lX11 -lX11-xcb -lXext -lxcb -ldl -pthread
#include <stdint.h>

#include "frmstats.h"

void tipsy_test_vk_note_frame_time(int32_t result, uint64_t now_ns);
*/
import "C"

// vkRecordFrameTime runs the adapter's frame-time note for one
// vkQueuePresentKHR return. Only tests use it; the adapter itself calls the
// same native function from its present wrapper.
func vkRecordFrameTime(result int32, nowNS uint64) {
	C.tipsy_test_vk_note_frame_time(C.int32_t(result), C.uint64_t(nowNS))
}

// publishFrameTimeStamp completes the publish that
// stampFrameTimeWithoutPublishing suspended. Tests only.
func publishFrameTimeStamp(sequence uint64) {
	C.tipsy_frmstats_test_publish_stamp(C.uint64_t(sequence))
}

// recordFrameTimePresentNumbered records one present whose stamp is the sequence
// it claims. Tests only.
func recordFrameTimePresentNumbered() {
	C.tipsy_frmstats_test_present_numbered()
}

// frameTimeDropped returns the number of presents dropped because a newer
// sample already held their slot. Tests only.
func frameTimeDropped() uint64 {
	return uint64(C.tipsy_frmstats_test_dropped())
}

// stampFrameTimeWithoutPublishing stores nowNS as sample sequence and leaves it
// unpublished, as a presenter suspended mid-store would. It returns the slot
// address so a test can assert the reader never reports that stamp while it is
// unpublished, then call RecordFrameTimePresent to publish it. Tests only.
func stampFrameTimeWithoutPublishing(sequence, nowNS uint64) *C.uint64_t {
	return C.tipsy_frmstats_test_stamp_for(C.uint64_t(sequence), C.uint64_t(nowNS))
}
