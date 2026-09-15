// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/jni"
)

type startupMeasurementTestSubscription struct {
	fn     jni.StartupHomeListener
	active bool
}

// startupMeasurementTestSubscriptions is deliberately payload-free, matching
// the public JNI observer contract. It lets Runtime pin registration,
// cancellation, snapshot, and stale-callback behavior without reaching into
// JNI's private registries.
type startupMeasurementTestSubscriptions struct {
	mu sync.Mutex

	order        []string
	model        []*startupMeasurementTestSubscription
	ready        []*startupMeasurementTestSubscription
	modelCancels int
	readyCancels int
}

func (s *startupMeasurementTestSubscriptions) subscribeModel(fn jni.StartupHomeListener) func() {
	return s.subscribe("model", &s.model, fn)
}

func (s *startupMeasurementTestSubscriptions) subscribeReady(fn jni.StartupHomeListener) func() {
	return s.subscribe("ready", &s.ready, fn)
}

func (s *startupMeasurementTestSubscriptions) subscribe(kind string, entries *[]*startupMeasurementTestSubscription, fn jni.StartupHomeListener) func() {
	s.mu.Lock()
	s.order = append(s.order, kind)
	entry := &startupMeasurementTestSubscription{fn: fn, active: true}
	*entries = append(*entries, entry)
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if entry.active {
				entry.active = false
				if kind == "model" {
					s.modelCancels++
				} else {
					s.readyCancels++
				}
			}
			s.mu.Unlock()
		})
	}
}

func (s *startupMeasurementTestSubscriptions) emitModel(event jni.StartupHomeEvent) {
	s.emit(s.model, event)
}

func (s *startupMeasurementTestSubscriptions) emitReady(event jni.StartupHomeEvent) {
	s.emit(s.ready, event)
}

func (s *startupMeasurementTestSubscriptions) emit(entries []*startupMeasurementTestSubscription, event jni.StartupHomeEvent) {
	s.mu.Lock()
	listeners := make([]jni.StartupHomeListener, 0, len(entries))
	for _, entry := range entries {
		if entry.active {
			listeners = append(listeners, entry.fn)
		}
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener(event)
	}
}

func (s *startupMeasurementTestSubscriptions) staleModelListener() jni.StartupHomeListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.model) == 0 {
		return nil
	}
	return s.model[len(s.model)-1].fn
}

func (s *startupMeasurementTestSubscriptions) staleReadyListener() jni.StartupHomeListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ready) == 0 {
		return nil
	}
	return s.ready[len(s.ready)-1].fn
}

// startupMeasurementTestScrollWindowFake models only the narrow sealed X11
// capability contract. completed is deliberately distinct from factoryCalls:
// an unfocused window rejects driver creation before any input dispatch.
type startupMeasurementTestScrollWindowFake struct {
	mu sync.Mutex

	focused      bool
	declareOK    bool
	factoryCalls int
	attempts     int
	completed    int
	declarations int
	order        []string
	onDispatch   func()
}

type startupMeasurementTestScrollCapabilityFake struct {
	mu sync.Mutex

	window *startupMeasurementTestScrollWindowFake
	closed bool
}

func (w *startupMeasurementTestScrollWindowFake) newCapability() startupMeasurementTestScrollCapability {
	w.mu.Lock()
	w.factoryCalls++
	if !w.focused {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()
	return &startupMeasurementTestScrollCapabilityFake{window: w}
}

func (c *startupMeasurementTestScrollCapabilityFake) Dispatch() bool {
	if c == nil || c.window == nil {
		return false
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false
	}
	c.closed = true
	c.mu.Unlock()
	w := c.window
	w.mu.Lock()
	w.attempts++
	w.completed++
	w.order = append(w.order, "dispatch")
	hook := w.onDispatch
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true
}

func (c *startupMeasurementTestScrollCapabilityFake) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
}

func (w *startupMeasurementTestScrollWindowFake) DeclareStartupMeasurementVerticalScrollDispatch() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.declarations++
	w.order = append(w.order, "declare")
	return w.declareOK
}

