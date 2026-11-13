// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && tipsy_frmstats_testhooks

package android

import (
	"testing"
	"time"
)

// TestFrameTimeRingStalledClaimCannotTearPublishedPair is the deterministic
// R1 oracle. A presenter claims sequence 1 and is suspended inside frm_fill
// after taking its slot; 8192 later presents fill the ring, and sequence 8193
// takes over slot 0 and publishes its own sample. Releasing the stalled
// presenter must not write its stamp into the slot the newer sample owns: a
// snapshot may return no sample, but every sample it does return must carry
// exactly the stamp its sequence published.
func TestFrameTimeRingStalledClaimCannotTearPublishedPair(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)

	holdFrameTimeFill(1)
	defer releaseFrameTimeFill()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		RecordFrameTimePresent(111111)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !frameTimeFillReached() {
		if time.Now().After(deadline) {
			t.Fatal("presenter was never suspended inside frm_fill")
		}
		time.Sleep(time.Millisecond)
	}

	// Claims 2..8193. Claim 8193 shares slot 0 with the stalled claim 1 and
	// publishes there, so that slot must keep stamp 8193 after the release.
	for i := 0; i < FrameTimeRingCapacity; i++ {
		recordFrameTimePresentNumbered()
	}
	cursor, _, samples := FrameTimeSnapshot(0)
	if cursor != uint64(FrameTimeRingCapacity+1) || len(samples) == 0 {
		t.Fatalf("before release: cursor=%d len=%d, want cursor %d with a retained window",
			cursor, len(samples), FrameTimeRingCapacity+1)
	}
	if last := samples[len(samples)-1]; last != uint64(FrameTimeRingCapacity+1) {
		t.Fatalf("before release: newest stamp = %d, want %d", last, FrameTimeRingCapacity+1)
	}

	releaseFrameTimeFill()
	<-writerDone

	// Re-read the newest retained sequence so a stamp written by the stalled
	// presenter after its publish is visible.
	cursor, _, samples = FrameTimeSnapshot(cursor - 1)
	for i, ns := range samples {
		seq := cursor - uint64(len(samples)) + 1 + uint64(i)
		if ns != seq {
			t.Fatalf("published pair torn: sequence %d holds stamp %d, want %d", seq, ns, seq)
		}
	}
	if len(samples) != 0 && samples[len(samples)-1] != uint64(FrameTimeRingCapacity+1) {
		t.Fatalf("newest stamp = %d, want %d (stalled claim overwrote it)",
			samples[len(samples)-1], FrameTimeRingCapacity+1)
	}
}

// TestFrameTimeRingConcurrentPresenters drives the ring from several C threads
// at once, the way more than one presenting thread reaches
// tipsy_frmstats_present. Every presenting thread takes its own sequence with
// an atomic increment, so no attempt is dropped for another presenter: the
// sample count reaches the attempt count exactly. A Go -race run cannot
// instrument C memory, so C readers assert instead that every returned stamp is
// one a presenter of this test wrote and that no accepted window is torn.
func TestFrameTimeRingConcurrentPresenters(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	const (
		writers = 6
		readers = 3
		// One present per thread takes a real clock, the rest carry their
		// attempt number.
		presentsPerThread = 20_001
		total             = writers * presentsPerThread
	)
	for run := 0; run < 3; run++ {
		SetFrameTimeArmed(true)
		if got := int(runFrameTimeStorm(writers, readers, presentsPerThread)); got != 0 {
			t.Fatalf("run %d: storm returned %d, want 0 (1 = torn or foreign stamp)", run, got)
		}
		if got := FrameTimeSequence(); got != total {
			t.Fatalf("run %d: sequence = %d, want %d (a claim was published twice or lost)",
				run, got, total)
		}
		if got := FrameTimeAttempts(); got != total {
			t.Fatalf("run %d: attempts = %d, want %d", run, got, total)
		}
		if got := FrameTimeContended(); got != 0 {
			t.Fatalf("run %d: contended = %d, want 0: concurrent presenters must not drop samples",
				run, got)
		}
		// The writers ran well past the ring length, so readers had to accept at
		// least a full ring: proof they really copied while the presenters ran.
		if seen := stormSeenFrameTimes(); seen < uint64(FrameTimeRingCapacity) {
			t.Fatalf("run %d: readers accepted %d samples, want at least %d",
				run, seen, FrameTimeRingCapacity)
		}
	}
}

// TestFrameTimeRingUnstampedPresentClaimsItsSequence pins the clock fallback
// under several presenters: a present that arrives without a stamp still takes
// its sequence and fills it with a host clock value, so no claim is skipped and
// the ring never holds a zero.
func TestFrameTimeRingUnstampedPresentClaimsItsSequence(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	const (
		writers = 4
		readers = 2
		writes  = 2_001
	)
	if got := int(runFrameTimeStormWithZeroFirst(writers, readers, writes)); got != 0 {
		t.Fatalf("storm returned %d, want 0 (1 = torn or foreign stamp)", got)
	}
	written := FrameTimeSequence()
	if written != writers*writes {
		t.Fatalf("sequence = %d, want %d", written, writers*writes)
	}
	_, overwritten, samples := FrameTimeSnapshot(0)
	if overwritten != 0 || uint64(len(samples)) != written {
		t.Fatalf("overwritten=%d len=%d, want 0/%d", overwritten, len(samples), written)
	}
	if len(samples) == 0 {
		t.Fatal("no samples after the storm")
	}
	for i, ns := range samples {
		if ns == 0 {
			t.Fatalf("sample %d is 0", i)
		}
	}
}

// TestFrameTimeRingTakeoverWithdrawsPendingMark is the R1 takeover-race oracle.
// A presenter claims sequence 1 and is suspended right after taking slot 0.
// Claim 8193 then takes that slot over and publishes there. Pending marks are
// registered before the slot becomes visible, so the takeover withdraws the
// stalled presenter's mark; a leftover mark pins the reader's horizon one slot
// behind the newest sample and the ring never returns it again.
func TestFrameTimeRingTakeoverWithdrawsPendingMark(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)

	holdFrameTimeAfterSlot(1)
	defer releaseFrameTimeAfterSlot()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		RecordFrameTimePresent(111111)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !frameTimeAfterSlotReached() {
		if time.Now().After(deadline) {
			t.Fatal("presenter was never suspended after taking its slot")
		}
		time.Sleep(time.Millisecond)
	}

	// Claims 2..8193. Claim 8193 shares slot 0 with the suspended claim 1 and
	// takes it over while claim 1 is still inside frm_fill.
	for i := 0; i < FrameTimeRingCapacity; i++ {
		recordFrameTimePresentNumbered()
	}
	releaseFrameTimeAfterSlot()
	<-writerDone

	cursor, _, samples := FrameTimeSnapshot(uint64(FrameTimeRingCapacity))
	if cursor != uint64(FrameTimeRingCapacity+1) || len(samples) != 1 {
		t.Fatalf("after takeover: cursor=%d len=%d samples=%v, want cursor %d with one sample",
			cursor, len(samples), samples, FrameTimeRingCapacity+1)
	}
	if samples[0] != uint64(FrameTimeRingCapacity+1) {
		t.Fatalf("newest stamp = %d, want %d", samples[0], FrameTimeRingCapacity+1)
	}
}
