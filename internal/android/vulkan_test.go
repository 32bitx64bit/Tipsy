// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"sync"
	"testing"
	"time"
)

func TestVulkanLoaderLookupsUseCompatibilityWrappers(t *testing.T) {
	for _, name := range []string{
		"vkGetInstanceProcAddr",
		"vkCreateInstance",
		"vkCreateAndroidSurfaceKHR",
		"vkEnumerateInstanceExtensionProperties",
		"vkGetPhysicalDeviceSurfacePresentModesKHR",
		"vkCreateSwapchainKHR",
		"vkQueuePresentKHR",
	} {
		ours, err := Provider().Lookup("libvulkan.so", name)
		if err != nil || ours == 0 {
			t.Fatalf("%s: p=%#x err=%v", name, ours, err)
		}
		alias, err := Provider().Lookup("libvulkan.so.1", name)
		if err != nil || alias != ours {
			t.Fatalf("%s libvulkan.so.1 p=%#x err=%v want %#x", name, alias, err, ours)
		}
		if !testVulkanProcIsWrapped(name) {
			t.Fatalf("vkGetInstanceProcAddr(%q) did not return Tipsy WSI wrapper", name)
		}
	}
}

func TestVulkanDrawFamilyGIPAIsHostPassthrough(t *testing.T) {
	for _, name := range []string{"vkCreateDevice", "vkDestroyInstance", "vkEnumeratePhysicalDevices"} {
		if !testVulkanProcIsHostPassthrough(name) {
			if _, err := Provider().Lookup("libvulkan.so", name); err != nil {
				t.Skipf("host Vulkan loader has no %s; passthrough cannot be proven", name)
			}
			t.Fatalf("%s GIPA must return the host loader pointer, not a Tipsy trampoline", name)
		}
	}
}

func TestVulkanDlopenReturnsRegisteredAdapter(t *testing.T) {
	handle, gipa := testVulkanDlopen()
	if handle == 0 || gipa == 0 {
		t.Fatal("dlopen(libvulkan.so) did not return the registered Android adapter")
	}
	want, err := Provider().Lookup("libvulkan.so", "vkGetInstanceProcAddr")
	if err != nil || gipa != want {
		t.Fatalf("dlsym vkGetInstanceProcAddr=%#x want %#x err=%v", gipa, want, err)
	}
}

func TestVulkanAndroidSurfaceAdvertisedOnlyWithHostWSI(t *testing.T) {
	if testVulkanAndroidSurfaceAdvertised([]string{"VK_KHR_surface"}) {
		t.Fatal("android_surface advertised without a host WSI extension")
	}
	if !testVulkanAndroidSurfaceAdvertised([]string{"VK_KHR_surface", "VK_KHR_xcb_surface"}) {
		t.Fatal("android_surface not advertised when host has VK_KHR_xcb_surface")
	}
	if !testVulkanAndroidSurfaceAdvertised([]string{"VK_KHR_xlib_surface"}) {
		t.Fatal("android_surface not advertised when host has VK_KHR_xlib_surface")
	}
	if testVulkanAndroidSurfaceAdvertised([]string{"VK_KHR_surface", "VK_KHR_wayland_surface"}) {
		t.Fatal("wayland-only host must not advertise VK_KHR_android_surface")
	}
}

func TestVulkanRewritesAndroidSurfaceToHostWSI(t *testing.T) {
	in := []string{"VK_KHR_surface", "VK_KHR_android_surface"}
	out, ok := testVulkanRewriteEnabledExtensions(in, true, false)
	if !ok || len(out) != 2 || out[0] != "VK_KHR_surface" || out[1] != "VK_KHR_xcb_surface" {
		t.Fatalf("xcb rewrite=%v ok=%v", out, ok)
	}
	out, ok = testVulkanRewriteEnabledExtensions(in, false, true)
	if !ok || out[1] != "VK_KHR_xlib_surface" {
		t.Fatalf("xlib rewrite=%v ok=%v", out, ok)
	}
	if _, ok = testVulkanRewriteEnabledExtensions(in, false, false); ok {
		t.Fatal("android_surface rewritten without a host WSI extension")
	}
}

func TestVulkanPresentModeFilterFollowsVSync(t *testing.T) {
	in := []uint32{2, 1, 0} // FIFO, MAILBOX, IMMEDIATE
	off := testVulkanFilterPresentModes(in, false)
	if len(off) != 2 || off[0] != 0 || off[1] != 1 {
		t.Fatalf("VSync-off present modes=%v want IMMEDIATE then MAILBOX", off)
	}
	on := testVulkanFilterPresentModes(in, true)
	if len(on) != 1 || on[0] != 2 {
		t.Fatalf("VSync-on present modes=%v want FIFO only", on)
	}
	fifoOnly := testVulkanFilterPresentModes([]uint32{2}, false)
	if len(fifoOnly) != 1 || fifoOnly[0] != 2 {
		t.Fatalf("VSync-off with only FIFO must keep FIFO: %v", fifoOnly)
	}
}

