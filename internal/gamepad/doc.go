// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package gamepad reads Linux evdev gamepads in pure Go and translates
// them to Android KeyEvent/MotionEvent vocabulary for the engine's private
// direct gamepad natives (Phase 0 verdict: direct-only, Z/RZ sticks).
//
// The package never touches X11, JNI, or engine symbols. It emits plain Go
// structs and channels that are unit-testable with recorded input_event
// byte streams. It never synthesizes a fake-connected pad: zero host pads
// means zero devices, and EACCES degrades to an actionable error.
package gamepad
