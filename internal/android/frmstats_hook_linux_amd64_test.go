// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && tipsy_frmstats_testhooks

package android

import (
	"runtime"
	"testing"
	"time"
)

// TestFrameTimeRingRejectsUnpublishedStamp pins the publication order: a
// presenter that has taken its sequence and written its stamp but not published
// is never returned, and no frame behind it is counted in its place. Once the
// publish completes, the window resumes and every frame appears in order.
func TestFrameTimeRingRejectsUnpublishedStamp(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	RecordFrameTimePresent(1000)
	if slot := stampFrameTimeWithoutPublishing(2, 2000); slot == nil {
		t.Fatal("test stamp helper refused sample 2")
	}
	RecordFrameTimePresent(3000)
	if got := FrameTimeSequence(); got != 3 {
		t.Fatalf("sequence = %d, want 3 (every present owns a sequence)", got)
	}
	if cursor, _, samples := FrameTimeSnapshot(0); cursor != 1 || len(samples) != 1 || samples[0] != 1000 {
		t.Fatalf("cursor=%d samples=%v, want 1/[1000] while sample 2 is unpublished",
			cursor, samples)
	}
	publishFrameTimeStamp(2)
	cursor, overwritten, samples := FrameTimeSnapshot(1)
	if overwritten != 0 || cursor != 3 || len(samples) != 2 ||
		samples[0] != 2000 || samples[1] != 3000 {
		t.Fatalf("after publish cursor=%d overwritten=%d samples=%v, want 3/0/[2000 3000]",
			cursor, overwritten, samples)
	}
}

// TestFrameTimeRingLateClaimDoesNotOverwriteNewerSlot pins the wrap rule: a
// presenter that claimed a sequence, stalled, and publishes after the ring has
// wrapped must not replace the newer sample that now owns its slot. It is
// dropped and reported in the overflow count instead.
//
// The stalled presenter holds claim 1, which shares a slot with sequence
// capacity+1. Presenting capacity times fills the ring and publishes sequence
// capacity+1 into that slot; publishing claim 1 afterwards must leave it alone,
// so the whole retained window is still returned and the newest sample survives.
func TestFrameTimeRingLateClaimDoesNotOverwriteNewerSlot(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	if slot := stampFrameTimeWithoutPublishing(1, 1000); slot == nil {
		t.Fatal("test stamp helper refused sample 1")
	}
	const newest = uint64(FrameTimeRingCapacity + 1)
	for seq := uint64(2); seq <= newest; seq++ {
		RecordFrameTimePresent(seq * 10)
	}
	publishFrameTimeStamp(1)
	if got := frameTimeDropped(); got != 1 {
		t.Fatalf("dropped = %d, want 1 (the late claim must be counted)", got)
	}
	cursor, overwritten, samples := FrameTimeSnapshot(0)
	// One sample is lost to the wrap (sequence 1 is never returned) and one to
	// the drop that replaced it, which the reader reports in the same count.
	if overwritten != 2 {
		t.Fatalf("overwritten = %d, want 2 (one wrap, one drop)", overwritten)
	}
	if cursor != newest || len(samples) != FrameTimeRingCapacity {
		t.Fatalf("cursor=%d len=%d, want %d/%d", cursor, len(samples), newest, FrameTimeRingCapacity)
	}
	first := cursor - uint64(len(samples)) + 1
	for i, ns := range samples {
		seq := first + uint64(i)
		if ns != seq*10 {
			t.Fatalf("sample %d = %d, want %d (a late claim replaced it)", seq, ns, seq*10)
		}
	}
	if last := samples[len(samples)-1]; last != newest*10 {
		t.Fatalf("newest sample = %d, want %d", last, newest*10)
	}
}

func TestFrameTimeRingResetWaitsForPresenters(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	for iteration := 0; iteration < 20; iteration++ {
		SetFrameTimeArmed(true)
		holdFrameTimeFill(1)
		presentDone := make(chan struct{})
		go func() {
			RecordFrameTimePresent(111)
			close(presentDone)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for !frameTimeFillReached() {
			if time.Now().After(deadline) {
				releaseFrameTimeFill()
				<-presentDone
				t.Fatal("presenter did not reach test hook")
			}
			runtime.Gosched()
		}
		armed := make(chan struct{})
		go func() {
			SetFrameTimeArmed(true)
			close(armed)
		}()
		select {
		case <-armed:
			releaseFrameTimeFill()
			<-presentDone
			t.Fatal("re-arm returned while presenter held a cell")
		case <-time.After(10 * time.Millisecond):
		}
		releaseFrameTimeFill()
		<-presentDone
		select {
		case <-armed:
		case <-time.After(5 * time.Second):
			t.Fatal("re-arm did not finish after presenter released")
		}
		// Disable the hold before the first present in the new generation.
		holdFrameTimeFill(0)
		RecordFrameTimePresent(222)
		cursor, lost, samples := FrameTimeSnapshot(0)
		if cursor != 1 || lost != 0 || len(samples) != 1 || samples[0] != 222 {
			t.Fatalf("iteration %d: cursor=%d lost=%d samples=%v", iteration, cursor, lost, samples)
		}
	}
}

func TestFrameTimeRingRepeatedOverruns(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	var cursor uint64
	for poll := 1; poll <= 2; poll++ {
		for i := 0; i < FrameTimeRingCapacity+3; i++ {
			RecordFrameTimePresent(uint64(i + 1))
		}
		var lost uint64
		var samples []uint64
		cursor, lost, samples = FrameTimeSnapshot(cursor)
		if lost != uint64(poll*3) || len(samples) != FrameTimeRingCapacity {
			t.Fatalf("poll %d: lost=%d len=%d, want %d/%d", poll, lost, len(samples), poll*3, FrameTimeRingCapacity)
		}
		_, repeated, _ := FrameTimeSnapshot(0)
		if repeated != lost {
			t.Fatalf("repeated old cursor counted losses again: %d != %d", repeated, lost)
		}
	}
}
