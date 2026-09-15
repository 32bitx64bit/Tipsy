// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"sync"
	"time"
)

// startupMeasurementRequested uses a separate exact opt-in from
// TIPSY_STUTTER_DIAG. A startup arm must not accidentally enable the existing
// high-frequency JNI, bionic, and input-drain observers it is trying to
// measure alongside.
func startupMeasurementRequested(getenv func(string) string) bool {
	return getenv != nil && getenv("TIPSY_STARTUP_MEASUREMENT") == "1"
}

// startupMeasurementUnavailableReason is a closed, content-free vocabulary
// for a requested boundary that an owner did not observe. Do not substitute a
// timeout, process state, mapped window, arbitrary app-ready text, or a
// present return for one of these unavailable sources.
type startupMeasurementUnavailableReason uint8

const (
	startupMeasurementAvailable startupMeasurementUnavailableReason = iota
	startupMeasurementDisabled
	startupMeasurementX11MapNotObserved
	startupMeasurementEngineHomeModelNotObserved
	startupMeasurementEngineHomeReadyNotObserved
	startupMeasurementExternalVisualHomeAcceptanceRequired
	startupMeasurementPredeclaredScrollNotObserved
	startupMeasurementX11PostScrollDrawableUnavailable
	startupMeasurementPostScrollDrawableUpdateNotObserved
	startupMeasurementClockOrderInvalid
)

func (r startupMeasurementUnavailableReason) String() string {
	switch r {
	case startupMeasurementAvailable:
		return "available"
	case startupMeasurementDisabled:
		return "disabled"
	case startupMeasurementX11MapNotObserved:
		return "x11_map_not_observed"
	case startupMeasurementEngineHomeModelNotObserved:
		return "engine_home_model_not_observed"
	case startupMeasurementEngineHomeReadyNotObserved:
		return "engine_home_ready_not_observed"
	case startupMeasurementExternalVisualHomeAcceptanceRequired:
		return "external_visual_home_acceptance_required"
	case startupMeasurementPredeclaredScrollNotObserved:
		return "predeclared_scroll_not_observed"
	case startupMeasurementX11PostScrollDrawableUnavailable:
		return "x11_post_scroll_drawable_unavailable"
	case startupMeasurementPostScrollDrawableUpdateNotObserved:
		return "post_scroll_drawable_update_not_observed"
	case startupMeasurementClockOrderInvalid:
		return "clock_order_invalid"
	default:
		return "unknown"
	}
}

// startupMeasurementSpan is a single duration for a bounded arm. It never
// releases an absolute clock value; Unavailable is always one of the fixed
// reasons above.
type startupMeasurementSpan struct {
	Available   bool
	DurationNS  uint64
	Unavailable startupMeasurementUnavailableReason
}

// startupMeasurementLogSpan keeps the emitted reason human-readable while
// retaining the recorder's closed enum internally. Its string field is always
// produced by startupMeasurementUnavailableReason.String; no owner input can
// enter it.
type startupMeasurementLogSpan struct {
	Available         bool
	DurationNS        uint64
	UnavailableReason string
}

func (s startupMeasurementSpan) logValue() startupMeasurementLogSpan {
	return startupMeasurementLogSpan{
		Available:         s.Available,
		DurationNS:        s.DurationNS,
		UnavailableReason: s.Unavailable.String(),
	}
}

// startupMeasurementSummary contains exactly one aggregate result for each
// boundary, not a sample stream. Engine Home is deliberately not called
// visible Home: the independent X11 visual acceptance still owns that claim.
type startupMeasurementSummary struct {
	RuntimeLaunchToX11Map              startupMeasurementSpan
	RuntimeLaunchToEngineHomeModel     startupMeasurementSpan
	RuntimeLaunchToEngineHome          startupMeasurementSpan
	VisibleHome                        startupMeasurementSpan
	InputToFirstObservedDrawableUpdate startupMeasurementSpan
}

// startupMeasurement keeps a fixed set of private boundary clocks for one
// launch arm. It is safe for the X11, JNI, and Runtime owners to report from
// their existing threads, but it neither subscribes to nor polls them. That
// separation prevents Runtime from inventing a MapNotify, Home, input, or
// redraw event from a weaker signal.
type startupMeasurement struct {
	mu      sync.Mutex
	enabled bool
	started time.Time

	x11Map            time.Time
	engineHomeModel   time.Time
	engineHomeReady   time.Time
	predeclaredScroll time.Time
	drawableUpdate    time.Time
	// drawableAvailabilityKnown is set only by X11's exact opt-in edge
	// registration. A failed XDamage setup is never replaced with Expose,
	// Present, or an input-pump wake.
	drawableAvailabilityKnown bool
	drawableAvailable         bool
}

// newStartupMeasurement allocates no observer work for an ordinary launch.
// A Launch owner should create it at the earliest Runtime lifecycle point it
// owns, then call Log exactly once when the arm ends. Its start is deliberately
// labelled Runtime launch, not process exec: only a test-owned parent
// supervisor can honestly measure before the child execs.
func newStartupMeasurement(getenv func(string) string, started time.Time) *startupMeasurement {
	if !startupMeasurementRequested(getenv) {
		return nil
	}
	return &startupMeasurement{enabled: true, started: started}
}

// noteX11Map accepts only X11's MapNotify edge for the owned client. Window
// construction, XID allocation, refresh wakeups, and a mapped-child overlay
// are not substitutes.
func (m *startupMeasurement) noteX11Map(at time.Time) {
	if m == nil || at.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && m.x11Map.IsZero() {
		m.x11Map = at
	}
}

