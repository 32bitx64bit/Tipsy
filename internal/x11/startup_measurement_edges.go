// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

// StartupMeasurementEdges contains the only two X11 callbacks admitted to
// the default-off startup recorder. Callbacks receive no event payload: they
// cannot observe text, input identity, coordinates, titles, XIDs, or raw
// timestamps.
//
// MapNotify reports the first real MapNotify for this Window's owned X11
// client. If that notification was observed during Open before registration,
// registration delivers the same fact once; it does not substitute window
// allocation, a refresh wake, a child overlay, or a mapping query.
//
// PostScrollDrawableUpdate reports one XDamage notification for this owned
// client after DeclareStartupMeasurementVerticalScrollDispatch armed it. It
// is an X drawable-damage observation only: it does not establish scanout,
// compositor presentation, frame time, FPS, visible Home, or that the
// declared scroll caused the update.
type StartupMeasurementEdges struct {
	MapNotify                func()
	PostScrollDrawableUpdate func()
}

// StartupMeasurementEdgeAvailability describes whether the exact XDamage
// edge is available for this subscription. False means the X Damage extension
// or its owned-window observation could not be established; callers must
// leave that boundary unavailable rather than replacing it with Expose,
// Present return, a pump wake, a timer, or wheel delivery.
type StartupMeasurementEdgeAvailability struct {
	PostScrollDrawableUpdate bool
}

type startupMeasurementObserver struct {
	id           uint64
	edges        StartupMeasurementEdges
	mapDelivered bool
}

// OnStartupMeasurementEdges subscribes to named, content-free startup
// measurement edges for this Window. It is composable: each call adds one
// observer and its cancel function removes only that observer. Normal X11
// launches have no subscription and never create an XDamage object.
//
// The returned availability is an explicit fail-closed signal. A caller that
// needs the post-scroll boundary must check it before declaring an arm.
func (w *Window) OnStartupMeasurementEdges(edges StartupMeasurementEdges) (cancel func(), availability StartupMeasurementEdgeAvailability) {
	if w == nil || (edges.MapNotify == nil && edges.PostScrollDrawableUpdate == nil) {
		return func() {}, availability
	}

	var immediate []func()
	w.mu.Lock()
	if w.closed || w.display == 0 || w.xid == 0 {
		w.mu.Unlock()
		return func() {}, availability
	}

	if edges.MapNotify != nil && !w.startupMeasurementMapEnabled {
		startupMeasurementSetMapObserver(w, true)
		w.startupMeasurementMapEnabled = true
	}
	if edges.PostScrollDrawableUpdate != nil {
		if !w.startupMeasurementDrawableEnabled {
			w.startupMeasurementDrawableEnabled = startupMeasurementEnableDrawableObservation(w)
		}
		availability.PostScrollDrawableUpdate = w.startupMeasurementDrawableEnabled
	}

	registered := edges
	// A subscription that was told XDamage is unavailable must remain
	// unavailable. Do not let a later, independent successful subscription
	// turn its requested callback into a guessed edge.
	if edges.PostScrollDrawableUpdate != nil && !availability.PostScrollDrawableUpdate {
		registered.PostScrollDrawableUpdate = nil
	}
	observer := w.addStartupMeasurementObserverLocked(registered)
	if edges.MapNotify != nil && startupMeasurementMapObserved(w) {
		observer.mapDelivered = true
		immediate = append(immediate, edges.MapNotify)
	}
	w.disableStartupMeasurementMapIfSatisfiedLocked()
	w.mu.Unlock()

	// Registration never invokes arbitrary callbacks under the Window lock.
	// This also preserves the cancellation/teardown path for a callback that
	// chooses to stop itself.
	notifyStartupMeasurementEdges(immediate)

	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.removeStartupMeasurementObserverLocked(observer.id)
	}, availability
}

func (w *Window) addStartupMeasurementObserverLocked(edges StartupMeasurementEdges) *startupMeasurementObserver {
	if w.startupMeasurementObservers == nil {
		w.startupMeasurementObservers = make(map[uint64]*startupMeasurementObserver)
	}
	w.startupMeasurementNext++
	observer := &startupMeasurementObserver{id: w.startupMeasurementNext, edges: edges}
	w.startupMeasurementObservers[observer.id] = observer
	return observer
}