func (w *startupMeasurementTestScrollWindowFake) snapshot() (factoryCalls, attempts, completed, declarations int, order []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.factoryCalls, w.attempts, w.completed, w.declarations, append([]string(nil), w.order...)
}

func startupMeasurementTestScrollGetenv(measurement, scroll string) func(string) string {
	return func(name string) string {
		switch name {
		case "TIPSY_STARTUP_MEASUREMENT":
			return measurement
		case startupMeasurementTestScrollEnvironment:
			return scroll
		default:
			return ""
		}
	}
}

func attachStartupMeasurementTestScrollWindow(t *testing.T, arm *startupMeasurementArm, window *startupMeasurementTestScrollWindowFake, mapped bool) {
	t.Helper()
	var mapNotify func()
	if available := arm.attachX11WithSubscription(func(mapCallback, drawableCallback func()) (func(), bool) {
		mapNotify = mapCallback
		return func() {}, true
	}); !available {
		t.Fatal("test X11 source unexpectedly reported unavailable drawable edge")
	}
	if window != nil {
		arm.attachTestScrollDriver(window.newCapability, func() bool {
			return arm.declarePredeclaredScrollDispatch(window)
		})
	}
	if mapped {
		if mapNotify == nil {
			t.Fatal("test X11 source did not receive MapNotify callback")
		}
		mapNotify()
	}
}

func TestStartupMeasurementRequiresExactOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", "2"} {
		value := value
		if startupMeasurementRequested(func(string) string { return value }) {
			t.Errorf("value %q enabled startup measurement", value)
		}
	}
	if !startupMeasurementRequested(func(name string) string {
		if name != "TIPSY_STARTUP_MEASUREMENT" {
			t.Fatalf("queried unexpected environment key %q", name)
		}
		return "1"
	}) {
		t.Fatal("exact opt-in did not enable startup measurement")
	}
	if startupMeasurementRequested(nil) {
		t.Fatal("nil environment reader enabled startup measurement")
	}
	if got := newStartupMeasurement(func(string) string { return "" }, time.Now()); got != nil {
		t.Fatal("default-off launch allocated a startup measurement")
	}
}

