/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

// Private frame-time ring for internal/android and the Go entry points that
// arm and read it.
//
// The Vulkan adapter cannot call internal/graphics directly: a Go type cannot
// be shared between internal/android and internal/graphics without an import
// cycle (internal/graphics imports internal/android). cgo also links only the
// C files of the package being built. This translation unit therefore owns the
// ring used by the platform present path, and internal/perf drives it through
// this package's Go wrappers in frmstats_linux_amd64.go.
//
// tipsy_frmstats_* below implement frmstats.h, which vulkan.c includes. The
// entry point set is exactly what internal/perf needs: arm, record, snapshot,
// reset.
//
// Several presenters. Roblox presents from more than one thread, so the ring
// never assumes a single writer and never drops a sample to serialise them:
//
//	present:  claim = fetch_add(claims, 1) + 1     (one LOCK XADD, unique)
//	          ahead[claim] += 1                    (this frame, pending)
//	          slot[slot] = {WRITING, 0}            (lock cmpxchg16b)
//	          if slot[slot].seq >= claim: drop     (a newer sample owns the slot)
//	          slot[slot] = {claim, now_ns}         (same atomic cell)
//	          ahead[claim] -= 1                    (published)
//	reader:   claims_ = acquire load of claims; the window ends at
//	          claims_ - ahead[every slot] (a pending frame pulls it down to its
//	          own slot); for each sequence before the end: read one 16-byte
//	          cell and accept only when cell.seq == sequence.
//
// Claims are handed out once and never reused, so two presenters can never
// write the same sequence. They publish in nearly claim order (a frame lets
// the frame FRM_SLOTS ahead finish first), which makes a gap inside the window
// normal; a slot still holding WRITING or an older claim is not accepted, and
// the reader resumes at the last sample it returned.
//
// The stamp is the whole CLOCK_MONOTONIC value. No tag, sequence counter or
// marker is packed into any of its 64 bits; a zero stamp is never stored, and
// a present that arrives without one reads the clock here.
//
// Every store the present path makes is one atomic instruction (lock xadd,
// lock cmpxchg and lock cmpxchg16b through inline asm), so a signal handler on
// the same thread must wait for it rather than see it half done, and none takes
// a library lock: the path is async-signal safe and lock-free. A slot's
// sequence and stamp live in one 16-byte cell and only ever change together in
// a lock cmpxchg16b, so a late claim can never write a stamp into a slot that a
// newer sample has already published.
//
// The public accessors a reader or a test calls live in frmstats_ring_state.c.

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include "frmstats.h"
#include "frmstats_internal.h"

#include <stdatomic.h>
#include <stddef.h>

// A slot holds either the sequence of the sample published in it or a writer
// marker. A marker is UINT64_MAX - claim: it names the presenter filling the
// slot, so a newer claim can tell a stalled older presenter from a live one and
// take the slot over. Markers are therefore above every real sequence, and
// FRM_INVALID (claim 0) is simply the marker of a slot no presenter has taken.
// Claims stay below 2^63 so no marker can pass for a sequence.
#define FRM_INVALID UINT64_MAX
#define FRM_MARKER_FLOOR ((uint64_t)1 << 63)
// frm_try_slot results.
#define FRM_TAKEN 1
#define FRM_TOOK_OVER 2
#define FRM_SLOTS ((uint64_t)TIPSY_FRMSTATS_CAPACITY)

// frm_marker is the writer marker for claim.
static inline uint64_t frm_marker(uint64_t claim)
{
	return FRM_INVALID - claim;
}

// These symbols have external linkage on purpose: frmstats_ring_state.c reads
// them, and an external definition is one process-wide variable even when the
// Go link step keeps two copies of this archive. A file-static would resolve
// inside the copy that is linked and leave the other one undefined.
_Atomic uint64_t frm_armed;
_Atomic uint64_t frm_inflight;
// frm_claims is the lifetime ticket counter. A presenter takes its sequence
// with fetch_add, so every sequence has exactly one owner.
_Atomic uint64_t frm_claims;
_Atomic uint64_t frm_contended;
// frm_dropped counts claims that found a newer sample in their slot and were
// discarded instead of overwriting it. The reader adds it to out_overwritten.
_Atomic uint64_t frm_dropped;
// The reader tracks the sum of disjoint wrapped ranges separately from drops.
_Atomic uint64_t frm_overwritten;
_Atomic uint64_t frm_attempts;
// frm_slot holds the sequence and stamp together. Every read and write of a
// slot is one lock cmpxchg16b, so a published pair is never torn. The array is
// 16-byte aligned because cmpxchg16b requires it.
frmstats_cell frm_slot[TIPSY_FRMSTATS_CAPACITY] __attribute__((aligned(16)));
// frm_ahead counts, per slot, frames that may still write that slot: +1 when a
// frame takes its claim, -1 when the frame FRM_SLOTS behind it occupies the
// slot. The reader's high bound is claims - ahead[slot] for every slot, so a
// frame that is pending always pulls the window boundary down to its slot.
// frm_ahead is uint64_t cells because the present path updates them with a
// 64-bit lock cmpxchg; a 32-bit cell would share a word with its neighbour.
_Atomic uint64_t frm_ahead[TIPSY_FRMSTATS_CAPACITY];

