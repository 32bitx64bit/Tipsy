// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
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
		SetVulkanPresentStats(true)
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
