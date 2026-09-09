// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"runtime"
	"testing"
	"unsafe"

	_ "github.com/tipsy-linux/tipsy/internal/loader"
)

const (
	testJNIOK        = int32(0)
	testJNIDetached  = int32(-2)
	testJNIEVersion  = int32(-3)
	testJNIVersion11 = int32(0x00010001)
	testJNIVersion16 = int32(0x00010006)
)

func TestJNIThreadAttachGetEnvDetachLifecycle(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mainEnv := vm.Env().raw
	if mainEnv == nil {
		t.Fatal("C Main JNIEnv is nil")
	}
	if got := envFunctionsForTest(mainEnv); got != vm.NativeInterface() {
		t.Fatalf("initial JNIEnv functions = %#x, want immutable native table %#x", got, vm.NativeInterface())
	}
	if !nativeMainEnvIsForTest(mainEnv) {
		t.Fatal("C Main was not attached to the VM before guest calls")
	}
	_ = detachCurrentThreadForTest()
	if rc, env := getEnvForTest(testJNIVersion16); rc != testJNIDetached || env != nil {
		t.Fatalf("GetEnv on unattached native thread = (%d, %p), want (%d, nil)", rc, env, testJNIDetached)
	}
	if rc, env := attachCurrentThreadForTest(false); rc != testJNIOK || env == nil || env == mainEnv {
		t.Fatalf("ordinary Attach = (%d, %p), C Main env %p", rc, env, mainEnv)
	}
	_, attachedEnv := getEnvForTest(testJNIVersion16)
	if rc, env := attachCurrentThreadForTest(true); rc != testJNIOK || env != attachedEnv {
		t.Fatalf("repeated daemon Attach = (%d, %p), want stable (%d, %p)", rc, env, testJNIOK, attachedEnv)
	}
	if rc := detachCurrentThreadForTest(); rc != testJNIOK {
		t.Fatalf("DetachCurrentThread = %d", rc)
	}
	if rc, env := getEnvForTest(testJNIVersion16); rc != testJNIDetached || env != nil {
		t.Fatalf("GetEnv after detach = (%d, %p), want (%d, nil)", rc, env, testJNIDetached)
	}
	if rc, env := getEnvForTest(0x00010008); rc != testJNIEVersion || env != nil {
		t.Fatalf("GetEnv unsupported version = (%d, %p), want (%d, nil)", rc, env, testJNIEVersion)
	}
	if rc := detachCurrentThreadForTest(); rc != testJNIOK {
		t.Fatalf("detach while unattached = %d, want no-op success", rc)
	}
	if rc, env := attachCurrentThreadForTest(true); rc != testJNIOK || env == nil || env == mainEnv {
		t.Fatalf("daemon Attach after detach = (%d, %p)", rc, env)
	}
	defer detachCurrentThreadForTest()
	if rc, first := attachCurrentThreadForTest(false); rc != testJNIOK || first == nil {
		t.Fatalf("repeated ordinary Attach = (%d, %p)", rc, first)
	} else if rc, second := getEnvForTest(testJNIVersion11); rc != testJNIOK || second != first {
		t.Fatalf("GetEnv identity after repeated Attach = (%d, %p), want (%d, %p)", rc, second, testJNIOK, first)
	}
}

type peerThreadResult struct {
	rc         int32
	before     unsafe.Pointer
	after      unsafe.Pointer
	functions  uintptr
	detachRC   int32
	stateAdded bool
}

