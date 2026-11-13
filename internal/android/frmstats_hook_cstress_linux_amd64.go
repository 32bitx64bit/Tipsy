// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && tipsy_frmstats_testhooks

package android

/*
#cgo CFLAGS: -DTIPSY_FRMSTATS_TEST_HOOKS
#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>

#include "frmstats.h"

extern _Atomic uint64_t frm_test_hold_claim;
extern _Atomic int frm_test_hold_reached;
extern _Atomic int frm_test_release;
extern _Atomic uint64_t frm_test_after_slot_claim;
extern _Atomic int frm_test_after_slot_reached;
extern _Atomic int frm_test_after_slot_release;

static void tipsy_frmstats_storm_hold(uint64_t claim)
{
	// Clear the release latch before publishing the claim, so a presenter that
	// sees the new claim cannot also read the previous run's release.
	atomic_store_explicit(&frm_test_release, 0, memory_order_seq_cst);
	atomic_store_explicit(&frm_test_hold_reached, 0, memory_order_seq_cst);
	atomic_store_explicit(&frm_test_hold_claim, claim, memory_order_seq_cst);
}

static int tipsy_frmstats_storm_held(void)
{
	return atomic_load_explicit(&frm_test_hold_reached, memory_order_seq_cst);
}

static void tipsy_frmstats_storm_release(void)
{
	atomic_store_explicit(&frm_test_release, 1, memory_order_seq_cst);
}

static void tipsy_frmstats_storm_hold_after_slot(uint64_t claim)
{
	atomic_store_explicit(&frm_test_after_slot_release, 0, memory_order_seq_cst);
	atomic_store_explicit(&frm_test_after_slot_reached, 0, memory_order_seq_cst);
	atomic_store_explicit(&frm_test_after_slot_claim, claim, memory_order_seq_cst);
}

static int tipsy_frmstats_storm_held_after_slot(void)
{
	return atomic_load_explicit(&frm_test_after_slot_reached, memory_order_seq_cst);
}

static void tipsy_frmstats_storm_release_after_slot(void)
{
	atomic_store_explicit(&frm_test_after_slot_release, 1, memory_order_seq_cst);
}

static int tipsy_frmstats_storm_writes;
static int tipsy_frmstats_storm_attempts;
static int tipsy_frmstats_storm_after_zero;
static _Atomic uint64_t tipsy_frmstats_storm_claimed;
static _Atomic int tipsy_frmstats_storm_torn;
static _Atomic uint64_t tipsy_frmstats_storm_seen;

// tipsy_frmstats_storm_write presents through the C entry point, the path a
// Vulkan presenter takes. Every present is numbered with the sequence it
// claims, so a reader knows the exact stamp a sequence must hold. With
// zero_first set, every thread's first present arrives without a clock instead:
// it must still get a full-width host stamp, and no later presenter may
// overwrite it.
static void *tipsy_frmstats_storm_write(void *arg)
{
	(void)arg;
	for (int i = 0; i < tipsy_frmstats_storm_writes; i++) {
		if (i == 0 && tipsy_frmstats_storm_after_zero == 1) {
			tipsy_frmstats_present(0);
			continue;
		}
		tipsy_frmstats_test_present_numbered();
	}
	return NULL;
}

// TIPSY_FRMSTATS_STORM_CLOCK_FLOOR is the smallest host clock value a
// zero-stamp present can store. Every sequence this test hands out is far
// below it, so a clock stamp can never pass for the numbered stamp of a
// sequence.
#define TIPSY_FRMSTATS_STORM_CLOCK_FLOOR (1ULL << 40)

// tipsy_frmstats_storm_is_own_stamp reports whether stamp is exactly the value
// its sequence must hold: the sequence itself for every numbered present, or a
// host clock value for a present that arrived without one. Anything else is a
// torn slot, another sequence's sample in its place, or a slot a late claim
// overwrote.
static int tipsy_frmstats_storm_is_own_stamp(uint64_t sequence, uint64_t stamp)
{
	if (stamp == sequence) {
		return 1;
	}
	if (tipsy_frmstats_storm_after_zero == 1 && stamp >= TIPSY_FRMSTATS_STORM_CLOCK_FLOOR) {
		return 1;
	}
	return 0;
}

// tipsy_frmstats_storm_read snapshots exactly as internal/perf does, polling
// the cursor forward. A sample it never saw, because the cursor advanced past a
// frame that was still unpublished, is a lost claim: the reader marks it the
// way it marks any overwritten window. Both are tracked so the test can tell a
// gap it is allowed to skip from a torn or foreign stamp.
static void *tipsy_frmstats_storm_read(void *arg)
{
	(void)arg;
	uint64_t after = 0;
	uint64_t buf[64];
	// out_overwritten is the ring's cumulative, increase-only lost-sample
	// count, not a per-call flag: once any window was lost it stays non-zero
	// for the rest of the run. Comparing it with the last value this reader saw
	// keeps a stale count from being read as a fresh gap, which would skip the
	// drain check below and spin forever once the writers have finished.
	uint64_t dropped_seen = 0;
	for (;;) {
		uint64_t cursor, overwritten;
		uint32_t n = tipsy_frmstats_snapshot(after, buf, 64, &cursor, &overwritten);
		if (n == 0) {
			if (overwritten != dropped_seen) {
				dropped_seen = overwritten;
				uint64_t sequence = tipsy_frmstats_sequence();
				if (sequence > TIPSY_FRMSTATS_CAPACITY) {
					after = sequence - TIPSY_FRMSTATS_CAPACITY;
				}
				continue;
			}
			// Presenters are done once every one of them has claimed all its
			// sequences: drain what is still unpublished, as a final snapshot
			// does, then stop.
			if (tipsy_frmstats_sequence() == (uint64_t)tipsy_frmstats_storm_attempts &&
				after >= tipsy_frmstats_sequence()) {
				break;
			}
			continue;
		}
		// The window is contiguous and ends at cursor, so the first stamp is
		// cursor-n+1 and each stamp must be exactly that sequence's value.
		uint64_t first = cursor - n + 1;
		for (uint32_t i = 0; i < n; i++) {
			if (!tipsy_frmstats_storm_is_own_stamp(first + i, buf[i])) {
				atomic_store_explicit(&tipsy_frmstats_storm_torn, 1, memory_order_relaxed);
			} else {
				atomic_fetch_add_explicit(&tipsy_frmstats_storm_seen, 1, memory_order_relaxed);
			}
		}
		if (cursor < after) {
			atomic_store_explicit(&tipsy_frmstats_storm_torn, 1, memory_order_relaxed);
		}
		after = cursor;
	}
	return NULL;
}

// tipsy_frmstats_storm_run returns 0 when every sample a reader saw was exactly
// the stamp its sequence owns, 1 when a torn, foreign or stale stamp was seen,
// and negative on a thread error.
static int32_t tipsy_frmstats_storm_run(int writers, int readers, int writes, int zero_first)
{
	pthread_t write_threads[8];
	pthread_t read_threads[8];
	if (writers < 1 || writers > 8 || readers < 0 || readers > 8 || writes < 1) {
		return -1;
	}
	tipsy_frmstats_storm_writes = writes;
	tipsy_frmstats_storm_attempts = writers * writes;
	tipsy_frmstats_storm_after_zero = zero_first;
	atomic_store(&tipsy_frmstats_storm_torn, 0);
	atomic_store(&tipsy_frmstats_storm_seen, 0);
	for (int i = 0; i < readers; i++) {
		if (pthread_create(&read_threads[i], NULL, tipsy_frmstats_storm_read, NULL) != 0) {
			return -2;
		}
	}
	for (int i = 0; i < writers; i++) {
		if (pthread_create(&write_threads[i], NULL, tipsy_frmstats_storm_write, NULL) != 0) {
			return -2;
		}
	}
	for (int i = 0; i < writers; i++) {
		pthread_join(write_threads[i], NULL);
	}
	for (int i = 0; i < readers; i++) {
		pthread_join(read_threads[i], NULL);
	}
	return atomic_load(&tipsy_frmstats_storm_torn) != 0 ? 1 : 0;
}

// tipsy_frmstats_storm_seen_count reports how many samples the readers accepted.
static uint64_t tipsy_frmstats_storm_seen_count(void)
{
	return atomic_load(&tipsy_frmstats_storm_seen);
}
*/
import "C"

