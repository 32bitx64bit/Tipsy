// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"sync/atomic"
)

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

// internKey identifies one JNI method or field. Roblox caches jmethodID
// values, but GetMethodID is also repeated; intern so CallA does not
// C.GoString the C name/sig on every invoke.
type internKey struct {
	class, name, sig string
	static           bool
}

// callHandler is the per-jmethodID invoke function stored on internedMethod.
// Fast-path CallA loads this atomically and skips the family chain + name+sig
// switch. A nil Load means the identity has not been bound yet.
type callHandler func(vm *VM, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool)

type internedMethod struct {
	class, name, sig string
	static           bool
	handler          atomic.Value // callHandler
}

func (m *internedMethod) loadHandler() callHandler {
	if m == nil {
		return nil
	}
	v := m.handler.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(callHandler)
	return h
}

func (m *internedMethod) storeHandler(h callHandler) {
	if m == nil || h == nil {
		return
	}
	// First binder wins; Store of a func is atomic so a lost CAS is not a torn write.
	m.handler.CompareAndSwap(nil, h)
}

type internedField struct {
	class, name, sig string
	static           bool
}

var (
	methodMu    sync.Mutex
	methodByKey map[internKey]C.jmethodID
	methodByPtr sync.Map // uintptr -> *internedMethod

	fieldMu    sync.Mutex
	fieldByKey map[internKey]C.jfieldID
	fieldByPtr sync.Map // uintptr -> *internedField

	missingMethodLogged sync.Map
)

func internMethod(class, name, sig string, static bool) (id C.jmethodID, first bool) {
	key := internKey{class: class, name: name, sig: sig, static: static}
	methodMu.Lock()
	defer methodMu.Unlock()
	if methodByKey == nil {
		methodByKey = make(map[internKey]C.jmethodID)
	}
	if existing, ok := methodByKey[key]; ok {
		return existing, false
	}
	id = allocMethod(class, name, sig, static)
	if jmethodNil(id) {
		return id, false
	}
	methodByKey[key] = id
	methodByPtr.Store(uintptr(id), &internedMethod{
		class:  class,
		name:   name,
		sig:    sig,
		static: static,
	})
	return id, true
}

func lookupMethod(mid C.jmethodID) (*internedMethod, bool) {
	if jmethodNil(mid) {
		return nil, false
	}
	v, ok := methodByPtr.Load(uintptr(mid))
	if !ok {
		return nil, false
	}
	return v.(*internedMethod), true
}

func internField(class, name, sig string, static bool) C.jfieldID {
	key := internKey{class: class, name: name, sig: sig, static: static}
	fieldMu.Lock()
	defer fieldMu.Unlock()
	if fieldByKey == nil {
		fieldByKey = make(map[internKey]C.jfieldID)
	}
	if existing, ok := fieldByKey[key]; ok {
		return existing
	}
	id := allocField(class, name, sig, static)
	if jfieldNil(id) {
		return id
	}
	fieldByKey[key] = id
	fieldByPtr.Store(uintptr(id), &internedField{
		class:  class,
		name:   name,
		sig:    sig,
		static: static,
	})
	return id
}

func lookupField(fid C.jfieldID) (*internedField, bool) {
	if jfieldNil(fid) {
		return nil, false
	}
	v, ok := fieldByPtr.Load(uintptr(fid))
	if !ok {
		return nil, false
	}
	return v.(*internedField), true
}

func logMissingMethodOnce(class, name, sig string) {
	key := methodLogName(class, name, sig)
	if _, dup := missingMethodLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logMissingMethod(class, name, sig)
}

func (vm *VM) internString(slot **Object, value string) C.jobject {
	vm.mu.Lock()
	o := vm.cachedStringLocked(slot, value)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internFilesDirFile() C.jobject {
	vm.mu.Lock()
	o := vm.cachedFileLocked(&vm.immortalFilesDir, vm.filesDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internFilesDirString() C.jobject {
	vm.mu.Lock()
	o := vm.cachedStringLocked(&vm.immortalFilesDirStr, vm.filesDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internCacheDirFile() C.jobject {
	vm.mu.Lock()
	o := vm.cachedFileLocked(&vm.immortalCacheDir, vm.cacheDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internObbDirFile() C.jobject {
	vm.mu.Lock()
	o := vm.cachedFileLocked(&vm.immortalObbDir, vm.obbDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internClassObject(className string) C.jobject {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.immortalServices == nil {
		vm.immortalServices = make(map[string]*Object)
	}
	if o := vm.immortalServices[className]; o != nil {
		vm.addLocalLocked(o.id)
		return idToJobject(o.id)
	}
	cls := vm.classes[className]
	if cls == nil {
		return jnull()
	}
	o := vm.newObjectLocked(cls)
	o.markImmortal()
	vm.immortalServices[className] = o
	return idToJobject(o.id)
}