func TestStartupMeasurementArmIsDefaultOffWithoutJNISubscriptions(t *testing.T) {
	source := &startupMeasurementTestSubscriptions{}
	arm := newStartupMeasurementArmWithSubscriptions(func(string) string { return "" }, time.Now, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
	if arm != nil {
		t.Fatal("default-off launch allocated a startup measurement arm")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if got := source.order; len(got) != 0 {
		t.Fatalf("default-off launch registered JNI subscriptions: %v", got)
	}
}

func TestStartupMeasurementTestScrollRequiresBothExactGates(t *testing.T) {
	for _, tc := range []struct {
		name        string
		measurement string
		scroll      string
		wantArm     bool
		wantDriver  bool
	}{
		{name: "both exact", measurement: "1", scroll: "1", wantArm: true, wantDriver: true},
		{name: "aggregate only", measurement: "1", scroll: "", wantArm: true},
		{name: "test flag without aggregate arm", measurement: "", scroll: "1"},
		{name: "non exact test flag", measurement: "1", scroll: "true", wantArm: true},
		{name: "non exact aggregate flag", measurement: "true", scroll: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &startupMeasurementTestSubscriptions{}
			getenv := startupMeasurementTestScrollGetenv(tc.measurement, tc.scroll)
			if got := startupMeasurementTestScrollRequested(getenv); got != tc.wantDriver {
				t.Fatalf("test-scroll request = %v, want %v", got, tc.wantDriver)
			}
			arm := newStartupMeasurementArmWithSubscriptions(getenv, time.Now, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
			if (arm != nil) != tc.wantArm {
				t.Fatalf("arm presence = %v, want %v", arm != nil, tc.wantArm)
			}
			if arm != nil {
				if (arm.testScroll != nil) != tc.wantDriver {
					t.Fatalf("test driver presence = %v, want %v", arm.testScroll != nil, tc.wantDriver)
				}
				arm.teardown()
			}
		})
	}
}

func TestStartupMeasurementTestScrollDispatchesOnceAfterExactHomeReady(t *testing.T) {
	source := &startupMeasurementTestSubscriptions{}
	arm := newStartupMeasurementArmWithSubscriptions(startupMeasurementTestScrollGetenv("1", "1"), time.Now, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
	if arm == nil || arm.testScroll == nil {
		t.Fatal("double opt-in did not create test scroll driver")
	}
	window := &startupMeasurementTestScrollWindowFake{focused: true, declareOK: true}
	attachStartupMeasurementTestScrollWindow(t, arm, window, true)

	// Another fixed enum and the model subscriber cannot substitute for the
	// one exact no-payload HomeReady source.
	source.emitModel(jni.StartupHomeDataModel)
	source.emitReady(jni.StartupHomeDataModel)
	if factoryCalls, attempts, completed, declarations, _ := window.snapshot(); factoryCalls != 0 || attempts != 0 || completed != 0 || declarations != 0 {
		t.Fatalf("non-HomeReady event created/dispatched test scroll: factory/attempts/completed/declarations=%d/%d/%d/%d", factoryCalls, attempts, completed, declarations)
	}

	source.emitReady(jni.StartupHomeReady)
	source.emitReady(jni.StartupHomeReady)
	factoryCalls, attempts, completed, declarations, order := window.snapshot()
	if factoryCalls != 1 || attempts != 1 || completed != 1 || declarations != 1 {
		t.Fatalf("one-shot factory/dispatch/declare = %d/%d/%d/%d, want 1/1/1/1", factoryCalls, attempts, completed, declarations)
	}
	if want := []string{"dispatch", "declare"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("dispatch/declaration order = %v, want %v", order, want)
	}
	if got := arm.m.summary().InputToFirstObservedDrawableUpdate.Unavailable; got != startupMeasurementPostScrollDrawableUpdateNotObserved {
		t.Fatalf("successful declaration did not record predeclared scroll: %q", got)
	}
	arm.teardown()
}

func TestStartupMeasurementTestScrollFailsClosedWithoutHomeReadyWindowMapOrFocus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		window    *startupMeasurementTestScrollWindowFake
		mapped    bool
		homeReady bool
		factory   int
		attempts  int
	}{
		{name: "no HomeReady", window: &startupMeasurementTestScrollWindowFake{focused: true, declareOK: true}, mapped: true},
		{name: "no attached window", mapped: true, homeReady: true},
		{name: "no owned MapNotify", window: &startupMeasurementTestScrollWindowFake{focused: true, declareOK: true}, homeReady: true},
		// The X11 owner rejects capability creation before it can dispatch any
		// input because focus is not its exact owned client.
		{name: "unfocused owned window", window: &startupMeasurementTestScrollWindowFake{declareOK: true}, mapped: true, homeReady: true, factory: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &startupMeasurementTestSubscriptions{}
			arm := newStartupMeasurementArmWithSubscriptions(startupMeasurementTestScrollGetenv("1", "1"), time.Now, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
			if arm == nil || arm.testScroll == nil {
				t.Fatal("double opt-in did not create test scroll driver")
			}
			attachStartupMeasurementTestScrollWindow(t, arm, tc.window, tc.mapped)
			if tc.homeReady {
				source.emitReady(jni.StartupHomeReady)
			}
			if tc.window != nil {
				factoryCalls, attempts, completed, declarations, _ := tc.window.snapshot()
				if factoryCalls != tc.factory || attempts != tc.attempts || completed != 0 || declarations != 0 {
					t.Fatalf("fail-closed result factory/attempts/completed/declarations=%d/%d/%d/%d, want %d/%d/0/0", factoryCalls, attempts, completed, declarations, tc.factory, tc.attempts)
				}
			}
			arm.teardown()
		})
	}
}

func TestStartupMeasurementTestScrollCancellationAndReentrancy(t *testing.T) {
	t.Run("cancelled arm rejects stale HomeReady", func(t *testing.T) {
		source := &startupMeasurementTestSubscriptions{}
		arm := newStartupMeasurementArmWithSubscriptions(startupMeasurementTestScrollGetenv("1", "1"), time.Now, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
		window := &startupMeasurementTestScrollWindowFake{focused: true, declareOK: true}
		attachStartupMeasurementTestScrollWindow(t, arm, window, true)
		stale := source.staleReadyListener()
		if stale == nil {
			t.Fatal("missing ready listener")
		}
		arm.teardown()
		stale(jni.StartupHomeReady)
		if factoryCalls, attempts, completed, declarations, _ := window.snapshot(); factoryCalls != 0 || attempts != 0 || completed != 0 || declarations != 0 {
			t.Fatalf("stale HomeReady created/dispatched after teardown: %d/%d/%d/%d", factoryCalls, attempts, completed, declarations)
		}
		source.mu.Lock()
		defer source.mu.Unlock()
		if source.readyCancels != 1 {
			t.Fatalf("ready cancellations = %d, want 1", source.readyCancels)
		}
	})

	t.Run("teardown from completed-dispatch hook suppresses declaration", func(t *testing.T) {
		source := &startupMeasurementTestSubscriptions{}
		var logs int
		arm := newStartupMeasurementArmWithSubscriptions(startupMeasurementTestScrollGetenv("1", "1"), time.Now, func(string, ...any) { logs++ }, source.subscribeModel, source.subscribeReady)
		window := &startupMeasurementTestScrollWindowFake{focused: true, declareOK: true}
		window.onDispatch = arm.teardown
		attachStartupMeasurementTestScrollWindow(t, arm, window, true)
		source.emitReady(jni.StartupHomeReady)
		factoryCalls, attempts, completed, declarations, order := window.snapshot()
		if factoryCalls != 1 || attempts != 1 || completed != 1 || declarations != 0 || !reflect.DeepEqual(order, []string{"dispatch"}) {
			t.Fatalf("re-entrant dispatch result = factory/attempts/completed/declarations/order %d/%d/%d/%d/%v", factoryCalls, attempts, completed, declarations, order)
		}
		if logs != 1 {
			t.Fatalf("re-entrant teardown logs = %d, want 1", logs)
		}
	})
}

func TestStartupMeasurementTestScrollConcurrentHomeReadyAndTeardown(t *testing.T) {
	source := &startupMeasurementTestSubscriptions{}
	var logs atomic.Int64
	arm := newStartupMeasurementArmWithSubscriptions(startupMeasurementTestScrollGetenv("1", "1"), time.Now, func(string, ...any) { logs.Add(1) }, source.subscribeModel, source.subscribeReady)
	window := &startupMeasurementTestScrollWindowFake{focused: true, declareOK: true}
	attachStartupMeasurementTestScrollWindow(t, arm, window, true)

	const workers = 48
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%4 == 0 {
				arm.teardown()
				return
			}
			source.emitReady(jni.StartupHomeReady)
		}(i)
	}
	close(start)
	wg.Wait()
	arm.teardown()
	if factoryCalls, attempts, completed, declarations, order := window.snapshot(); factoryCalls > 1 || attempts > 1 || completed > 1 || declarations > 1 || len(order) > 2 {
		t.Fatalf("concurrent one-shot violated: factory/attempts/completed/declarations/order=%d/%d/%d/%d/%v", factoryCalls, attempts, completed, declarations, order)
	}
	if got := logs.Load(); got != 1 {
		t.Fatalf("concurrent arm logs = %d, want 1", got)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.modelCancels != 1 || source.readyCancels != 1 {
		t.Fatalf("concurrent cancellation model/ready = %d/%d, want 1/1", source.modelCancels, source.readyCancels)
	}
}

func TestStartupMeasurementArmSubscribesInOrderAndLogsAtTeardown(t *testing.T) {
	start := time.Unix(4_000, 0)
	times := []time.Time{start, start.Add(11 * time.Millisecond), start.Add(19 * time.Millisecond)}
	next := 0
	now := func() time.Time {
		got := times[next]
		next++
		return got
	}
	source := &startupMeasurementTestSubscriptions{}
	var logs int
	arm := newStartupMeasurementArmWithSubscriptions(func(string) string { return "1" }, now, func(string, ...any) { logs++ }, source.subscribeModel, source.subscribeReady)
	if arm == nil {
		t.Fatal("exact opt-in did not arm")
	}
	source.emitReady(jni.StartupHomeReady)
	source.emitModel(jni.StartupHomeDataModel)
	if logs != 0 {
		t.Fatalf("arm logged before lifecycle teardown: %d", logs)
	}
	source.mu.Lock()
	if want := []string{"model", "ready"}; !reflect.DeepEqual(source.order, want) {
		source.mu.Unlock()
		t.Fatalf("subscription order = %v, want %v", source.order, want)
	}
	source.mu.Unlock()

	summary := arm.m.summary()
	if got, want := summary.RuntimeLaunchToEngineHomeModel.DurationNS, uint64(19*time.Millisecond); !summary.RuntimeLaunchToEngineHomeModel.Available || got != want {
		t.Fatalf("Home DataModel receipt = %+v, want %dns", summary.RuntimeLaunchToEngineHomeModel, want)
	}
	// Ready arrived first, so the overall engine-Home boundary remains the
	// later Home DataModel receipt rather than assuming callback order.
	if got, want := summary.RuntimeLaunchToEngineHome.DurationNS, uint64(19*time.Millisecond); !summary.RuntimeLaunchToEngineHome.Available || got != want {
		t.Fatalf("engine Home receipt = %+v, want %dns", summary.RuntimeLaunchToEngineHome, want)
	}
	arm.teardown()
	if logs != 1 {
		t.Fatalf("teardown logs = %d, want 1", logs)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.modelCancels != 1 || source.readyCancels != 1 {
		t.Fatalf("cancellations model/ready = %d/%d, want 1/1", source.modelCancels, source.readyCancels)
	}
}

func TestStartupMeasurementArmTeardownIsReentrantAndRejectsStaleCallbacks(t *testing.T) {
	start := time.Unix(5_000, 0)
	source := &startupMeasurementTestSubscriptions{}
	var (
		arm    *startupMeasurementArm
		clocks int
		logs   int
	)
	now := func() time.Time {
		clocks++
		if clocks == 2 {
			// Simulate the lifecycle owner ending an arm from inside a
			// snapshotted callback. The callback must neither deadlock nor
			// record a post-close boundary.
			arm.teardown()
		}
		return start.Add(time.Duration(clocks) * time.Millisecond)
	}
	arm = newStartupMeasurementArmWithSubscriptions(func(string) string { return "1" }, now, func(string, ...any) { logs++ }, source.subscribeModel, source.subscribeReady)
	saved := source.staleModelListener()
	source.emitModel(jni.StartupHomeDataModel)
	if logs != 1 {
		t.Fatalf("re-entrant teardown logs = %d, want 1", logs)
	}
	if got := arm.m.summary().RuntimeLaunchToEngineHomeModel.Unavailable; got != startupMeasurementEngineHomeModelNotObserved {
		t.Fatalf("re-entrant callback recorded Home DataModel: %q", got)
	}
	if saved == nil {
		t.Fatal("missing saved callback")
	}
	saved(jni.StartupHomeDataModel)
	arm.teardown()
	if logs != 1 {
		t.Fatalf("stale/repeated teardown logged %d aggregates, want 1", logs)
	}
}

func TestStartupMeasurementArmDoesNotLeakStaleSessionCallbacks(t *testing.T) {
	start := time.Unix(6_000, 0)
	source := &startupMeasurementTestSubscriptions{}
	first := newStartupMeasurementArmWithSubscriptions(func(string) string { return "1" }, func() time.Time { return start }, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
	stale := source.staleModelListener()
	first.teardown()
	second := newStartupMeasurementArmWithSubscriptions(func(string) string { return "1" }, func() time.Time { return start.Add(time.Millisecond) }, func(string, ...any) {}, source.subscribeModel, source.subscribeReady)
	if stale == nil {
		t.Fatal("missing first-session callback")
	}
	stale(jni.StartupHomeDataModel)
	if got := second.m.summary().RuntimeLaunchToEngineHomeModel.Unavailable; got != startupMeasurementEngineHomeModelNotObserved {
		t.Fatalf("stale first-session callback leaked into second arm: %q", got)
	}
	source.emitModel(jni.StartupHomeDataModel)
	if !second.m.summary().RuntimeLaunchToEngineHomeModel.Available {
		t.Fatal("active second-session subscription did not receive Home DataModel")
	}
	second.teardown()
}

func TestStartupMeasurementArmUsesOnlyAttachedX11Edges(t *testing.T) {
	start := time.Unix(7_000, 0)
	var clocks int
	now := func() time.Time {
		clocks++
		return start.Add(time.Duration(clocks) * time.Millisecond)
	}
	jniSource := &startupMeasurementTestSubscriptions{}
	var (
		mapNotify func()
		drawable  func()
		cancels   int
		logs      int
	)
	arm := newStartupMeasurementArmWithSubscriptions(func(string) string { return "1" }, now, func(string, ...any) { logs++ }, jniSource.subscribeModel, jniSource.subscribeReady)
	if arm == nil {
		t.Fatal("exact opt-in did not arm")
	}
	if available := arm.attachX11WithSubscription(func(mapCallback, drawableCallback func()) (func(), bool) {
		mapNotify, drawable = mapCallback, drawableCallback
		return func() { cancels++ }, true
	}); !available {
		t.Fatal("exact X11 drawable availability was not preserved")
	}
	if mapNotify == nil || drawable == nil {
		t.Fatal("X11 subscription did not receive both authoritative callbacks")
	}
	mapNotify()
	// This direct note models the separately owned, predeclared non-text
	// dispatch only. It is not a captured wheel event or synthetic input.
	arm.m.notePredeclaredScrollDispatch(start.Add(3 * time.Millisecond))
	drawable()
	summary := arm.m.summary()
	if !summary.RuntimeLaunchToX11Map.Available {
		t.Fatalf("MapNotify callback did not record: %+v", summary.RuntimeLaunchToX11Map)
	}
	if !summary.InputToFirstObservedDrawableUpdate.Available {
		t.Fatalf("post-scroll drawable callback did not record: %+v", summary.InputToFirstObservedDrawableUpdate)
	}
	arm.teardown()
	if cancels != 1 || logs != 1 {
		t.Fatalf("X11 teardown cancels/logs = %d/%d, want 1/1", cancels, logs)
	}
	// A snapshotted X11 callback after cancellation is ignored by the closed
	// arm and cannot create another aggregate or mutate the final summary.
	mapNotify()
	drawable()
	if logs != 1 || !reflect.DeepEqual(summary, arm.m.summary()) {
		t.Fatalf("stale X11 callback changed final arm: logs=%d summary=%+v", logs, arm.m.summary())
	}
}

func TestStartupMeasurementArmConcurrentEventAndTeardown(t *testing.T) {
	source := &startupMeasurementTestSubscriptions{}
	var logs atomic.Int64
	arm := newStartupMeasurementArmWithSubscriptions(func(string) string { return "1" }, time.Now, func(string, ...any) { logs.Add(1) }, source.subscribeModel, source.subscribeReady)
	if arm == nil {
		t.Fatal("exact opt-in did not arm")
	}
	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				arm.teardown()
				return
			}
			source.emitModel(jni.StartupHomeDataModel)
			source.emitReady(jni.StartupHomeReady)
		}(i)
	}
	wg.Wait()
	arm.teardown()
	if got := logs.Load(); got != 1 {
		t.Fatalf("concurrent arm logged %d aggregates, want 1", got)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.modelCancels != 1 || source.readyCancels != 1 {
		t.Fatalf("concurrent cancellation model/ready = %d/%d, want 1/1", source.modelCancels, source.readyCancels)
	}
}

func TestStartupMeasurementRecordsFixedAuthoritativeBoundaries(t *testing.T) {
	start := time.Unix(1_000, 0)
	m := newStartupMeasurement(func(string) string { return "1" }, start)
	if m == nil {
		t.Fatal("opted-in measurement was nil")
	}
	m.noteX11Map(start.Add(11 * time.Millisecond))
	m.noteEngineHomeModel(start.Add(21 * time.Millisecond))
	m.noteEngineHomeReady(start.Add(34 * time.Millisecond))
	m.notePredeclaredScrollDispatch(start.Add(45 * time.Millisecond))
	m.notePostScrollDrawableUpdate(start.Add(53 * time.Millisecond))

	summary := m.summary()
	for _, tc := range []struct {
		name string
		got  startupMeasurementSpan
		want startupMeasurementSpan
	}{
		{"map", summary.RuntimeLaunchToX11Map, startupMeasurementSpan{Available: true, DurationNS: uint64(11 * time.Millisecond)}},
		{"home model", summary.RuntimeLaunchToEngineHomeModel, startupMeasurementSpan{Available: true, DurationNS: uint64(21 * time.Millisecond)}},
		{"engine home", summary.RuntimeLaunchToEngineHome, startupMeasurementSpan{Available: true, DurationNS: uint64(34 * time.Millisecond)}},
		{"scroll update", summary.InputToFirstObservedDrawableUpdate, startupMeasurementSpan{Available: true, DurationNS: uint64(8 * time.Millisecond)}},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %+v, want %+v", tc.name, tc.got, tc.want)
		}
	}
	if got, want := summary.VisibleHome.Unavailable, startupMeasurementExternalVisualHomeAcceptanceRequired; got != want {
		t.Fatalf("visible Home = %q, want %q", got, want)
	}
}