#ifdef TIPSY_FRMSTATS_TEST_HOOKS
// The hook lets a tagged test suspend one presenter inside frm_fill, after it
// has taken its slot and before it publishes, to prove a stalled claim cannot
// write into a slot a newer sample already owns. The build tag supplies the C
// macro, so a production build has neither the symbols nor the branch. The
// handshake cells are C11 atomics: the Go test thread and the presenter thread
// synchronise through them, so a plain volatile store would be a data race.
_Atomic uint64_t frm_test_hold_claim;
_Atomic int frm_test_hold_reached;
_Atomic int frm_test_release;
// frm_test_after_slot_* suspends a presenter immediately after it takes its
// slot, before the takeover bookkeeping and publication that follow. Tests use
// it to drive a takeover of that slot while the older presenter is still there.
_Atomic uint64_t frm_test_after_slot_claim;
_Atomic int frm_test_after_slot_reached;
_Atomic int frm_test_after_slot_release;
#endif

static uint32_t frm_slot_for(uint64_t sequence)
{
	return (uint32_t)((sequence - 1) % FRM_SLOTS);
}

#if !defined(__x86_64__)
#error "frmstats_ring.c uses x86-64 inline asm for its atomic publication"
#endif

// frm_cas64 is lock cmpxchg. It is written as inline asm rather than C11
// atomics because this cgo build lowers a C11 release store to a plain MOV
// (checked in the linked object), which lets a reader count a slot before its
// stamp is visible; a memory_order_seq_cst C11 store may also route through
// libatomic, which can take a lock and is not async-signal safe.
static inline int frm_cas64(_Atomic uint64_t *p, uint64_t *expected, uint64_t desired)
{
	uint8_t ok;
	__asm__ __volatile__("lock cmpxchgq %3, %1\n\tsete %0"
		: "=q"(ok), "+m"(*p), "+a"(*expected)
		: "r"(desired)
		: "cc", "memory");
	return ok;
}

// frm_load64 forces a real load. This cgo build may keep a C11 atomic load in
// a register across a lock cmpxchg retry loop, which turns the retry into a
// spin on a stale value; an asm operand can only be re-read.
static inline uint64_t frm_load64(const _Atomic uint64_t *p)
{
	uint64_t v = __atomic_load_n(p, __ATOMIC_RELAXED);
	return v;
}

// frm_xadd64 is lock xadd, the ticket counter.
static inline uint64_t frm_xadd64(_Atomic uint64_t *p, uint64_t value)
{
	__asm__ __volatile__("lock xaddq %1, %0"
		: "+m"(*p), "+r"(value)
		:
		: "cc", "memory");
	return value;
}

// frm_try_slot takes slot for claim: it moves the slot to claim's writer marker
// with one lock cmpxchg, and returns 0 when the slot already belongs to a newer
// presenter or sample.
//
// A slot holds a published sequence (always below FRM_MARKER_FLOOR) or a marker
// (always above it). It is older than claim when it holds a sequence below claim
// or a marker below frm_marker(claim); markers decrease as their claims
// increase, so that is the marker of a presenter that claimed earlier. Such a
// presenter has stalled: the sample it holds is late, and it may not come back
// and overwrite this one, because its publish only succeeds while the slot still
// holds its own marker. An empty slot is FRM_INVALID, the marker of claim 0, so
// the first presenter always takes it.
// It returns 0 when the slot may not be taken, FRM_TAKEN when it was free, and
// FRM_TOOK_OVER when it held the marker of an older claim whose presenter
// stalled: that presenter's pending mark must then be withdrawn, because it can
// no longer publish.
static int frm_try_slot(uint32_t slot, uint64_t claim)
{
	frmstats_cell *p = &frm_slot[slot];
	frmstats_cell expected = frmstats_load_cell(p);
	for (;;) {
		int older;
		int takeover = 0;
		if (expected.seq >= FRM_MARKER_FLOOR) {
			// An empty slot is the marker of claim 0, so it is older than
			// every real claim and the first presenter always takes it.
			older = expected.seq > frm_marker(claim);
			takeover = older && expected.seq != FRM_INVALID;
		} else {
			older = expected.seq < claim;
		}
		if (!older) {
			return 0;
		}
		// One atomic swap installs this claim's marker and clears the stamp,
		// so an older presenter's publish can no longer match the slot even if
		// it resumes after this point.
		if (frmstats_cas_cell(p, &expected.seq, &expected.stamp,
			    frm_marker(claim), 0)) {
			return takeover ? FRM_TOOK_OVER : FRM_TAKEN;
		}
	}
}

