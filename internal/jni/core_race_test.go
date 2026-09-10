// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"testing"
)

// These tests must be run with -race: they drive the production dispatch
// paths that read Object.fields/Object.elems concurrently with writers that
// hold vm.mu. A missing read lock aborts the process under the race
// detector's concurrent-map-access check.

func TestObjectFieldsConcurrentGetSet(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	vm.mu.Lock()
	o := vm.newObjectLocked(vm.classes["java/lang/Object"])
	id := o.id
	vm.mu.Unlock()
	env := vm.Env()
	obj := idToJobject(id)

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 1500; i++ {
				env.PutField(uintptr(obj), "name", "concurrent")
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 1500; i++ {
				// getName reads o.fields["name"] inside dispatch.
				_, _ = vm.dispatch(obj, "java/lang/Object", "getName", "()Ljava/lang/String;", nil)
			}
		}()
	}
	wg.Wait()
}

func TestObjectArrayElemsConcurrentAccess(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	vm.mu.Lock()
	initObj := vm.newObjectLocked(vm.classes["java/lang/Object"])
	item := vm.newObjectLocked(vm.classes["java/lang/Object"])
	it := vm.newObjectLocked(vm.classes["java/util/Iterator"])
	vm.mu.Unlock()

	arr := GoJNI_NewObjectArray(nil, 4, jclassNull(), idToJobject(initObj.id))
	if uintptr(arr) == 0 {
		t.Fatal("NewObjectArray")
	}
	arrID := jobjectToID(uintptr(jobjectArrayOf(arr)))
	arrHandle := jobjectArrayOf(idToJobject(arrID))
	vm.mu.Lock()
	vm.storeFieldObjLocked(it, "list", arrID)
	it.fields["index"] = int32(0)
	vm.mu.Unlock()
	itObj := idToJobject(it.id)
	itemHandle := idToJobject(item.id)

	// Constant indices keep every call site on an untyped constant, which
	// this cgo-free test file can pass without naming C.jsize.
	setters := []func(){
		func() { GoJNI_SetObjectArrayElement(nil, arrHandle, 0, itemHandle) },
		func() { GoJNI_SetObjectArrayElement(nil, arrHandle, 1, itemHandle) },
		func() { GoJNI_SetObjectArrayElement(nil, arrHandle, 2, itemHandle) },
		func() { GoJNI_SetObjectArrayElement(nil, arrHandle, 3, itemHandle) },
	}
	getters := []func(){
		func() { _ = GoJNI_GetObjectArrayElement(nil, arrHandle, 0) },
		func() { _ = GoJNI_GetObjectArrayElement(nil, arrHandle, 1) },
		func() { _ = GoJNI_GetObjectArrayElement(nil, arrHandle, 2) },
		func() { _ = GoJNI_GetObjectArrayElement(nil, arrHandle, 3) },
	}

	var wg sync.WaitGroup
	for i := range setters {
		wg.Add(3)
		go func(f func()) {
			defer wg.Done()
			for j := 0; j < 600; j++ {
				f()
			}
		}(setters[i])
		go func(f func()) {
			defer wg.Done()
			for j := 0; j < 600; j++ {
				f()
			}
		}(getters[i])
		go func() {
			defer wg.Done()
			for j := 0; j < 600; j++ {
				// GetArrayLength and get read o.elems under vm.mu; next
				// reads the iterator fields and array elems, then writes
				// the iterator index under vm.mu.
				_ = int(GoJNI_GetArrayLength(nil, jarrayOf(idToJobject(arrID))))
				_, _ = vm.dispatch(idToJobject(arrID), "java/util/ArrayList", "get", "(I)Ljava/lang/Object;", packJint(1))
				_, _ = vm.dispatch(itObj, "java/util/Iterator", "next", "()Ljava/lang/Object;", nil)
			}
		}()
	}
	wg.Wait()
}

// TestConnectivityFieldsConcurrentAccess drives the ConnectivityManager /
// NetworkInfo / NetworkCapabilities getters concurrently with writers that
// hold vm.mu, the way dispatchConnectivity sees live events. Every field read
// must take vm.mu.RLock, so a missing read lock aborts here under -race.
func TestConnectivityFieldsConcurrentAccess(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	vm.mu.Lock()
	info := vm.newNetworkInfoLocked(true)
	caps := vm.newNetworkCapabilitiesLocked(true)
	infoObj := idToJobject(info.id)
	capsObj := idToJobject(caps.id)
	vm.mu.Unlock()

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 1500; i++ {
				vm.mu.Lock()
				info.fields["connected"] = i%2 == 0
				info.fields["state"] = int64(0)
				caps.fields["capMask"] = int64(i)
				vm.mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 1500; i++ {
				_, _ = vm.dispatch(infoObj, "android/net/NetworkInfo", "isConnected", "()Z", nil)
				_, _ = vm.dispatch(infoObj, "android/net/NetworkInfo", "getState", "()Landroid/net/NetworkInfo$State;", nil)
				_, _ = vm.dispatch(capsObj, "android/net/NetworkCapabilities", "hasCapability", "(I)Z", packJint(netCapInternet))
			}
		}()
	}
	wg.Wait()
}

// TestInputEventFieldsConcurrentAccess drives dispatchInput getters
// concurrently with the vm.mu-held resets that reuse the pooled MotionEvent /
// KeyEvent objects. A missing read lock aborts here under -race.
func TestInputEventFieldsConcurrentAccess(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	vm.mu.Lock()
	motion, _ := vm.pooledMotionEventLocked(motionActionMove, 1, 2, 0, 3)
	key, _ := vm.pooledKeyEventLocked(65, true, 0, 3, 38)
	motionObj := idToJobject(motion.id)
	keyObj := idToJobject(key.id)
	vm.mu.Unlock()

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 1500; i++ {
				vm.mu.Lock()
				vm.resetMotionEventLocked(motion, motionActionMove, float32(i), float32(i), 0, int64(i))
				vm.resetKeyEventLocked(key, 65, true, 0, int64(i), 38)
				vm.mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 1500; i++ {
				_, _ = vm.dispatch(motionObj, motionEventClass, "getX", "()F", nil)
				_, _ = vm.dispatch(keyObj, keyEventClass, "getAction", "()I", nil)
				_, _ = vm.dispatch(keyObj, keyEventClass, "getKeyCode", "()I", nil)
			}
		}()
	}
	wg.Wait()
}
