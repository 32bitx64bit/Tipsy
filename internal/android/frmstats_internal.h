/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

// Private interface between the frame-time ring's two translation units.
//
// frmstats_ring.c owns the present path (armed flag, claim counter, slot
// publication, ahead marks, stats) and defines tipsy_frmstats_clock_ns.
// ring_state.c owns the publication cursor, the reader, arming and the
// test-only helpers, so a reader never touches a cell the present path writes
// to update a stat. Not included by any Go file; frmstats.h is the public
// header.

#ifndef TIPSY_FRMSTATS_INTERNAL_H
#define TIPSY_FRMSTATS_INTERNAL_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// tipsy_frmstats_clock_ns returns the host CLOCK_MONOTONIC value in
// nanoseconds.
uint64_t tipsy_frmstats_clock_ns(void);

// frmstats_cell is one ring slot. seq is the published sequence or a writer
// marker; stamp is the present-return clock value. Both words are published
// and read as one 16-byte atomic (lock cmpxchg16b), so a reader can never pair
// one presenter's sequence with another presenter's stamp.
typedef struct {
	uint64_t seq;
	uint64_t stamp;
} frmstats_cell;

#if defined(__x86_64__)
// frmstats_cas_cell is one locked 16-byte compare-and-swap. On failure it
// writes the current cell into the expected pair, so a caller can retry
// without a separate load. A locked RMW is a full barrier: it orders the
// stamp store that precedes it with the sequence that publishes it, and it
// orders the sequence load that opens a reader's copy with the stamp that
// follows it.
static inline int frmstats_cas_cell(frmstats_cell *p, uint64_t *expected_seq,
	uint64_t *expected_stamp, uint64_t new_seq, uint64_t new_stamp)
{
	uint8_t ok;
	__asm__ __volatile__("lock cmpxchg16b %1\n\tsete %0"
		: "=q"(ok), "+m"(*p), "+a"(*expected_seq), "+d"(*expected_stamp)
		: "b"(new_seq), "c"(new_stamp)
		: "cc", "memory");
	return ok;
}

// frmstats_load_cell returns one atomic copy of a cell. cmpxchg16b is the
// architectural 16-byte read; using it (with an unchanged desired value) keeps
// every slot access one locked instruction and never takes a library lock.
static inline frmstats_cell frmstats_load_cell(frmstats_cell *p)
{
	uint64_t seq = 0;
	uint64_t stamp = 0;
	(void)frmstats_cas_cell(p, &seq, &stamp, 0, 0);
	frmstats_cell cell = {seq, stamp};
	return cell;
}
#endif

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_FRMSTATS_INTERNAL_H */
