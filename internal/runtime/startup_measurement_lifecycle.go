// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"sync"
	"time"

	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// startupMeasurementArm owns the two composable JNI subscriptions for one
// diagnostic launch arm. It deliberately receives only the fixed enum from
// JNI and takes its own receipt clock; no Java payload, place ID, route, or
// arbitrary AppReady string reaches Runtime.
//
// teardown first closes the arm, then cancels both subscriptions, then emits
// its single aggregate. Closing before cancellation makes an already-snapshotted
// JNI callback harmless, while the arm lock ensures a callback that entered
// before teardown has finished recording before the aggregate is formed.
type startupMeasurementArm struct {
	mu sync.Mutex

	closed bool
	m      *startupMeasurement
	now    func() time.Time
	log    func(msg string, args ...any)

	cancelModel func()
	cancelReady func()
	cancelX11   func()

	// testScroll is a sealed, separately double-opted-in visual-test driver.
	// It is not an input subscriber and it has no normal-launch allocation or
	// behavior. Its sole source event is this arm's existing exact HomeReady
	// subscription.
	testScroll *startupMeasurementTestScrollDriver
}

type startupMeasurementX11Subscribe func(mapNotify, postScrollDrawableUpdate func()) (cancel func(), drawableAvailable bool)

type startupHomeSubscribe func(jni.StartupHomeListener) func()

const startupMeasurementTestScrollEnvironment = "TIPSY_STARTUP_MEASUREMENT_TEST_SCROLL"

// startupMeasurementTestScrollRequested is deliberately stricter than the
// aggregate recorder's opt-in. It gates the one controlled visual-test
// interaction behind two exact values, so neither diagnostics nor an ordinary
// aggregate arm can change client input.
func startupMeasurementTestScrollRequested(getenv func(string) string) bool {
	return startupMeasurementRequested(getenv) && getenv(startupMeasurementTestScrollEnvironment) == "1"
}

// startupMeasurementTestScrollCapability is the sealed X11 interaction
// capability. Its owner accepts no caller-controlled event details: Dispatch
// can send one fixed detent or fail closed, and Close only invalidates an
// unused capability.
type startupMeasurementTestScrollCapability interface {
	Dispatch() bool
	Close()
}

// startupMeasurementTestScrollDeclaration is the existing Runtime/X11
// declaration surface, kept separate from the dispatch capability so tests
// can prove dispatch-to-declaration order without exposing an arbitrary input
// operation.
type startupMeasurementTestScrollDeclaration interface {
	DeclareStartupMeasurementVerticalScrollDispatch() bool
}

// startupMeasurementTestScrollDriver is a sealed one-shot visual-test
// interaction. It never observes a navigator, map query, timer, process
// state, UI string, or input event. The existing fixed JNI HomeReady callback
// is its only trigger; the paired X11 MapNotify callback merely proves the
// same attached owned window observed its authoritative map edge.
type startupMeasurementTestScrollDriver struct {
	mu sync.Mutex

	closed           bool
	attempted        bool
	mapped           bool
	newCapability    func() startupMeasurementTestScrollCapability
	declare          func() bool
	activeCapability startupMeasurementTestScrollCapability
}

func newStartupMeasurementTestScrollDriver(arm *startupMeasurementArm) *startupMeasurementTestScrollDriver {
	if arm == nil {
		return nil
	}
	return &startupMeasurementTestScrollDriver{}
}

func (d *startupMeasurementTestScrollDriver) attachWindow(newCapability func() startupMeasurementTestScrollCapability, declare func() bool) {
	if d == nil || newCapability == nil || declare == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.newCapability = newCapability
		d.declare = declare
	}
}

func (d *startupMeasurementTestScrollDriver) noteX11Map() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.mapped = true
	}
}

