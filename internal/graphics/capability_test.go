// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import (
	"errors"
	"strings"
	"testing"
)

func TestAutoAndOpenGLResolveToConstructedPath(t *testing.T) {
	caps := buildRendererCapabilities(true, VulkanHostProbe{})
	for _, choice := range []Renderer{RendererAuto, RendererOpenGL} {
		got, err := caps.Resolve(choice)
		if err != nil || got != RendererOpenGL {
			t.Fatalf("Resolve(%q)=%q, %v; want %q", choice, got, err, RendererOpenGL)
		}
	}
}

func TestVulkanRequiresCompletePlatformBridge(t *testing.T) {
	caps := buildRendererCapabilities(true, VulkanHostProbe{
		Library:         true,
		Loader:          true,
		PhysicalDevices: 1,
		Detail:          "host Vulkan loader found 1 physical device(s)",
	})
	if caps.Vulkan.Available {
		t.Fatal("host Vulkan device incorrectly made the Android client path selectable")
	}
	_, err := caps.Resolve(RendererVulkan)
	if !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("Resolve(Vulkan) error=%v, want typed renderer-unavailable error", err)
	}
	var unsupported *UnsupportedRendererError
	if !errors.As(err, &unsupported) || unsupported.Renderer != RendererVulkan {
		t.Fatalf("Resolve(Vulkan) error=%T %v", err, err)
	}
	if !strings.Contains(err.Error(), "VK_KHR_android_surface") {
		t.Fatalf("Vulkan error is not actionable: %v", err)
	}
}

func TestVulkanLibraryAloneIsNotSupport(t *testing.T) {
	caps := buildRendererCapabilities(true, VulkanHostProbe{Library: true})
	if caps.Vulkan.Available || !strings.Contains(caps.Vulkan.Reason, "loader is unusable") {
		t.Fatalf("Vulkan capability=%+v", caps.Vulkan)
	}
}

func TestUnavailableBuildRejectsAutoBeforeStartup(t *testing.T) {
	caps := buildRendererCapabilities(false, VulkanHostProbe{})
	_, err := caps.Resolve(RendererAuto)
	if !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("Resolve(Auto) error=%v", err)
	}
}

func TestUnknownRendererRejected(t *testing.T) {
	caps := buildRendererCapabilities(true, VulkanHostProbe{})
	if _, err := caps.Resolve(Renderer("metal")); err == nil {
		t.Fatal("unknown renderer resolved")
	}
}

func TestLiveProbeNeverClaimsVulkanClientSupport(t *testing.T) {
	caps := ProbeRendererCapabilities()
	t.Logf("renderer capabilities: OpenGL=%+v Vulkan=%+v", caps.OpenGL, caps.Vulkan)
	if caps.Vulkan.Available {
		t.Fatalf("Vulkan became selectable without an Android surface bridge: %+v", caps.Vulkan)
	}
	if caps.Vulkan.Reason == "" || caps.Vulkan.Host.Detail == "" {
		t.Fatalf("Vulkan capability lacks diagnostic detail: %+v", caps.Vulkan)
	}
}
