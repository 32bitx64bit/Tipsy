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
