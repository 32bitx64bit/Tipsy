// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVulkanOutputKeepsOldKhronosHeaderFallbacks(t *testing.T) {
	data, err := os.ReadFile("vulkan_output.c")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		"#ifndef VK_DEVICE_QUEUE_CREATE_INTERNALLY_SYNCHRONIZED_BIT_KHR",
		"#define VK_DEVICE_QUEUE_CREATE_INTERNALLY_SYNCHRONIZED_BIT_KHR 0x00000002u",
		"#ifndef VK_PIPELINE_CACHE_CREATE_EXTERNALLY_SYNCHRONIZED_BIT_EXT",
		"#define VK_PIPELINE_CACHE_CREATE_EXTERNALLY_SYNCHRONIZED_BIT_EXT 0x00000001u",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("vulkan_output.c is missing old-header fallback %q", required)
		}
	}
}

func TestVulkanLoaderLookupsUseCompatibilityWrappers(t *testing.T) {
	for _, name := range []string{
		"vkGetInstanceProcAddr",
		"vkCreateInstance",
		"vkDestroyInstance",
		"vkCreateDevice",
		"vkCreateAndroidSurfaceKHR",
		"vkDestroySurfaceKHR",
		"vkEnumerateInstanceExtensionProperties",
		"vkGetPhysicalDeviceSurfacePresentModesKHR",
		"vkCreateSwapchainKHR",
		"vkDestroySwapchainKHR",
		"vkGetSwapchainImagesKHR",
		"vkAcquireNextImageKHR",
		"vkAcquireNextImage2KHR",
		"vkGetDeviceQueue",
		"vkGetDeviceQueue2",
		"vkQueueSubmit",
		"vkQueueSubmit2",
		"vkQueuePresentKHR",
		"vkDestroyDevice",
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
	for _, name := range []string{
		"vkEnumeratePhysicalDevices",
		"vkGetPhysicalDeviceQueueFamilyProperties",
		"vkGetPhysicalDeviceSurfaceSupportKHR",
	} {
		if !testVulkanProcIsHostPassthrough(name) {
			if _, err := Provider().Lookup("libvulkan.so", name); err != nil {
				t.Skipf("host Vulkan loader has no %s; passthrough cannot be proven", name)
			}
			t.Fatalf("%s GIPA must return the host loader pointer, not a Tipsy trampoline", name)
		}
	}
}

func TestVulkanPrivateOutputQueueQualification(t *testing.T) {
	for _, scenario := range []uint32{0, 1} {
		if !testVulkanOutputQueueEligible(scenario) {
			t.Errorf("ordinary same-family scenario %d was rejected", scenario)
		}
	}
	for _, scenario := range []uint32{2, 3, 4, 5} {
		if testVulkanOutputQueueEligible(scenario) {
			t.Errorf("unsupported/protected queue scenario %d was accepted", scenario)
		}
	}
}

func TestVulkanSameQueueTransactionOrderAndCounts(t *testing.T) {
	got := testVulkanOutputTransactionFixture(0)
	if !slices.Equal(got.order, []uint32{1, 2, 3, 4}) ||
		got.acquireCalls != 1 || got.compositionSubmitCalls != 1 ||
		got.acquireConsumeCalls != 0 || got.guestPresentCalls != 1 ||
		got.outputPresentCalls != 1 || got.originalGuestForwarded ||
		got.fullCopyCount != 1 || got.drawCount != 1 || got.uploadCount != 1 ||
		!got.guestWaitConsumedOnce || !got.gWaitConsumedOnce ||
		!got.oWaitConsumedOnce || got.quarantined || got.deviceTerminal ||
		got.returnedResult != 0 || got.overlayAcquireCalls != 1 ||
		got.presentMutexLocks != 1 ||
		got.copySrcX != 2 || got.copySrcY != 3 ||
		got.copyDstX != 0 || got.copyDstY != 0 ||
		got.copyWidth != 1 || got.copyHeight != 1 ||
		got.outputCreateCalls != 0 ||
		got.childX != 2 || got.childY != 3 ||
		got.childWidth != 1 || got.childHeight != 1 ||
		got.pushRect != [4]float32{-1, -1, 1, 1} ||
		got.viewportWidth != 1 || got.viewportHeight != 1 {
		t.Fatalf("qualified same-queue operation sequence=%+v", got)
	}
}

func TestVulkanSameQueueDirectGatesHaveNoPrivateOperations(t *testing.T) {
	for _, scenario := range []uint32{1, 2} {
		got := testVulkanOutputTransactionFixture(scenario)
		if !slices.Equal(got.order, []uint32{3}) || got.acquireCalls != 0 ||
			got.compositionSubmitCalls != 0 || got.acquireConsumeCalls != 0 ||
			got.guestPresentCalls != 1 || got.outputPresentCalls != 0 ||
			!got.originalGuestForwarded || got.fullCopyCount != 0 ||
			got.drawCount != 0 || got.uploadCount != 0 || got.quarantined ||
			got.deviceTerminal || got.returnedResult != 0 ||
			got.overlayAcquireCalls != 1 || got.presentMutexLocks != 1 ||
			got.copyWidth != 0 || got.copyHeight != 0 ||
			got.outputCreateCalls != 0 {
			t.Errorf("direct scenario %d performed private work: %+v", scenario, got)
		}
	}
}

func TestVulkanUnpublishedPresentSkipsOverlayAcquireAndMutex(t *testing.T) {
	idle := testVulkanOutputTransactionFixture(11)
	if !slices.Equal(idle.order, []uint32{3}) || idle.acquireCalls != 0 ||
		idle.compositionSubmitCalls != 0 || idle.guestPresentCalls != 1 ||
		idle.outputPresentCalls != 0 || !idle.originalGuestForwarded ||
		idle.fullCopyCount != 0 || idle.drawCount != 0 || idle.uploadCount != 0 ||
		idle.overlayAcquireCalls != 0 || idle.presentMutexLocks != 0 ||
		idle.unpublishedFollowupMutexLocks != 0 || idle.quarantined ||
		idle.deviceTerminal || idle.returnedResult != 0 ||
		idle.copyWidth != 0 || idle.outputCreateCalls != 0 {
		t.Fatalf("unpublished idle present = %+v", idle)
	}
	teardown := testVulkanOutputTransactionFixture(12)
	if !slices.Equal(teardown.order, []uint32{3}) || teardown.overlayAcquireCalls != 0 ||
		teardown.presentMutexLocks != 1 || teardown.unpublishedFollowupMutexLocks != 0 ||
		!teardown.originalGuestForwarded || teardown.fullCopyCount != 0 ||
		teardown.drawCount != 0 || teardown.guestPresentCalls != 1 ||
		teardown.outputPresentCalls != 0 || teardown.quarantined ||
		teardown.deviceTerminal || teardown.returnedResult != 0 {
		t.Fatalf("unpublished teardown present = %+v", teardown)
	}
}

func TestVulkanSameQueueFailureMatrix(t *testing.T) {
	tests := []struct {
		scenario          uint32
		order             []uint32
		consume           uint32
		guestPresents     uint32
		outputPresents    uint32
		originalForwarded bool
		gConsumed         bool
		oConsumed         bool
		terminal          bool
		guestResult       int32
	}{
		{3, []uint32{1, 2, 5, 3}, 1, 1, 0, true, false, false, false, 0},
		{4, []uint32{1, 2}, 0, 0, 0, false, false, false, true, -4},
		{5, []uint32{1, 2}, 0, 0, 0, false, false, false, false, -13},
		{6, []uint32{1, 2, 3, 4}, 0, 1, 1, false, false, true, false, -1},
		{7, []uint32{1, 2, 3}, 0, 1, 0, false, false, false, true, -4},
		{8, []uint32{1, 2, 3, 4}, 0, 1, 1, false, true, true, false, -1000001004},
		{9, []uint32{1, 2, 3, 4}, 0, 1, 1, false, true, false, false, 0},
		{10, []uint32{1, 2, 3, 4}, 0, 1, 1, false, true, false, true, -4},
	}
	for _, tt := range tests {
		got := testVulkanOutputTransactionFixture(tt.scenario)
		if !slices.Equal(got.order, tt.order) ||
			got.acquireCalls != 1 || got.compositionSubmitCalls != 1 ||
			got.acquireConsumeCalls != tt.consume ||
			got.guestPresentCalls != tt.guestPresents ||
			got.outputPresentCalls != tt.outputPresents ||
			got.originalGuestForwarded != tt.originalForwarded ||
			got.gWaitConsumedOnce != tt.gConsumed ||
			got.oWaitConsumedOnce != tt.oConsumed || !got.quarantined ||
			got.deviceTerminal != tt.terminal || got.returnedResult != tt.guestResult ||
			got.overlayAcquireCalls != 1 || got.presentMutexLocks != 1 {
			t.Errorf("failure scenario %d=%+v", tt.scenario, got)
		}
	}
}

func TestVulkanOutputOverlaySubrectAndSizeDrivenRecreate(t *testing.T) {
	got := testVulkanOutputOverlayGeometryFixture()
	if !slices.Equal(got.first.order, []uint32{1, 2, 3, 4}) ||
		got.first.fullCopyCount != 1 || got.first.copySrcX != 1 ||
		got.first.copySrcY != 2 || got.first.copyDstX != 0 ||
		got.first.copyDstY != 0 || got.first.copyWidth != 1 ||
		got.first.copyHeight != 1 || got.first.outputCreateCalls != 1 ||
		got.first.outputCreateWidth != 1 || got.first.outputCreateHeight != 1 ||
		got.first.childX != 1 || got.first.childY != 2 ||
		got.first.childWidth != 1 || got.first.childHeight != 1 ||
		got.first.pushRect != [4]float32{-1, -1, 1, 1} ||
		got.first.viewportWidth != 1 || got.first.viewportHeight != 1 ||
		got.first.originalGuestForwarded || got.first.quarantined ||
		got.first.returnedResult != 0 {
		t.Fatalf("size-changing overlay present = %+v", got.first)
	}
	if !slices.Equal(got.second.order, []uint32{1, 2, 3, 4}) ||
		got.second.fullCopyCount != 1 || got.second.copySrcX != 4 ||
		got.second.copySrcY != 5 || got.second.copyWidth != 1 ||
		got.second.copyHeight != 1 || got.second.outputCreateCalls != 0 ||
		got.second.childX != 4 || got.second.childY != 5 ||
		got.second.childWidth != 1 || got.second.childHeight != 1 ||
		got.second.pushRect != [4]float32{-1, -1, 1, 1} ||
		got.second.originalGuestForwarded || got.second.quarantined ||
		got.second.returnedResult != 0 {
		t.Fatalf("overlay move-only present = %+v", got.second)
	}
	if !slices.Equal(got.empty.order, []uint32{3}) ||
		got.empty.fullCopyCount != 0 || got.empty.outputCreateCalls != 0 ||
		got.empty.outputPresentCalls != 0 || !got.empty.originalGuestForwarded ||
		got.empty.drawCount != 0 || got.empty.returnedResult != 0 {
		t.Fatalf("empty clipped overlay present = %+v", got.empty)
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

func TestVulkanCreateSwapchainPreservesActualSurfaceCapability(t *testing.T) {
	const (
		immediate        = uint32(0)
		mailbox          = uint32(1)
		fifo             = uint32(2)
		sharedDemand     = uint32(1000111000)
		sharedContinuous = uint32(1000111001)
		fifoLatestReady  = uint32(1000361000)
		success          = int32(0)
		incomplete       = int32(5)
		verified         = uint32(0)
		unavailable      = uint32(1)
		malformed        = uint32(2)
		probeIncomplete  = uint32(3)
	)
	assertCreate := func(t *testing.T, got vulkanPresentModeCapabilityResult, requested uint32) {
		t.Helper()
		if got.createResult != success || got.calls != 1 || got.firstMode != requested {
			t.Fatalf("create=%d calls=%d first=%d want success and supplied request %d once", got.createResult, got.calls, got.firstMode, requested)
		}
	}

	for _, tc := range []struct {
		name       string
		host       []uint32
		vsync      bool
		requested  uint32
		advertised []uint32
	}{
		{"fifo-only", []uint32{fifo}, false, fifo, []uint32{fifo}},
		{"fifo-mailbox", []uint32{fifo, mailbox}, false, mailbox, []uint32{mailbox}},
		{"fifo-immediate", []uint32{fifo, immediate}, false, immediate, []uint32{immediate}},
		{"vsync-fifo", []uint32{fifo, mailbox, immediate}, true, fifo, []uint32{fifo}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := testVulkanPresentModeCapability(tc.host, success, success, uint32(len(tc.host)), tc.vsync, tc.requested)
			if got.enumerateResult != success || !slices.Equal(got.advertised, tc.advertised) || !slices.Contains(got.advertised, tc.requested) {
				t.Fatalf("enumerate=%d advertised=%v want successful complete list %v containing %d", got.enumerateResult, got.advertised, tc.advertised, tc.requested)
			}
			if got.probe.status != verified || got.probe.result != success || got.probe.modeCount != uint32(len(tc.host)) {
				t.Fatalf("probe=%+v want verified successful host list", got.probe)
			}
			assertCreate(t, got, tc.requested)
		})
	}

	// Extended KHR present modes are valid capability observations. The adapter
	// keeps the client request unchanged; this only prevents diagnostics from
	// falsely calling an otherwise valid host list malformed.
	for _, requested := range []uint32{sharedDemand, sharedContinuous, fifoLatestReady} {
		got := testVulkanPresentModeCapability([]uint32{fifo, requested}, success, success, 2, false, requested)
		if got.probe.status != verified || got.probe.result != success || got.probe.modeCount != 2 ||
			!slices.Contains(got.advertised, requested) {
			t.Fatalf("extended mode %d diagnostic=%+v advertised=%v", requested, got.probe, got.advertised)
		}
		assertCreate(t, got, requested)
	}

	for _, tc := range []struct {
		name, want  string
		host        []uint32
		count, list int32
		capacity    uint32
		requested   uint32
		result      int32
		status      uint32
		countWant   uint32
		advertised  []uint32
	}{
		{"unavailable", "unavailable", nil, -3, success, 0, fifo, -3, unavailable, 0, nil},
		{"malformed-empty", "malformed", nil, success, success, 0, mailbox, success, malformed, 0, nil},
		{"malformed-duplicate", "malformed", []uint32{fifo, mailbox, mailbox}, success, success, 3, mailbox, success, malformed, 3, []uint32{mailbox}},
		{"malformed-unknown", "malformed", []uint32{fifo, 99}, success, success, 2, 99, success, malformed, 2, []uint32{fifo, 99}},
		{"incomplete-host-list", "incomplete", []uint32{fifo, mailbox}, success, incomplete, 2, mailbox, incomplete, probeIncomplete, 2, []uint32{mailbox}},
		{"incomplete-client-buffer", "incomplete", []uint32{fifo, mailbox}, success, success, 0, mailbox, incomplete, probeIncomplete, 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := testVulkanPresentModeCapability(tc.host, tc.count, tc.list, tc.capacity, false, tc.requested)
			if got.enumerateResult != tc.result || !slices.Equal(got.advertised, tc.advertised) {
				t.Fatalf("enumerate=%d advertised=%v want %d/%v", got.enumerateResult, got.advertised, tc.result, tc.advertised)
			}
			if got.probe.status != tc.status || got.probe.result != tc.result || got.probe.modeCount != tc.countWant {
				t.Fatalf("probe=%+v want %s status=%d result=%d count=%d", got.probe, tc.want, tc.status, tc.result, tc.countWant)
			}
			assertCreate(t, got, tc.requested)
		})
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

func TestVulkanPresentStatsFollowTIPSYDIAGConsumer(t *testing.T) {
	t.Cleanup(func() {
		SetVulkanPresentStats(false)
		resetVulkanPresentStats()
	})

	t.Setenv("TIPSY_DIAG", "")
	if presentStatsLoggerEnabled() {
		t.Fatal("present stats gate enabled without TIPSY_DIAG=1")
	}
	SetVulkanPresentStats(presentStatsLoggerEnabled())
	if VulkanPresentStatsEnabled() {
		t.Fatal("present stats enabled with no running consumer")
	}

	t.Setenv("TIPSY_DIAG", "1")
	if !presentStatsLoggerEnabled() {
		t.Skip("graphics Info logger disabled by TIPSY_LOG; TIPSY_DIAG=1 case not observable")
	}
	SetVulkanPresentStats(presentStatsLoggerEnabled())
	if !VulkanPresentStatsEnabled() {
		t.Fatal("present stats disabled with TIPSY_DIAG=1 and the graphics Info logger active")
	}
}

func TestVulkanCreateDeviceIsWrappedReadOnly(t *testing.T) {
	if !testVulkanProcIsWrapped("vkCreateDevice") {
		t.Fatal("vkCreateDevice GIPA is not the Tipsy observation wrapper")
	}
}

func TestVulkanDeviceCapabilityObservationIsOneTime(t *testing.T) {
	first := testVulkanObserveDeviceCreate([]string{"VK_KHR_swapchain", "VK_KHR_present_wait"})
	if !first {
		t.Fatal("first device-capability observation did not log")
	}
	if testVulkanObserveDeviceCreate(nil) {
		t.Fatal("device-capability observation logged more than once")
	}
}

func TestVulkanPacingProcQueryCounts(t *testing.T) {
	before := testVulkanPacingQueryCounts()
	testVulkanNotePacingQuery("vkWaitForPresentKHR")
	testVulkanNotePacingQuery("vkWaitForPresentKHR")
	testVulkanNotePacingQuery("vkGetPastPresentationTimingEXT")
	testVulkanNotePacingQuery("vkQueuePresentKHR")
	after := testVulkanPacingQueryCounts()
	if after[0] != before[0]+2 || after[3] != before[3]+1 {
		t.Fatalf("pacing counts before=%v after=%v", before, after)
	}
	if after[1] != before[1] || after[2] != before[2] {
		t.Fatalf("unqueried pacing names moved: before=%v after=%v", before, after)
	}
	if !testVulkanPacingQueryLogged() {
		t.Fatal("pacing query observation never logged")
	}
}

func TestVulkanSwapchainResolverPrefersLoaderTrampoline(t *testing.T) {
	want := testVulkanHostLoaderSymbol("vkCreateSwapchainKHR")
	if want == 0 {
		t.Skip("host Vulkan loader not present; loader-preferred resolver cannot be proven")
	}
	got := testVulkanResolveCreateSwapchain()
	if got != want {
		t.Fatalf("swapchain resolver = %#x want loader %#x", got, want)
	}
	if again := testVulkanResolveCreateSwapchain(); again != want {
		t.Fatalf("swapchain resolver not stable: %#x want %#x", again, want)
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

func drainVulkanPresentCallDurations() uint64 {
	_, cursor := VulkanPresentCallDurations(0)
	return cursor
}

func TestVulkanPresentCallDurationsOnlyInTimingEpoch(t *testing.T) {
	SetVulkanPresentTiming(false)
	SetVulkanPresentTiming(true)
	t.Cleanup(func() { SetVulkanPresentTiming(false) })
	base := drainVulkanPresentCallDurations()

	testVulkanNotePresentResultDuration(0, 1_000_000_000, 100)
	testVulkanNotePresentResultDuration(0, 1_001_000_000, 300)
	testVulkanNotePresentResultDuration(0, 1_002_000_000, 200)
	testVulkanNotePresentResultDuration(-3, 1_003_000_000, 9_999) // failures are not samples
	stats, next := VulkanPresentCallDurations(base)
	if stats.Count != 3 || stats.P50NS != 200 || stats.P99NS != 300 || stats.MaxNS != 300 ||
		stats.Overwritten != 0 {
		t.Fatalf("duration stats = %+v next=%d", stats, next)
	}
	if next != base+3 {
		t.Fatalf("duration cursor = %d want %d", next, base+3)
	}
	SetVulkanPresentTiming(false)
	testVulkanNotePresentResultDuration(0, 1_004_000_000, 9_999)
	if again, _ := VulkanPresentCallDurations(next); again.Count != 0 {
		t.Fatalf("disabled timing epoch recorded durations: %+v", again)
	}
}

func TestVulkanPresentCallDurationsOverwriteAccounting(t *testing.T) {
	SetVulkanPresentTiming(false)
	SetVulkanPresentTiming(true)
	t.Cleanup(func() { SetVulkanPresentTiming(false) })
	base := drainVulkanPresentCallDurations()
	const excess = 5
	for i := 0; i < VulkanPresentTimingCapacity+excess; i++ {
		testVulkanNotePresentResultDuration(0, 1_000+uint64(i), uint64(i)+1)
	}
	stats, _ := VulkanPresentCallDurations(base)
	if stats.Overwritten != excess || stats.Count != VulkanPresentTimingCapacity {
		t.Fatalf("duration overwrite accounting: %+v", stats)
	}
	if stats.MaxNS != uint64(VulkanPresentTimingCapacity+excess) {
		t.Fatalf("max retained duration = %d, want %d", stats.MaxNS, VulkanPresentTimingCapacity+excess)
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
