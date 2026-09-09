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
	"sync"
	"sync/atomic"
	"unsafe"
)

// localFrame is one JNI local-reference frame. refs maps object id → count
// of local refs in this frame. Field-held IDs without a remaining global or
// local ref can dangle after DeleteLocalRef / PopLocalFrame; that matches
// real JNI. This VM does not scan Object.fields (no GC).
type localFrame struct {
	refs map[int64]int
}

type jniThreadState struct {
	mu          sync.Mutex
	localFrames []localFrame
	pending     atomic.Int64
}

func (vm *VM) currentEnvKey() uintptr {
	if vm == nil {
		return 0
	}
	if current := currentEnvPtr(); current != nil {
		return uintptr(current)
	}
	return uintptr(vm.envRaw)
}

func (vm *VM) envKey(env unsafe.Pointer) uintptr {
	if env != nil {
		return uintptr(env)
	}
	return vm.currentEnvKey()
}

func (vm *VM) ensureThreadStateLocked(env unsafe.Pointer) *jniThreadState {
	if vm == nil {
		return nil
	}
	key := vm.envKey(env)
	if key == 0 {
		return nil
	}
	state := vm.threadStates[key]
	if state == nil {
		state = &jniThreadState{localFrames: []localFrame{{refs: make(map[int64]int)}}}
		vm.threadStates[key] = state
	}
	return state
}

func (vm *VM) threadState(env unsafe.Pointer) *jniThreadState {
	if vm == nil {
		return nil
	}
	key := vm.envKey(env)
	if key == 0 {
		return nil
	}
	vm.mu.RLock()
	state := vm.threadStates[key]
	vm.mu.RUnlock()
	if state != nil {
		return state
	}
	vm.mu.Lock()
	state = vm.ensureThreadStateLocked(env)
	vm.mu.Unlock()
	return state
}

func addFrameRef(state *jniThreadState, id int64) {
	if state == nil || id == 0 {
		return
	}
	if len(state.localFrames) == 0 {
		state.localFrames = []localFrame{{refs: make(map[int64]int)}}
	}
	f := &state.localFrames[len(state.localFrames)-1]
	if f.refs == nil {
		f.refs = make(map[int64]int)
	}
	f.refs[id]++
}

func dropFrameRef(state *jniThreadState, id int64) bool {
	if state == nil || id == 0 {
		return false
	}
	for i := len(state.localFrames) - 1; i >= 0; i-- {
		c := state.localFrames[i].refs[id]
		if c <= 0 {
			continue
		}
		if c == 1 {
			delete(state.localFrames[i].refs, id)
		} else {
			state.localFrames[i].refs[id] = c - 1
		}
		return true
	}
	return false
}

func (vm *VM) addLocalOnLocked(env unsafe.Pointer, id int64) {
	if vm == nil || id == 0 {
		return
	}
	o := vm.objects[id]
	if o == nil {
		return
	}
	o.localRefs.Add(1)
	state := vm.ensureThreadStateLocked(env)
	if state == nil {
		o.localRefs.Add(-1)
		return
	}
	state.mu.Lock()
	addFrameRef(state, id)
	state.mu.Unlock()
}

func (vm *VM) addLocal(env unsafe.Pointer, id int64) bool {
	if vm == nil || id == 0 {
		return false
	}
	vm.mu.RLock()
	o := vm.objects[id]
	if o == nil {
		vm.mu.RUnlock()
		return false
	}
	o.localRefs.Add(1)
	vm.mu.RUnlock()

	state := vm.threadState(env)
	if state == nil {
		o.localRefs.Add(-1)
		return false
	}
	state.mu.Lock()
	addFrameRef(state, id)
	state.mu.Unlock()
	return true
}

