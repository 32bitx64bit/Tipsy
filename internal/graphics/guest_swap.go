// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import "sync"

// guestSwapIdentity is the complete, content-free identity emitted by the
// Android EGL compatibility boundary. Its generation is owned by the surface,
// not by the longer-lived target registration. An X11 window alone is not
// sufficient: an old EGLSurface may be destroyed and its numeric handle reused
// while the same XID remains mapped.
type guestSwapIdentity struct {
	window     uintptr
	display    uintptr
	surface    uintptr
	generation uint64
}

func (id guestSwapIdentity) valid() bool {
	return id.window != 0 && id.display != 0 && id.surface != 0 && id.generation != 0
}

type guestSwapSurfaceKey struct {
	window  uintptr
	display uintptr
	surface uintptr
}

// guestSwapDelivery keeps the target-registration lifetime internal to the
// router. Android retains only identity (including its per-surface generation)
// across its EGL calls. The registration generation prevents a delayed router
// callback from an old StartSwapThread instance from changing a later one that
// reused its XID.
type guestSwapDelivery struct {
	identity               guestSwapIdentity
	registrationGeneration uint64
}

// guestSwapTarget is deliberately small so tests can exercise the Android to
// graphics ordering without an EGL driver or a client process. EGL supplies
// the production implementation; it serializes every method with its own
// teardown lock before touching the C sentinel allocation.
type guestSwapTarget interface {
	guestSwapSurfaceCreated(guestSwapDelivery) bool
	guestSwapSignaled(guestSwapDelivery) bool
	guestSwapSurfaceDestroyed(guestSwapDelivery)
}

type guestSwapRegistration struct {
	target          guestSwapTarget
	window          uintptr
	generation      uint64
	handoffAccepted bool
}

// guestSwapSurface is a single guest EGLSurface lifetime. The Android ABI
// retains generation from this object, so a handle being numerically reused
// under the same active registration cannot inherit authority from its former
// lifetime.
type guestSwapSurface struct {
	registration *guestSwapRegistration
	generation   uint64
	pending      bool
	inFlight     bool
}

// guestSwapResult distinguishes an accepted handoff from an exact surface
// whose target is already retired. The latter lets the Android bridge retain
// lifecycle accounting but stop calling Go for later successful swaps.
type guestSwapResult int

const (
	guestSwapRejected  guestSwapResult = 0
	guestSwapAccepted  guestSwapResult = 1
	guestSwapNoPending guestSwapResult = -1
)

// guestSwapRouter is the graphics-side contract for the Android EGL shim. It
// never accepts a swap based on a window or surface alone: the matching active
// registration, complete surface identity, and generation are all required.
//
// Lock ordering is intentional. The router never holds mu while calling a
// target. EGL Start/Stop hold the EGL mutex before entering the router, while
// callbacks take a router snapshot, drop its mutex, then enter EGL. A signal
// racing Stop therefore either completes before join or observes the removed
// registration; neither path can touch a freed C sentinel.
type guestSwapRouter struct {
	mu             sync.Mutex
	nextGeneration uint64
	windows        map[uintptr]*guestSwapRegistration
	surfaces       map[guestSwapSurfaceKey]*guestSwapSurface
}

func newGuestSwapRouter() *guestSwapRouter {
	return &guestSwapRouter{
		windows:  make(map[uintptr]*guestSwapRegistration),
		surfaces: make(map[guestSwapSurfaceKey]*guestSwapSurface),
	}
}

var eglGuestSwaps = newGuestSwapRouter()

// nextGenerationLocked creates globally unique nonzero values. Registration
// and surface lifetimes use different fields and are intentionally drawn from
// the same sequence so they can never be mistaken for one another in traces
// or a delayed internal callback.
func (r *guestSwapRouter) nextGenerationLocked() uint64 {
	r.nextGeneration++
	if r.nextGeneration == 0 {
		r.nextGeneration++
	}
	return r.nextGeneration
}

func (r *guestSwapRouter) register(target guestSwapTarget, window uintptr) *guestSwapRegistration {
	if target == nil || window == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// A replacement registration owns this XID immediately. Normal lifecycle
	// teardown unregisters first, but deleting an unexpectedly lingering route
	// here keeps a delayed old callback from reaching the new target.
	if old := r.windows[window]; old != nil {
		for key, surface := range r.surfaces {
			if surface.registration == old {
				delete(r.surfaces, key)
			}
		}
	}
	registration := &guestSwapRegistration{
		target:     target,
		window:     window,
		generation: r.nextGenerationLocked(),
	}
	r.windows[window] = registration
	return registration
}

