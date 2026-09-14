// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import "testing"

// TestEGLGuestHandoffUsesExactRetainedSurfaceIdentity exercises the Android
// shim without loading graphics. Its direct host-EGL fixture covers failed
// creation and swap, zero-XID windows, wrong display/surface calls, destroy,
// numeric surface reuse, and externally re-entrant create/destroy callbacks.
func TestEGLGuestHandoffUsesExactRetainedSurfaceIdentity(t *testing.T) {
	got := testEGLGuestHandoffFixture()
	if !got.passed {
		t.Fatalf("guest-handoff fixture reported failure: %+v", got)
	}
	if got.createCalls != 4 || len(got.createHostWindows) != 4 {
		t.Fatalf("host creates=%d windows=%#x want four translated attempts", got.createCalls, got.createHostWindows)
	}
	if got.createHostWindows[0] != 0x2001 || got.createHostWindows[1] != 0x2001 ||
		got.createHostWindows[2] != 0 || got.createHostWindows[3] != 0x2001 {
		t.Fatalf("host create windows=%#x want [0x2001 0x2001 0 0x2001]", got.createHostWindows)
	}
	if got.swapCalls < 11 || got.destroyCalls != 3 {
		t.Fatalf("host swap/destroy calls=%d/%d want lifecycle including reentrant stale paths", got.swapCalls, got.destroyCalls)
	}
	if got.destroyOrderFaults != 0 {
		t.Fatalf("guest destroy callback reached the host after destruction %d times", got.destroyOrderFaults)
	}

	const (
		created   = uint32(1)
		swap      = uint32(2)
		destroyed = uint32(3)
	)
	wantKinds := []uint32{created, swap, destroyed, created, swap, destroyed}
	wantGens := []uint64{41, 41, 41, 42, 42, 42}
	if len(got.events) != len(wantKinds) {
		t.Fatalf("bridge events=%+v want exactly six generation-checked lifecycle events", got.events)
	}
	for i, event := range got.events {
		if event.kind != wantKinds[i] || event.window != 0x2001 || event.display != 0x1001 ||
			event.surface != 0x3001 || event.generation != wantGens[i] {
			t.Fatalf("event[%d]=%+v want kind=%d xid=0x2001 dpy=0x1001 surface=0x3001 generation=%d", i, event, wantKinds[i], wantGens[i])
		}
	}
}
