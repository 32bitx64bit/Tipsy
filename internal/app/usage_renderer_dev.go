// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package app

const launchTestRendererHelp = `
  TIPSY_TEST_RENDERER=opengl
            One launch only: request the EGL/OpenGL client path for visual
            compatibility testing. This does not alter saved renderer or
            client settings. A guest EGL swap is required to confirm client
            selection; Vulkan is intentionally not a test value.
`
