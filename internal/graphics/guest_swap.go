// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import "sync"

// guestSwapIdentity is the complete, content-free identity emitted by the
// Android EGL compatibility boundary. An X11 window alone is not sufficient:
// an old EGLSurface may be destroyed and its numeric handle reused while the
// same XID remains mapped.
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

// guestSwapTarget is deliberately small so tests can exercise the Android to
// graphics ordering without an EGL driver or a client process. EGL supplies
// the production implementation; it serializes every method with its own
// teardown lock before touching the C sentinel allocation.
type guestSwapTarget interface {
	guestSwapSurfaceCreated(guestSwapIdentity) bool
	guestSwapSignaled(guestSwapIdentity) bool
	guestSwapSurfaceDestroyed(guestSwapIdentity)
}

type guestSwapRegistration struct {
	target     guestSwapTarget
	window     uintptr
	generation uint64
}

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
	mu       sync.Mutex
	next     uint64
	windows  map[uintptr]*guestSwapRegistration
	surfaces map[guestSwapSurfaceKey]*guestSwapRegistration
}

func newGuestSwapRouter() *guestSwapRouter {
	return &guestSwapRouter{
		windows:  make(map[uintptr]*guestSwapRegistration),
		surfaces: make(map[guestSwapSurfaceKey]*guestSwapRegistration),
	}
}

var eglGuestSwaps = newGuestSwapRouter()

func (r *guestSwapRouter) register(target guestSwapTarget, window uintptr) *guestSwapRegistration {
	if target == nil || window == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	if r.next == 0 { // keep zero reserved as an invalid foreign generation
		r.next++
	}
	registration := &guestSwapRegistration{target: target, window: window, generation: r.next}
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
		if candidate == registration {
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
		if candidate == registration {
			delete(r.surfaces, candidateKey)
		}
	}
	r.surfaces[key] = registration
	id := guestSwapIdentity{window: window, display: display, surface: surface, generation: registration.generation}
	r.mu.Unlock()
	if registration.target.guestSwapSurfaceCreated(id) {
		return id.generation
	}
	// The target was stopped or replaced between the lookup and its own lock.
	// Remove only our exact registration: a newer start on this XID must win.
	r.mu.Lock()
	if r.surfaces[key] == registration {
		delete(r.surfaces, key)
	}
	r.mu.Unlock()
	return 0
}

func (r *guestSwapRouter) guestSwap(window, display, surface uintptr, generation uint64) bool {
	key := guestSwapSurfaceKey{window: window, display: display, surface: surface}
	r.mu.Lock()
	registration := r.surfaces[key]
	if registration == nil || registration.generation != generation {
		r.mu.Unlock()
		return false
	}
	r.mu.Unlock()
	return registration.target.guestSwapSignaled(guestSwapIdentity{
		window: window, display: display, surface: surface, generation: generation,
	})
}

func (r *guestSwapRouter) surfaceDestroyed(window, display, surface uintptr, generation uint64) {
	key := guestSwapSurfaceKey{window: window, display: display, surface: surface}
	r.mu.Lock()
	registration := r.surfaces[key]
	if registration == nil || registration.generation != generation {
		r.mu.Unlock()
		return
	}
	delete(r.surfaces, key)
	r.mu.Unlock()
	registration.target.guestSwapSurfaceDestroyed(guestSwapIdentity{
		window: window, display: display, surface: surface, generation: generation,
	})
}
