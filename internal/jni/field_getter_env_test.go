// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"runtime"
	"testing"
)

func newFieldGetterTestObject(t *testing.T, vm *VM) *Object {
	t.Helper()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	o := vm.newObjectOn(vm.envRaw, vm.classes["java/lang/Object"])
	o.fields["title"] = "field value 😀"
	o.fields["count"] = int32(7)
	return o
}

func TestFieldGetterOnExplicitAndLegacyEnvironment(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	o := newFieldGetterTestObject(t, vm)
	mainEnv := vm.Env().raw
	before := localRefsForState(vm, mainEnv, o.id)

	value, ok := vm.fieldGetterOn(mainEnv, o, "title", "()Ljava/lang/String;")
	if !ok || value == 0 {
		t.Fatal("explicit-env string field getter")
	}
	valueID := jobjectToID(uintptr(value))
	if got := vm.get(valueID); got == nil || got.str != "field value 😀" {
		t.Fatalf("explicit-env string result = %#v", got)
	}
	if got := localRefsForState(vm, mainEnv, valueID); got != 1 {
		t.Fatalf("explicit-env local refs = %d, want 1", got)
	}
	vm.deleteLocalRef(mainEnv, valueID)
	if vm.get(valueID) != nil {
		t.Fatal("explicit-env string local survived DeleteLocalRef")
	}

	legacy, ok := vm.fieldGetter(o, "title", "()Ljava/lang/String;")
	if !ok || legacy == 0 {
		t.Fatal("legacy nil-env string field getter")
	}
	legacyID := jobjectToID(uintptr(legacy))
	if got := localRefsForState(vm, mainEnv, legacyID); got != 1 {
		t.Fatalf("legacy nil-env local refs = %d, want 1", got)
	}
	vm.deleteLocalRef(mainEnv, legacyID)
	if got := localRefsForState(vm, mainEnv, o.id); got != before {
		t.Fatalf("receiver refs after field getters = %d, want %d", got, before)
	}

	if scalar, ok := vm.fieldGetterOn(mainEnv, o, "count", "()I"); !ok || uintptr(scalar) != 7 {
		t.Fatalf("scalar field getter = (%#x, %v), want (7, true)", uintptr(scalar), ok)
	}
	if missing, ok := vm.fieldGetterOn(mainEnv, o, "absent", "()Ljava/lang/String;"); ok || missing != 0 {
		t.Fatalf("missing field getter = (%#x, %v), want (0, false)", uintptr(missing), ok)
	}
}
