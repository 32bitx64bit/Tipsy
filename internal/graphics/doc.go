// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package graphics binds EGL and OpenGL ES 2, and an Android Vulkan WSI
// adapter, to a native Tipsy X11 window and reports pre-client renderer
// capabilities.
//
// The OpenGL path is EGL-on-X11 (EGL_KHR_platform_x11). The Vulkan path is an
// identity-handle loader adapter: VK_KHR_android_surface is translated to
// VK_KHR_xcb_surface (Xlib fallback) on the same X11 window. Wayland is not
// required. Auto selects Vulkan when that complete path is available.
package graphics