func (vm *VM) dropLocal(env unsafe.Pointer, id int64) {
	if vm == nil || id == 0 {
		return
	}
	state := vm.threadState(env)
	if state == nil {
		return
	}
	state.mu.Lock()
	dropped := dropFrameRef(state, id)
	state.mu.Unlock()
	if !dropped {
		return
	}
	if o := vm.get(id); o != nil {
		o.localRefs.Add(-1)
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

func (vm *VM) maybeReclaim(id int64) {
	if vm == nil || id == 0 {
		return
	}
	vm.mu.Lock()
	vm.maybeReclaimLocked(id)
	vm.mu.Unlock()
}

func (vm *VM) maybeReclaimLocked(id int64) {
	if vm == nil || id == 0 {
		return
	}
	o := vm.objects[id]
	if o == nil || o.global || o.immortal {
		return
	}
	if o.localRefs.Load() > 0 || o.pendingRefs.Load() > 0 || o.heapRefs > 0 {
		return
	}
	delete(vm.objects, id)
	vm.unpinOutgoingLocked(o)
}

func (vm *VM) pushLocalFrame(env unsafe.Pointer) {
	state := vm.threadState(env)
	if state == nil {
		return
	}
	state.mu.Lock()
	state.localFrames = append(state.localFrames, localFrame{refs: make(map[int64]int)})
	state.mu.Unlock()
}

func (vm *VM) popLocalFrame(env unsafe.Pointer, resultID int64) {
	state := vm.threadState(env)
	if state == nil {
		return
	}
	state.mu.Lock()
	if len(state.localFrames) <= 1 {
		state.mu.Unlock()
		if resultID != 0 {
			vm.addLocal(env, resultID)
		}
		return
	}
	top := state.localFrames[len(state.localFrames)-1]
	state.localFrames = state.localFrames[:len(state.localFrames)-1]
	state.mu.Unlock()

	for id, n := range top.refs {
		if o := vm.get(id); o != nil {
			o.localRefs.Add(-int32(n))
		}
	}
	if resultID != 0 {
		vm.addLocal(env, resultID)
	}
	vm.mu.Lock()
	for id := range top.refs {
		if id != resultID {
			vm.maybeReclaimLocked(id)
		}
	}
	vm.mu.Unlock()
}

func (vm *VM) detachThreadState(env unsafe.Pointer) {
	if vm == nil || env == nil {
		return
	}
	vm.mu.Lock()
	state := vm.threadStates[uintptr(env)]
	delete(vm.threadStates, uintptr(env))
	vm.mu.Unlock()
	if state == nil {
		return
	}
	state.mu.Lock()
	frames := state.localFrames
	state.localFrames = nil
	pending := state.pending.Swap(0)
	state.mu.Unlock()

	counts := make(map[int64]int)
	for _, frame := range frames {
		for id, n := range frame.refs {
			counts[id] += n
		}
	}
	vm.mu.Lock()
	for id, n := range counts {
		if o := vm.objects[id]; o != nil {
			o.localRefs.Add(-int32(n))
		}
		vm.maybeReclaimLocked(id)
	}
	if pending != 0 {
		if o := vm.objects[pending]; o != nil {
			o.pendingRefs.Add(-1)
		}
		vm.maybeReclaimLocked(pending)
	}
	vm.mu.Unlock()
}

func (vm *VM) pending(env unsafe.Pointer) int64 {
	state := vm.threadState(env)
	if state == nil {
		return 0
	}
	return state.pending.Load()
}

func (vm *VM) setPending(env unsafe.Pointer, id int64) {
	state := vm.threadState(env)
	if state == nil {
		return
	}
	old := state.pending.Swap(id)
	if old == id {
		return
	}
	if old != 0 {
		if o := vm.get(old); o != nil {
			o.pendingRefs.Add(-1)
		}
		vm.maybeReclaim(old)
	}
	if id != 0 {
		if o := vm.get(id); o != nil {
			o.pendingRefs.Add(1)
		}
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

func (vm *VM) deleteLocalRef(env unsafe.Pointer, id int64) {
	if vm == nil || id == 0 {
		return
	}
	vm.dropLocal(env, id)
	vm.maybeReclaim(id)
}

func (vm *VM) newLocalRef(env unsafe.Pointer, id int64) bool {
	return vm.addLocal(env, id)
}

//export GoJNI_PushLocalFrame
func GoJNI_PushLocalFrame(env *C.JNIEnv, capacity C.jint) C.jint {
	_ = capacity // advisory; Tipsy does not cap frame size
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return C.JNI_OK
	}
	vm.pushLocalFrame(unsafe.Pointer(env))
	return C.JNI_OK
}

//export GoJNI_PopLocalFrame
func GoJNI_PopLocalFrame(env *C.JNIEnv, result C.jobject) C.jobject {
	vm := vmFromEnv(unsafe.Pointer(env))
	if vm == nil {
		return result
	}
	vm.popLocalFrame(unsafe.Pointer(env), jobjectToID(uintptr(result)))
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
	vm.deleteLocalRef(unsafe.Pointer(env), jobjectToID(uintptr(obj)))
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
	if !vm.newLocalRef(unsafe.Pointer(env), id) {
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
