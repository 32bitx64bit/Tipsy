// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"fmt"
	"strings"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Observation-only MotionEvent/KeyEvent getter-consumption trace.
//
// Every getter the engine's native code resolves through the JNI dispatch
// is counted here under its known method identity (name+sig) — never any
// event payload: no coordinates, no times, no keycodes beyond the
// delivered identity names, no strings. Attribution is exact per event
// object: each delivered event is a fresh object, so the identities
// recorded against its id are precisely what the engine's native consumed
// while handling that one event. This proves which getters the engine
// actually reads during a real gesture without touching any return value
// or dispatch decision.

const getterTraceCap = 64 // bounded ring of per-event traces

type getterTraceEntry struct {
	order []string          // identities in first-call order
	count map[string]uint64 // identity → calls for this event
}

var getterTraceState struct {
	mu     sync.Mutex
	events map[int64]*getterTraceEntry
	ring   []int64 // insertion order, for eviction and drain
	totals map[string]uint64
}

// noteGetterCall records one getter call the engine's native made on an
// event object. Called only from the handled MotionEvent/KeyEvent getter
// paths in dispatchInput.
func noteGetterCall(objID int64, name, sig string) {
	if objID == 0 {
		return
	}
	identity := name + sig
	getterTraceState.mu.Lock()
	defer getterTraceState.mu.Unlock()
	if getterTraceState.events == nil {
		getterTraceState.events = make(map[int64]*getterTraceEntry)
		getterTraceState.totals = make(map[string]uint64)
	}
	e := getterTraceState.events[objID]
	if e == nil {
		if len(getterTraceState.ring) >= getterTraceCap {
			evict := getterTraceState.ring[0]
			getterTraceState.ring = getterTraceState.ring[1:]
			delete(getterTraceState.events, evict)
		}
		e = &getterTraceEntry{count: make(map[string]uint64)}
		getterTraceState.events[objID] = e
		getterTraceState.ring = append(getterTraceState.ring, objID)
	}
	if _, seen := e.count[identity]; !seen {
		e.order = append(e.order, identity)
	}
	e.count[identity]++
	getterTraceState.totals[identity]++
}

// drainEventGetterTrace returns the getter identities the engine's native
// consumed for this event object as "identity:count" strings in
// first-call order, and releases the entry. An empty result means native
// consumed no getters for the event — equally valid evidence.
func drainEventGetterTrace(objID int64) []string {
	getterTraceState.mu.Lock()
	defer getterTraceState.mu.Unlock()
	e := getterTraceState.events[objID]
	if e == nil {
		return nil
	}
	delete(getterTraceState.events, objID)
	for i, id := range getterTraceState.ring {
		if id == objID {
			getterTraceState.ring = append(getterTraceState.ring[:i], getterTraceState.ring[i+1:]...)
			break
		}
	}
	out := make([]string, 0, len(e.order))
	for _, identity := range e.order {
		out = append(out, fmt.Sprintf("%s:%d", identity, e.count[identity]))
	}
	return out
}

// GetterConsumptionTotals snapshots cumulative per-identity getter
// consumption counts (known method identities only, never event data).
func GetterConsumptionTotals() map[string]uint64 {
	getterTraceState.mu.Lock()
	defer getterTraceState.mu.Unlock()
	out := make(map[string]uint64, len(getterTraceState.totals))
	for k, v := range getterTraceState.totals {
		out[k] = v
	}
	return out
}

// resetGetterTrace clears all trace state (test helper only).
func resetGetterTrace() {
	getterTraceState.mu.Lock()
	defer getterTraceState.mu.Unlock()
	getterTraceState.events = nil
	getterTraceState.ring = nil
	getterTraceState.totals = nil
}

// motionActionName renders a MotionEvent/KeyEvent action constant as a
// safe enum name (never a coordinate or payload).
func motionActionName(action int32) string {
	switch action {
	case motionActionDown:
		return "down"
	case motionActionUp:
		return "up"
	case motionActionMove:
		return "move"
	case motionActionCancel:
		return "cancel"
	case motionActionPointerDown:
		return "pointerdown"
	case motionActionPointerUp:
		return "pointerup"
	default:
		return fmt.Sprintf("v%d", action)
	}
}

// traceEventGetterLine logs one observation line per delivered input
// event: which known getter identities the engine's native consumed while
// reading it. Identities and counts only — never coordinates, times, or
// text.
func traceEventGetterLine(kind string, action int32, objID int64) {
	hits := drainEventGetterTrace(objID)
	joined := "none"
	if len(hits) > 0 {
		joined = strings.Join(hits, ",")
		if len(joined) > 480 {
			joined = joined[:480]
		}
	}
	logging.Logger(logging.CatJNI).Info("[jni] input getters",
		"kind", kind, "action", motionActionName(action), "getters", joined)
}