func TestJNIEnvironmentsAreThreadOwned(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mainEnv := vm.Env().raw
	_ = detachCurrentThreadForTest()
	rc, controllerEnv := attachCurrentThreadForTest(false)
	if rc != testJNIOK || controllerEnv == nil || controllerEnv == mainEnv {
		t.Fatalf("controller Attach = (%d, %p), C Main env %p", rc, controllerEnv, mainEnv)
	}
	defer detachCurrentThreadForTest()
	peerReady := make(chan peerThreadResult, 1)
	releasePeer := make(chan struct{})
	peerDone := make(chan peerThreadResult, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_ = detachCurrentThreadForTest()
		beforeRC, before := getEnvForTest(testJNIVersion16)
		rc, peerEnv := attachCurrentThreadForTest(false)
		if rc == testJNIOK && peerEnv != nil {
			(&Env{vm: vm, raw: peerEnv}).FindClass("java/lang/Object")
		}
		vm.mu.RLock()
		_, stateAdded := vm.threadStates[uintptr(peerEnv)]
		vm.mu.RUnlock()
		result := peerThreadResult{
			rc:         rc,
			before:     before,
			after:      peerEnv,
			functions:  envFunctionsForTest(peerEnv),
			stateAdded: stateAdded,
		}
		if beforeRC != testJNIDetached {
			result.rc = beforeRC
		}
		peerReady <- result
		<-releasePeer
		result.detachRC = detachCurrentThreadForTest()
		peerDone <- result
	}()

	peer := <-peerReady
	if peer.rc != testJNIOK || peer.before != nil || peer.after == nil {
		close(releasePeer)
		<-peerDone
		t.Fatalf("peer attach lifecycle = %+v", peer)
	}
	if peer.after == mainEnv {
		close(releasePeer)
		<-peerDone
		t.Fatalf("two attached pthreads shared JNIEnv %p", mainEnv)
	}
	if peer.functions != vm.NativeInterface() {
		close(releasePeer)
		<-peerDone
		t.Fatalf("peer functions = %#x, want %#x", peer.functions, vm.NativeInterface())
	}
	if !peer.stateAdded {
		close(releasePeer)
		<-peerDone
		t.Fatal("peer JNI callback did not create peer-owned local state")
	}

	close(releasePeer)
	peer = <-peerDone
	if peer.detachRC != testJNIOK {
		t.Fatalf("peer detach = %d", peer.detachRC)
	}
	vm.mu.RLock()
	_, peerStateRemains := vm.threadStates[uintptr(peer.after)]
	_, mainStateRemains := vm.threadStates[uintptr(mainEnv)]
	vm.mu.RUnlock()
	if peerStateRemains {
		t.Fatal("peer local/exception state survived DetachCurrentThread")
	}
	if !mainStateRemains {
		t.Fatal("peer DetachCurrentThread removed C Main state")
	}
	if rc, env := getEnvForTest(testJNIVersion16); rc != testJNIOK || env != controllerEnv {
		t.Fatalf("controller GetEnv after peer detach = (%d, %p), want (%d, %p)", rc, env, testJNIOK, controllerEnv)
	}
}

func TestJNIThreadExitDestructorReleasesEnvironment(t *testing.T) {
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	before := attachedThreadCountForTest()
	if rc := attachAndExitThreadForTest(); rc != testJNIOK {
		t.Fatalf("native pthread AttachCurrentThread = %d", rc)
	}
	if after := attachedThreadCountForTest(); after != before {
		t.Fatalf("attached environment count after pthread exit = %d, want %d", after, before)
	}
}

func TestPendingExceptionRootsAndReturnsLocalReference(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	_ = detachCurrentThreadForTest()
	rc, env := attachCurrentThreadForTest(false)
	if rc != testJNIOK || env == nil {
		t.Fatalf("AttachCurrentThread = (%d, %p)", rc, env)
	}
	defer detachCurrentThreadForTest()

	if testPushFrame(env, 8) != 0 {
		t.Fatal("PushLocalFrame")
	}
	throwable := testNewStringOn(env, "pending")
	if rc := throwForTest(env, throwable); rc != testJNIOK {
		t.Fatalf("Throw = %d", rc)
	}
	testPopFrame(env, 0)
	if vm.get(int64(throwable)) == nil {
		t.Fatal("PopLocalFrame reclaimed the pending exception")
	}
	returned := exceptionOccurredForTest(env)
	if returned != throwable {
		t.Fatalf("ExceptionOccurred = %#x, want %#x", returned, throwable)
	}
	exceptionClearForTest(env)
	if vm.get(int64(throwable)) == nil {
		t.Fatal("ExceptionClear reclaimed the local reference returned by ExceptionOccurred")
	}
	testDeleteLocalRef(env, int64(returned))
	if vm.get(int64(throwable)) != nil {
		t.Fatal("throwable survived clearing pending state and deleting its last local ref")
	}

	second := testNewStringOn(env, "pending until clear")
	if rc := throwForTest(env, second); rc != testJNIOK {
		t.Fatalf("second Throw = %d", rc)
	}
	testDeleteLocalRef(env, int64(second))
	if vm.get(int64(second)) == nil {
		t.Fatal("DeleteLocalRef reclaimed the pending exception")
	}
	exceptionClearForTest(env)
	if vm.get(int64(second)) != nil {
		t.Fatal("ExceptionClear did not reclaim a pending-only throwable")
	}
}