// holdFrameTimeFill suspends the presenter that claims sequence until
// releaseFrameTimeFill, after it has taken its slot and before it publishes.
func holdFrameTimeFill(sequence uint64) {
	C.tipsy_frmstats_storm_hold(C.uint64_t(sequence))
}

// frameTimeFillReached reports that the held presenter is suspended.
func frameTimeFillReached() bool {
	return C.tipsy_frmstats_storm_held() != 0
}

// releaseFrameTimeFill lets the held presenter finish its fill.
func releaseFrameTimeFill() {
	C.tipsy_frmstats_storm_release()
}

// holdFrameTimeAfterSlot suspends the presenter that claims sequence right
// after it has taken its slot, so a test can take the slot over while that
// presenter is still there.
func holdFrameTimeAfterSlot(sequence uint64) {
	C.tipsy_frmstats_storm_hold_after_slot(C.uint64_t(sequence))
}

// frameTimeAfterSlotReached reports that the held presenter is suspended.
func frameTimeAfterSlotReached() bool {
	return C.tipsy_frmstats_storm_held_after_slot() != 0
}

// releaseFrameTimeAfterSlot lets the held presenter continue its fill.
func releaseFrameTimeAfterSlot() {
	C.tipsy_frmstats_storm_release_after_slot()
}

// runFrameTimeStorm presents from writers C threads while readers snapshot, and
// returns 0 when no torn or foreign stamp was returned.
func runFrameTimeStorm(writers, readers, writes int) int32 {
	return int32(C.tipsy_frmstats_storm_run(C.int(writers), C.int(readers), C.int(writes), 0))
}

// runFrameTimeStormWithZeroFirst is runFrameTimeStorm with every writer's first
// present carrying no clock, to exercise the clock fallback under contention.
func runFrameTimeStormWithZeroFirst(writers, readers, writes int) int32 {
	return int32(C.tipsy_frmstats_storm_run(C.int(writers), C.int(readers), C.int(writes), 1))
}

// stormSeenFrameTimes reports how many samples the storm readers accepted.
func stormSeenFrameTimes() uint64 {
	return uint64(C.tipsy_frmstats_storm_seen_count())
}
