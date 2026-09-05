// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import "unsafe"

// localFrame is one JNI local-reference frame. refs maps object id → count
// of local refs in this frame. Field-held IDs without a remaining global or
// local ref can dangle after DeleteLocalRef / PopLocalFrame; that matches
// real JNI. This VM does not scan Object.fields (no GC).
type localFrame struct {
	refs map[int64]int
}

func (vm *VM) ensureFramesLocked() {
	if vm == nil {
		return
	}
	if len(vm.localFrames) == 0 {
		vm.localFrames = []localFrame{{refs: make(map[int64]int)}}
	}
}

func (vm *VM) addLocalLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	vm.ensureFramesLocked()
	f := &vm.localFrames[len(vm.localFrames)-1]
	if f.refs == nil {
		f.refs = make(map[int64]int)
	}
	f.refs[id]++
}

func (vm *VM) localCountLocked(id int64) int {
	if vm == nil || id == 0 {
		return 0
	}
	n := 0
	for i := range vm.localFrames {
		n += vm.localFrames[i].refs[id]
	}
	return n
}

// dropLocalLocked removes one local ref, searching from the current frame
// down to the env-lifetime frame 0.
func (vm *VM) dropLocalLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	for i := len(vm.localFrames) - 1; i >= 0; i-- {
		c := vm.localFrames[i].refs[id]
		if c <= 0 {
			continue
		}
		if c == 1 {
			delete(vm.localFrames[i].refs, id)
		} else {
			vm.localFrames[i].refs[id] = c - 1
		}
		return
	}
}

func (vm *VM) pinHeapLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	if o := vm.objects[id]; o != nil {
		o.heapRefs++
	}
}

func (vm *VM) unpinHeapLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	o := vm.objects[id]
	if o == nil || o.heapRefs <= 0 {
		return
	}
	o.heapRefs--
	vm.maybeReclaimLocked(id)
}

// replaceHeapEdgeLocked updates a holder→target heap edge. oldID/newID are
// jobject identities previously pinned from this holder, never primitive
// int64 field values such as networkHandle.
func (vm *VM) replaceHeapEdgeLocked(holder *Object, oldID, newID int64) {
	if vm == nil || holder == nil {
		return
	}
	if oldID != 0 && oldID != newID {
		for i, id := range holder.heapEdges {
			if id == oldID {
				holder.heapEdges = append(holder.heapEdges[:i], holder.heapEdges[i+1:]...)
				break
			}
		}
		vm.unpinHeapLocked(oldID)
	}
	if newID != 0 && oldID != newID {
		holder.heapEdges = append(holder.heapEdges, newID)
		vm.pinHeapLocked(newID)
	}
}

func (vm *VM) storeFieldObjLocked(o *Object, key string, id int64) {
	if o == nil {
		return
	}
	old, _ := o.fields[key].(int64)
	vm.replaceHeapEdgeLocked(o, old, id)
	o.fields[key] = id
}

func (vm *VM) unpinOutgoingLocked(o *Object) {
	if o == nil {
		return
	}
	edges := o.heapEdges
	o.heapEdges = nil
	for _, id := range edges {
		vm.unpinHeapLocked(id)
	}
}

func (vm *VM) maybeReclaimLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	o := vm.objects[id]
	if o == nil || o.global || o.immortal {
		return
	}
	if vm.localCountLocked(id) > 0 || o.heapRefs > 0 {
		return
	}
	delete(vm.objects, id)
	vm.unpinOutgoingLocked(o)
}

func (vm *VM) pushLocalFrameLocked() {
	vm.ensureFramesLocked()
	vm.localFrames = append(vm.localFrames, localFrame{refs: make(map[int64]int)})
}

func (vm *VM) popLocalFrameLocked(resultID int64) {
	vm.ensureFramesLocked()
	if len(vm.localFrames) <= 1 {
		// Frame 0 lives for the env. Promoting result into it is still
		// useful if native pops too far.
		if resultID != 0 {
			vm.addLocalLocked(resultID)
		}
		return
	}
	top := vm.localFrames[len(vm.localFrames)-1]
	vm.localFrames = vm.localFrames[:len(vm.localFrames)-1]
	for id := range top.refs {
		if id == resultID {
			continue
		}
		vm.maybeReclaimLocked(id)
	}
	if resultID != 0 {
		vm.addLocalLocked(resultID)
	}
}

