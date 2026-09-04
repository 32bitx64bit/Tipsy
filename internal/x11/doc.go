// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package x11 creates a native X11 InputOutput window for Tipsy.
//
// This is the display backend Roblox will render into. It does not use
// Wayland. It owns the EWMH title/icon/fullscreen contract and reports real
// ConfigureNotify geometry and desktop input to the Android compatibility
// layer.
package x11