func TestPendingExceptionsAreThreadLocalAndDetachReclaims(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	_ = detachCurrentThreadForTest()
	rc, controllerEnv := attachCurrentThreadForTest(false)
	if rc != testJNIOK || controllerEnv == nil {
		t.Fatalf("controller Attach = (%d, %p)", rc, controllerEnv)
	}
	defer detachCurrentThreadForTest()
	controllerThrowable := testNewStringOn(controllerEnv, "controller pending")
	if rc := throwForTest(controllerEnv, controllerThrowable); rc != testJNIOK {
		t.Fatalf("controller Throw = %d", rc)
	}
	testDeleteLocalRef(controllerEnv, int64(controllerThrowable))

	type pendingPeer struct {
		env       unsafe.Pointer
		throwable uintptr
		err       int32
	}
	ready := make(chan pendingPeer, 1)
	release := make(chan struct{})
	done := make(chan pendingPeer, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_ = detachCurrentThreadForTest()
		rc, env := attachCurrentThreadForTest(false)
		result := pendingPeer{env: env, err: rc}
		if rc == testJNIOK {
			result.throwable = testNewStringOn(env, "peer pending")
			result.err = throwForTest(env, result.throwable)
			testDeleteLocalRef(env, int64(result.throwable))
		}
		ready <- result
		<-release
		result.err = detachCurrentThreadForTest()
		done <- result
	}()

	peer := <-ready
	if peer.err != testJNIOK || peer.env == nil || peer.throwable == 0 {
		close(release)
		<-done
		t.Fatalf("peer pending setup = %+v", peer)
	}
	if got := exceptionOccurredForTest(controllerEnv); got != controllerThrowable {
		close(release)
		<-done
		t.Fatalf("controller observed peer exception %#x, want own %#x", got, controllerThrowable)
	}
	testDeleteLocalRef(controllerEnv, int64(controllerThrowable)) // ExceptionOccurred's new local.
	close(release)
	peer = <-done
	if peer.err != testJNIOK {
		t.Fatalf("peer detach = %d", peer.err)
	}
	if vm.get(int64(peer.throwable)) != nil {
		t.Fatal("peer pending-only throwable survived peer detach")
	}
	if vm.get(int64(controllerThrowable)) == nil {
		t.Fatal("peer detach reclaimed controller pending throwable")
	}
	exceptionClearForTest(controllerEnv)
	if vm.get(int64(controllerThrowable)) != nil {
		t.Fatal("controller throwable survived final ExceptionClear")
	}
}

func localRefsForState(vm *VM, env unsafe.Pointer, id int64) int {
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	state := vm.threadStates[uintptr(env)]
	if state == nil {
		return 0
	}
	n := 0
	for _, frame := range state.localFrames {
		n += frame.refs[id]
	}
	return n
}

func TestMainEnvironmentHostHelperRunsOnOwnerThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mainEnv := vm.Env().raw
	_ = detachCurrentThreadForTest()
	rc, controllerEnv := attachCurrentThreadForTest(false)
	if rc != testJNIOK || controllerEnv == nil || controllerEnv == mainEnv {
		t.Fatalf("controller Attach = (%d, %p), C Main env %p", rc, controllerEnv, mainEnv)
	}
	defer detachCurrentThreadForTest()

	vm.mu.RLock()
	classID := vm.classes["java/lang/Object"].obj.id
	vm.mu.RUnlock()
	mainBefore := localRefsForState(vm, mainEnv, classID)
	controllerBefore := localRefsForState(vm, controllerEnv, classID)
	if rc := wrapNativeMainForTest(true); rc != testJNIOK {
		t.Fatalf("install test JNIEnv wrapper = %d", rc)
	}
	defer wrapNativeMainForTest(false)
	if got := vm.Env().FindClass("java/lang/Object"); got == 0 {
		t.Fatal("host FindClass through C Main env failed")
	}
	if calls, ownerOK := wrappedFindCallsForTest(); calls != 1 || !ownerOK {
		t.Fatalf("wrapped FindClass calls = %d, owner thread correct = %v", calls, ownerOK)
	}
	if got := localRefsForState(vm, mainEnv, classID); got != mainBefore+1 {
		t.Fatalf("C Main local refs = %d, want %d", got, mainBefore+1)
	}
	if got := localRefsForState(vm, controllerEnv, classID); got != controllerBefore {
		t.Fatalf("controller local refs changed to %d, want %d", got, controllerBefore)
	}
}

