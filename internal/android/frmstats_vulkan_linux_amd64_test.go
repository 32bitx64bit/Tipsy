// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && tipsy_frmstats_testhooks

package android

import "testing"

// vkPresentReturn runs the adapter's frame-time note for one
// vkQueuePresentKHR result. vkRecordFrameTime lives in
// frmstats_hook_linux_amd64.go, because a _test.go file cannot import "C".
func vkPresentReturn(t *testing.T, result int32, nowNS uint64) {
	t.Helper()
	vkRecordFrameTime(result, nowNS)
}

// TestVulkanPresentFeedsFrameTimeRing pins the Vulkan-side wiring: one
// successful vkQueuePresentKHR return records one retained stamp after the
// call boundary, and a failed present records nothing. Without this a capture
// could look valid while depending on the Vulkan present-timing epoch.
func TestVulkanPresentFeedsFrameTimeRing(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	if cursor := SetFrameTimeArmed(true); cursor != 0 {
		t.Fatalf("arming a reset ring returned cursor %d, want 0", cursor)
	}
	const success = 0                   // TIPSY_VK_SUCCESS
	vkPresentReturn(t, success-4, 5000) // TIPSY_VK_ERROR_OUT_OF_DATE_KHR
	if _, _, samples := FrameTimeSnapshot(0); len(samples) != 0 {
		t.Fatalf("failed present recorded %d samples", len(samples))
	}
	vkPresentReturn(t, success, 5001)
	vkPresentReturn(t, success, 5002)
	cursor, overwritten, samples := FrameTimeSnapshot(0)
	if overwritten != 0 {
		t.Fatalf("overwritten = %d, want 0", overwritten)
	}
	if cursor != 2 || len(samples) != 2 {
		t.Fatalf("cursor=%d len=%d, want 2/2", cursor, len(samples))
	}
	if samples[0] != 5001 || samples[1] != 5002 {
		t.Fatalf("samples = %v, want 5001 5002", samples)
	}
}

// TestVulkanPresentStampsClockWhenUnstamped checks the zero-stamp fallback:
// a present that arrives without a present-timing stamp still gets a host
// monotonic stamp, so the ring never stores zero.
func TestVulkanPresentStampsClockWhenUnstamped(t *testing.T) {
	ResetFrameTimeRing()
	t.Cleanup(ResetFrameTimeRing)
	SetFrameTimeArmed(true)
	vkPresentReturn(t, 0, 0)
	_, _, samples := FrameTimeSnapshot(0)
	if len(samples) != 1 {
		t.Fatalf("len = %d, want 1", len(samples))
	}
	if samples[0] == 0 {
		t.Fatal("unstamped present recorded 0 ns")
	}
}
