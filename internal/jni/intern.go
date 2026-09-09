// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"sync/atomic"
	"unsafe"
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
type callHandler func(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool)

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
	methodTab   atomic.Pointer[[]*internedMethod]

	fieldMu    sync.Mutex
	fieldByKey map[internKey]C.jfieldID
	fieldTab   atomic.Pointer[[]*internedField]

	missingMethodLogged sync.Map
)

func publishMethod(info *internedMethod) uint32 {
	old := methodTab.Load()
	n := 1
	if old != nil {
		n = len(*old)
	}
	next := make([]*internedMethod, n+1)
	if old != nil {
		copy(next, *old)
	}
	next[n] = info
	methodTab.Store(&next)
	return uint32(n)
}

func publishField(info *internedField) uint32 {
	old := fieldTab.Load()
	n := 1
	if old != nil {
		n = len(*old)
	}
	next := make([]*internedField, n+1)
	if old != nil {
		copy(next, *old)
	}
	next[n] = info
	fieldTab.Store(&next)
	return uint32(n)
}

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
	info := &internedMethod{
		class:  class,
		name:   name,
		sig:    sig,
		static: static,
	}
	slot := publishMethod(info)
	(*C.TipsyMethod)(unsafe.Pointer(id)).slot = C.uint32_t(slot)
	methodByKey[key] = id
	return id, true
}

func lookupMethod(mid C.jmethodID) (*internedMethod, bool) {
	if jmethodNil(mid) {
		return nil, false
	}
	m := (*C.TipsyMethod)(unsafe.Pointer(mid))
	if m.magic != C.TIPSY_METHOD_MAGIC {
		return nil, false
	}
	slot := int(m.slot)
	tab := methodTab.Load()
	if tab == nil || slot <= 0 || slot >= len(*tab) {
		return nil, false
	}
	info := (*tab)[slot]
	return info, info != nil
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
	info := &internedField{
		class:  class,
		name:   name,
		sig:    sig,
		static: static,
	}
	slot := publishField(info)
	(*C.TipsyField)(unsafe.Pointer(id)).slot = C.uint32_t(slot)
	fieldByKey[key] = id
	return id
}

func lookupField(fid C.jfieldID) (*internedField, bool) {
	if jfieldNil(fid) {
		return nil, false
	}
	f := (*C.TipsyField)(unsafe.Pointer(fid))
	if f.magic != C.TIPSY_FIELD_MAGIC {
		return nil, false
	}
	slot := int(f.slot)
	tab := fieldTab.Load()
	if tab == nil || slot <= 0 || slot >= len(*tab) {
		return nil, false
	}
	info := (*tab)[slot]
	return info, info != nil
}

func logMissingMethodOnce(class, name, sig string) {
	key := methodLogName(class, name, sig)
	if _, dup := missingMethodLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logMissingMethod(class, name, sig)
}

func (vm *VM) internString(env unsafe.Pointer, slot **Object, value string) C.jobject {
	vm.mu.RLock()
	o := *slot
	hit := o != nil && o.str == value
	vm.mu.RUnlock()
	if hit {
		vm.addLocal(env, o.id)
		return idToJobject(o.id)
	}
	vm.mu.Lock()
	o = vm.cachedStringLocked(env, slot, value)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internFilesDirFile(env unsafe.Pointer) C.jobject {
	vm.mu.RLock()
	o := vm.immortalFilesDir
	hit := o != nil && o.str == vm.filesDir
	vm.mu.RUnlock()
	if hit {
		vm.addLocal(env, o.id)
		return idToJobject(o.id)
	}
	vm.mu.Lock()
	o = vm.cachedFileLocked(env, &vm.immortalFilesDir, vm.filesDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internFilesDirString(env unsafe.Pointer) C.jobject {
	vm.mu.RLock()
	path := vm.filesDir
	vm.mu.RUnlock()
	return vm.internString(env, &vm.immortalFilesDirStr, path)
}

func (vm *VM) internCacheDirFile(env unsafe.Pointer) C.jobject {
	vm.mu.RLock()
	o := vm.immortalCacheDir
	hit := o != nil && o.str == vm.cacheDir
	vm.mu.RUnlock()
	if hit {
		vm.addLocal(env, o.id)
		return idToJobject(o.id)
	}
	vm.mu.Lock()
	o = vm.cachedFileLocked(env, &vm.immortalCacheDir, vm.cacheDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internObbDirFile(env unsafe.Pointer) C.jobject {
	vm.mu.RLock()
	o := vm.immortalObbDir
	hit := o != nil && o.str == vm.obbDir
	vm.mu.RUnlock()
	if hit {
		vm.addLocal(env, o.id)
		return idToJobject(o.id)
	}
	vm.mu.Lock()
	o = vm.cachedFileLocked(env, &vm.immortalObbDir, vm.obbDir)
	vm.mu.Unlock()
	return idToJobject(o.id)
}

func (vm *VM) internClassObject(env unsafe.Pointer, className string) C.jobject {
	vm.mu.RLock()
	if vm.immortalServices != nil {
		if o := vm.immortalServices[className]; o != nil {
			vm.mu.RUnlock()
			vm.addLocal(env, o.id)
			return idToJobject(o.id)
		}
	}
	vm.mu.RUnlock()

	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.immortalServices == nil {
		vm.immortalServices = make(map[string]*Object)
	}
	if o := vm.immortalServices[className]; o != nil {
		vm.addLocalOnLocked(env, o.id)
		return idToJobject(o.id)
	}
	cls := vm.classes[className]
	if cls == nil {
		return jnull()
	}
	o := vm.newObjectOn(env, cls)
	o.markImmortal()
	vm.immortalServices[className] = o
	return idToJobject(o.id)
}
