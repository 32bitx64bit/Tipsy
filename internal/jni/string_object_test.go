// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "testing"

func TestNewStringDefersFieldsUntilWrite(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	str := vm.newStringOn(vm.envRaw, "lazy fields")
	if str.fields != nil {
		vm.mu.Unlock()
		t.Fatal("new string eagerly allocated fields")
	}
	target := vm.newObjectOn(vm.envRaw, vm.classes["java/lang/Object"])
	vm.storeFieldObjLocked(str, "target", target.id)
	if got := str.fields["target"]; got != target.id {
		vm.mu.Unlock()
		t.Fatalf("lazy string object field = %v, want %d", got, target.id)
	}
	vm.mu.Unlock()

	vm.Env().PutField(uintptr(idToJobject(str.id)), "label", "written through Env")
	vm.mu.RLock()
	got := str.fields["label"]
	vm.mu.RUnlock()
	if got != "written through Env" {
		t.Fatalf("Env.PutField string field = %v", got)
	}
}
