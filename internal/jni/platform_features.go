// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

const (
	androidHardwareTypePC      = "android.hardware.type.pc"
	androidHardwareTouchscreen = "android.hardware.touchscreen"
)

// platformSystemFeature is the truthful PackageManager feature surface exposed
// by the current Linux host bridge. Keep this deliberately narrow: the
// official Roblox APK uses android.hardware.type.pc to derive its keyboard and
// mouse PlatformParams values and queries touchscreen separately. Mirror the
// same process-wide pointer profile used by the input bridge so PackageManager,
// MotionEvent, and PlatformParams cannot disagree. Camera, microphone, sensors,
// and other Android features stay false until their own host bridge is
// constructed and verified.
func platformSystemFeature(name string) bool {
	touch := pointerDeviceIsTouch()
	switch name {
	case androidHardwareTypePC:
		return !touch
	case androidHardwareTouchscreen:
		return touch
	default:
		return false
	}
}
