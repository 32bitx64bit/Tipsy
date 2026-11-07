/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#ifndef TIPSY_FRMSTATS_H
#define TIPSY_FRMSTATS_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Frame-time sample ring for the process-wide present path.
//
// The Vulkan present path (vulkan.c) is the interval source: one monotonic
// stamp immediately after each successful vkQueuePresentKHR return. EGL swaps
// are not recorded.
//
// An interval is a present-return interval: the time between two successive
// returns from a successful present call. It is not display scan-out and not
// input-to-photon latency.
//
// Recording cost when disarmed is one relaxed atomic load and a predictable
// branch. Producers never block and never drop a sample for another producer:
// each present takes a unique sequence with one atomic increment, so any number
// of threads may present at once. The one present that can be dropped is a
// claim too late for the ring, whose slot a newer sample already holds; it is
// counted in out_overwritten rather than overwriting that newer sample.

#define TIPSY_FRMSTATS_CAPACITY 8192

// tipsy_frmstats_set_armed arms or disarms recording and returns the sequence
// cursor of the current capture. Arming starts a fresh capture; disarming
// retains samples until the next arm so a reader can take one final snapshot.
// Arming waits for in-flight presenters before clearing the ring.
uint64_t tipsy_frmstats_set_armed(int armed);

// tipsy_frmstats_present records one successful present return stamped at
// now_ns (host CLOCK_MONOTONIC). A zero stamp means no clock was taken: the
// present still records, with the clock read here. Called from any thread; safe
// when disarmed and async-signal safe.
void tipsy_frmstats_present(uint64_t now_ns);

// tipsy_frmstats_snapshot copies retained timestamps newer than after into
// out_ns (capacity entries), writing the last returned sequence to out_cursor
// and the capture's lost-sample count to out_overwritten. Returns the number of
// entries written. Presenters publish in claim order or out of it, so a gap
// inside the window is normal: the copy stops before the first sequence whose
// slot is still being published, and out_cursor names only the samples returned.
//
// out_overwritten is a cumulative, increase-only lost-sample count for the
// capture: it includes every present this capture dropped because a newer
// sample had already taken its slot (a late claim publishing after the ring
// wrapped), plus the samples the ring has wrapped past before the oldest one
// this call can still return. Wrap losses are accumulated across polls. A late
// claim can also be counted in a wrapped range. Overlapping snapshots do not
// charge a wrapped range twice. The reported value never goes down within one capture. It is not a
// per-call delta: compare it with the previous call's value and count only the
// increase, and after any loss do not read zero as "caught up". It is reset by
// tipsy_frmstats_set_armed and tipsy_frmstats_reset, so a value below the
// previous one means the ring was re-armed.
uint32_t tipsy_frmstats_snapshot(uint64_t after, uint64_t *out_ns, uint32_t capacity,
	uint64_t *out_cursor, uint64_t *out_overwritten);

// tipsy_frmstats_sequence returns the count of arrived samples. It is the same
// value tipsy_frmstats_set_armed returns and never goes backwards during a
// capture.
uint64_t tipsy_frmstats_sequence(void);

// tipsy_frmstats_contended returns the number of armed presentations dropped
// because neither a supplied stamp nor the host clock was available. It is
// cleared when the ring is armed.
uint64_t tipsy_frmstats_contended(void);

// tipsy_frmstats_attempts returns the number of armed presentations reaching
// the ring, including dropped presentations, since it was armed.
uint64_t tipsy_frmstats_attempts(void);

#ifdef TIPSY_FRMSTATS_TEST_HOOKS
// tipsy_frmstats_test_dropped returns the number of presents dropped because a
// newer sample already held their slot. It is part of what
// tipsy_frmstats_snapshot reports in out_overwritten. Tests only.
uint64_t tipsy_frmstats_test_dropped(void);

// tipsy_frmstats_test_stamp_for fills the slot of sample sequence with now_ns
// as if its presenter were suspended mid-store, and returns the address of the
// slot holding it. Tests only: it proves a reader rejects an unpublished stamp.
uint64_t *tipsy_frmstats_test_stamp_for(uint64_t sequence, uint64_t now_ns);

// tipsy_frmstats_test_publish_stamp completes the publish that
// tipsy_frmstats_test_stamp_for suspended. Tests only.
void tipsy_frmstats_test_publish_stamp(uint64_t sequence);

// tipsy_frmstats_test_present_numbered records one present whose stamp is the
// sequence it claims, so a reader can assert the exact value a sequence must
// hold. Tests only.
void tipsy_frmstats_test_present_numbered(void);

#endif /* TIPSY_FRMSTATS_TEST_HOOKS */

// tipsy_frmstats_reset disarms recording and clears the ring. Tests only.
void tipsy_frmstats_reset(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_FRMSTATS_H */
