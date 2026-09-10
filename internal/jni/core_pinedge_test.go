// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "testing"

// TestNewObjectArrayPinAccounting documents why NewObjectArray seeds one heap
// edge (and one pin) per slot: each slot is independently replaceable via
// SetObjectArrayElement, and replacement unpins exactly one edge. Collapsing
// the initial fill to a single pin would free a still-referenced target when
// one slot is overwritten.
func TestNewObjectArrayPinAccounting(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	initObj := vm.newObjectLocked(vm.classes["java/lang/Object"])
	other := vm.newObjectLocked(vm.classes["java/lang/Object"])
	vm.mu.Unlock()

	arr := GoJNI_NewObjectArray(nil, 3, jclassNull(), idToJobject(initObj.id))
	if uintptr(arr) == 0 {
		t.Fatal("NewObjectArray")
	}
	arrID := jobjectToID(uintptr(jobjectArrayOf(arr)))

	vm.mu.RLock()
	got := vm.get(initObj.id).heapRefs
	vm.mu.RUnlock()
	if got != 3 {
		t.Fatalf("initial heapRefs = %d, want one per slot (3)", got)
	}

	GoJNI_SetObjectArrayElement(nil, jobjectArrayOf(idToJobject(arrID)), 0, idToJobject(other.id))
	vm.mu.RLock()
	got = vm.get(initObj.id).heapRefs
	vm.mu.RUnlock()
	if got != 2 {
		t.Fatalf("heapRefs after one replacement = %d, want 2", got)
	}
	vm.mu.RLock()
	elems := append([]int64(nil), vm.get(arrID).elems...)
	vm.mu.RUnlock()
	if len(elems) != 3 || elems[1] != initObj.id || elems[2] != initObj.id || elems[0] != other.id {
		t.Fatalf("elems = %v", elems)
	}
}
