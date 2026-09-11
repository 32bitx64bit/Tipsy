// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import "testing"

// drainRefreshWake clears coalesced refresh tokens left by earlier tests so a
// test observes only its own producer edges.
func drainRefreshWake() {
	for {
		select {
		case <-refreshWake:
			continue
		default:
			return
		}
	}
}

func TestRefreshReadyCoalescesAndIsNilSafe(t *testing.T) {
	drainRefreshWake()
	t.Cleanup(drainRefreshWake)
	if got := (*Window)(nil).RefreshReady(); got != nil {
		t.Fatal("nil window exposed a refresh wake channel")
	}
	if got := (&Window{}).RefreshReady(); got == nil {
		t.Fatal("open window has no selectable refresh wake channel")
	} else {
		select {
		case <-got:
			t.Fatal("refresh wake started with a pending token")
		default:
		}
	}
	// A version change is the producer edge. Extra notifies coalesce into the
	// single pending token until the launch loop consumes it.
	GoX11_RefreshNotify()
	GoX11_RefreshNotify()
	select {
	case <-(&Window{}).RefreshReady():
	default:
		t.Fatal("refresh notify produced no token")
	}
	select {
	case <-(&Window{}).RefreshReady():
		t.Fatal("extra refresh notify was not coalesced")
	default:
	}
}
