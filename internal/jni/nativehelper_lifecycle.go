// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "sync"

// NativeHelperLifecycleKind identifies an engine-to-Java callback declared by
// the APK's NativeHelper. Values are not inferred from window state, timers,
// or UI actions.
type NativeHelperLifecycleKind uint8

const (
	NativeHelperExperienceStarted NativeHelperLifecycleKind = iota + 1
	NativeHelperExperienceStopped
	NativeHelperLuaAppDidReturn
	NativeHelperGameLoadedEvent
)

// NativeHelperLifecycleEvent is one exact NativeHelper callback. Duration is
// set only for gameActivity_onExperienceStop(D)V; PlaceID is set only for
// gameActivity_onGameLoaded(J)V. A zero value in either field is still the
// engine's own value, not a sentinel fabricated by Tipsy.
type NativeHelperLifecycleEvent struct {
	Kind            NativeHelperLifecycleKind
	DurationSeconds float64
	PlaceID         int64
}

// NativeHelperLifecycleListener observes engine-declared application
// lifecycle callbacks. It is intentionally separate from GameLoadedListener:
// Discord's existing place-presence observer retains its single owner while
// GameActivity and future platform consumers can compose lifecycle observers.
type NativeHelperLifecycleListener func(NativeHelperLifecycleEvent)

var nativeHelperLifecycleListeners struct {
	mu   sync.Mutex
	next uint64
	set  map[uint64]NativeHelperLifecycleListener
}

// SubscribeNativeHelperLifecycle registers an independent lifecycle observer.
// Its return function is idempotent and may be called from an observer. The
// dispatcher snapshots listeners before invoking them, so no callback runs
// while the registry lock is held.
func SubscribeNativeHelperLifecycle(fn NativeHelperLifecycleListener) func() {
	if fn == nil {
		return func() {}
	}
	nativeHelperLifecycleListeners.mu.Lock()
	nativeHelperLifecycleListeners.next++
	id := nativeHelperLifecycleListeners.next
	if nativeHelperLifecycleListeners.set == nil {
		nativeHelperLifecycleListeners.set = make(map[uint64]NativeHelperLifecycleListener)
	}
	nativeHelperLifecycleListeners.set[id] = fn
	nativeHelperLifecycleListeners.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			nativeHelperLifecycleListeners.mu.Lock()
			delete(nativeHelperLifecycleListeners.set, id)
			nativeHelperLifecycleListeners.mu.Unlock()
		})
	}
}

// noteNativeHelperLifecycle fans out an exact callback to independent
// observers. It deliberately has no route or state-machine policy: the
// GameActivity owner decides whether a particular sequence can request a
// named APK route.
func noteNativeHelperLifecycle(event NativeHelperLifecycleEvent) {
	nativeHelperLifecycleListeners.mu.Lock()
	listeners := make([]NativeHelperLifecycleListener, 0, len(nativeHelperLifecycleListeners.set))
	for _, fn := range nativeHelperLifecycleListeners.set {
		listeners = append(listeners, fn)
	}
	nativeHelperLifecycleListeners.mu.Unlock()
	for _, fn := range listeners {
		fn(event)
	}
}