func TestVulkanCreateSwapchainForcesImmediateWhenVSyncOff(t *testing.T) {
	const fifo = uint32(2)
	first, second, calls, result := testVulkanCreateSwapchainPolicy(false, fifo, true, true)
	if calls != 1 || first != 0 || second != 0 || result != 0 {
		t.Fatalf("VSync-off FIFO request: first=%d second=%d calls=%d result=%d want IMMEDIATE once", first, second, calls, result)
	}
	first, _, calls, result = testVulkanCreateSwapchainPolicy(false, 1, true, true)
	if calls != 1 || first != 0 || result != 0 {
		t.Fatalf("VSync-off MAILBOX request: first=%d calls=%d result=%d want IMMEDIATE once", first, calls, result)
	}
	first, second, calls, result = testVulkanCreateSwapchainPolicy(false, fifo, false, true)
	if calls != 2 || first != 0 || second != fifo || result != 0 {
		t.Fatalf("VSync-off IMMEDIATE rejected: first=%d second=%d calls=%d result=%d want client FIFO fallback", first, second, calls, result)
	}
	first, second, calls, result = testVulkanCreateSwapchainPolicy(true, 0, true, true)
	if calls != 1 || first != fifo || result != 0 {
		t.Fatalf("VSync-on IMMEDIATE request: first=%d second=%d calls=%d result=%d want FIFO once", first, second, calls, result)
	}
}

func TestVulkanPresentStatsReportsSuccessfulPresentRate(t *testing.T) {
	SetVulkanPresentStats(true)
	resetVulkanPresentStats()
	SetVulkanVSync(false)
	testVulkanRecordPresent(1_000_000_000)
	testVulkanRecordPresent(1_004_000_000)
	testVulkanRecordPresent(1_008_000_000)
	got := VulkanPresentStats()
	if got.SuccessfulPresents != 3 || got.Elapsed != 8*time.Millisecond || got.RateFPS != 250 {
		t.Fatalf("VulkanPresentStats() = %+v", got)
	}
	if vulkanVSyncEnabled() {
		t.Fatal("VSync-off test left VSync enabled")
	}
}

func TestVulkanPresentStatsDisabledSkipsHotPath(t *testing.T) {
	SetVulkanPresentStats(true)
	resetVulkanPresentStats()
	t.Cleanup(func() {
		SetVulkanPresentStats(false)
		resetVulkanPresentStats()
	})

	SetVulkanPresentStats(false)
	if VulkanPresentStatsEnabled() {
		t.Fatal("SetVulkanPresentStats(false) left stats enabled")
	}
	testVulkanNotePresentSuccess()
	got := VulkanPresentStats()
	if got.SuccessfulPresents != 0 {
		t.Fatalf("disabled hot path recorded %+v", got)
	}

	testVulkanRecordPresent(1_000_000_000)
	if VulkanPresentStats().SuccessfulPresents != 1 {
		t.Fatal("tipsy_test_vk_record_present must record while stats are disabled")
	}

	resetVulkanPresentStats()
	SetVulkanPresentStats(true)
	testVulkanNotePresentSuccess()
	testVulkanNotePresentSuccess()
	got = VulkanPresentStats()
	if got.SuccessfulPresents != 2 {
		t.Fatalf("enabled counter-only stats = %+v", got)
	}
	if got.Elapsed != 0 || got.RateFPS != 0 {
		t.Fatalf("first counter-only observation should have no rate: %+v", got)
	}
}

func TestVulkanWSIBindRequiresDisplayAndWindow(t *testing.T) {
	t.Cleanup(UnbindVulkanWSI)
	if err := BindVulkanWSI(0, 1); err == nil || vulkanWSIBound() {
		t.Fatal("zero display bound")
	}
	if err := BindVulkanWSI(1, 0); err == nil {
		t.Fatal("zero xid bound")
	}
}

func TestVulkanPresentStatsFollowGraphicsInfoLogger(t *testing.T) {
	t.Cleanup(func() {
		SetVulkanPresentStats(false)
		resetVulkanPresentStats()
	})
	SetVulkanPresentStats(!presentStatsLoggerEnabled())
	SetVulkanPresentStats(presentStatsLoggerEnabled())
	if got, want := VulkanPresentStatsEnabled(), presentStatsLoggerEnabled(); got != want {
		t.Fatalf("Vulkan present stats enabled=%v, want logger Info gate %v", got, want)
	}
}

