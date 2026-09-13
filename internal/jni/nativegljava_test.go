// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// TestNativeGLGameDidLeaveExactSynchronousFanout drives the production
// dispatch-or-stub path used by the engine's static CallVoidMethod. It pins
// exact identity, synchronous registration order, listener coexistence, and
// the absence of a fallback stub.
func TestNativeGLGameDidLeaveExactSynchronousFanout(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeGLClass)
	receiver := vm.newObjectLocked(cls)
	vm.mu.Unlock()

	var (
		mu    sync.Mutex
		order = []string{"before"}
	)
	appendEvent := func(event string) {
		mu.Lock()
		order = append(order, event)
		mu.Unlock()
	}
	cancelFirst := SubscribeNativeGLGameDidLeave(func() { appendEvent("first") })
	cancelSecond := SubscribeNativeGLGameDidLeave(func() { appendEvent("second") })
	t.Cleanup(cancelFirst)
	t.Cleanup(cancelSecond)

	v, handled := callDispatchOrStub(vm, idToJobject(receiver.id), nativeGLClass, "gameDidLeave", gameDidLeaveSig, nil, 'V')
	appendEvent("after")
	if !handled {
		t.Fatal("gameDidLeave()V fell through to the JNI stub")
	}
	if uintptr(v) != uintptr(idToJobject(receiver.id)) {
		t.Fatalf("void dispatch return = %#x, want receiver", uintptr(v))
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"before", "first", "second", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatch order = %v, want %v", got, want)
	}
	if out := buf.String(); !strings.Contains(out, "[jni] gameDidLeave") || strings.Contains(out, "stub-dispatch") {
		t.Fatalf("unexpected callback diagnostics: %s", out)
	}
	if !isImplementedMethod("gameDidLeave", gameDidLeaveSig) {
		t.Fatal("gameDidLeave()V missing from implementedMethods")
	}

	for _, call := range []struct{ class, name, sig string }{
		{"java/io/File", "gameDidLeave", gameDidLeaveSig},
		{nativeGLClass, "gameDidLeave", "(Z)V"},
		{nativeGLClass, "gameDidLeaveNow", gameDidLeaveSig},
	} {
		if _, ok := vm.dispatch(idToJobject(receiver.id), call.class, call.name, call.sig, nil); ok {
			t.Fatalf("lookalike handled: %s.%s%s", call.class, call.name, call.sig)
		}
	}
}

// TestNativeGLGameDidLeaveCoexistsWithNativeHelperLifecycle proves the exact
// NativeGL callback and the existing NativeHelper lifecycle stream remain
// independent. Adding the missing callback must not consume, reorder, or
// duplicate NativeHelper events used by the GameActivity route gate.
func TestNativeGLGameDidLeaveCoexistsWithNativeHelperLifecycle(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		events []string
	)
	cancelLifecycle := SubscribeNativeHelperLifecycle(func(event NativeHelperLifecycleEvent) {
		mu.Lock()
		events = append(events, "nativehelper")
		mu.Unlock()
	})
	cancelLeave := SubscribeNativeGLGameDidLeave(func() {
		mu.Lock()
		events = append(events, "gameDidLeave")
		mu.Unlock()
	})
	t.Cleanup(cancelLifecycle)
	t.Cleanup(cancelLeave)

	if _, handled := vm.dispatch(jnull(), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("NativeHelper game-loaded callback was not handled")
	}
	if _, handled := vm.dispatch(jnull(), nativeGLClass, "gameDidLeave", gameDidLeaveSig, nil); !handled {
		t.Fatal("NativeGL gameDidLeave callback was not handled")
	}
	if _, handled := vm.dispatch(jnull(), nativeHelperClass, "gameActivity_onExperienceStop", "(D)V", packJdouble(1)); !handled {
		t.Fatal("NativeHelper stop callback was not handled")
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if want := []string{"nativehelper", "gameDidLeave", "nativehelper"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("coexisting callback order = %v, want %v", got, want)
	}
}

// TestNativeGLGameDidLeaveCancellationLifetime pins the cancellation contract:
// it is safe and idempotent from inside a callback, and it prevents all later
// dispatches without deadlocking the JNI caller.
func TestNativeGLGameDidLeaveCancellationLifetime(t *testing.T) {
	var calls atomic.Int64
	var cancel func()
	cancel = SubscribeNativeGLGameDidLeave(func() {
		calls.Add(1)
		cancel()
	})
	t.Cleanup(cancel)

	noteNativeGLGameDidLeave()
	noteNativeGLGameDidLeave()
	cancel()
	if got := calls.Load(); got != 1 {
		t.Fatalf("listener calls = %d, want 1", got)
	}
}

// TestNativeGLGameDidLeaveConcurrentSubscribeCancel exercises the registry
// under the same concurrent subscribe/dispatch/cancel shape a session teardown
// can produce. The race build supplies the data-race assertion; the count only
// proves callbacks were not globally lost.
func TestNativeGLGameDidLeaveConcurrentSubscribeCancel(t *testing.T) {
	const workers = 24
	var calls atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cancel := SubscribeNativeGLGameDidLeave(func() { calls.Add(1) })
			noteNativeGLGameDidLeave()
			cancel()
			cancel()
		}()
	}
	wg.Wait()
	if calls.Load() == 0 {
		t.Fatal("concurrent dispatch lost every registered callback")
	}
}
