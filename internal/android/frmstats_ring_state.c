/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

// Reader side of the frame-time ring: the snapshot that walks it, arming, reset
// and the test-only helpers.
//
// It is a separate translation unit from frmstats_ring.c on purpose. The
// present path (ring) and the reader (here) are different code in different
// units, so the in-process C stress test exercises a real cross-unit
// publication protocol. The state both units share is a process-wide C11
// _Atomic variable with external linkage, not a file static: cgo links both
// units into one DSO and one test binary, so the symbol has one instance even
// when the Go link step keeps two copies of the archive (one with its own
// static TLS).

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include "frmstats.h"
#include "frmstats_internal.h"

#include <stdatomic.h>
#include <stddef.h>
#include <pthread.h>
#include <sched.h>
#include <time.h>

// The state owned by frmstats_ring.c.
extern _Atomic uint64_t frm_armed;
extern _Atomic uint64_t frm_inflight;
extern _Atomic uint64_t frm_claims;
extern _Atomic uint64_t frm_contended;
extern _Atomic uint64_t frm_dropped;
extern _Atomic uint64_t frm_overwritten;
extern _Atomic uint64_t frm_attempts;
extern frmstats_cell frm_slot[TIPSY_FRMSTATS_CAPACITY];
extern _Atomic uint64_t frm_ahead[TIPSY_FRMSTATS_CAPACITY];
#ifdef TIPSY_FRMSTATS_TEST_HOOKS
extern uint64_t *tipsy_frmstats_test_stamp_for(uint64_t sequence, uint64_t now_ns);
extern void tipsy_frmstats_test_publish_stamp(uint64_t sequence);
#endif

// A slot is not published for any sequence; it is also the mid-arm value. It is
// the writer marker of claim 0, so the first presenter takes the slot.
#define FRM_STATE_INVALID UINT64_MAX
#define FRM_STATE_SLOTS ((uint64_t)TIPSY_FRMSTATS_CAPACITY)

static pthread_mutex_t frm_control_mu = PTHREAD_MUTEX_INITIALIZER;
// Remember wrapped ranges already charged, including overlapping snapshots.
static uint64_t frm_wrap_through;

static void frm_disarm(void)
{
	uint64_t epoch = atomic_load_explicit(&frm_armed, memory_order_seq_cst);
	if (epoch & 1) atomic_store_explicit(&frm_armed, epoch + 1, memory_order_seq_cst);
	while (atomic_load_explicit(&frm_inflight, memory_order_seq_cst) != 0) sched_yield();
}

// frmctl_gate, when set, lets a snapshot read up to after+capacity even though
// no frame has arrived, so a test can hold the cursor at a known value. Tests
// only; never set in a normal run.
#ifdef TIPSY_FRMSTATS_TEST_HOOKS
static _Atomic uint32_t frmctl_gate;
#endif

uint64_t tipsy_frmstats_clock_ns(void)
{
	struct timespec ts;
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return 0;
	}
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

// frmctl_read_slot returns the clock value published for sequence, or 0 when
// the slot does not hold exactly that sequence.
//
// The sequence and stamp are one 16-byte cell, read by one lock cmpxchg16b, so
// the copy cannot pair a sequence with another presenter's stamp. A rewrite
// replaces the whole cell, and a cell that still names a writer marker or a
// different sequence is rejected.
static uint64_t frmctl_read_slot(uint64_t sequence)
{
	uint32_t slot = (uint32_t)((sequence - 1) % FRM_STATE_SLOTS);
	frmstats_cell cell = frmstats_load_cell(&frm_slot[slot]);
	if (cell.seq != sequence || cell.stamp == 0) {
		return 0;
	}
	return cell.stamp;
}

static void frm_reset_locked(void)
{
	frm_disarm();
	for (uint32_t i = 0; i < TIPSY_FRMSTATS_CAPACITY; i++) {
		// Control operations wait for presenters; the capture owns its reader.
		frm_slot[i].seq = FRM_STATE_INVALID;
		frm_slot[i].stamp = 0;
		atomic_store_explicit(&frm_ahead[i], 0, memory_order_relaxed);
	}
	atomic_store_explicit(&frm_contended, 0, memory_order_relaxed);
	atomic_store_explicit(&frm_dropped, 0, memory_order_relaxed);
	atomic_store_explicit(&frm_overwritten, 0, memory_order_relaxed);
	frm_wrap_through = 0;
	atomic_store_explicit(&frm_attempts, 0, memory_order_relaxed);
	atomic_store_explicit(&frm_claims, 0, memory_order_release);
}

void tipsy_frmstats_reset(void)
{
	pthread_mutex_lock(&frm_control_mu);
	frm_reset_locked();
	pthread_mutex_unlock(&frm_control_mu);
}

uint64_t tipsy_frmstats_set_armed(int armed)
{
	pthread_mutex_lock(&frm_control_mu);
	if (armed) {
		frm_reset_locked();
		atomic_fetch_add_explicit(&frm_armed, 1, memory_order_seq_cst);
	} else {
		frm_disarm();
	}
	uint64_t claims = atomic_load_explicit(&frm_claims, memory_order_acquire);
	pthread_mutex_unlock(&frm_control_mu);
	return claims;
}

// Wrap ranges and late claims are cumulative for the capture.
static uint64_t frm_overwritten_total(void)
{
	return atomic_load_explicit(&frm_overwritten, memory_order_relaxed) +
		atomic_load_explicit(&frm_dropped, memory_order_acquire);
}

