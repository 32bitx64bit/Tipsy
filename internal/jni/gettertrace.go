// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

var getterTraceEnabled atomic.Bool

func init() {
	if os.Getenv("TIPSY_JNI_GETTER_TRACE") == "1" {
		getterTraceEnabled.Store(true)
	}
}

// SetGetterTrace enables or disables MotionEvent/KeyEvent getter tracing.
// Default is off.
func SetGetterTrace(enabled bool) {
	getterTraceEnabled.Store(enabled)
}

func getterTraceOn() bool {
	return getterTraceEnabled.Load()
}

// Observation-only getter-consumption trace. Records known method
// identities (name+sig) only, never event payload.

const getterTraceCap = 64

type getterTraceEntry struct {
	order []string
	count map[string]uint64
}

var getterTraceState struct {
	mu     sync.Mutex
	events map[int64]*getterTraceEntry
	ring   []int64
	totals map[string]uint64
}

// noteGetterCall records one getter call on an event object, from
// dispatchInput's handled getter paths only.
func noteGetterCall(objID int64, name, sig string) {
	if objID == 0 || !getterTraceOn() {
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

// drainEventGetterTrace returns the getter identities consumed for this
// event object as "identity:count" strings in first-call order, and
// releases the entry.
func drainEventGetterTrace(objID int64) []string {
	if !getterTraceOn() {
		return nil
	}
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

// GetterConsumptionTotals snapshots cumulative per-identity getter counts
// (method identities only, never event data).
func GetterConsumptionTotals() map[string]uint64 {
	if !getterTraceOn() {
		return map[string]uint64{}
	}
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
// safe enum name, never a coordinate or payload.
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

// traceEventGetterLine logs one line per delivered input event: the known
// getter identities consumed reading it, never coordinates, times, or text.
func traceEventGetterLine(kind string, action int32, objID int64) {
	if !getterTraceOn() {
		return
	}
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