// frm_bump_ahead adds delta to slot's pending counter: +1 when a frame that may
// still rewrite the slot is between its claim and its publication, and
// (uint32_t)-1 when a frame has occupied its slot. The withdrawal runs once the
// frame FRM_SLOTS behind the previous frame has occupied the same slot, so a
// reader that sees no mark reads a claim count that has already passed every
// frame that could still write that slot.
static void frm_bump_ahead(uint32_t slot, uint32_t delta)
{
	// The claim counter is loaded before the ahead cell so the release store
	// that publishes this change is ordered after it. A zero count is the
	// mid-arm value, between clearing the marks and arming.
	if (frm_load64(&frm_claims) == 0) {
		return;
	}
	// delta is fixed for the whole call: decide what the next value is from n,
	// never from delta, so a retry after a concurrent change cannot flip the
	// operation and spin between the two frames that share this slot.
	if (delta == (uint32_t)-1) {
		for (;;) {
			uint64_t n = frm_load64(&frm_ahead[slot]);
			if (n == 0) {
				return;
			}
			uint64_t expected = n;
			if (frm_cas64(&frm_ahead[slot], &expected, n - 1)) {
				return;
			}
		}
	}
	for (;;) {
		uint64_t n = frm_load64(&frm_ahead[slot]);
		uint64_t expected = n;
		if (frm_cas64(&frm_ahead[slot], &expected, n + delta)) {
			return;
		}
	}
}

// frm_fill publishes stamp as the sample for claim, or drops the claim when the
// slot already holds a newer sample.
static void frm_fill(uint64_t claim, uint64_t stamp)
{
	uint32_t slot = frm_slot_for(claim);
	// Mark this frame pending before its slot marker can be seen. A takeover
	// withdraws the mark of the presenter it displaces, so that mark has to
	// exist first: if a takeover ran between taking the slot and registering,
	// its withdrawal would see nothing and this later registration would leak a
	// pending count that pins the reader's horizon forever.
	frm_bump_ahead(slot, 1);
	// A claim whose slot now belongs to a newer sample or a newer presenter is
	// late: the ring wrapped past it while it waited. It must not overwrite that
	// newer sample, so it is dropped here and counted as a lost sample; the
	// reader reports the count in overflow_samples. The count is taken instead
	// of the slot, so a snapshot that sees the claim in the claims counter also
	// sees the drop.
	int took = frm_try_slot(slot, claim);
#ifdef TIPSY_FRMSTATS_TEST_HOOKS
	if (atomic_load_explicit(&frm_test_after_slot_claim, memory_order_acquire) == claim) {
		atomic_store_explicit(&frm_test_after_slot_reached, 1, memory_order_release);
		while (atomic_load_explicit(&frm_test_after_slot_release, memory_order_acquire) == 0) {
		}
	}
#endif
	if (took == 0) {
		frm_bump_ahead(slot, (uint32_t)-1);
		frm_xadd64(&frm_dropped, 1);
		return;
	}
	if (took == FRM_TOOK_OVER) {
		// The presenter that held this slot stalled and will never publish;
		// its pending mark would otherwise hold the reader's window back.
		frm_bump_ahead(slot, (uint32_t)-1);
	}
#ifdef TIPSY_FRMSTATS_TEST_HOOKS
	if (atomic_load_explicit(&frm_test_hold_claim, memory_order_acquire) == claim) {
		atomic_store_explicit(&frm_test_hold_reached, 1, memory_order_release);
		while (atomic_load_explicit(&frm_test_release, memory_order_acquire) == 0) {
		}
	}
#endif
	// Publish sequence and stamp as one 16-byte atomic. A newer claim that
	// found this presenter stalled has already taken the slot, so the CAS sees
	// its marker or its published pair and fails: the stamp is never written
	// into a slot this claim no longer owns.
	frmstats_cell expected = frmstats_load_cell(&frm_slot[slot]);
	if (expected.seq != frm_marker(claim) ||
		!frmstats_cas_cell(&frm_slot[slot], &expected.seq, &expected.stamp,
			claim, stamp)) {
		frm_xadd64(&frm_dropped, 1);
		return;
	}
	frm_bump_ahead(slot, (uint32_t)-1);
}