static uint32_t frm_snapshot_locked(uint64_t after, uint64_t *out_ns, uint32_t capacity,
	uint64_t *out_cursor, uint64_t *out_overwritten)
{
	*out_cursor = after;
	// A claim whose slot held a newer sample is discarded by the presenter rather
	// than overwriting it. frm_dropped is the capture's running total of those
	// discards; the value this call reports also folds in any samples the ring
	// wrapped past before the oldest one this call can still return.
	*out_overwritten = frm_overwritten_total();
	if (out_ns == NULL || capacity == 0) {
		return 0;
	}
	// The claim counter is the sample count: a presenter has claimed its
	// sequence before it touches a slot, so a claim below the counter may still
	// be publishing, and one at or past it does not exist yet.
	uint64_t claims = atomic_load_explicit(&frm_claims, memory_order_acquire);
#ifdef TIPSY_FRMSTATS_TEST_HOOKS
	if (atomic_load_explicit(&frmctl_gate, memory_order_acquire) != 0 && claims < after) {
		claims = after;
	}
#endif
	if (claims < after) {
		// A sequence below the caller's cursor means the ring was reset or
		// re-armed under the reader; returning the new cursor lets the caller
		// see the backstep (and drop the gap) instead of inventing an interval.
		*out_cursor = claims;
		*out_overwritten = frm_overwritten_total();
		return 0;
	}
	if (claims == after) {
		*out_overwritten = frm_overwritten_total();
		return 0;
	}
	// The claims counter names every sequence handed out, but a claim may still
	// be uncommitted: its slot may hold WRITING, or an older sequence. The ahead
	// marks name the slots such a frame can hold: for every slot, the frames
	// that claimed it or a later one and have not published also contribute to
	// every slot in front of it. So claims - ahead[i] marks the first slot every
	// later claim may have rewritten, and nothing at or past that bound is
	// accepted.
	uint64_t started = after + 1;
	if (claims > after + FRM_STATE_SLOTS) {
		// The ring wrapped past the cursor: report what it lost and start at the
		// oldest sequence still retained. A cursor inside the ring is not
		// overwritten, however far the publication has moved (an unpublished
		// presenter holds its sequence back).
		uint64_t oldest = claims - FRM_STATE_SLOTS + 1;
		if (after + 1 < oldest) {
			uint64_t accounted = after > frm_wrap_through ? after : frm_wrap_through;
			if (oldest - 1 > accounted) {
				atomic_fetch_add_explicit(&frm_overwritten, oldest - 1 - accounted, memory_order_relaxed);
				frm_wrap_through = oldest - 1;
			}
			started = oldest;
		}
	}
	uint64_t horizon = claims;
	{
		uint64_t first = claims >= FRM_STATE_SLOTS ? claims - FRM_STATE_SLOTS + 1 : 1;
		if (first < started) {
			first = started;
		}
		for (uint64_t seq = first; seq <= claims; seq++) {
			uint32_t slot = (uint32_t)((seq - 1) % FRM_STATE_SLOTS);
			uint64_t ahead = atomic_load_explicit(&frm_ahead[slot], memory_order_acquire);
			if (ahead < claims) {
				uint64_t slot_horizon = claims - ahead;
				if (slot_horizon < horizon) {
					horizon = slot_horizon;
				}
			}
		}
	}
	if (horizon < started) {
		// Every claim in the window is still publishing. Return nothing; the next
		// poll picks it up, so an unpublished frame is never counted or returned.
		*out_overwritten = frm_overwritten_total();
		return 0;
	}
	// Stop at the first sequence not published in its slot, so the returned
	// cursor names only the samples handed back.
	uint64_t count = horizon - started + 1;
	if (count > capacity) {
		count = capacity;
	}
	uint32_t written = 0;
	for (uint64_t seq = started; seq < started + count; seq++) {
		uint64_t ns = frmctl_read_slot(seq);
		if (ns == 0) {
			break;
		}
		out_ns[written++] = ns;
	}
	if (written != 0) {
		*out_cursor = started + written - 1;
	}
	*out_overwritten = frm_overwritten_total();
	return written;
}

uint32_t tipsy_frmstats_snapshot(uint64_t after, uint64_t *out_ns, uint32_t capacity,
	uint64_t *out_cursor, uint64_t *out_overwritten)
{
	pthread_mutex_lock(&frm_control_mu);
	uint32_t count = frm_snapshot_locked(after, out_ns, capacity, out_cursor, out_overwritten);
	pthread_mutex_unlock(&frm_control_mu);
	return count;
}

uint64_t tipsy_frmstats_sequence(void)
{
	return atomic_load_explicit(&frm_claims, memory_order_acquire);
}

uint64_t tipsy_frmstats_contended(void)
{
	return atomic_load_explicit(&frm_contended, memory_order_acquire);
}

uint64_t tipsy_frmstats_attempts(void)
{
	return atomic_load_explicit(&frm_attempts, memory_order_acquire);
}

#ifdef TIPSY_FRMSTATS_TEST_HOOKS
uint64_t tipsy_frmstats_test_dropped(void)
{
	return atomic_load_explicit(&frm_dropped, memory_order_acquire);
}

#endif /* TIPSY_FRMSTATS_TEST_HOOKS */
