// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math"
	"testing"
	"unsafe"
)

func TestUnitRegionRejectsNegative(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		start, length, total int
		ok                   bool
	}{
		{"empty", 0, 0, 4, true},
		{"full", 0, 4, 4, true},
		{"tail", 2, 2, 4, true},
		{"negative start", -1, 2, 4, false},
		{"negative length", 0, -1, 4, false},
		{"past end", 3, 2, 4, false},
		{"start past end", 5, 0, 4, false},
	} {
		s, n, ok := unitRegion(tc.start, tc.length, tc.total)
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v want %v", tc.name, ok, tc.ok)
		}
		if ok && (s != tc.start || n != tc.length) {
			t.Fatalf("%s: (%d,%d) want (%d,%d)", tc.name, s, n, tc.start, tc.length)
		}
	}
}

func TestByteRegionRejectsNegative(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		start, length, total, es int
		ok                       bool
		off, n                   int
	}{
		{"bytes", 1, 2, 4, 1, true, 1, 2},
		{"ints", 1, 1, 8, 4, true, 4, 4},
		{"negative start", -1, 1, 4, 1, false, 0, 0},
		{"negative length", 0, -1, 4, 1, false, 0, 0},
		{"negative int length", 1, -1, 8, 4, false, 0, 0},
		{"past end", 3, 2, 4, 1, false, 0, 0},
		{"zero element size", 0, 1, 4, 0, false, 0, 0},
	} {
		off, n, ok := byteRegion(tc.start, tc.length, tc.total, tc.es)
		if ok != tc.ok || off != tc.off || n != tc.n {
			t.Fatalf("%s: (off=%d n=%d ok=%v) want (%d %d %v)",
				tc.name, off, n, ok, tc.off, tc.n, tc.ok)
		}
	}
}

func TestNewStringRejectsNegativeLength(t *testing.T) {
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	// A negative jsize used to panic in make([]uint16, n) across the
	// //export boundary; the invalid request must return NULL instead.
	if s := uintptr(GoJNI_NewString(nil, nil, -1)); s != 0 {
		t.Fatalf("NewString(len=-1) = %#x, want NULL", s)
	}
}

func TestUnitRegionRejectsOverflowingPositive(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		start, length, total int
	}{
		{"max start", math.MaxInt, 1, 4},
		{"max length", 0, math.MaxInt, 4},
		{"max start and length", math.MaxInt, math.MaxInt, 4},
		{"sum would wrap", math.MaxInt/2 + 1, math.MaxInt/2 + 1, 4},
		{"length past start", 3, math.MaxInt, 4},
	} {
		if _, _, ok := unitRegion(tc.start, tc.length, tc.total); ok {
			t.Fatalf("%s: huge positive region accepted", tc.name)
		}
	}
}

func TestByteRegionRejectsOverflowingPositive(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		start, length, total, es int
	}{
		{"max start", math.MaxInt, 1, 4, 1},
		{"max length", 0, math.MaxInt, 4, 1},
		{"max start and length", math.MaxInt, math.MaxInt, 4, 1},
		{"product would wrap", math.MaxInt/2 + 1, math.MaxInt/2 + 1, 4, 8},
		{"wide element max length", 0, math.MaxInt, math.MaxInt, 8},
	} {
		if _, _, ok := byteRegion(tc.start, tc.length, tc.total, tc.es); ok {
			t.Fatalf("%s: huge positive region accepted", tc.name)
		}
	}
	// Positive control: element counts that fit stay accepted without wrapping.
	off, n, ok := byteRegion(0, math.MaxInt/8, math.MaxInt, 8)
	if !ok || off != 0 || n < 0 || n > math.MaxInt {
		t.Fatalf("fitting wide region rejected or wrapped: off=%d n=%d ok=%v", off, n, ok)
	}
}

func TestNewStringRejectsOverflowingLength(t *testing.T) {
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	// A huge positive jsize must not reach make([]uint16, n) and force a
	// multi-gigabyte allocation across the //export boundary.
	if s := uintptr(GoJNI_NewString(nil, nil, math.MaxInt32)); s != 0 {
		t.Fatalf("NewString(len=MaxInt32) = %#x, want NULL", s)
	}
	if s := uintptr(GoJNI_NewString(nil, nil, maxGuestStringUnits+1)); s != 0 {
		t.Fatalf("NewString(len=cap+1) = %#x, want NULL", s)
	}
}

func TestRegisterNativesRejectsOverflowingCount(t *testing.T) {
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	if got := testRegisterNativesCount(0); got != 0 {
		t.Fatalf("RegisterNatives(0) = %d, want JNI_OK", got)
	}
	if got := testRegisterNativesCount(math.MaxInt32); got != -1 {
		t.Fatalf("RegisterNatives(MaxInt32) = %d, want JNI_ERR", got)
	}
	if got := testRegisterNativesCount(maxRegisteredNatives + 1); got != -1 {
		t.Fatalf("RegisterNatives(cap+1) = %d, want JNI_ERR", got)
	}
}

func TestArrayRegionRejectsNegativeLength(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	arr := testNewByteObject(vm, []byte{1, 2, 3, 4})
	arrHandle := jarrayOf(idToJobject(arr))
	var buf [4]byte

	// Negative length used to slip past the off+n check and panic in
	// unsafe.Slice / the reverse slice expression.
	GoJNI_GetArrayRegion(nil, arrHandle, 0, -1, unsafe.Pointer(&buf[0]), 'B')
	GoJNI_SetArrayRegion(nil, arrHandle, 0, -1, unsafe.Pointer(&buf[0]), 'B')
	if got := vm.get(arr).bytes[0]; got != 1 {
		t.Fatalf("invalid region call mutated array: %d", got)
	}

	// Positive control: valid regions still copy in both directions.
	GoJNI_GetArrayRegion(nil, arrHandle, 1, 2, unsafe.Pointer(&buf[0]), 'B')
	if buf[0] != 2 || buf[1] != 3 {
		t.Fatalf("GetArrayRegion control = %v", buf[:2])
	}
	buf[0] = 9
	GoJNI_SetArrayRegion(nil, arrHandle, 0, 1, unsafe.Pointer(&buf[0]), 'B')
	if got := vm.get(arr).bytes[0]; got != 9 {
		t.Fatalf("SetArrayRegion control = %d, want 9", got)
	}
}