func (w *Window) removeStartupMeasurementObserverLocked(id uint64) {
	if w.startupMeasurementObservers == nil {
		return
	}
	delete(w.startupMeasurementObservers, id)
	w.disableUnusedStartupMeasurementEdgesLocked()
}

func (w *Window) disableUnusedStartupMeasurementEdgesLocked() {
	drawableNeeded := false
	for _, observer := range w.startupMeasurementObservers {
		drawableNeeded = drawableNeeded || observer.edges.PostScrollDrawableUpdate != nil
	}
	w.disableStartupMeasurementMapIfSatisfiedLocked()
	if w.startupMeasurementDrawableEnabled && !drawableNeeded {
		startupMeasurementDisableDrawableObservation(w)
		w.startupMeasurementDrawableEnabled = false
		w.startupMeasurementDrawableArmed = false
	}
}

func (w *Window) disableStartupMeasurementMapIfSatisfiedLocked() {
	if !w.startupMeasurementMapEnabled {
		return
	}
	for _, observer := range w.startupMeasurementObservers {
		if observer.edges.MapNotify != nil && !observer.mapDelivered {
			return
		}
	}
	startupMeasurementSetMapObserver(w, false)
	w.startupMeasurementMapEnabled = false
}

func (w *Window) clearStartupMeasurementEdgesLocked() {
	for id := range w.startupMeasurementObservers {
		delete(w.startupMeasurementObservers, id)
	}
	if w.startupMeasurementMapEnabled {
		startupMeasurementSetMapObserver(w, false)
		w.startupMeasurementMapEnabled = false
	}
	if w.startupMeasurementDrawableEnabled {
		startupMeasurementDisableDrawableObservation(w)
		w.startupMeasurementDrawableEnabled = false
	}
	w.startupMeasurementDrawableArmed = false
	w.clearStartupMeasurementScrollTestDriverLocked()
}

// DeclareStartupMeasurementVerticalScrollDispatch arms exactly one later
// XDamage observation. Call it only immediately after the measurement owner
// has dispatched its fixed, non-text vertical scroll interaction. It neither
// reads nor dispatches a wheel event, changes input routing, synthesizes
// input, polls, captures the screen, or observes arbitrary ongoing damage.
//
// False is fail-closed: no active drawable subscriber, no XDamage support,
// a closed window, or an already-armed boundary. Callers must not replace a
// false result with an inferred redraw signal.
func (w *Window) DeclareStartupMeasurementVerticalScrollDispatch() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.display == 0 || w.xid == 0 ||
		!w.startupMeasurementDrawableEnabled || w.startupMeasurementDrawableArmed {
		return false
	}
	if !startupMeasurementArmDrawableObservation(w) {
		return false
	}
	w.startupMeasurementDrawableArmed = true
	return true
}

// collectStartupMeasurementEdgesLocked observes only the two named native
// edges. It runs after the C event reader drained X events but before input
// subscribers run. A drawable notification is produced by C only after a
// successful Declare call; a same-turn input subscriber can therefore arm a
// later pump without an earlier queued damage event being attributed to it.
func (w *Window) collectStartupMeasurementEdgesLocked() []func() {
	if len(w.startupMeasurementObservers) == 0 {
		return nil
	}
	var callbacks []func()
	if w.startupMeasurementMapEnabled && w.startupMeasurementMapPendingLocked() && startupMeasurementMapObserved(w) {
		for _, observer := range w.startupMeasurementObservers {
			if observer.edges.MapNotify != nil && !observer.mapDelivered {
				observer.mapDelivered = true
				callbacks = append(callbacks, observer.edges.MapNotify)
			}
		}
		w.disableStartupMeasurementMapIfSatisfiedLocked()
	}
	if w.startupMeasurementDrawableArmed && startupMeasurementTakeDrawableObservation(w) {
		w.startupMeasurementDrawableArmed = false
		for _, observer := range w.startupMeasurementObservers {
			if observer.edges.PostScrollDrawableUpdate != nil {
				callbacks = append(callbacks, observer.edges.PostScrollDrawableUpdate)
			}
		}
	}
	return callbacks
}

func (w *Window) startupMeasurementMapPendingLocked() bool {
	for _, observer := range w.startupMeasurementObservers {
		if observer.edges.MapNotify != nil && !observer.mapDelivered {
			return true
		}
	}
	return false
}

func notifyStartupMeasurementEdges(callbacks []func()) {
	for _, callback := range callbacks {
		if callback != nil {
			callback()
		}
	}
}
