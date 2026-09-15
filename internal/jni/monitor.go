// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"unsafe"
)

// All monitor metadata uses VM.mu. Cond.Wait releases it while a contender
// sleeps, so the global VM lock is never held while waiting for an owner.
// env is the actual native-thread JNIEnv identity, NOT a Go goroutine ID.
type jniMonitor struct {
	owner uintptr
	depth int
	cond  *sync.Cond
}

func (vm *VM) monitorEnter(env unsafe.Pointer, id int64) bool {
	key := vm.envKey(env)
	if key == 0 || id == 0 {
		return false
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	o := vm.objects[id]
	if o == nil {
		return false
	}
	m := vm.monitors[id]
	if m == nil {
		m = &jniMonitor{cond: sync.NewCond(&vm.mu)}
		vm.monitors[id] = m
	}
	// One VM-owned edge for every successful/pending enter, including
	// recursive enters. It keeps identity alive while an owner or waiter
	// exists even when ordinary JNI references disappear.
	o.heapRefs++
	for m.owner != 0 && m.owner != key {
		m.cond.Wait()
	}
	m.owner = key
	m.depth++
	return true
}

func (vm *VM) monitorExit(env unsafe.Pointer, id int64) bool {
	key := vm.envKey(env)
	if key == 0 || id == 0 {
		return false
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	m := vm.monitors[id]
	if m == nil || m.depth == 0 || m.owner != key {
		return false
	}
	m.depth--
	if m.depth == 0 {
		m.owner = 0
		m.cond.Broadcast()
	}
	vm.unpinHeapLocked(id)
	return true
}

// DetachCurrentThread implicitly releases JNI monitors. Caller holds VM.mu.
func (vm *VM) releaseThreadMonitorsLocked(key uintptr) {
	for id, m := range vm.monitors {
		if m.owner != key || m.depth == 0 {
			continue
		}
		depth := m.depth
		m.owner, m.depth = 0, 0
		m.cond.Broadcast()
		if o := vm.objects[id]; o != nil {
			o.heapRefs -= depth
		}
		vm.maybeReclaimLocked(id)
	}
}