func TestStartupMeasurementNeverFillsMissingBoundariesFromWeakerSignals(t *testing.T) {
	start := time.Unix(2_000, 0)
	m := newStartupMeasurement(func(string) string { return "1" }, start)
	// An update before the predeclared input is discarded rather than being
	// misattributed as a response. A Home-ready edge also cannot replace the
	// required Home DataModel announcement.
	m.noteEngineHomeReady(start.Add(time.Millisecond))
	m.notePostScrollDrawableUpdate(start.Add(2 * time.Millisecond))
	summary := m.summary()
	if got, want := summary.RuntimeLaunchToX11Map.Unavailable, startupMeasurementX11MapNotObserved; got != want {
		t.Fatalf("map unavailable = %q, want %q", got, want)
	}
	if got, want := summary.RuntimeLaunchToEngineHome.Unavailable, startupMeasurementEngineHomeModelNotObserved; got != want {
		t.Fatalf("engine Home unavailable = %q, want %q", got, want)
	}
	if got, want := summary.InputToFirstObservedDrawableUpdate.Unavailable, startupMeasurementPredeclaredScrollNotObserved; got != want {
		t.Fatalf("scroll/update unavailable = %q, want %q", got, want)
	}

	m.noteEngineHomeModel(start.Add(3 * time.Millisecond))
	m.notePredeclaredScrollDispatch(start.Add(5 * time.Millisecond))
	// The discarded pre-scroll update must not be recovered after the input.
	summary = m.summary()
	if got, want := summary.InputToFirstObservedDrawableUpdate.Unavailable, startupMeasurementPostScrollDrawableUpdateNotObserved; got != want {
		t.Fatalf("post-scroll unavailable = %q, want %q", got, want)
	}
}

