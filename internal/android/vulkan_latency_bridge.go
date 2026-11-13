// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "vulkan_latency.h"
*/
import "C"

import "github.com/tipsy-linux/tipsy/internal/logging"

// The latency C unit calls these two bridges. Anti-lag failures land on
// CatGraphics as a soft report; the shader-cache note is informational.

//export GoAndroid_LogVulkanAntiLag
func GoAndroid_LogVulkanAntiLag(state C.int, reason *C.char) {
	logging.Logger(logging.CatGraphics).Info("Android Vulkan anti-lag disabled",
		"reason", C.GoString(reason))
}

//export GoAndroid_LogVulkanShaderCache
func GoAndroid_LogVulkanShaderCache(persistent C.int, dir *C.char) {
	logging.Logger(logging.CatGraphics).Info("Mesa shader cache persistence",
		"persistent", persistent != 0, "dir", C.GoString(dir))
}
