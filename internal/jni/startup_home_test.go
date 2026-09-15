// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func newStartupHomeTestReceiver(t *testing.T, vm *VM) *Object {
	t.Helper()
	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	receiver := vm.newObjectLocked(cls)
	vm.mu.Unlock()
	return receiver
}

func dispatchStartupHomeAppReady(t *testing.T, vm *VM, receiver *Object, step string) {
	t.Helper()
	vm.mu.Lock()
	value := vm.newStringLocked(step)
	vm.mu.Unlock()
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onAppReady", "(Ljava/lang/String;)V", testPackObjectArg(value.id)); !handled {
		t.Fatalf("gameActivity_onAppReady(%q) was not handled", step)
	}
}

// TestStartupHomeDataModelExactIdentityAndDiscordCoexistence proves the new
// Home DataModel stream filters the existing composable lifecycle fanout. It
// neither replaces nor consumes Discord's existing GameLoadedListener.
func TestStartupHomeDataModelExactIdentityAndDiscordCoexistence(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	receiver := newStartupHomeTestReceiver(t, vm)

	var (
		mu           sync.Mutex
		events       []StartupHomeEvent
		discordCalls atomic.Int64
	)
	cancel := SubscribeStartupHomeDataModel(func(event StartupHomeEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	t.Cleanup(cancel)
	SetGameLoadedListener(func(int64) { discordCalls.Add(1) })
	t.Cleanup(func() { SetGameLoadedListener(nil) })

	// AppReady alone is a distinct Java callback, and a nonzero GameLoaded is
	// an experience DataModel; neither may be relabelled as Home DataModel.
	dispatchStartupHomeAppReady(t, vm, receiver, "Home")
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(1)); !handled {
		t.Fatal("nonzero gameActivity_onGameLoaded was not handled")
	}
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("Home gameActivity_onGameLoaded was not handled")
	}

	mu.Lock()
	got := append([]StartupHomeEvent(nil), events...)
	mu.Unlock()
	if want := []StartupHomeEvent{StartupHomeDataModel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("startup events = %v, want %v", got, want)
	}
	if got := discordCalls.Load(); got != 2 {
		t.Fatalf("Discord GameLoadedListener calls = %d, want 2", got)
	}

	// Only NativeHelper's exact method/signature can reach the lifecycle
	// stream. A sibling capture path remains unrelated.
	before := len(got)
	_, _ = vm.dispatch(idToJobject(receiver.id), "java/io/File", "gameActivity_onGameLoaded", "(J)V", packJlong(0))
	_, _ = vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onGameLoaded", "(JI)V", packJlong(0))
	_, _ = callDispatchOrStub(vm, jnull(), nativeGLClass, "gameLoadedCallback", "(J)V", packJlong(0), 'V')
	mu.Lock()
	got = append([]StartupHomeEvent(nil), events...)
	mu.Unlock()
	if len(got) != before {
		t.Fatalf("lookalike callback emitted Home DataModel: %v", got)
	}
}

// TestStartupHomeReadyExactIdentityAndRegistrationOrder proves the only
// current Home-ready classification is the APK's named AppReady `Home`
// transition. Nearby navigator names are intentionally rejected: they also
// occur after a Games return and cannot mean Home by themselves.
func TestStartupHomeReadyExactIdentityAndRegistrationOrder(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	receiver := newStartupHomeTestReceiver(t, vm)

	var (
		mu    sync.Mutex
		order []string
	)
	appendEvent := func(name string) StartupHomeListener {
		return func(event StartupHomeEvent) {
			if event != StartupHomeReady {
				t.Errorf("event = %v, want StartupHomeReady", event)
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
		}
	}
	cancelFirst := SubscribeStartupHomeReady(appendEvent("first"))
	cancelSecond := SubscribeStartupHomeReady(appendEvent("second"))
	t.Cleanup(cancelFirst)
	t.Cleanup(cancelSecond)

	for _, step := range []string{"Startup", "Landing", "HomeContainer", "RootSwitchNavigator", "Games"} {
		dispatchStartupHomeAppReady(t, vm, receiver, step)
	}
	mu.Lock()
	if len(order) != 0 {
		t.Fatalf("non-Home AppReady steps emitted readiness: %v", order)
	}
	mu.Unlock()

	dispatchStartupHomeAppReady(t, vm, receiver, "Home")
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Home-ready fanout order = %v, want %v", got, want)
	}

	vm.mu.Lock()
	value := vm.newStringLocked("Home")
	vm.mu.Unlock()
	_, _ = vm.dispatch(idToJobject(receiver.id), "java/io/File", "gameActivity_onAppReady", "(Ljava/lang/String;)V", testPackObjectArg(value.id))
	_, _ = vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onAppReady", "()V", nil)
	mu.Lock()
	got = append([]string(nil), order...)
	mu.Unlock()
	if want := []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lookalike AppReady emitted Home readiness: %v", got)
	}
}

// TestStartupHomeSubscriptionsAreReentrantAndHaveBoundedLifetime proves a
// consumer may tear down from its callback and that it sees no later event.
// This matches the normal arm-teardown shape without blocking the JNI caller.
func TestStartupHomeSubscriptionsAreReentrantAndHaveBoundedLifetime(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	receiver := newStartupHomeTestReceiver(t, vm)

	var modelCalls atomic.Int64
	var cancelModel func()
	cancelModel = SubscribeStartupHomeDataModel(func(StartupHomeEvent) {
		modelCalls.Add(1)
		cancelModel()
	})
	t.Cleanup(cancelModel)
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("first Home game-loaded was not handled")
	}
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("second Home game-loaded was not handled")
	}
	if got := modelCalls.Load(); got != 1 {
		t.Fatalf("Home DataModel calls = %d, want 1", got)
	}

	var readyCalls atomic.Int64
	var cancelReady func()
	cancelReady = SubscribeStartupHomeReady(func(StartupHomeEvent) {
		readyCalls.Add(1)
		cancelReady()
	})
	t.Cleanup(cancelReady)
	dispatchStartupHomeAppReady(t, vm, receiver, "Home")
	dispatchStartupHomeAppReady(t, vm, receiver, "Home")
	if got := readyCalls.Load(); got != 1 {
		t.Fatalf("Home-ready calls = %d, want 1", got)
	}
}

// TestStartupHomeSubscriptionsConcurrentSubscribeCancel exercises both
// registries under concurrent registration, dispatch, and teardown. Run it
// with -race to cover the JNI session handoff's normal concurrent shape.
func TestStartupHomeSubscriptionsConcurrentSubscribeCancel(t *testing.T) {
	const workers = 24
	var calls atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cancelModel := SubscribeStartupHomeDataModel(func(StartupHomeEvent) { calls.Add(1) })
			cancelReady := SubscribeStartupHomeReady(func(StartupHomeEvent) { calls.Add(1) })
			noteNativeHelperLifecycle(NativeHelperLifecycleEvent{Kind: NativeHelperGameLoadedEvent})
			noteStartupHomeReady()
			cancelModel()
			cancelModel()
			cancelReady()
			cancelReady()
		}()
	}
	wg.Wait()
	if got := calls.Load(); got == 0 {
		t.Fatal("concurrent subscriptions lost every Home event")
	}
}
