// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func TestAuditMonitorRecursionAndOwnership(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	a := testNewByteObject(vm, []byte{1})
	// Explicit identities test the monitor bookkeeping independently of
	// native pthread attachment, which has its own integration tests.
	var tokens [2]byte
	e1, e2 := unsafe.Pointer(&tokens[0]), unsafe.Pointer(&tokens[1])
	if !vm.monitorEnter(e1, a) || !vm.monitorEnter(e1, a) {
		t.Fatal("recursive enter")
	}
	if vm.monitorExit(e2, a) {
		t.Fatal("wrong owner accepted")
	}
	if !vm.monitorExit(e1, a) || !vm.monitorExit(e1, a) {
		t.Fatal("recursive exit")
	}
	if vm.monitorExit(e1, a) {
		t.Fatal("unbalanced exit accepted")
	}
	vm.mu.RLock()
	refs := vm.objects[a].heapRefs
	vm.mu.RUnlock()
	if refs != 0 {
		t.Fatalf("monitor reference leak: %d", refs)
	}
}

func TestAuditMonitorDetachWakesWaiter(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	a := testNewByteObject(vm, []byte{1})
	var tokens [2]byte
	e1, e2 := unsafe.Pointer(&tokens[0]), unsafe.Pointer(&tokens[1])
	if !vm.monitorEnter(e1, a) || !vm.monitorEnter(e1, a) {
		t.Fatal("enter")
	}
	done := make(chan bool, 1)
	go func() {
		ok := vm.monitorEnter(e2, a)
		if ok {
			ok = vm.monitorExit(e2, a)
		}
		done <- ok
	}()
	deadline := time.Now().Add(time.Second)
	for {
		vm.mu.RLock()
		refs := vm.objects[a].heapRefs
		vm.mu.RUnlock()
		if refs == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("contender did not wait")
		}
		runtime.Gosched()
	}
	vm.detachThreadState(e1)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("waiter failed")
		}
	case <-time.After(time.Second):
		t.Fatal("detach did not wake waiter")
	}
	vm.mu.RLock()
	refs := vm.objects[a].heapRefs
	vm.mu.RUnlock()
	if refs != 0 {
		t.Fatalf("detach reference leak: %d", refs)
	}
}