func TestVulkanPresentTimingGatingAndResults(t *testing.T) {
	cursor := SetVulkanPresentTiming(false)
	t.Cleanup(func() {
		SetVulkanPresentTiming(false)
		SetVulkanPresentStats(false)
		resetVulkanPresentStats()
	})
	SetVulkanPresentStats(true)
	resetVulkanPresentStats()
	testVulkanNotePresentResult(0, 1_000_000)
	if got := VulkanPresentTimingSnapshot(cursor); got.Cursor != cursor || len(got.Samples) != 0 {
		t.Fatalf("disabled timing recorded samples: %+v", got)
	}
	if got := VulkanPresentStats().SuccessfulPresents; got != 1 {
		t.Fatalf("disabled timing changed existing counter: %d", got)
	}

	SetVulkanPresentStats(false)
	if got := SetVulkanPresentTiming(true); got != cursor {
		t.Fatalf("enable reset cursor: %d, want %d", got, cursor)
	}
	for _, result := range []int32{-3, -1000001004, 5, 1000001003} {
		testVulkanNotePresentResult(result, 2_000_000)
	}
	if got := VulkanPresentTimingSnapshot(cursor); len(got.Samples) != 0 {
		t.Fatalf("non-VK_SUCCESS result masqueraded as present: %+v", got)
	}
	testVulkanNotePresentResult(0, 3_000_000)
	batch := VulkanPresentTimingSnapshot(cursor)
	if len(batch.Samples) != 1 || batch.Samples[0].MonotonicNS != 3_000_000 || batch.Cursor != cursor+1 {
		t.Fatalf("enabled timing = %+v", batch)
	}
	if got := VulkanPresentStats().SuccessfulPresents; got != 1 {
		t.Fatalf("timing unexpectedly enabled ordinary counter: %d", got)
	}
	SetVulkanPresentTiming(false)
	testVulkanNotePresentResult(0, 4_000_000)
	if got := VulkanPresentTimingSnapshot(batch.Cursor); len(got.Samples) != 0 {
		t.Fatalf("disabled timing appended samples: %+v", got)
	}
}

func TestVulkanPresentTimingIntervalsAndLifetimeCursor(t *testing.T) {
	SetVulkanPresentTiming(false)
	cursor := SetVulkanPresentTiming(true)
	t.Cleanup(func() { SetVulkanPresentTiming(false) })
	want := []uint64{1_000_000_000, 1_004_000_000, 1_013_000_000}
	for _, ns := range want {
		testVulkanNotePresentResult(0, ns)
	}
	batch := VulkanPresentTimingSnapshot(cursor)
	if len(batch.Samples) != len(want) || batch.Overwritten != 0 || batch.Cursor != cursor+3 {
		t.Fatalf("timing batch = %+v", batch)
	}
	for i, sample := range batch.Samples {
		if sample.Sequence != cursor+uint64(i)+1 || sample.MonotonicNS != want[i] {
			t.Fatalf("sample %d = %+v", i, sample)
		}
	}
	if gap := time.Duration(batch.Samples[2].MonotonicNS - batch.Samples[1].MonotonicNS); gap != 9*time.Millisecond {
		t.Fatalf("long present interval = %v", gap)
	}
	batch.Samples[0].MonotonicNS = 9
	if again := VulkanPresentTimingSnapshot(cursor); again.Samples[0].MonotonicNS != want[0] {
		t.Fatal("snapshot exposed native ring storage")
	}
	if empty := VulkanPresentTimingSnapshot(batch.Cursor); len(empty.Samples) != 0 || empty.Cursor != batch.Cursor {
		t.Fatalf("consumed cursor repeated samples: %+v", empty)
	}
	if future := VulkanPresentTimingSnapshot(batch.Cursor + 99); len(future.Samples) != 0 || future.Cursor != batch.Cursor+99 {
		t.Fatalf("future cursor rewound: %+v", future)
	}
	SetVulkanPresentTiming(false)
	resetVulkanPresentStats()
	if next := SetVulkanPresentTiming(true); next != batch.Cursor {
		t.Fatalf("counter reset or enable rewound timing cursor: %d", next)
	}
	testVulkanNotePresentSuccess() // The actual host CLOCK_MONOTONIC path.
	next := VulkanPresentTimingSnapshot(batch.Cursor)
	if len(next.Samples) != 1 || next.Samples[0].MonotonicNS == 0 || next.Cursor != batch.Cursor+1 {
		t.Fatalf("real monotonic clock sample = %+v", next)
	}
}