func (vm *VM) newGlobalRefLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	o := vm.objects[id]
	if o == nil {
		return
	}
	o.global = true
}

func (vm *VM) deleteGlobalRefLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	o := vm.objects[id]
	if o == nil || o.immortal {
		return
	}
	o.global = false
	vm.maybeReclaimLocked(id)
}

func (vm *VM) deleteLocalRefLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	vm.dropLocalLocked(id)
	vm.maybeReclaimLocked(id)
}

func (vm *VM) newLocalRefLocked(id int64) bool {
	if vm == nil || id == 0 {
		return false
	}
	if vm.objects[id] == nil {
		return false
	}
	vm.addLocalLocked(id)
	return true
}

//export GoJNI_PushLocalFrame
func GoJNI_PushLocalFrame(env *C.JNIEnv, capacity C.jint) C.jint {
	_ = capacity // advisory; Tipsy does not cap frame size
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_OK
	}
	vm.mu.Lock()
	vm.pushLocalFrameLocked()
	vm.mu.Unlock()
	return C.JNI_OK
}

//export GoJNI_PopLocalFrame
func GoJNI_PopLocalFrame(env *C.JNIEnv, result C.jobject) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return result
	}
	vm.mu.Lock()
	vm.popLocalFrameLocked(jobjectToID(uintptr(result)))
	vm.mu.Unlock()
	return result
}

//export GoJNI_NewGlobalRef
func GoJNI_NewGlobalRef(env *C.JNIEnv, lobj C.jobject) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return lobj
	}
	id := jobjectToID(uintptr(lobj))
	if id == 0 {
		return jnull()
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.objects[id] == nil {
		return jnull()
	}
	vm.newGlobalRefLocked(id)
	return lobj
}

//export GoJNI_DeleteGlobalRef
func GoJNI_DeleteGlobalRef(env *C.JNIEnv, gref C.jobject) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	vm.mu.Lock()
	vm.deleteGlobalRefLocked(jobjectToID(uintptr(gref)))
	vm.mu.Unlock()
}

//export GoJNI_DeleteLocalRef
func GoJNI_DeleteLocalRef(env *C.JNIEnv, obj C.jobject) {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return
	}
	vm.mu.Lock()
	vm.deleteLocalRefLocked(jobjectToID(uintptr(obj)))
	vm.mu.Unlock()
}

//export GoJNI_NewLocalRef
func GoJNI_NewLocalRef(env *C.JNIEnv, ref C.jobject) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return ref
	}
	id := jobjectToID(uintptr(ref))
	if id == 0 {
		return jnull()
	}
	vm.mu.Lock()
	ok := vm.newLocalRefLocked(id)
	vm.mu.Unlock()
	if !ok {
		return jnull()
	}
	return ref
}

func (vm *VM) deleteLocal(id int64) {
	GoJNI_DeleteLocalRef((*C.JNIEnv)(vm.envRaw), idToJobject(id))
}

func (vm *VM) newGlobal(id int64) int64 {
	return jobjectToID(uintptr(GoJNI_NewGlobalRef((*C.JNIEnv)(vm.envRaw), idToJobject(id))))
}

func (vm *VM) deleteGlobal(id int64) {
	GoJNI_DeleteGlobalRef((*C.JNIEnv)(vm.envRaw), idToJobject(id))
}

func (vm *VM) newLocal(id int64) int64 {
	return jobjectToID(uintptr(GoJNI_NewLocalRef((*C.JNIEnv)(vm.envRaw), idToJobject(id))))
}

func (vm *VM) pushFrame(capacity int) int {
	return int(GoJNI_PushLocalFrame((*C.JNIEnv)(vm.envRaw), C.jint(capacity)))
}

func (vm *VM) popFrame(resultID int64) int64 {
	return jobjectToID(uintptr(GoJNI_PopLocalFrame((*C.JNIEnv)(vm.envRaw), idToJobject(resultID))))
}
