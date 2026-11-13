// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux || !amd64

package android

// VulkanPresentTimingSupported reports that this build has no Vulkan adapter
// and therefore no present-timing ring.
func VulkanPresentTimingSupported() bool { return false }
