// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package graphics

func platformOpenGLConstructed() bool { return false }

func platformVulkanConstructed() bool { return false }

func platformProbeHostVulkan() VulkanHostProbe {
	return VulkanHostProbe{Detail: "this build has no native Vulkan host probe"}
}
