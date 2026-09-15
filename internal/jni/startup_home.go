// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sort"
	"sync"
	"sync/atomic"
)

// StartupHomeEvent is one fixed, payload-free milestone declared by the
// active APK's NativeHelper callbacks. It deliberately contains no place ID,
// navigator step, account, UI, or content data.
type StartupHomeEvent uint8

const (
	// StartupHomeDataModel is emitted only for the exact
	// NativeHelper.gameActivity_onGameLoaded(0) callback. The APK uses zero
	// to announce its Home/App DataModel; it is not a visible-Home claim.
	StartupHomeDataModel StartupHomeEvent = iota + 1

	// StartupHomeReady is emitted only for the active APK's exact
	// NativeHelper.gameActivity_onAppReady("Home") transition. The string is
	// classified inside the JNI receiver and never reaches a subscriber. It
	// is an engine Home-route readiness milestone, not a visible-Home claim.
	StartupHomeReady
)

// StartupHomeListener receives one fixed enum event. It receives no mutable
// state or source payload, so a measurement consumer cannot retain a place ID
// or an arbitrary NativeHelper AppReady step.
type StartupHomeListener func(StartupHomeEvent)

// SubscribeStartupHomeDataModel registers an independent observer of the
// active APK's Home DataModel announcement. It filters the existing
// composable NativeHelper lifecycle stream rather than replacing the
// process-wide GameLoadedListener used by Discord presence. The returned
// cancellation function is idempotent, may run from the observer, and has the
// same snapshot lifetime as SubscribeNativeHelperLifecycle.
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

// SubscribeStartupHomeReady registers an independent observer of the active
// APK's fixed Home-ready transition. Registration is dormant by default:
// NativeHelper dispatch neither logs nor retains readiness data for this seam
// unless a consumer attaches. Cancellation is idempotent and re-entrant;
// dispatch snapshots registration order before invoking listeners.
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

// noteStartupHomeReady is called only after dispatchNativeHelper accepted the
// exact NativeHelper AppReady identity and classified its stable `Home` step.
// It contains no source payload. A normal launch with no Runtime consumer
// avoids both allocation and mutex work here.
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
