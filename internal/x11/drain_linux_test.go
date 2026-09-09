// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"fmt"
	"strings"
	"testing"
)

func TestDrainCapturedRawPumpCoalescesWarps(t *testing.T) {
	t.Cleanup(testClearInputRing)
	const n = 8
	if rc := testCoalesceRawPump(n); rc != 0 {
		t.Fatalf("testCoalesceRawPump(%d) = %d", n, rc)
	}
	if samples := testLastPumpRawSamples(); samples != n {
		t.Fatalf("raw samples = %d, want %d", samples, n)
	}
	if warps := testLastPumpWarps(); warps != 1 {
		t.Fatalf("warps = %d, want 1 for %d raw samples in one pump", warps, n)
	}
	w := &Window{}
	w.mu.Lock()
	evs, closed := w.drainInputLocked()
	w.mu.Unlock()
	if closed {
		t.Fatal("unexpected close")
	}
	if len(evs) != n {
		t.Fatalf("relative records = %d, want %d", len(evs), n)
	}
	for i, ev := range evs {
		if ev.Kind != InputPointer || ev.PointerAction != PointerMove || !ev.Relative ||
			ev.X != 160 || ev.Y != 90 || ev.DeltaX != 1 || ev.DeltaY != 0 {
			t.Fatalf("relative sample %d = %+v, want (dx=1,dy=0) at anchor (160,90)", i, ev)
		}
	}
}

func TestInputDrainABISizes(t *testing.T) {
	evBytes, textBytes := inputABISizes()
	if evBytes <= 0 || evBytes > 48 {
		t.Fatalf("tipsy_input_ev size = %d, want a small pointer slot (<=48)", evBytes)
	}
	if textBytes != inputTextCap {
		t.Fatalf("TIPSY_INPUT_TEXT_BYTES = %d, want %d", textBytes, inputTextCap)
	}
}

func TestDrainInputLockedInjectedPointerAndText(t *testing.T) {
	t.Cleanup(testClearInputRing)
	testClearInputRing()
	w := &Window{}
	testPushPointer(PointerDown, 1, 8, 9)
	testPushPointer(PointerMove, 0, 11, 13)
	testPushPointer(PointerUp, 1, 11, 13)
	testPushText("hi")

	w.mu.Lock()
	evs, closed := w.drainInputLocked()
	w.mu.Unlock()
	if closed {
		t.Fatal("unexpected close")
	}
	if len(evs) != 4 {
		t.Fatalf("events = %d, want 4: %+v", len(evs), evs)
	}
	if evs[0].Kind != InputPointer || evs[0].PointerAction != PointerDown || evs[0].Button != 1 || evs[0].X != 8 || evs[0].Y != 9 {
		t.Fatalf("down = %+v", evs[0])
	}
	if evs[1].Kind != InputPointer || evs[1].PointerAction != PointerMove || evs[1].X != 11 || evs[1].Y != 13 {
		t.Fatalf("move = %+v", evs[1])
	}
	if evs[2].Kind != InputPointer || evs[2].PointerAction != PointerUp || evs[2].Button != 1 {
		t.Fatalf("up = %+v", evs[2])
	}
	if evs[3].Kind != InputText || evs[3].Text != "hi" {
		t.Fatalf("text = kind=%d text=%q", evs[3].Kind, evs[3].Text)
	}
	if !testInputTextSlotsClean() {
		t.Fatal("C text side ring retained bytes after drain")
	}

	w.mu.Lock()
	empty, _ := w.drainInputLocked()
	w.mu.Unlock()
	if len(empty) != 0 {
		t.Fatalf("second drain = %+v", empty)
	}
}