func (r *guestSwapRouter) unregister(registration *guestSwapRegistration) {
	if registration == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.windows[registration.window] == registration {
		delete(r.windows, registration.window)
	}
	for key, candidate := range r.surfaces {
		if candidate.registration == registration {
			delete(r.surfaces, key)
		}
	}
}

func (r *guestSwapRouter) surfaceCreated(window, display, surface uintptr) uint64 {
	key := guestSwapSurfaceKey{window: window, display: display, surface: surface}
	if window == 0 || display == 0 || surface == 0 {
		return 0
	}
	r.mu.Lock()
	registration := r.windows[window]
	if registration == nil {
		r.mu.Unlock()
		return 0
	}
	// A later guest surface supersedes an older surface on this target. Delete
	// every prior mapping before publishing the new one, so a queued old swap
	// is rejected even if Mesa recycles a surface value.
	for candidateKey, candidate := range r.surfaces {
		if candidate.registration == registration {
			delete(r.surfaces, candidateKey)
		}
	}
	entry := &guestSwapSurface{
		registration: registration,
		generation:   r.nextGenerationLocked(),
		pending:      !registration.handoffAccepted,
	}
	r.surfaces[key] = entry
	delivery := guestSwapDelivery{
		identity: guestSwapIdentity{
			window: window, display: display, surface: surface, generation: entry.generation,
		},
		registrationGeneration: registration.generation,
	}
	r.mu.Unlock()
	if registration.target.guestSwapSurfaceCreated(delivery) {
		// The target call is deliberately outside the router lock. A delayed
		// create must not hand an obsolete token back to Android after the same
		// registration has replaced this numeric handle.
		r.mu.Lock()
		stillCurrent := r.windows[window] == registration && r.surfaces[key] == entry
		r.mu.Unlock()
		if stillCurrent {
			return delivery.identity.generation
		}
		// The target may have recorded this surface before a later create won
		// the route. Roll it back only if that old delivery is still current in
		// the target; a newer delivery has a greater surface generation.
		registration.target.guestSwapSurfaceDestroyed(delivery)
		return 0
	}
	// The target was stopped or replaced between the lookup and its own lock.
	// Remove only our exact registration: a newer start on this XID must win.
	r.mu.Lock()
	if r.surfaces[key] == entry {
		delete(r.surfaces, key)
	}
	r.mu.Unlock()
	return 0
}

func (r *guestSwapRouter) guestSwap(window, display, surface uintptr, generation uint64) guestSwapResult {
	key := guestSwapSurfaceKey{window: window, display: display, surface: surface}
	r.mu.Lock()
	entry := r.surfaces[key]
	if entry == nil || entry.generation != generation {
		r.mu.Unlock()
		return guestSwapRejected
	}
	registration := entry.registration
	if r.windows[window] != registration {
		r.mu.Unlock()
		return guestSwapRejected
	}
	if registration.handoffAccepted || !entry.pending {
		r.mu.Unlock()
		return guestSwapNoPending
	}
	if entry.inFlight {
		r.mu.Unlock()
		return guestSwapRejected
	}
	entry.inFlight = true
	r.mu.Unlock()
	delivery := guestSwapDelivery{
		identity: guestSwapIdentity{
			window: window, display: display, surface: surface, generation: generation,
		},
		registrationGeneration: registration.generation,
	}
	if registration.target.guestSwapSignaled(delivery) {
		r.mu.Lock()
		if r.windows[window] == registration {
			registration.handoffAccepted = true
		}
		entry.inFlight = false
		entry.pending = false
		r.mu.Unlock()
		return guestSwapAccepted
	}
	r.mu.Lock()
	// A signal can lose to destroy, a replacement, or target teardown. Only
	// re-arm the exact still-live surface; a stale token must remain rejected.
	if r.windows[window] == registration && r.surfaces[key] == entry && !registration.handoffAccepted {
		entry.inFlight = false
		entry.pending = true
	}
	r.mu.Unlock()
	return guestSwapRejected
}

func (r *guestSwapRouter) surfaceDestroyed(window, display, surface uintptr, generation uint64) {
	key := guestSwapSurfaceKey{window: window, display: display, surface: surface}
	r.mu.Lock()
	entry := r.surfaces[key]
	if entry == nil || entry.generation != generation {
		r.mu.Unlock()
		return
	}
	registration := entry.registration
	delete(r.surfaces, key)
	r.mu.Unlock()
	registration.target.guestSwapSurfaceDestroyed(guestSwapDelivery{
		identity: guestSwapIdentity{
			window: window, display: display, surface: surface, generation: generation,
		},
		registrationGeneration: registration.generation,
	})
}
