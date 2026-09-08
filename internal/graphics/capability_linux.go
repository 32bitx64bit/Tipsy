// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#cgo LDFLAGS: -ldl
#include "vulkan_host.h"
*/
import "C"

import "fmt"

func platformOpenGLConstructed() bool { return true }

func platformVulkanConstructed() bool { return true }

func platformProbeHostVulkan() VulkanHostProbe {
	var api, devices C.uint32_t
	var result C.int32_t
	var xcb, xlib C.int
	stage := int(C.tipsy_vulkan_host_probe(&api, &devices, &result, &xcb, &xlib))
	probe := VulkanHostProbe{
		Library:         stage != int(C.TIPSY_VK_NO_LIBRARY),
		Loader:          stage >= int(C.TIPSY_VK_NO_CREATE),
		APIVersion:      uint32(api),
		PhysicalDevices: uint32(devices),
		XcbSurface:      xcb != 0,
		XlibSurface:     xlib != 0,
		Result:          int32(result),
	}
	switch stage {
	case int(C.TIPSY_VK_NO_LIBRARY):
		probe.Detail = "host libvulkan was not found"
	case int(C.TIPSY_VK_NO_LOADER):
		probe.Detail = "vkGetInstanceProcAddr is missing"
	case int(C.TIPSY_VK_NO_CREATE):
		probe.Detail = "vkCreateInstance is missing"
	case int(C.TIPSY_VK_CREATE_FAILED):
		probe.Detail = fmt.Sprintf("vkCreateInstance failed with VkResult %d", probe.Result)
	case int(C.TIPSY_VK_NO_ENUMERATE):
		probe.Detail = "vkEnumeratePhysicalDevices is missing"
	case int(C.TIPSY_VK_ENUMERATE_FAILED):
		probe.Detail = fmt.Sprintf("vkEnumeratePhysicalDevices failed with VkResult %d", probe.Result)
	case int(C.TIPSY_VK_NO_DEVICE):
		probe.Detail = "the host Vulkan loader found no physical devices"
	case int(C.TIPSY_VK_DEVICE_READY):
		probe.Detail = fmt.Sprintf("host Vulkan loader found %d physical device(s)", probe.PhysicalDevices)
	default:
		probe.Detail = "unknown host Vulkan probe result"
	}
	return probe
}