// noteHomeReady is called only from the arm's existing no-payload exact
// HomeReady subscriber. The first readiness event consumes this driver's one
// chance for the session. If the matching owned X11 MapNotify or attached
// window is absent, it fails closed rather than retrying from a map, timer, or
// arbitrary UI state. X11 rejects an unfocused/closed/captured window before
// it can complete its fixed dispatch. A declaration follows synchronously and
// only after that dispatch reports success.
func (d *startupMeasurementTestScrollDriver) noteHomeReady() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed || d.attempted {
		d.mu.Unlock()
		return
	}
	d.attempted = true
	newCapability, declare := d.newCapability, d.declare
	mapped := d.mapped
	d.mu.Unlock()
	if !mapped || newCapability == nil || declare == nil {
		return
	}
	capability := newCapability()
	if capability == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		capability.Close()
		return
	}
	d.activeCapability = capability
	d.mu.Unlock()
	if capability.Dispatch() {
		// Keep the declaration in this same call path, with no yield, pump,
		// timer, or secondary observer between the successful dispatch and
		// the existing Runtime/X11 declaration.
		declare()
	}
}

func (d *startupMeasurementTestScrollDriver) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.closed = true
	capability := d.activeCapability
	d.activeCapability = nil
	d.newCapability = nil
	d.declare = nil
	d.mu.Unlock()
	if capability != nil {
		capability.Close()
	}
}

// newStartupMeasurementArm starts an exact diagnostic arm. The default-off
// path returns nil before it reads a clock or subscribes, leaving normal
// launch behavior and JNI dispatch unchanged.
func newStartupMeasurementArm(getenv func(string) string, now func() time.Time, log func(msg string, args ...any)) *startupMeasurementArm {
	return newStartupMeasurementArmWithSubscriptions(getenv, now, log,
		jni.SubscribeStartupHomeDataModel, jni.SubscribeStartupHomeReady)
}

// newStartupMeasurementArmWithSubscriptions keeps subscription lifetime
// independently testable. Production always supplies the two fixed JNI
// subscriptions above; tests may provide content-free callback sources only.
func newStartupMeasurementArmWithSubscriptions(getenv func(string) string, now func() time.Time, log func(msg string, args ...any), subscribeModel, subscribeReady startupHomeSubscribe) *startupMeasurementArm {
	if !startupMeasurementRequested(getenv) {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	arm := &startupMeasurementArm{
		m:   newStartupMeasurement(func(string) string { return "1" }, now()),
		now: now,
		log: log,
	}
	if startupMeasurementTestScrollRequested(getenv) {
		arm.testScroll = newStartupMeasurementTestScrollDriver(arm)
	}
	if !arm.installModel(subscribeModel) {
		return arm
	}
	_ = arm.installReady(subscribeReady)
	return arm
}

func (a *startupMeasurementArm) installModel(subscribe startupHomeSubscribe) bool {
	if a == nil || subscribe == nil {
		return a != nil
	}
	cancel := subscribe(func(event jni.StartupHomeEvent) {
		if event == jni.StartupHomeDataModel {
			a.noteEngineHomeModel()
		}
	})
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		cancel()
		return false
	}
	a.cancelModel = cancel
	a.mu.Unlock()
	return true
}

func (a *startupMeasurementArm) installReady(subscribe startupHomeSubscribe) bool {
	if a == nil || subscribe == nil {
		return a != nil
	}
	cancel := subscribe(func(event jni.StartupHomeEvent) {
		if event == jni.StartupHomeReady {
			a.noteEngineHomeReady()
			a.noteTestScrollHomeReady()
		}
	})
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		cancel()
		return false
	}
	a.cancelReady = cancel
	a.mu.Unlock()
	return true
}

// attachX11 subscribes only to X11's owned MapNotify and explicit post-scroll
// XDamage edge. The boolean is consumed as a fail-closed availability fact;
// Runtime never replaces a missing drawable observation with an Expose,
// renderer return, or input-pump wake.
func (a *startupMeasurementArm) attachX11(window *x11.Window) bool {
	if a == nil || window == nil {
		return false
	}
	available := a.attachX11WithSubscription(func(mapNotify, postScrollDrawableUpdate func()) (func(), bool) {
		cancel, availability := window.OnStartupMeasurementEdges(x11.StartupMeasurementEdges{
			MapNotify:                mapNotify,
			PostScrollDrawableUpdate: postScrollDrawableUpdate,
		})
		return cancel, availability.PostScrollDrawableUpdate
	})
	// attachX11 is the single production binding for the test driver. The
	// driver remains inert unless the two exact environment gates created it.
	a.attachTestScrollWindow(window)
	return available
}