func TestDrainInputLockedTextCapAndWipe(t *testing.T) {
	t.Cleanup(testClearInputRing)
	testClearInputRing()
	w := &Window{}
	testPushText(strings.Repeat("x", 300))
	w.mu.Lock()
	evs, _ := w.drainInputLocked()
	w.mu.Unlock()
	if len(evs) != 1 || evs[0].Kind != InputText || evs[0].Text != strings.Repeat("x", inputTextCap) {
		t.Fatalf("truncated text = kind=%d len=%d", evs[0].Kind, len(evs[0].Text))
	}
	if !testInputTextSlotsClean() {
		t.Fatal("truncated text lingered in the C side ring")
	}
}

func TestDrainInputLockedOverflowWipesText(t *testing.T) {
	t.Cleanup(testClearInputRing)
	testClearInputRing()
	testPushText("secret")
	for i := 0; i < TIPSYInputRingLen; i++ {
		testPushPointer(PointerDown, 1, float32(i), 0)
	}
	if !testInputTextSlotsClean() {
		t.Fatal("overflow drop left IME bytes in the side ring")
	}
	w := &Window{}
	w.mu.Lock()
	evs, _ := w.drainInputLocked()
	w.mu.Unlock()
	for _, ev := range evs {
		if ev.Kind == InputText {
			t.Fatalf("overflow-dropped text was delivered: kind=%d", ev.Kind)
		}
	}
}

func BenchmarkDrainInputLocked(b *testing.B) {
	w := &Window{}
	w.mu.Lock()
	_, _ = w.drainInputLocked()
	w.mu.Unlock()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.mu.Lock()
		_, _ = w.drainInputLocked()
		w.mu.Unlock()
	}
}

func BenchmarkDrainInputLockedPointers(b *testing.B) {
	w := &Window{}
	w.mu.Lock()
	_, _ = w.drainInputLocked()
	w.mu.Unlock()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testClearInputRing()
		testPushPointer(PointerDown, 1, 10, 20)
		testPushPointer(PointerMove, 0, 11, 21)
		testPushPointer(PointerUp, 1, 11, 21)
		w.mu.Lock()
		_, _ = w.drainInputLocked()
		w.mu.Unlock()
	}
}

func BenchmarkNotifyInput(b *testing.B) {
	cancel := OnInput(func(InputEvent) {})
	defer cancel()
	evs := []InputEvent{{Kind: InputPointer, PointerAction: PointerMove, X: 1, Y: 2}}
	notifyInput(evs)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		notifyInput(evs)
	}
}

// BenchmarkDrainInputLockedBatch is a synthetic captured-motion load: N
// non-coalescing pointer downs, then one drain. It is Tipsy ring+Go cost only
// (no X server, warp, or JNI). 1 and 8 model 1 kHz / 8 kHz samples that
// arrive in one coalesced wake; 64 and 128 are burst/backlog sizes under the
// 255-slot usable ring.
func BenchmarkDrainInputLockedBatch(b *testing.B) {
	for _, n := range []int{1, 8, 64, 128} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			benchDrainPointerBatch(b, n)
		})
	}
}

func BenchmarkDrainInputLockedText(b *testing.B) {
	w := &Window{}
	w.mu.Lock()
	_, _ = w.drainInputLocked()
	w.mu.Unlock()
	sample := strings.Repeat("x", 32)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testClearInputRing()
		testPushText(sample)
		w.mu.Lock()
		_, _ = w.drainInputLocked()
		w.mu.Unlock()
	}
}

func benchDrainPointerBatch(b *testing.B, n int) {
	b.Helper()
	w := &Window{}
	testClearInputRing()
	for j := 0; j < n; j++ {
		testPushPointer(PointerDown, 1, float32(j), 0)
	}
	w.mu.Lock()
	got, _ := w.drainInputLocked()
	w.mu.Unlock()
	if len(got) != n {
		b.Fatalf("warmup drain = %d events, want %d", len(got), n)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		testClearInputRing()
		for j := 0; j < n; j++ {
			testPushPointer(PointerDown, 1, float32(j), 0)
		}
		w.mu.Lock()
		_, _ = w.drainInputLocked()
		w.mu.Unlock()
	}
}
