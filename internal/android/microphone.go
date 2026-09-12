// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

// SetCaptureMuted gates host PCM into the OpenSL recorder. When muted, client
// buffers complete with silence and the queue callback still fires so the
// official client does not stall. Default is unmuted. JNI
// WebRtcAudioManager.setMicrophoneMute drives this; FMOD's live recorder
// start/stops capture itself.
func SetCaptureMuted(muted bool) {
	v := C.int(0)
	if muted {
		v = 1
	}
	C.tipsy_audio_set_capture_muted(v)
}

// CaptureMuted reports the process-wide OpenSL capture mute gate.
func CaptureMuted() bool {
	return C.tipsy_audio_capture_muted() != 0
}

// MicrophoneDisabled reports the process-wide capture kill-switch:
// TIPSY_DISABLE_MICROPHONE=1|true|yes, or TIPSY_MICROPHONE=0|off|false|no.
// The deprecated DISABLE alias stays working. JNI uses the same env door.
func MicrophoneDisabled() bool {
	return C.tipsy_audio_microphone_disabled() != 0
}