func (a *startupMeasurementArm) attachX11WithSubscription(subscribe startupMeasurementX11Subscribe) bool {
	if a == nil || subscribe == nil {
		return false
	}
	cancel, drawableAvailable := subscribe(a.noteX11Map, a.notePostScrollDrawableUpdate)
	if cancel == nil {
		cancel = func() {}
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		cancel()
		return false
	}
	a.m.notePostScrollDrawableAvailability(drawableAvailable)
	a.cancelX11 = cancel
	a.mu.Unlock()
	return drawableAvailable
}

// declarePredeclaredScrollDispatch records only an already-dispatched,
// fixed, non-text vertical scroll. The caller must use the X11 owner's exact
// declaration at that dispatch boundary; ordinary input observers and normal
// client behavior never call this method.
func (a *startupMeasurementArm) declarePredeclaredScrollDispatch(window startupMeasurementTestScrollDeclaration) bool {
	if a == nil || window == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || !window.DeclareStartupMeasurementVerticalScrollDispatch() {
		return false
	}
	a.m.notePredeclaredScrollDispatch(a.now())
	return true
}

func (a *startupMeasurementArm) noteX11Map() {
	if a == nil {
		return
	}
	at := a.now()
	a.mu.Lock()
	driver := a.testScroll
	if !a.closed {
		a.m.noteX11Map(at)
	}
	a.mu.Unlock()
	if driver != nil {
		driver.noteX11Map()
	}
}

func (a *startupMeasurementArm) notePostScrollDrawableUpdate() {
	if a == nil {
		return
	}
	at := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.m.notePostScrollDrawableUpdate(at)
	}
}

func (a *startupMeasurementArm) noteEngineHomeModel() {
	if a == nil {
		return
	}
	at := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.m.noteEngineHomeModel(at)
	}
}

func (a *startupMeasurementArm) noteEngineHomeReady() {
	if a == nil {
		return
	}
	at := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.m.noteEngineHomeReady(at)
	}
}

func (a *startupMeasurementArm) noteTestScrollHomeReady() {
	if a == nil {
		return
	}
	a.mu.Lock()
	driver := a.testScroll
	closed := a.closed
	a.mu.Unlock()
	if !closed && driver != nil {
		driver.noteHomeReady()
	}
}

func (a *startupMeasurementArm) attachTestScrollWindow(window *x11.Window) {
	if a == nil || window == nil {
		return
	}
	a.attachTestScrollDriver(func() startupMeasurementTestScrollCapability {
		return window.NewStartupMeasurementVerticalScrollTestDriver()
	}, func() bool {
		return a.declarePredeclaredScrollDispatch(window)
	})
}

// attachTestScrollDriver separates the sealed X11 capability factory from the
// Runtime lifecycle so its one-shot and cancellation rules can be exercised
// without a display. Production reaches it only through attachTestScrollWindow.
func (a *startupMeasurementArm) attachTestScrollDriver(newCapability func() startupMeasurementTestScrollCapability, declare func() bool) {
	if a == nil || newCapability == nil || declare == nil {
		return
	}
	a.mu.Lock()
	driver := a.testScroll
	closed := a.closed
	a.mu.Unlock()
	if !closed && driver != nil {
		driver.attachWindow(newCapability, declare)
	}
}

// teardown is idempotent and safe from a subscription callback. It emits the
// bounded, content-free aggregate once, after the arm no longer accepts
// events. The ordinary Launch defer calls it on every normal and early-return
// lifecycle path before Runtime's surrounding resources are released.
func (a *startupMeasurementArm) teardown() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	cancelModel, cancelReady, cancelX11 := a.cancelModel, a.cancelReady, a.cancelX11
	a.cancelModel, a.cancelReady, a.cancelX11 = nil, nil, nil
	m, log, driver := a.m, a.log, a.testScroll
	a.mu.Unlock()

	if driver != nil {
		driver.close()
	}
	if cancelX11 != nil {
		cancelX11()
	}
	if cancelReady != nil {
		cancelReady()
	}
	if cancelModel != nil {
		cancelModel()
	}
	if m != nil {
		m.log(log)
	}
}