// noteEngineHomeModel accepts only the exact JNI
// NativeHelper.gameActivity_onGameLoaded(0) callback. The caller must classify
// zero before invoking this method and must never pass a place ID here.
func (m *startupMeasurement) noteEngineHomeModel(at time.Time) {
	if m == nil || at.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && m.engineHomeModel.IsZero() {
		m.engineHomeModel = at
	}
}

// noteEngineHomeReady accepts a JNI owner-established fixed Home-ready enum.
// It intentionally takes no step string: Runtime must not read, retain, or
// publish arbitrary onAppReady text to obtain this boundary.
func (m *startupMeasurement) noteEngineHomeReady(at time.Time) {
	if m == nil || at.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && m.engineHomeReady.IsZero() {
		m.engineHomeReady = at
	}
}

// notePredeclaredScrollDispatch accepts one test-owner declared non-text,
// vertical-scroll dispatch after separate visual Home acceptance. It retains
// neither wheel payload nor coordinates and does not dispatch any input.
func (m *startupMeasurement) notePredeclaredScrollDispatch(at time.Time) {
	if m == nil || at.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && m.predeclaredScroll.IsZero() {
		m.predeclaredScroll = at
	}
}

// notePostScrollDrawableUpdate accepts one owner-established drawable update
// that was observed after the declared scroll. A mapped window, event-pump
// wake, timeout, or uncorrelated present must never call this method.
func (m *startupMeasurement) notePostScrollDrawableUpdate(at time.Time) {
	if m == nil || at.IsZero() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || !m.drawableUpdate.IsZero() || m.predeclaredScroll.IsZero() || at.Before(m.predeclaredScroll) {
		return
	}
	m.drawableUpdate = at
}

func (m *startupMeasurement) notePostScrollDrawableAvailability(available bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled {
		m.drawableAvailabilityKnown = true
		m.drawableAvailable = available
	}
}

func startupMeasurementDuration(start, end time.Time, missing startupMeasurementUnavailableReason) startupMeasurementSpan {
	if end.IsZero() {
		return startupMeasurementSpan{Unavailable: missing}
	}
	if start.IsZero() || end.Before(start) {
		return startupMeasurementSpan{Unavailable: startupMeasurementClockOrderInvalid}
	}
	return startupMeasurementSpan{Available: true, DurationNS: uint64(end.Sub(start))}
}

func (m *startupMeasurement) summary() startupMeasurementSummary {
	if m == nil {
		span := startupMeasurementSpan{Unavailable: startupMeasurementDisabled}
		return startupMeasurementSummary{
			RuntimeLaunchToX11Map:              span,
			RuntimeLaunchToEngineHomeModel:     span,
			RuntimeLaunchToEngineHome:          span,
			VisibleHome:                        span,
			InputToFirstObservedDrawableUpdate: span,
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		span := startupMeasurementSpan{Unavailable: startupMeasurementDisabled}
		return startupMeasurementSummary{
			RuntimeLaunchToX11Map:              span,
			RuntimeLaunchToEngineHomeModel:     span,
			RuntimeLaunchToEngineHome:          span,
			VisibleHome:                        span,
			InputToFirstObservedDrawableUpdate: span,
		}
	}

	summary := startupMeasurementSummary{
		RuntimeLaunchToX11Map:          startupMeasurementDuration(m.started, m.x11Map, startupMeasurementX11MapNotObserved),
		RuntimeLaunchToEngineHomeModel: startupMeasurementDuration(m.started, m.engineHomeModel, startupMeasurementEngineHomeModelNotObserved),
		VisibleHome:                    startupMeasurementSpan{Unavailable: startupMeasurementExternalVisualHomeAcceptanceRequired},
	}
	if m.engineHomeModel.IsZero() {
		summary.RuntimeLaunchToEngineHome = startupMeasurementSpan{Unavailable: startupMeasurementEngineHomeModelNotObserved}
	} else if m.engineHomeReady.IsZero() {
		summary.RuntimeLaunchToEngineHome = startupMeasurementSpan{Unavailable: startupMeasurementEngineHomeReadyNotObserved}
	} else {
		home := m.engineHomeModel
		if m.engineHomeReady.After(home) {
			home = m.engineHomeReady
		}
		summary.RuntimeLaunchToEngineHome = startupMeasurementDuration(m.started, home, startupMeasurementClockOrderInvalid)
	}
	if m.predeclaredScroll.IsZero() {
		summary.InputToFirstObservedDrawableUpdate = startupMeasurementSpan{Unavailable: startupMeasurementPredeclaredScrollNotObserved}
	} else if m.drawableAvailabilityKnown && !m.drawableAvailable {
		summary.InputToFirstObservedDrawableUpdate = startupMeasurementSpan{Unavailable: startupMeasurementX11PostScrollDrawableUnavailable}
	} else {
		summary.InputToFirstObservedDrawableUpdate = startupMeasurementDuration(m.predeclaredScroll, m.drawableUpdate, startupMeasurementPostScrollDrawableUpdateNotObserved)
	}
	return summary
}

// log emits exactly one fixed-shape, aggregate-only arm result. The caller is
// responsible for the lifecycle boundary: Runtime intentionally does not add
// an interval ticker, sleep, input action, or readiness poll.
func (m *startupMeasurement) log(log func(msg string, args ...any)) {
	if m == nil || log == nil {
		return
	}
	summary := m.summary()
	log("startup measurement aggregate",
		"runtimeLaunchToX11Map", summary.RuntimeLaunchToX11Map.logValue(),
		"runtimeLaunchToEngineHomeModel", summary.RuntimeLaunchToEngineHomeModel.logValue(),
		"runtimeLaunchToEngineHome", summary.RuntimeLaunchToEngineHome.logValue(),
		"visibleHome", summary.VisibleHome.logValue(),
		"inputToFirstObservedDrawableUpdate", summary.InputToFirstObservedDrawableUpdate.logValue())
}
