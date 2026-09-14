// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"testing"
)

func TestJNIGlobalRefMultiplicityThroughVtable(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if vm.pushFrame(8) != 0 {
		t.Fatal("PushLocalFrame")
	}
	vm.mu.Lock()
	o := vm.newObjectOn(vm.envRaw, vm.classes["java/lang/Object"])
	vm.mu.Unlock()
	id := o.id
	if got := referenceVtableNewGlobal(vm.envRaw, id); got != id {
		t.Fatalf("first NewGlobalRef = %d, want %d", got, id)
	}
	if got := referenceVtableNewGlobal(vm.envRaw, id); got != id {
		t.Fatalf("second NewGlobalRef = %d, want %d", got, id)
	}
	vm.popFrame(0)
	vm.mu.RLock()
	refs := vm.objects[id].globalRefs
	vm.mu.RUnlock()
	if refs != 2 {
		t.Fatalf("global multiplicity after two NewGlobalRef calls = %d, want 2", refs)
	}
	referenceVtableDeleteGlobal(vm.envRaw, id)
	vm.mu.RLock()
	afterOne := vm.objects[id]
	refs = 0
	if afterOne != nil {
		refs = afterOne.globalRefs
	}
	vm.mu.RUnlock()
	if afterOne == nil || refs != 1 {
		t.Fatalf("first DeleteGlobalRef reclaimed or miscounted object: object=%p refs=%d", afterOne, refs)
	}
	referenceVtableDeleteGlobal(vm.envRaw, id)
	if vm.get(id) != nil {
		t.Fatal("final DeleteGlobalRef did not reclaim the unreferenced object")
	}
}

func TestJNIConcurrentGlobalRefsThroughVtable(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newObjectOn(vm.envRaw, vm.classes["java/lang/Object"])
	vm.mu.Unlock()
	const workers = 12
	const iterations = 250
	var work sync.WaitGroup
	errs := make(chan string, workers)
	for range workers {
		work.Add(1)
		go func() {
			defer work.Done()
			for range iterations {
				if got := referenceVtableNewGlobal(vm.envRaw, o.id); got != o.id {
					errs <- "NewGlobalRef returned the wrong handle"
					return
				}
				referenceVtableDeleteGlobal(vm.envRaw, o.id)
			}
		}()
	}
	work.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	vm.mu.RLock()
	got := vm.objects[o.id]
	refs := 0
	if got != nil {
		refs = got.globalRefs
	}
	vm.mu.RUnlock()
	if got == nil || refs != 0 {
		t.Fatalf("concurrent global refs = object=%p refs=%d, want live local with zero globals", got, refs)
	}
	vm.deleteLocal(o.id)
	if vm.get(o.id) != nil {
		t.Fatal("local cleanup did not reclaim concurrent fixture")
	}
}

func TestJNIModifiedUTF8AndUTF16RegionsThroughVtable(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	wantMUTF := []byte{'A', 0xc0, 0x80, 0xc3, 0xa9, 0xed, 0xa0, 0xbd, 0xed, 0xb8, 0x80, 'B'}
	wantUnits := []uint16{0x0041, 0x0000, 0x00e9, 0xd83d, 0xde00, 0x0042}
	id := referenceVtableNewModifiedUTF8(vm.envRaw, wantMUTF)
	if id == 0 {
		t.Fatal("NewStringUTF rejected valid Modified UTF-8")
	}
	if got := referenceVtableStringLength(vm.envRaw, id); got != len(wantUnits) {
		t.Fatalf("GetStringLength = %d, want %d UTF-16 units", got, len(wantUnits))
	}
	if got := referenceVtableModifiedUTF8(vm.envRaw, id); !sameBytes(got, wantMUTF) {
		t.Fatalf("GetStringUTFChars = %x, want %x", got, wantMUTF)
	}
	if got := referenceVtableUTF16Region(vm.envRaw, id, 1, 4, []uint16{0xfeed, 0xfeed, 0xfeed, 0xfeed}); !sameUTF16(got, wantUnits[1:5]) {
		t.Fatalf("GetStringRegion = %#x, want %#x", got, wantUnits[1:5])
	}
	if got := referenceVtableModifiedUTF8Region(vm.envRaw, id, 1, 4, 10, 0xee); !sameBytes(got, wantMUTF[1:11]) {
		t.Fatalf("GetStringUTFRegion = %x, want %x", got, wantMUTF[1:11])
	}
	if got := referenceVtableModifiedUTF8Region(vm.envRaw, id, 3, 1, 3, 0xee); !sameBytes(got, wantMUTF[5:8]) {
		t.Fatalf("GetStringUTFRegion high surrogate = %x, want %x", got, wantMUTF[5:8])
	}
}

