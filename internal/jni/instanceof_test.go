// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "testing"

func TestIsInstanceOfDoesNotCreateClassLocals(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	objectClass := env.FindClass("java/lang/Object")
	if objectClass == 0 {
		t.Fatal("FindClass(java/lang/Object)")
	}
	object := env.AllocObject(objectClass)
	if object == 0 {
		t.Fatal("AllocObject(java/lang/Object)")
	}
	classID := jobjectToID(objectClass)
	before := localRefsForState(vm, env.raw, classID)
	for range 257 {
		if !testIsInstanceOf(env.raw, object, objectClass) {
			t.Fatal("Object is not an instance of Object")
		}
	}
	if after := localRefsForState(vm, env.raw, classID); after != before {
		t.Fatalf("IsInstanceOf class local refs = %d, want %d", after, before)
	}
	if !testIsInstanceOf(env.raw, 0, objectClass) {
		t.Fatal("null object is not assignable to a valid class")
	}
	if testIsInstanceOf(env.raw, 0, uintptr(1<<30)) {
		t.Fatal("null object accepted an invalid class")
	}
	stringClass := env.FindClass("java/lang/String")
	if stringClass == 0 {
		t.Fatal("FindClass(java/lang/String)")
	}
	if testIsInstanceOf(env.raw, object, stringClass) {
		t.Fatal("Object accepted as String")
	}
}
