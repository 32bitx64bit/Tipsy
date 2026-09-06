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
