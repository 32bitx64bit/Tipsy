// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import (
	"sort"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const gameDidLeaveSig = "()V"

// NativeGLGameDidLeaveListener receives the synchronous
// NativeGLJavaInterface.gameDidLeave() callback.
type NativeGLGameDidLeaveListener func()

var nativeGLGameDidLeaveListeners struct {
	mu   sync.Mutex
	next uint64
	set  map[uint64]NativeGLGameDidLeaveListener
}

// SubscribeNativeGLGameDidLeave registers an observer of the gameDidLeave
// callback, run synchronously on the JNI caller. Cancellation is idempotent and
// re-entrant; dispatch snapshots observers before calling them.
func SubscribeNativeGLGameDidLeave(fn NativeGLGameDidLeaveListener) func() {
	if fn == nil {
		return func() {}
	}
	nativeGLGameDidLeaveListeners.mu.Lock()
	nativeGLGameDidLeaveListeners.next++
	id := nativeGLGameDidLeaveListeners.next
	if nativeGLGameDidLeaveListeners.set == nil {
		nativeGLGameDidLeaveListeners.set = make(map[uint64]NativeGLGameDidLeaveListener)
	}
	nativeGLGameDidLeaveListeners.set[id] = fn
	nativeGLGameDidLeaveListeners.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			nativeGLGameDidLeaveListeners.mu.Lock()
			delete(nativeGLGameDidLeaveListeners.set, id)
			nativeGLGameDidLeaveListeners.mu.Unlock()
		})
	}
}

// noteNativeGLGameDidLeave snapshots observers in registration order, then
// invokes them without holding the registry lock.
func noteNativeGLGameDidLeave() {
	nativeGLGameDidLeaveListeners.mu.Lock()
	ids := make([]uint64, 0, len(nativeGLGameDidLeaveListeners.set))
	for id := range nativeGLGameDidLeaveListeners.set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	listeners := make([]NativeGLGameDidLeaveListener, 0, len(ids))
	for _, id := range ids {
		listeners = append(listeners, nativeGLGameDidLeaveListeners.set[id])
	}
	nativeGLGameDidLeaveListeners.mu.Unlock()

	for _, fn := range listeners {
		fn()
	}
}

// dispatchNativeGLJavaInterface serves only the NativeGLJavaInterface
// gameDidLeave callback; text-input callbacks stay in dispatchTextInput. Exact
// class/name/signature matching keeps lookalikes from acquiring exit semantics.
func (vm *VM) dispatchNativeGLJavaInterface(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	_ = vm
	_ = args
	if class != nativeGLClass || name != "gameDidLeave" || sig != gameDidLeaveSig {
		return jnull(), false
	}
	logging.Logger(logging.CatJNI).Info("[jni] gameDidLeave")
	noteNativeGLGameDidLeave()
	if o != nil {
		return idToJobject(o.id), true
	}
	return jnull(), true
}