func TestJNIStringUTF16PreservesUnpairedSurrogatesAndRejectsMalformedMUTF8(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	units := []uint16{0x0041, 0xd800, 0x0042, 0xdc00}
	id := referenceVtableNewUTF16(vm.envRaw, units)
	if id == 0 {
		t.Fatal("NewString rejected UTF-16 code units")
	}
	if got := referenceVtableUTF16Region(vm.envRaw, id, 0, len(units), make([]uint16, len(units))); !sameUTF16(got, units) {
		t.Fatalf("unpaired UTF-16 round trip = %#x, want %#x", got, units)
	}
	wantMUTF := []byte{'A', 0xed, 0xa0, 0x80, 'B', 0xed, 0xb0, 0x80}
	if got := referenceVtableModifiedUTF8(vm.envRaw, id); !sameBytes(got, wantMUTF) {
		t.Fatalf("unpaired UTF-16 Modified UTF-8 = %x, want %x", got, wantMUTF)
	}

	for name, malformed := range map[string][]byte{
		"continuation":       {0x80},
		"bad-nul":            {0xc0, 0x81},
		"truncated-two":      {0xc2},
		"overlong-three":     {0xe0, 0x80, 0x80},
		"four-byte-standard": {0xf0, 0x9f, 0x98, 0x80},
	} {
		if got := referenceVtableNewModifiedUTF8(vm.envRaw, malformed); got != 0 {
			t.Fatalf("NewStringUTF(%s) = %d, want NULL", name, got)
		}
	}
	if got := referenceVtableNewModifiedUTF8Nil(vm.envRaw); got != 0 {
		t.Fatalf("NewStringUTF(NULL) = %#x, want NULL", got)
	}
	if got := referenceVtableNewUTF16Nil(vm.envRaw, 1); got != 0 {
		t.Fatalf("NewString(NULL, 1) = %#x, want NULL", got)
	}
}

func TestJNIStringRegionsRejectOutOfBoundsWithoutWriting(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	id := referenceVtableNewModifiedUTF8(vm.envRaw, []byte{'a', 0xed, 0xa0, 0xbd, 0xed, 0xb8, 0x80})
	if id == 0 {
		t.Fatal("NewStringUTF")
	}
	initialUnits := []uint16{0xcafe, 0xcafe}
	if got := referenceVtableUTF16Region(vm.envRaw, id, -1, 1, initialUnits); !sameUTF16(got, initialUnits) {
		t.Fatalf("negative GetStringRegion wrote %#x", got)
	}
	if got := referenceVtableUTF16Region(vm.envRaw, id, 2, 2, initialUnits); !sameUTF16(got, initialUnits) {
		t.Fatalf("overrun GetStringRegion wrote %#x", got)
	}
	if got := referenceVtableModifiedUTF8Region(vm.envRaw, id, -1, 1, 4, 0xaa); !sameBytes(got, []byte{0xaa, 0xaa, 0xaa, 0xaa}) {
		t.Fatalf("negative GetStringUTFRegion wrote %x", got)
	}
	if got := referenceVtableModifiedUTF8Region(vm.envRaw, id, 2, 2, 4, 0xaa); !sameBytes(got, []byte{0xaa, 0xaa, 0xaa, 0xaa}) {
		t.Fatalf("overrun GetStringUTFRegion wrote %x", got)
	}
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