func TestConcurrentAttachedThreadLocalAndExceptionLifecycle(t *testing.T) {
	const (
		workers    = 2
		iterations = 128
	)

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mainEnv := vm.Env().raw
	type stressResult struct {
		env       unsafe.Pointer
		pendingID uintptr
		err       string
	}
	ready := make(chan struct{}, workers)
	start := make(chan struct{})
	staged := make(chan stressResult, workers)
	detach := make(chan struct{})
	done := make(chan stressResult, workers)

	for range workers {
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			_ = detachCurrentThreadForTest()
			rc, env := attachCurrentThreadForTest(false)
			result := stressResult{env: env}
			ready <- struct{}{}
			<-start
			if rc != testJNIOK || env == nil {
				result.err = "attach failed"
				staged <- result
				<-detach
				done <- result
				return
			}
			e := &Env{vm: vm, raw: env}
			for range iterations {
				testPushFrame(env, 8)
				obj := e.NewStringUTF("thread lifecycle sentinel")
				if obj == 0 || throwForTest(env, obj) != testJNIOK || exceptionOccurredForTest(env) != obj {
					result.err = "JNI callback lifecycle failed"
					break
				}
				exceptionClearForTest(env)
				testPopFrame(env, 0)
				if vm.get(int64(obj)) != nil {
					result.err = "local reference survived PopLocalFrame"
					break
				}
			}
			if result.err == "" {
				result.pendingID = e.NewStringUTF("detach sentinel")
				if result.pendingID == 0 || throwForTest(env, result.pendingID) != testJNIOK {
					result.err = "pending detach setup failed"
				} else {
					testDeleteLocalRef(env, int64(result.pendingID))
				}
			}
			staged <- result
			<-detach
			if rc := detachCurrentThreadForTest(); rc != testJNIOK && result.err == "" {
				result.err = "detach failed"
			}
			done <- result
		}()
	}
	for range workers {
		<-ready
	}
	close(start)
	results := make([]stressResult, 0, workers)
	for range workers {
		result := <-staged
		results = append(results, result)
	}
	for _, result := range results {
		if result.err != "" {
			close(detach)
			for range workers {
				<-done
			}
			t.Fatal(result.err)
		}
	}
	if results[0].env == results[1].env || results[0].pendingID == results[1].pendingID {
		close(detach)
		for range workers {
			<-done
		}
		t.Fatalf("concurrent workers shared state: %+v", results)
	}
	for _, result := range results {
		vm.mu.RLock()
		state := vm.threadStates[uintptr(result.env)]
		pending := uintptr(0)
		if state != nil {
			pending = uintptr(state.pending.Load())
		}
		vm.mu.RUnlock()
		if state == nil || pending != result.pendingID || vm.get(int64(result.pendingID)) == nil {
			close(detach)
			for range workers {
				<-done
			}
			t.Fatalf("worker pending state not isolated before detach: %+v", result)
		}
	}
	close(detach)
	for range workers {
		result := <-done
		if result.err != "" {
			t.Fatal(result.err)
		}
		vm.mu.RLock()
		_, remains := vm.threadStates[uintptr(result.env)]
		_, mainRemains := vm.threadStates[uintptr(mainEnv)]
		vm.mu.RUnlock()
		if remains || !mainRemains || vm.get(int64(result.pendingID)) != nil {
			t.Fatalf("detach cleanup failed: state=%v main=%v object=%v", remains, mainRemains, vm.get(int64(result.pendingID)) != nil)
		}
	}
}
