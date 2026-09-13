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

// NativeGLGameDidLeaveListener receives the APK's exact synchronous
// NativeGLJavaInterface.gameDidLeave() callback. The active APK installs an
// ExperienceSession-owned EngineExitJavaCallback2 when a game starts; its
// implementation finishes that session and records a successful game end.
// Host/UI policy remains with the GameActivity owner.
type NativeGLGameDidLeaveListener func()

var nativeGLGameDidLeaveListeners struct {
	mu   sync.Mutex
	next uint64
	set  map[uint64]NativeGLGameDidLeaveListener
}

// SubscribeNativeGLGameDidLeave registers one independent observer of the
// engine's gameDidLeave callback. Observers run synchronously on the JNI
// caller, matching the APK's direct Java call shape; an observer that needs
// UI-thread work must hand it to its owner loop.
//
// The returned cancellation function is idempotent and may be called from an
// observer. Dispatch snapshots observers before calling them, so cancellation
// prevents later dispatches but deliberately does not wait for a callback that
// was already copied. Session owners can pair cancellation with their own
// in-flight barrier before unloading engine or UI state.
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

// noteNativeGLGameDidLeave snapshots in registration order, then invokes
// without holding the registry lock. Registration order makes composition
// deterministic while retaining re-entrant cancellation.
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

// dispatchNativeGLJavaInterface serves only the verified non-keyboard
// NativeGLJavaInterface callback owned by this file. Text-input callbacks on
// the same class remain in dispatchTextInput. Exact class/name/signature
// matching prevents lookalike methods from acquiring exit semantics.
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
