// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package x11 creates a native X11 InputOutput window for Tipsy.
//
// This is the display backend Roblox will render into. It does not use
// Wayland. Fullscreen, DPI, and input beyond WM_DELETE_WINDOW / Expose
// are later Milestone 9 work.
package x11
