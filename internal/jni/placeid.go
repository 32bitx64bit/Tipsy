// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"sync"
)

// PlaceIDListener is notified of observed StartGameParams.placeId values.
// The id is never a ticket, cookie, or user identity.
type PlaceIDListener func(placeID int64)

var (
	placeIDMu       sync.Mutex
	placeIDListener PlaceIDListener
)

// SetPlaceIDListener replaces the process-wide place-id observer. Pass nil to clear.
func SetPlaceIDListener(fn PlaceIDListener) {
	placeIDMu.Lock()
	placeIDListener = fn
	placeIDMu.Unlock()
}

// GameLoadedListener is notified of the place id the engine passes to
// NativeHelper.gameActivity_onGameLoaded(J)V. Unlike StartGameParams, this
// fires for every DataModel the client loads, including in-client Home →
// Play joins that never construct Java StartGameParams. Zero means the
// client returned to Home. The id is never a ticket, cookie, job id, or
// user identity.
type GameLoadedListener func(placeID int64)

var (
	gameLoadedListenerMu sync.Mutex
	gameLoadedListener   GameLoadedListener
)

// SetGameLoadedListener replaces the process-wide onGameLoaded observer.
// Pass nil to clear.
func SetGameLoadedListener(fn GameLoadedListener) {
	gameLoadedListenerMu.Lock()
	gameLoadedListener = fn
	gameLoadedListenerMu.Unlock()
}

// noteGameLoadedPlaceID forwards the engine-provided onGameLoaded place id.
// It is only reached from dispatchNativeHelper's receiver for the exact
// NativeHelper contract, so the value is always the engine's own statement.
// Zero is forwarded (Home). Negatives are dropped. Nothing is fabricated.
func noteGameLoadedPlaceID(id int64) {
	if id < 0 {
		return
	}
	gameLoadedListenerMu.Lock()
	fn := gameLoadedListener
	gameLoadedListenerMu.Unlock()
	if fn != nil {
		fn(id)
	}
}

func noteStartGamePlaceID(class, name string, id int64) {
	if name != "placeId" || id <= 0 {
		return
	}
	if class != "" && !strings.HasSuffix(class, "StartGameParams") {
		return
	}
	placeIDMu.Lock()
	fn := placeIDListener
	placeIDMu.Unlock()
	if fn != nil {
		fn(id)
	}
}

func int64Field(val any) (int64, bool) {
	switch t := val.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	default:
		return 0, false
	}
}
