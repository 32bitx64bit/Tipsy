// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

const (
	androidHardwareTypePC      = "android.hardware.type.pc"
	androidHardwareTouchscreen = "android.hardware.touchscreen"
	// android.hardware.microphone follows the live microphone door (file < env), the same
	// door as the RECORD_AUDIO grant.
	androidHardwareMicrophone = "android.hardware.microphone"
	// android.hardware.audio.low_latency follows hostAudioLowLatency();
	// android.hardware.audio.pro stays false: no host guarantee behind it.
	androidHardwareAudioLowLatency = "android.hardware.audio.low_latency"
)

// platformSystemFeature is the truthful PackageManager feature surface. pc and
// touchscreen mirror the input bridge's pointer profile; microphone follows
// live microphone door; all other features stay false until their bridge is verified.
func platformSystemFeature(name string) bool {
	touch := pointerDeviceIsTouch()
	switch name {
	case androidHardwareTypePC:
		return !touch
	case androidHardwareTouchscreen:
		return touch
	case androidHardwareMicrophone:
		return microphoneDoorOpen()
	case androidHardwareAudioLowLatency:
		return hostAudioLowLatency()
	default:
		return false
	}
}