func TestStartupMeasurementLogsOneAggregateWithoutAbsoluteClock(t *testing.T) {
	start := time.Unix(3_000, 0)
	m := newStartupMeasurement(func(string) string { return "1" }, start)
	m.noteX11Map(start.Add(time.Millisecond))
	var (
		calls int
		msg   string
		args  []any
	)
	m.log(func(gotMsg string, gotArgs ...any) {
		calls++
		msg = gotMsg
		args = append([]any(nil), gotArgs...)
	})
	if calls != 1 || msg != "startup measurement aggregate" {
		t.Fatalf("aggregate log = calls=%d msg=%q", calls, msg)
	}
	for i := 0; i+1 < len(args); i += 2 {
		if _, ok := args[i+1].(time.Time); ok {
			t.Fatalf("absolute time escaped in %q", args[i])
		}
	}
	mapValue, ok := diagnosticAttr(t, args, "runtimeLaunchToX11Map").(startupMeasurementLogSpan)
	if !ok || !mapValue.Available || mapValue.DurationNS != uint64(time.Millisecond) || mapValue.UnavailableReason != "available" {
		t.Fatalf("map aggregate = %+v", mapValue)
	}
	visible, ok := diagnosticAttr(t, args, "visibleHome").(startupMeasurementLogSpan)
	if !ok || visible.UnavailableReason != "external_visual_home_acceptance_required" {
		t.Fatalf("visible Home aggregate = %+v", visible)
	}
}

