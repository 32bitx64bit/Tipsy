// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#include "egl.h"
#include <stdatomic.h>
#include <time.h>

// Strong definition of the weak test hook in egl.c: a one-shot stall of the
// armed duration, run without holding the swap mutex, exactly like a wedge
// inside eglSwapBuffers. Production never arms it, so the hook is an atomic
// no-op there (the Vulkan transaction fixtures use the same inert-unless-armed
// pattern).
static atomic_int tipsy_swap_stall_armed;
static atomic_int tipsy_swap_stall_entered;
static atomic_long tipsy_swap_stall_ns;

void tipsy_egl_swap_test_stall(void) {
	if (atomic_exchange_explicit(&tipsy_swap_stall_armed, 0, memory_order_acq_rel)) {
		long ns = atomic_load_explicit(&tipsy_swap_stall_ns, memory_order_relaxed);
		atomic_store_explicit(&tipsy_swap_stall_entered, 1, memory_order_release);
		struct timespec ts = {ns / 1000000000L, ns % 1000000000L};
		struct timespec rem;
		// Restart on EINTR so the wedge lasts the full duration.
		while (nanosleep(&ts, &rem) != 0) {
			ts = rem;
		}
	}
}

static void tipsy_swap_stall_arm(long ns) {
	atomic_store_explicit(&tipsy_swap_stall_ns, ns, memory_order_relaxed);
	atomic_store_explicit(&tipsy_swap_stall_entered, 0, memory_order_relaxed);
	atomic_store_explicit(&tipsy_swap_stall_armed, 1, memory_order_release);
}

static int tipsy_swap_stall_did_enter(void) {
	return atomic_load_explicit(&tipsy_swap_stall_entered, memory_order_acquire);
}
*/
import "C"

import "time"

func armSwapStallForTest(d time.Duration) { C.tipsy_swap_stall_arm(C.long(d.Nanoseconds())) }

func swapStallEnteredForTest() bool { return C.tipsy_swap_stall_did_enter() != 0 }

// startSwapThreadForTest starts the C sentinel directly with a test-owned
// cgo.Handle, bypassing EGL registration so the test owns the handoff channel
// and no watcher consumes its token.
func startSwapThreadForTest(e *EGL, handle uintptr) uintptr {
	e.mu.Lock()
	defer e.mu.Unlock()
	return uintptr(C.tipsy_egl_swap_thread_start(C.uintptr_t(e.x11Display), C.ulong(e.x11XID),
		C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context),
		C.uintptr_t(handle)))
}

func stopSwapThreadForTest(p uintptr) int {
	return int(C.tipsy_egl_swap_thread_stop(C.uintptr_t(p)))
}

func swapThreadFinishedForTest(p uintptr) bool {
	var retired, finished, source C.int
	var probes, failed C.ulong
	C.tipsy_egl_swap_thread_state(C.uintptr_t(p), &retired, &finished, &source, &probes, &failed)
	return finished != 0
}
