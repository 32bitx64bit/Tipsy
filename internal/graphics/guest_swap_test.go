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

func (t *fakeGuestSwapTarget) guestSwapSurfaceCreated(delivery guestSwapDelivery) bool {
	id := delivery.identity
	t.mu.Lock()
	defer t.mu.Unlock()
	// Match EGL's monotonic per-surface rule: an old callback that was delayed
	// outside the router must not overwrite a replacement that reused its
	// numeric EGL handle.
	if t.current.valid() && t.current.generation > id.generation {
		return false
	}
	t.current = id
	t.events = append(t.events, "created")
	return true
}

func (t *fakeGuestSwapTarget) guestSwapSignaled(delivery guestSwapDelivery) bool {
	id := delivery.identity
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current != id {
		return false
	}
	t.signals = append(t.signals, id)
	t.events = append(t.events, "swap")
	return true
}

func (t *fakeGuestSwapTarget) guestSwapSurfaceDestroyed(delivery guestSwapDelivery) {
	id := delivery.identity
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
	if router.guestSwap(71, 101, 201, 1) != guestSwapRejected {
		t.Fatal("swap before surface creation was accepted")
	}
	generation := router.surfaceCreated(71, 101, 201)
	if generation == 0 || generation == registration.generation {
		t.Fatalf("surface generation = %d, registration generation = %d; want distinct nonzero lifetimes", generation, registration.generation)
	}
	for _, stale := range []guestSwapIdentity{
		{window: 72, display: 101, surface: 201, generation: generation},
		{window: 71, display: 102, surface: 201, generation: generation},
		{window: 71, display: 101, surface: 202, generation: generation},
		{window: 71, display: 101, surface: 201, generation: generation + 1},
	} {
		if router.guestSwap(stale.window, stale.display, stale.surface, stale.generation) != guestSwapRejected {
			t.Fatalf("stale identity %+v was accepted", stale)
		}
	}
	if router.guestSwap(71, 101, 201, generation) != guestSwapAccepted {
		t.Fatal("exact guest swap was rejected")
	}
	if router.guestSwap(71, 101, 201, generation) != guestSwapNoPending {
		t.Fatal("accepted surface permitted a recurring target callback")
	}
	router.surfaceDestroyed(71, 101, 201, generation)
	if router.guestSwap(71, 101, 201, generation) != guestSwapRejected {
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
	if router.guestSwap(71, 101, 201, generation) != guestSwapRejected {
		t.Fatal("old generation reached replacement registration")
	}
	if router.guestSwap(71, 101, 201, newGeneration) != guestSwapAccepted {
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
	if router.guestSwap(91, 111, 211, generation) != guestSwapRejected {
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

func TestGuestSwapRouterSurfaceGenerationChangesOnSameRegistrationReuse(t *testing.T) {
	router := newGuestSwapRouter()
	target := &fakeGuestSwapTarget{}
	registration := router.register(target, 101)
	if registration == nil {
		t.Fatal("register returned nil")
	}

	const display = uintptr(201)
	const surface = uintptr(301)
	first := router.surfaceCreated(101, display, surface)
	if first == 0 {
		t.Fatal("first surface creation was rejected")
	}
	router.surfaceDestroyed(101, display, surface, first)
	second := router.surfaceCreated(101, display, surface)
	if second == 0 || second == first || second == registration.generation {
		t.Fatalf("reused surface generation = %d, first = %d, registration = %d", second, first, registration.generation)
	}

	if router.guestSwap(101, display, surface, first) != guestSwapRejected {
		t.Fatal("stale swap token was accepted after same-registration handle reuse")
	}
	router.surfaceDestroyed(101, display, surface, first)
	if router.guestSwap(101, display, surface, second) != guestSwapAccepted {
		t.Fatal("fresh surface token was rejected")
	}
	target.mu.Lock()
	destroyCount := len(target.destroys)
	target.mu.Unlock()
	if destroyCount != 1 {
		t.Fatalf("stale destroy reached target after replacement: destroys = %d, want 1", destroyCount)
	}
	router.unregister(registration)
}

type delayedCreateTarget struct {
	fakeGuestSwapTarget
	mu      sync.Mutex
	block   bool
	entered chan struct{}
	release chan struct{}
}

func (t *delayedCreateTarget) guestSwapSurfaceCreated(delivery guestSwapDelivery) bool {
	t.mu.Lock()
	block := t.block
	if block {
		t.block = false
		close(t.entered)
	}
	t.mu.Unlock()
	if block {
		<-t.release
	}
	return t.fakeGuestSwapTarget.guestSwapSurfaceCreated(delivery)
}

func TestGuestSwapRouterRejectsDelayedCreateAfterSameRegistrationReuse(t *testing.T) {
	router := newGuestSwapRouter()
	target := &delayedCreateTarget{
		block:   true,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	registration := router.register(target, 111)
	if registration == nil {
		t.Fatal("register returned nil")
	}

	created := make(chan uint64, 1)
	go func() {
		created <- router.surfaceCreated(111, 211, 311)
	}()
	<-target.entered
	// Reuse all numeric handles under the same target registration while the
	// old target callback is delayed outside the router mutex.
	fresh := router.surfaceCreated(111, 211, 311)
	if fresh == 0 {
		t.Fatal("replacement surface creation was rejected")
	}
	close(target.release)
	if stale := <-created; stale != 0 {
		t.Fatalf("delayed create returned stale generation %d; want 0", stale)
	}
	if router.guestSwap(111, 211, 311, fresh) != guestSwapAccepted {
		t.Fatal("fresh replacement did not retain its swap authority")
	}
	target.fakeGuestSwapTarget.mu.Lock()
	current := target.fakeGuestSwapTarget.current
	target.fakeGuestSwapTarget.mu.Unlock()
	if current.generation != fresh {
		t.Fatalf("delayed create displaced replacement: current = %+v, fresh generation = %d", current, fresh)
	}
	router.unregister(registration)
}
