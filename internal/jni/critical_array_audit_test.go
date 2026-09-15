// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"runtime"
	"sync"
	"testing"
	"unsafe"
)

func TestCriticalArrayRegistryNested(t *testing.T) {
	var r criticalArrayRegistry
	o := &Object{id: 1, bytes: make([]byte, 64)}
	p1, p2 := r.pin(o), r.pin(o)
	if p1 == nil || p1 != p2 {
		t.Fatal("expected shared direct address")
	}
	key := criticalArrayKey{array: 1, data: p1}
	if r.borrows[key].refs != 2 {
		t.Fatal("second acquisition not counted")
	}
	// Invalid identity cannot unpin or claim ownership of the pointer.
	if r.release(2, p1, false) != nil || r.borrows[key].refs != 2 {
		t.Fatal("wrong-array release")
	}
	if r.release(1, p1, false) != nil {
		t.Fatal("Go memory classified as C-owned")
	}
	runtime.GC()
	if r.borrows[key] == nil || r.borrows[key].refs != 1 {
		t.Fatal("pin released too soon")
	}
	unsafe.Slice((*byte)(p2), 64)[3] = 77
	// COMMIT is ignored for a non-copy, per JNI; this ends the second borrow.
	if r.release(1, p2, true) != nil {
		t.Fatal("Go memory classified as C-owned")
	}
	if len(r.borrows) != 0 || o.bytes[3] != 77 {
		t.Fatal("final release/write visibility")
	}
	if r.release(1, p2, false) != nil {
		t.Fatal("unknown pointer must never be freed")
	}
}

func TestCriticalArrayRegistryConcurrent(t *testing.T) {
	var r criticalArrayRegistry
	o := &Object{id: 1, bytes: make([]byte, 64)}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				p := r.pin(o)
				if p == nil {
					panic("nil pin")
				}
				r.release(1, p, false)
			}
		}()
	}
	wg.Wait()
	runtime.GC()
	if len(r.borrows) != 0 {
		t.Fatal("borrow leak")
	}
}

func TestCriticalArrayNestedJNI(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	a := testNewByteObject(vm, []byte{1, 2, 3, 4})
	p1, c1 := testPrimitiveArrayCritical(vm.envRaw, a)
	p2, c2 := testPrimitiveArrayCritical(vm.envRaw, a)
	if p1 == nil || p2 != p1 || c1 || c2 {
		t.Fatal("nested critical acquisition")
	}
	testReleasePrimitiveArrayCritical(vm.envRaw, a, p1, 0)
	runtime.GC()
	unsafe.Slice((*byte)(p2), 4)[0] = 9
	testReleasePrimitiveArrayCritical(vm.envRaw, a, p2, 0)
	if vm.get(a).bytes[0] != 9 {
		t.Fatal("lost pinned write")
	}
}

func TestCriticalArrayEmptyJNI(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	a := testNewByteObject(vm, []byte{})
	p, c := testPrimitiveArrayCritical(vm.envRaw, a)
	if p == nil || !c {
		t.Fatal("empty copied sentinel")
	}
	testReleasePrimitiveArrayCritical(vm.envRaw, a, p, 1) // JNI_COMMIT retains C copy
	key := criticalArrayKey{array: a, data: p}
	criticalArrays.mu.Lock()
	present := criticalArrays.borrows[key] != nil
	criticalArrays.mu.Unlock()
	if !present {
		t.Fatal("COMMIT freed copied sentinel")
	}
	testReleasePrimitiveArrayCritical(vm.envRaw, a, p, 2) // JNI_ABORT frees it
}