func TestSummarizeVulkanPresentGapsIsAggregateOnly(t *testing.T) {
	batch := android.VulkanPresentTimingBatch{Samples: []android.VulkanPresentTimingSample{
		{Sequence: 1, MonotonicNS: 1_000_000},
		{Sequence: 2, MonotonicNS: 5_000_000},
		{Sequence: 3, MonotonicNS: 14_000_000},
		{Sequence: 4, MonotonicNS: 31_000_000},
		{Sequence: 5, MonotonicNS: 65_000_000},
		{Sequence: 6, MonotonicNS: 64_000_000},
	}}
	got := summarizeVulkanPresentGaps(batch)
	want := vulkanPresentGapStatistics{
		Eligible: true, Samples: 6, Gaps: 4, P50NS: 9_000_000, P95NS: 34_000_000, P99NS: 34_000_000, MaxNS: 34_000_000,
		Over8MS: 3, Over16MS: 2, Over33MS: 1, NonMonotonicAdjacent: 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gap aggregate = %+v, want %+v", got, want)
	}
	if got := summarizeVulkanPresentGaps(android.VulkanPresentTimingBatch{Samples: batch.Samples[:1]}); got.Eligible || got.Gaps != 0 || got.P50NS != 0 || got.Unavailable != vulkanPresentGapInsufficientSuccessfulReturns {
		t.Fatalf("single return was eligible: %+v", got)
	}
	if got := summarizeVulkanPresentGaps(android.VulkanPresentTimingBatch{Samples: batch.Samples, Overwritten: 3}); got.Eligible || got.Gaps != 0 || got.Unavailable != vulkanPresentGapObserverOverwritten || got.Overwritten != 3 {
		t.Fatalf("overwritten batch was eligible: %+v", got)
	}
}

func TestLogVulkanPresentTimingNeverReleasesRawClockOrCursor(t *testing.T) {
	previous := slog.Default()
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logVulkanPresentTiming(android.VulkanPresentTimingBatch{
		Cursor: 99,
		Samples: []android.VulkanPresentTimingSample{
			{Sequence: 98, MonotonicNS: 1_234_567_890},
			{Sequence: 99, MonotonicNS: 1_244_567_890},
		},
	})
	got := output.String()
	for _, forbidden := range []string{"1_234_567_890", "1234567890", "1244567890", "cursor", "monotonic"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("present output exposed %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "Vulkan present return-boundary gap aggregate") || !strings.Contains(got, "eligible=true") || !strings.Contains(got, "unavailableReason=available") || !strings.Contains(got, "p50NS=10000000") {
		t.Fatalf("present output did not contain bounded aggregate: %s", got)
	}
}
