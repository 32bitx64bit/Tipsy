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
// official client does not stall. Default is unmuted.
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

// MicrophoneDisabled reports the process-wide capture gate: the env
// kill-switches (TIPSY_MICROPHONE=0|off|false|no closes, 1|on|true|yes opens,
// TIPSY_DISABLE_MICROPHONE=1|true|yes closes; the newer name wins over the
// alias) over the persisted Settings switch last published by the door
// refresh. It is a pure read of that state (no file or Go work), the same value
// the OpenSL recorder and MicrophoneDoorOpen use. Call MicrophoneDoorOpen to
// refresh the persisted switch first.
func MicrophoneDisabled() bool {
	return C.tipsy_audio_microphone_disabled() != 0
}