// An epoch prevents a presenter paused before registration from entering a
// later capture. Sequential consistency orders registration against reset.
static int frm_enter(void)
{
	uint64_t epoch = atomic_load_explicit(&frm_armed, memory_order_seq_cst);
	if ((epoch & 1) == 0) {
		return 0;
	}
	atomic_fetch_add_explicit(&frm_inflight, 1, memory_order_seq_cst);
	if (atomic_load_explicit(&frm_armed, memory_order_seq_cst) != epoch) {
		atomic_fetch_sub_explicit(&frm_inflight, 1, memory_order_seq_cst);
		return 0;
	}
	return 1;
}

static void frm_leave(void)
{
	atomic_fetch_sub_explicit(&frm_inflight, 1, memory_order_seq_cst);
}

void tipsy_frmstats_present(uint64_t now_ns)
{
	if (!frm_enter()) return;
	atomic_fetch_add_explicit(&frm_attempts, 1, memory_order_relaxed);
	// A zero stamp means no clock was taken for this present. The frame still
	// owns a sequence, so it takes one like any other and fills it with the host
	// clock; a claim is never skipped, because sequences are the reader's
	// offsets into the ring.
	if (now_ns == 0) {
		now_ns = tipsy_frmstats_clock_ns();
		if (now_ns == 0) {
			atomic_fetch_add_explicit(&frm_contended, 1, memory_order_relaxed);
			frm_leave();
			return;
		}
	}
	frm_fill(frm_xadd64(&frm_claims, 1) + 1, now_ns);
	frm_leave();
}

#ifdef TIPSY_FRMSTATS_TEST_HOOKS
// tipsy_frmstats_test_present_numbered records one present whose stamp is the
// sequence it claims, so a reader can assert the exact value a sequence must
// hold. Tests only.
void tipsy_frmstats_test_present_numbered(void)
{
	if (!frm_enter()) return;
	atomic_fetch_add_explicit(&frm_attempts, 1, memory_order_relaxed);
	uint64_t claim = frm_xadd64(&frm_claims, 1) + 1;
	frm_fill(claim, claim);
	frm_leave();
}

// tipsy_frmstats_test_stamp_for claims sequence the way a presenter does, marks
// it pending, writes now_ns and suspends before publishing, then returns the
// address of the slot holding now_ns. Tests only.
uint64_t *tipsy_frmstats_test_stamp_for(uint64_t sequence, uint64_t now_ns)
{
	if (sequence != frm_load64(&frm_claims) + 1) {
		return NULL;
	}
	uint32_t slot = frm_slot_for(sequence);
	(void)frm_xadd64(&frm_claims, 1);
	// Register the pending mark before the slot marker is visible, for the same
	// takeover reason as frm_fill.
	frm_bump_ahead(slot, 1);
	int took = frm_try_slot(slot, sequence);
	if (took == 0) {
		frm_bump_ahead(slot, (uint32_t)-1);
		frm_xadd64(&frm_dropped, 1);
		return NULL;
	}
	if (took == FRM_TOOK_OVER) {
		frm_bump_ahead(slot, (uint32_t)-1);
	}
	// Stage the stamp without publishing the sequence. A reader still rejects
	// the cell because its seq is this claim's marker, not the sequence.
	frmstats_cell expected = frmstats_load_cell(&frm_slot[slot]);
	if (expected.seq != frm_marker(sequence) ||
		!frmstats_cas_cell(&frm_slot[slot], &expected.seq, &expected.stamp,
			frm_marker(sequence), now_ns)) {
		frm_xadd64(&frm_dropped, 1);
		return NULL;
	}
	return &frm_slot[slot].stamp;
}

// tipsy_frmstats_test_publish_stamp completes the publish that
// tipsy_frmstats_test_stamp_for suspended: the slot names sequence and the
// frame's pending mark is withdrawn, as a real presenter's fill would. Tests
// only.
void tipsy_frmstats_test_publish_stamp(uint64_t sequence)
{
	uint32_t slot = frm_slot_for(sequence);
	// The same rule as a real presenter's publish: a later claim that already
	// took this slot keeps it, and the suspended frame is counted as dropped.
	// The sequence and the staged stamp move together, so a stale publish can
	// never rewrite the newer owner's stamp.
	frmstats_cell expected = frmstats_load_cell(&frm_slot[slot]);
	if (expected.seq != frm_marker(sequence) ||
		!frmstats_cas_cell(&frm_slot[slot], &expected.seq, &expected.stamp,
			sequence, expected.stamp)) {
		// A slot that no longer holds this presenter's marker either already
		// holds a newer sample, or holds the marker of a later claim that took
		// the slot over. Either way that publisher keeps the slot; this sample
		// is lost. A failed CAS also means a newer claim took the slot between
		// the load and the CAS, so the stamp is untouched.
		frm_xadd64(&frm_dropped, 1);
		return;
	}
	frm_bump_ahead(slot, (uint32_t)-1);
}

#endif /* TIPSY_FRMSTATS_TEST_HOOKS */
