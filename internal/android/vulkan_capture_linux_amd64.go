// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

// VulkanPresentTimingSupported reports whether this build's Vulkan adapter
// records the opt-in present-timing ring.
func VulkanPresentTimingSupported() bool { return true }
