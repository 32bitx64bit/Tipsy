// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

// TestNativeHelperExperienceLifecycleFanout drives the production JNI
// dispatcher through the APK-declared callbacks. It proves a real exit
// sequence reaches composable lifecycle observers while preserving the
// existing onGameLoaded observer used by Discord presence. No route is
// executed here: event ordering is the GameActivity handoff boundary.
func TestNativeHelperExperienceLifecycleFanout(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	receiver := vm.newObjectLocked(cls)
	vm.mu.Unlock()

	var (
		mu     sync.Mutex
		events []NativeHelperLifecycleEvent
		places atomic.Int64
		calls  atomic.Int64
	)
	cancel := SubscribeNativeHelperLifecycle(func(event NativeHelperLifecycleEvent) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	t.Cleanup(cancel)
	SetGameLoadedListener(func(placeID int64) {
		places.Store(placeID)
		calls.Add(1)
	})
	t.Cleanup(func() { SetGameLoadedListener(nil) })

	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onExperienceStart", "()V", nil); !handled {
		t.Fatal("gameActivity_onExperienceStart()V was not handled")
	}
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onExperienceStop", "(D)V", packJdouble(42.5)); !handled {
		t.Fatal("gameActivity_onExperienceStop(D)V was not handled")
	}
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onLuaAppDidReturn", "()V", nil); !handled {
		t.Fatal("gameActivity_onLuaAppDidReturn()V was not handled")
	}
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0)); !handled {
		t.Fatal("gameActivity_onGameLoaded(J)V was not handled")
	}

	mu.Lock()
	got := append([]NativeHelperLifecycleEvent(nil), events...)
	mu.Unlock()
	wantKinds := []NativeHelperLifecycleKind{
		NativeHelperExperienceStarted,
		NativeHelperExperienceStopped,
		NativeHelperLuaAppDidReturn,
		NativeHelperGameLoadedEvent,
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("events = %#v, want %d callbacks", got, len(wantKinds))
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("event %d kind = %d, want %d", i, got[i].Kind, want)
		}
	}
	if math.Float64bits(got[1].DurationSeconds) != math.Float64bits(42.5) {
		t.Fatalf("stop duration = %v, want 42.5", got[1].DurationSeconds)
	}
	if got[3].PlaceID != 0 {
		t.Fatalf("Home game-loaded place = %d, want 0", got[3].PlaceID)
	}
	if calls.Load() != 1 || places.Load() != 0 {
		t.Fatalf("existing game-loaded listener = (%d, %d), want (1, 0)", calls.Load(), places.Load())
	}

	for _, method := range []struct{ name, sig string }{
		{"gameActivity_onExperienceStart", "()V"},
		{"gameActivity_onExperienceStop", "(D)V"},
		{"gameActivity_onLuaAppDidReturn", "()V"},
	} {
		if !isImplementedMethod(method.name, method.sig) {
			t.Fatalf("%s%s missing from implementedMethods", method.name, method.sig)
		}
	}

	// Exact class and signature matching prevent unrelated Java calls from
	// requesting a post-experience action.
	if _, handled := vm.dispatch(idToJobject(receiver.id), "java/io/File", "gameActivity_onExperienceStop", "(D)V", packJdouble(1)); handled {
		t.Fatal("lookalike class handled lifecycle callback")
	}
	if _, handled := vm.dispatch(idToJobject(receiver.id), nativeHelperClass, "gameActivity_onExperienceStop", "(F)V", packJdouble(1)); handled {
		t.Fatal("lookalike signature handled lifecycle callback")
	}
}

// TestNativeHelperLifecycleUnsubscribeMayRunFromListener verifies the
// registry's re-entrant cleanup contract. This is needed for a GameActivity
// session to release its observer without racing the engine callback thread.
func TestNativeHelperLifecycleUnsubscribeMayRunFromListener(t *testing.T) {
	var calls atomic.Int64
	var cancel func()
	cancel = SubscribeNativeHelperLifecycle(func(NativeHelperLifecycleEvent) {
		calls.Add(1)
		cancel()
	})
	t.Cleanup(cancel)
	noteNativeHelperLifecycle(NativeHelperLifecycleEvent{Kind: NativeHelperExperienceStarted})
	noteNativeHelperLifecycle(NativeHelperLifecycleEvent{Kind: NativeHelperExperienceStopped})
	if calls.Load() != 1 {
		t.Fatalf("listener calls = %d, want 1", calls.Load())
	}
}
