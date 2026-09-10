// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestHostNetworkDetectionIsTTLCached pins the connectivity cache contract:
// repeated ConnectivityManager/Network queries within the TTL share one
// interface scan, a changed host state is visible once the TTL expires, and
// the cached value is never invented (it always mirrors the last real probe).
func TestHostNetworkDetectionIsTTLCached(t *testing.T) {
	orig := probeHostNetworkFn
	t.Cleanup(func() {
		probeHostNetworkFn = orig
		resetHostNetworkCacheForTest()
	})
	resetHostNetworkCacheForTest()

	var calls atomic.Int32
	up := atomic.Bool{}
	up.Store(true)
	probeHostNetworkFn = func() bool {
		calls.Add(1)
		return up.Load()
	}

	if !detectHostNetwork() {
		t.Fatal("first probe returned up=false, want true")
	}
	for i := 0; i < 8; i++ {
		if !detectHostNetwork() {
			t.Fatal("cached probe flipped the honest result")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1 within TTL", got)
	}

	// Expire the cache without sleeping the TTL: a real network change must
	// be observable after the TTL, never sticky.
	hostNetworkCache.mu.Lock()
	hostNetworkCache.checked = time.Now().Add(-hostNetworkTTL - time.Second)
	hostNetworkCache.mu.Unlock()
	up.Store(false)
	if detectHostNetwork() {
		t.Fatal("expired cache did not re-probe the changed host state")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("probe calls = %d, want 2 after TTL expiry", got)
	}
}
