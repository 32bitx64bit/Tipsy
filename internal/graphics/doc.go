// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package graphics binds EGL and OpenGL ES 2 to a native Tipsy X11 window and
// reports pre-client renderer capabilities.
//
// The first-frame path is EGL-on-X11 (EGL_KHR_platform_x11). Wayland is not
// required. Vulkan is not selectable until the Android Vulkan loader and
// VK_KHR_android_surface contract are translated to the native X11 window.
package graphics
