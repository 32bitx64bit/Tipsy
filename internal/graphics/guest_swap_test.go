// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import (
	"sync"
	"testing"
)

// fakeGuestSwapTarget is the controlled, content-free seam for the Android
// bridge contract. It captures only window/display/surface/generation and the
// callback order; it never creates an EGL context or simulates client pixels.
type fakeGuestSwapTarget struct {
	mu       sync.Mutex
	current  guestSwapIdentity
	events   []string
	signals  []guestSwapIdentity
	destroys []guestSwapIdentity
}

func (t *fakeGuestSwapTarget) guestSwapSurfaceCreated(id guestSwapIdentity) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current = id
	t.events = append(t.events, "created")
	return true
}

func (t *fakeGuestSwapTarget) guestSwapSignaled(id guestSwapIdentity) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != id {
		return false
	}
	t.signals = append(t.signals, id)
	t.events = append(t.events, "swap")
	return true
}

func (t *fakeGuestSwapTarget) guestSwapSurfaceDestroyed(id guestSwapIdentity) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == id {
		t.current = guestSwapIdentity{}
	}
	t.destroys = append(t.destroys, id)
	t.events = append(t.events, "destroyed")
}

func TestGuestSwapRouterRejectsStaleWindowSurfaceAndGeneration(t *testing.T) {
	router := newGuestSwapRouter()
	first := &fakeGuestSwapTarget{}
	registration := router.register(first, 71)
	if registration == nil {
		t.Fatal("register returned nil")
	}
	// A swap cannot bootstrap a mapping; create must precede successful swap.
	if router.guestSwap(71, 101, 201, registration.generation) {
		t.Fatal("swap before surface creation was accepted")
	}
	generation := router.surfaceCreated(71, 101, 201)
	if generation != registration.generation {
		t.Fatalf("surface generation = %d, want %d", generation, registration.generation)
	}
	for _, stale := range []guestSwapIdentity{
		{window: 72, display: 101, surface: 201, generation: generation},
		{window: 71, display: 102, surface: 201, generation: generation},
		{window: 71, display: 101, surface: 202, generation: generation},
		{window: 71, display: 101, surface: 201, generation: generation + 1},
	} {
		if router.guestSwap(stale.window, stale.display, stale.surface, stale.generation) {
			t.Fatalf("stale identity %+v was accepted", stale)
		}
	}
	if !router.guestSwap(71, 101, 201, generation) {
		t.Fatal("exact guest swap was rejected")
	}
	router.surfaceDestroyed(71, 101, 201, generation)
	if router.guestSwap(71, 101, 201, generation) {
		t.Fatal("destroyed surface swap was accepted")
	}

	// Reusing an XID cannot resurrect a stale signal: the later registration
	// owns a new generation even if an EGL implementation reused its handles.
	router.unregister(registration)
	second := &fakeGuestSwapTarget{}
	newRegistration := router.register(second, 71)
	newGeneration := router.surfaceCreated(71, 101, 201)
	if newGeneration == 0 || newGeneration == generation {
		t.Fatalf("reused XID generation = %d, old = %d", newGeneration, generation)
	}
	if router.guestSwap(71, 101, 201, generation) {
		t.Fatal("old generation reached replacement registration")
	}
	if !router.guestSwap(71, 101, 201, newGeneration) {
		t.Fatal("new generation was rejected")
	}
	if got := first.events; len(got) != 3 || got[0] != "created" || got[1] != "swap" || got[2] != "destroyed" {
		t.Fatalf("first callback order = %v", got)
	}
	if got := second.events; len(got) != 2 || got[0] != "created" || got[1] != "swap" {
		t.Fatalf("second callback order = %v", got)
	}
	router.unregister(newRegistration)
}

func TestGuestSwapRouterConcurrentSignalAndDestroy(t *testing.T) {
	router := newGuestSwapRouter()
	target := &fakeGuestSwapTarget{}
	registration := router.register(target, 91)
	generation := router.surfaceCreated(91, 111, 211)
	if generation == 0 {
		t.Fatal("surface creation was rejected")
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = router.guestSwap(91, 111, 211, generation)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		router.surfaceDestroyed(91, 111, 211, generation)
	}()
	close(start)
	wg.Wait()
	if router.guestSwap(91, 111, 211, generation) {
		t.Fatal("swap after concurrent destroy was accepted")
	}
	target.mu.Lock()
	deferred := len(target.destroys)
	target.mu.Unlock()
	if deferred != 1 {
		t.Fatalf("destroy callbacks = %d, want 1", deferred)
	}
	router.unregister(registration)
}
