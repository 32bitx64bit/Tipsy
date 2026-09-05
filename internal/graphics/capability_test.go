// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import (
	"errors"
	"strings"
	"testing"
)

func readyVulkanHost() VulkanHostProbe {
	return VulkanHostProbe{
		Library:         true,
		Loader:          true,
		PhysicalDevices: 1,
		XcbSurface:      true,
		Detail:          "host Vulkan loader found 1 physical device(s)",
	}
}

func TestAutoAndOpenGLResolveToConstructedPath(t *testing.T) {
	caps := buildRendererCapabilities(true, true, VulkanHostProbe{})
	for _, choice := range []Renderer{RendererAuto, RendererOpenGL} {
		got, err := caps.Resolve(choice)
		if err != nil || got != RendererOpenGL {
			t.Fatalf("Resolve(%q)=%q, %v; want %q", choice, got, err, RendererOpenGL)
		}
	}
}

func TestAutoPrefersVulkanWhenCompletePathIsAvailable(t *testing.T) {
	caps := buildRendererCapabilities(true, true, readyVulkanHost())
	got, err := caps.Resolve(RendererAuto)
	if err != nil || got != RendererVulkan {
		t.Fatalf("Resolve(Auto)=%q, %v; want vulkan", got, err)
	}
	explicit, err := caps.Resolve(RendererOpenGL)
	if err != nil || explicit != RendererOpenGL {
		t.Fatalf("explicit OpenGL=%q, %v", explicit, err)
	}
}

func TestVulkanRequiresHostDeviceAndWSI(t *testing.T) {
	noBridge := buildRendererCapabilities(true, false, readyVulkanHost())
	if noBridge.Vulkan.Available {
		t.Fatal("Vulkan selectable without the Android loader adapter")
	}
	noWSI := buildRendererCapabilities(true, true, VulkanHostProbe{
		Library: true, Loader: true, PhysicalDevices: 1,
	})
	if noWSI.Vulkan.Available {
		t.Fatal("Vulkan selectable without host xcb/xlib WSI")
	}
	_, err := noWSI.Resolve(RendererVulkan)
	if !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("Resolve(Vulkan) error=%v, want typed renderer-unavailable error", err)
	}
	var unsupported *UnsupportedRendererError
	if !errors.As(err, &unsupported) || unsupported.Renderer != RendererVulkan {
		t.Fatalf("Resolve(Vulkan) error=%T %v", err, err)
	}
	if !strings.Contains(err.Error(), "VK_KHR_xcb_surface") {
		t.Fatalf("Vulkan error is not actionable: %v", err)
	}
	ready := buildRendererCapabilities(true, true, readyVulkanHost())
	if !ready.Vulkan.Available {
		t.Fatalf("complete Vulkan path not selectable: %+v", ready.Vulkan)
	}
	got, err := ready.Resolve(RendererVulkan)
	if err != nil || got != RendererVulkan {
		t.Fatalf("Resolve(Vulkan)=%q %v", got, err)
	}
}

func TestVulkanLibraryAloneIsNotSupport(t *testing.T) {
	caps := buildRendererCapabilities(true, true, VulkanHostProbe{Library: true})
	if caps.Vulkan.Available || !strings.Contains(caps.Vulkan.Reason, "loader is unusable") {
		t.Fatalf("Vulkan capability=%+v", caps.Vulkan)
	}
}

func TestUnavailableBuildRejectsAutoBeforeStartup(t *testing.T) {
	caps := buildRendererCapabilities(false, false, VulkanHostProbe{})
	_, err := caps.Resolve(RendererAuto)
	if !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("Resolve(Auto) error=%v", err)
	}
}

func TestUnknownRendererRejected(t *testing.T) {
	caps := buildRendererCapabilities(true, true, VulkanHostProbe{})
	if _, err := caps.Resolve(Renderer("metal")); err == nil {
		t.Fatal("unknown renderer resolved")
	}
}

func TestLiveProbeVulkanMatchesAdapterAndHost(t *testing.T) {
	caps := ProbeRendererCapabilities()
	t.Logf("renderer capabilities: OpenGL=%+v Vulkan=%+v", caps.OpenGL, caps.Vulkan)
	want := platformVulkanConstructed() && caps.Vulkan.Host.Library && caps.Vulkan.Host.Loader &&
		caps.Vulkan.Host.PhysicalDevices > 0 && caps.Vulkan.Host.HasWSI()
	if caps.Vulkan.Available != want {
		t.Fatalf("Vulkan.Available=%v want %v: %+v", caps.Vulkan.Available, want, caps.Vulkan)
	}
	if caps.Vulkan.Reason == "" || caps.Vulkan.Host.Detail == "" {
		t.Fatalf("Vulkan capability lacks diagnostic detail: %+v", caps.Vulkan)
	}
	if !caps.Vulkan.Available {
		return
	}
	got, err := caps.Resolve(RendererAuto)
	if err != nil || got != RendererVulkan {
		t.Fatalf("live Auto resolve=%q %v", got, err)
	}
}
