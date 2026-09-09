// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync/atomic"
	"testing"
)

func TestStartGameParamsPlaceIDNotifiesListener(t *testing.T) {
	t.Cleanup(func() { SetPlaceIDListener(nil) })
	var got atomic.Int64
	SetPlaceIDListener(func(id int64) { got.Store(id) })

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	obj := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/StartGameParams"))
	env.PutField(obj, "placeId", int64(1818))
	if got.Load() != 1818 {
		t.Fatalf("PutField placeId listener=%d", got.Load())
	}
	got.Store(0)
	v, ok := vm.dispatch(idToJobject(jobjectToID(obj)), "com/roblox/engine/jni/autovalue/StartGameParams", "placeId", "()J", nil)
	if !ok {
		t.Fatal("placeId()J not handled")
	}
	if int64(uintptr(v)) != 1818 {
		t.Fatalf("placeId()J=%d", uintptr(v))
	}
	if got.Load() != 1818 {
		t.Fatalf("fieldGetter placeId listener=%d", got.Load())
	}

	other := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/InitParams"))
	env.PutField(other, "placeId", int64(99))
	if got.Load() != 1818 {
		t.Fatalf("non-StartGameParams placeId notified listener=%d", got.Load())
	}
	env.PutField(obj, "placeId", int64(0))
	if got.Load() != 1818 {
		t.Fatalf("zero placeId notified listener=%d", got.Load())
	}
}

// TestGameLoadedPlaceIDNotifiesListener drives the production
// dispatchNativeHelper path for gameActivity_onGameLoaded(J)V and proves the
// engine-provided place id reaches SetGameLoadedListener verbatim — including
// 0, which is the client's own "back to Home" statement and must not be
// filtered the way an empty StartGameParams is.
func TestGameLoadedPlaceIDNotifiesListener(t *testing.T) {
	t.Cleanup(func() { SetGameLoadedListener(nil) })
	var (
		got   atomic.Int64
		calls atomic.Int64
	)
	SetGameLoadedListener(func(id int64) {
		got.Store(id)
		calls.Add(1)
	})

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	vm.mu.Unlock()
	recv := idToJobject(h.id)

	// Live Home → Play → Home → Play sequence (2026-09-06 launch log).
	for i, want := range []int64{0, 18667984660, 0, 8735521924} {
		if _, handled := vm.dispatch(recv, nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(want)); !handled {
			t.Fatalf("step %d: gameActivity_onGameLoaded not handled", i)
		}
		if got.Load() != want {
			t.Fatalf("step %d: listener place=%d, want %d", i, got.Load(), want)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("listener calls=%d, want 4", calls.Load())
	}

	// Lookalike identities are not NativeHelper's contract and must not
	// notify: another class, another signature, and the sibling
	// NativeGLJavaInterface.gameLoadedCallback capture path.
	before := calls.Load()
	_, _ = vm.dispatch(recv, "com/roblox/client/startup/NotNativeHelper", "gameActivity_onGameLoaded", "(J)V", packJlong(5))
	_, _ = vm.dispatch(recv, nativeHelperClass, "gameActivity_onGameLoaded", "(JI)V", packJlong(5))
	_, _ = callDispatchOrStub(vm, jnull(), "com/roblox/engine/jni/NativeGLJavaInterface", "gameLoadedCallback", "(J)V", packJlong(5), 'V')
	if calls.Load() != before || got.Load() != 8735521924 {
		t.Fatalf("lookalike dispatch notified listener: calls=%d place=%d", calls.Load(), got.Load())
	}

	// Negatives are recorded on the receiver but must not notify.
	if _, handled := vm.dispatch(recv, nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(-1)); !handled {
		t.Fatal("negative gameActivity_onGameLoaded not handled")
	}
	if calls.Load() != before || got.Load() != 8735521924 {
		t.Fatalf("negative place id notified listener: calls=%d place=%d", calls.Load(), got.Load())
	}

	// A cleared listener is a no-op; the receiver still records the value.
	SetGameLoadedListener(nil)
	countBefore, _ := NativeHelperGameLoaded()
	if _, handled := vm.dispatch(recv, nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(1818)); !handled {
		t.Fatal("gameActivity_onGameLoaded not handled after listener cleared")
	}
	if count, place := NativeHelperGameLoaded(); count != countBefore+1 || place != 1818 {
		t.Fatalf("record after clear = (%d, %d), want (%d, 1818)", count, place, countBefore+1)
	}
	if calls.Load() != before {
		t.Fatalf("cleared listener still called: %d", calls.Load())
	}
}
