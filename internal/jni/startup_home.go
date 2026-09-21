// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sort"
	"sync"
	"sync/atomic"
)

// StartupHomeEvent is a fixed, payload-free milestone. It carries no place
// ID, navigator step, account, UI, or content data.
type StartupHomeEvent uint8

const (
	// StartupHomeDataModel is emitted only for the exact
	// NativeHelper.gameActivity_onGameLoaded(0) callback; zero announces the
	// Home/App DataModel, not a visible-Home claim.
	StartupHomeDataModel StartupHomeEvent = iota + 1

	// StartupHomeReady is emitted only for the exact
	// NativeHelper.gameActivity_onAppReady("Home") transition. It is a
	// Home-route readiness milestone, not a visible-Home claim.
	StartupHomeReady
)

// StartupHomeListener receives one fixed enum event and no source payload.
type StartupHomeListener func(StartupHomeEvent)

// SubscribeStartupHomeDataModel registers an independent observer of the Home
// DataModel announcement, filtering the NativeHelper lifecycle stream without
// replacing GameLoadedListener. Its cancellation is idempotent and re-entrant.
func SubscribeStartupHomeDataModel(fn StartupHomeListener) func() {
	if fn == nil {
		return func() {}
	}
	return SubscribeNativeHelperLifecycle(func(event NativeHelperLifecycleEvent) {
		if event.Kind == NativeHelperGameLoadedEvent && event.PlaceID == 0 {
			fn(StartupHomeDataModel)
		}
	})
}

var startupHomeReadyListeners struct {
	mu     sync.Mutex
	next   uint64
	set    map[uint64]StartupHomeListener
	active atomic.Uint64
}

// SubscribeStartupHomeReady registers an independent observer of the Home-ready
// transition. Cancellation is idempotent and re-entrant; dispatch snapshots
// registration order before invoking listeners.
func SubscribeStartupHomeReady(fn StartupHomeListener) func() {
	if fn == nil {
		return func() {}
	}
	startupHomeReadyListeners.mu.Lock()
	startupHomeReadyListeners.next++
	id := startupHomeReadyListeners.next
	if startupHomeReadyListeners.set == nil {
		startupHomeReadyListeners.set = make(map[uint64]StartupHomeListener)
	}
	startupHomeReadyListeners.set[id] = fn
	startupHomeReadyListeners.active.Add(1)
	startupHomeReadyListeners.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			startupHomeReadyListeners.mu.Lock()
			if _, ok := startupHomeReadyListeners.set[id]; ok {
				delete(startupHomeReadyListeners.set, id)
				startupHomeReadyListeners.active.Add(^uint64(0))
			}
			startupHomeReadyListeners.mu.Unlock()
		})
	}
}

// noteStartupHomeReady runs only after dispatchNativeHelper accepted the exact
// NativeHelper AppReady identity and classified its `Home` step. It carries no
// source payload.
func noteStartupHomeReady() {
	if startupHomeReadyListeners.active.Load() == 0 {
		return
	}
	startupHomeReadyListeners.mu.Lock()
	ids := make([]uint64, 0, len(startupHomeReadyListeners.set))
	for id := range startupHomeReadyListeners.set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	listeners := make([]StartupHomeListener, 0, len(ids))
	for _, id := range ids {
		listeners = append(listeners, startupHomeReadyListeners.set[id])
	}
	startupHomeReadyListeners.mu.Unlock()

	for _, fn := range listeners {
		fn(StartupHomeReady)
	}
}
