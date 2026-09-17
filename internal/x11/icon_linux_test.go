// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"fmt"
	"testing"
)

func TestWindowIconNativeCachesBuffer(t *testing.T) {
	first, err := windowIconNative()
	if err != nil {
		t.Fatalf("windowIconNative: %v", err)
	}
	if len(first) < 3 {
		t.Fatalf("native icon item count = %d, want width + height + pixels", len(first))
	}
	card32, err := windowIconARGB()
	if err != nil {
		t.Fatalf("windowIconARGB: %v", err)
	}
	if len(first) != len(card32) {
		t.Fatalf("native icon len = %d, CARD32 len = %d", len(first), len(card32))
	}
	if uint32(first[0]) != card32[0] || uint32(first[1]) != card32[1] {
		t.Fatalf("native icon header = %dx%d, want %dx%d", first[0], first[1], card32[0], card32[1])
	}

	second, err := windowIconNative()
	if err != nil {
		t.Fatalf("second windowIconNative: %v", err)
	}
	if len(second) != len(first) || &second[0] != &first[0] {
		t.Fatal("windowIconNative returned a new C.ulong buffer")
	}

	var failed error
	allocs := testing.AllocsPerRun(100, func() {
		got, err := windowIconNative()
		if err != nil {
			failed = err
			return
		}
		if len(got) != len(first) || &got[0] != &first[0] {
			failed = fmt.Errorf("windowIconNative allocated a new C.ulong buffer")
		}
	})
	if failed != nil {
		t.Fatal(failed)
	}
	if allocs != 0 {
		t.Fatalf("windowIconNative allocated %.2f times per call after Once, want 0", allocs)
	}
}