func TestVulkanPresentTimingReportsOverwrittenSamples(t *testing.T) {
	SetVulkanPresentTiming(false)
	cursor := SetVulkanPresentTiming(true)
	t.Cleanup(func() { SetVulkanPresentTiming(false) })
	const excess = 7
	for i := 0; i < VulkanPresentTimingCapacity+excess; i++ {
		testVulkanNotePresentResult(0, 1_000+uint64(i))
	}
	batch := VulkanPresentTimingSnapshot(cursor)
	if batch.Overwritten != excess || len(batch.Samples) != VulkanPresentTimingCapacity {
		t.Fatalf("ring overrun lost accounting: overwritten=%d count=%d", batch.Overwritten, len(batch.Samples))
	}
	for i, sample := range batch.Samples {
		if sample.Sequence != cursor+excess+uint64(i)+1 || sample.MonotonicNS != 1_000+excess+uint64(i) {
			t.Fatalf("wrapped sample %d = %+v", i, sample)
		}
	}
	if next := VulkanPresentTimingSnapshot(batch.Cursor); len(next.Samples) != 0 || next.Overwritten != 0 {
		t.Fatalf("overwritten samples repeated on consumed cursor: %+v", next)
	}
}

func TestVulkanPresentTimingConcurrentSnapshot(t *testing.T) {
	SetVulkanPresentTiming(false)
	cursor := SetVulkanPresentTiming(true)
	t.Cleanup(func() { SetVulkanPresentTiming(false) })
	const writers, perWriter = 4, 5_000
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				testVulkanNotePresentResult(0, 1_000+uint64(i))
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	t.Cleanup(func() { <-done })
	var observed, overwritten uint64
	finished := false
	for {
		batch := VulkanPresentTimingSnapshot(cursor)
		for i, sample := range batch.Samples {
			wantSequence := cursor + batch.Overwritten + uint64(i) + 1
			if sample.Sequence != wantSequence || sample.MonotonicNS < 1_000 || sample.MonotonicNS >= 1_000+perWriter {
				t.Fatalf("torn or discontinuous sample: %+v, want sequence %d", sample, wantSequence)
			}
		}
		observed += uint64(len(batch.Samples))
		overwritten += batch.Overwritten
		cursor = batch.Cursor
		if finished && len(batch.Samples) == 0 {
			break
		}
		select {
		case <-done:
			finished = true
		default:
		}
	}
	if got := observed + overwritten; got != writers*perWriter {
		t.Fatalf("concurrent snapshot lost samples: received=%d overwritten=%d total=%d", observed, overwritten, got)
	}
}

func TestVulkanPresentTimingToggleWhileRecording(t *testing.T) {
	SetVulkanPresentTiming(false)
	start := SetVulkanPresentTiming(true)
	t.Cleanup(func() { SetVulkanPresentTiming(false) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20_000; i++ {
			testVulkanNotePresentResult(0, 2_000+uint64(i))
		}
	}()
	t.Cleanup(func() { <-done })
	last := start
	for i := 0; i < 100; i++ {
		stop := SetVulkanPresentTiming(false)
		if stop < last {
			t.Fatalf("disable rewound cursor: %d < %d", stop, last)
		}
		if still := SetVulkanPresentTiming(false); still != stop {
			t.Fatalf("disabled in-flight recorder appended after stop: %d != %d", still, stop)
		}
		last = SetVulkanPresentTiming(true)
		if last != stop {
			t.Fatalf("re-enable unexpectedly changed sequence: %d != %d", last, stop)
		}
	}
	<-done
	end := SetVulkanPresentTiming(false)
	batch := VulkanPresentTimingSnapshot(start)
	if batch.Cursor != end || uint64(len(batch.Samples))+batch.Overwritten != end-start {
		t.Fatalf("toggle/snapshot accounting mismatch: start=%d end=%d batch=%+v", start, end, batch)
	}
}

func BenchmarkVulkanPresentTiming(b *testing.B) {
	SetVulkanPresentStats(true)
	b.Cleanup(func() {
		SetVulkanPresentTiming(false)
		SetVulkanPresentStats(false)
	})
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			SetVulkanPresentTiming(enabled)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				testVulkanNotePresentSuccess()
			}
		})
	}
}

func BenchmarkVulkanPresentStats(b *testing.B) {
	SetVulkanPresentTiming(false)
	b.Cleanup(func() {
		SetVulkanPresentStats(false)
		resetVulkanPresentStats()
	})
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			SetVulkanPresentStats(enabled)
			resetVulkanPresentStats()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				testVulkanNotePresentSuccess()
			}
		})
	}
}
