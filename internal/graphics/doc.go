// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package graphics binds EGL and OpenGL ES 2 to a native Tipsy X11 window.
//
// The first-frame path is EGL-on-X11 (EGL_KHR_platform_x11). Wayland is not
// required. Mapping Roblox ANativeWindow* onto this surface is later work.
package graphics
