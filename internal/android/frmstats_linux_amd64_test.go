// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"fmt"
	"sync"
	"testing"
)

// TestFrameTimeRingSnapshotSemantics pins the ring contract used by
// internal/perf: the capacity is 8192, re-arming starts a fresh capture, and
// overflow is reported rather than hidden.
func TestFrameTimeRingSnapshotSemantics(t *testing.T) {
	if FrameTimeRingCapacity != 8192 {
		t.Fatalf("FrameTimeRingCapacity = %d, want 8192", FrameTimeRingCapacity)
	}
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	total := FrameTimeRingCapacity + 3
	for i := 0; i < total; i++ {
		RecordFrameTimePresent(uint64(i+1) * 10)
	}
	cursor, overwritten, samples := FrameTimeSnapshot(0)
	if overwritten != 3 || cursor != uint64(total) || len(samples) != FrameTimeRingCapacity {
		t.Fatalf("overwritten=%d cursor=%d len=%d, want 3/%d/%d",
			overwritten, cursor, len(samples), total, FrameTimeRingCapacity)
	}
	if samples[0] != 4*10 || samples[len(samples)-1] != uint64(total)*10 {
		t.Fatalf("bounds = %d..%d, want 40..%d", samples[0], samples[len(samples)-1], total*10)
	}
}

// TestFrameTimeRingOverwrittenIsIncreaseOnly pins R2: within one capture the
// reported lost-sample count is a high-water total. The first snapshot sees that
// the ring wrapped past its zero cursor and reports that loss. The second
// snapshot starts at the first cursor, where the wrap loss relative to that
// cursor is zero, but those samples are still gone, so the count must not fall
// back to zero.
func TestFrameTimeRingOverwrittenIsIncreaseOnly(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	total := FrameTimeRingCapacity + 37
	for i := 0; i < total; i++ {
		RecordFrameTimePresent(uint64(i+1) * 10)
	}
	cursor, first, samples := FrameTimeSnapshot(0)
	if first != 37 || cursor != uint64(total) || len(samples) != FrameTimeRingCapacity {
		t.Fatalf("first snapshot overwritten=%d cursor=%d len=%d, want 37/%d/%d",
			first, cursor, len(samples), total, FrameTimeRingCapacity)
	}
	secondCursor, second, more := FrameTimeSnapshot(cursor)
	if second < first {
		t.Fatalf("overwritten fell from %d to %d within one capture", first, second)
	}
	if secondCursor != cursor || len(more) != 0 {
		t.Fatalf("second snapshot cursor=%d len=%d, want %d/0", secondCursor, len(more), cursor)
	}
}

// TestFrameTimeRingConcurrentReaderSeesNoTornSlot writes strictly increasing
// stamps from one goroutine while another snapshots the way internal/perf does,
// polling the cursor forward. Each stamp encodes its own sequence number, so a
// reader that observed a count before the stamp was written would see a zero,
// stale, or foreign stamp; the check below catches exactly that.
func TestFrameTimeRingConcurrentReaderSeesNoTornSlot(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	const presents = 200_000

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := uint64(1); i <= presents; i++ {
			RecordFrameTimePresent(i << 20)
		}
	}()

	// readUntil drains snapshots until stop is closed and the ring is drained.
	// The snapshot returns a contiguous window ending at its cursor, so the
	// first sequence in the window is cursor-len+1; each returned stamp must be
	// exactly that sequence's stamp.
	readUntil := func(stop <-chan struct{}) error {
		var after, overwrittenSeen uint64
		for {
			cursor, overwritten, samples := FrameTimeSnapshot(after)
			if len(samples) != 0 {
				first := cursor - uint64(len(samples)) + 1
				for i, ns := range samples {
					if seq := first + uint64(i); ns != seq<<20 {
						return fmt.Errorf("sample %d = %d, want %d (torn or stale slot)", seq, ns, seq<<20)
					}
				}
				after = cursor
				overwrittenSeen = overwritten
				continue
			}
			// Nothing returned: either caught up, or the ring wrapped past the
			// cursor. out_overwritten is increase-only, so only a rise over the
			// last value this reader saw means the cursor fell behind the ring;
			// zero is not a catch-up signal after any loss.
			if overwritten > overwrittenSeen {
				overwrittenSeen = overwritten
				after = FrameTimeSequence() - uint64(FrameTimeRingCapacity)
				continue
			}
			select {
			case <-stop:
				return nil
			default:
			}
		}
	}

	stop := make(chan struct{})
	readErr := make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readErr <- readUntil(stop)
	}()
	<-writerDone
	close(stop)
	<-readerDone
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	if got := FrameTimeSequence(); got != presents {
		t.Fatalf("sequence = %d after %d presents", got, presents)
	}
}

// TestFrameTimeRingMultiwriterSequenceUnique pins the multi-writer claim itself:
// eight Go goroutines present stamped with their own identity, and the snapshot
// holds every one of them exactly once. A duplicated claim, or a slot that
// shows another sequence's stamp, fails here.
func TestFrameTimeRingMultiwriterSequenceUnique(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	const (
		writers = 8
		each    = 1_000
		total   = writers * each
	)
	if total > FrameTimeRingCapacity {
		t.Fatalf("test writes %d samples into a %d-slot ring", total, FrameTimeRingCapacity)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := uint64(0); w < writers; w++ {
		wg.Add(1)
		go func(w uint64) {
			defer wg.Done()
			<-start
			for i := uint64(0); i < each; i++ {
				RecordFrameTimePresent(1 + w*each + i)
			}
		}(w)
	}
	close(start)
	wg.Wait()
	cursor, overwritten, samples := FrameTimeSnapshot(0)
	// Presenters publish in nearly claim order, so the window may stop before a
	// frame that landed late; poll once more from the cursor to drain it.
	cursor2, _, more := FrameTimeSnapshot(cursor)
	if cursor2 > cursor {
		samples = append(samples, more...)
		cursor = cursor2
	}
	if overwritten != 0 || cursor != total || len(samples) != total {
		t.Fatalf("overwritten=%d cursor=%d len=%d, want 0/%d/%d",
			overwritten, cursor, len(samples), total, total)
	}
	seen := make(map[uint64]bool, total)
	for _, ns := range samples {
		if ns == 0 || ns > total || seen[ns] {
			t.Fatalf("stamp %d is out of range or repeated (duplicate claim)", ns)
		}
		seen[ns] = true
	}
}

// TestFrameTimeRingContendedCountsGateMisses pins the contended counter: it
// stays zero for a presenter even while the ring is busy, because presenters
// are not serialised by a gate.
func TestFrameTimeRingContendedCountsGateMisses(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	RecordFrameTimePresent(1000)
	if got := FrameTimeContended(); got != 0 {
		t.Fatalf("contended = %d after one present, want 0", got)
	}
	if got := FrameTimeAttempts(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if got := FrameTimeSequence(); got != 1 {
		t.Fatalf("sequence = %d, want 1", got)
	}
}

// TestFrameTimeRingKeepsFullWidthStamp pins that the stamp is the whole
// CLOCK_MONOTONIC value: a stamp with the top bits set is returned unchanged,
// so no tag is packed into any of its 64 bits.
func TestFrameTimeRingKeepsFullWidthStamp(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	const stamp = 0xfedcba9876543210
	RecordFrameTimePresent(stamp)
	_, _, samples := FrameTimeSnapshot(0)
	if len(samples) != 1 || samples[0] != stamp {
		t.Fatalf("samples = %v, want [%#x] (the stamp must keep all 64 bits)", samples, uint64(stamp))
	}
}
