// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

const (
	androidHardwareTypePC      = "android.hardware.type.pc"
	androidHardwareTouchscreen = "android.hardware.touchscreen"
	// android.hardware.microphone follows mic.Allowed() (file < env), the
	// same door as RECORD_AUDIO grant. OpenSL C getenv is a separate native
	// kill-switch.
	androidHardwareMicrophone = "android.hardware.microphone"
	// android.hardware.audio.low_latency (FEATURE_AUDIO_LOW_LATENCY) follows
	// hostAudioLowLatency() (fmod_audio.go): the same answer FMOD's
	// supportsLowLatency() and the WebRTC ADM parameters give, so FMOD,
	// WebRTC and PackageManager cannot disagree about the host output.
	// android.hardware.audio.pro stays false: no host guarantee behind it.
	androidHardwareAudioLowLatency = "android.hardware.audio.low_latency"
)

// platformSystemFeature is the truthful PackageManager feature surface exposed
// by the current Linux host bridge. Keep this deliberately narrow: the
// official Roblox APK uses android.hardware.type.pc to derive its keyboard and
// mouse PlatformParams values and queries touchscreen separately. Mirror the
// same process-wide pointer profile used by the input bridge so PackageManager,
// MotionEvent, and PlatformParams cannot disagree. Microphone follows
// mic.Allowed() after host-verified capture. Camera, sensors, and other
// Android features stay false until their own host bridge is verified.
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
