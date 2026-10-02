// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

import "errors"

// doorStats is the content-free state of the live-door fixture recorder
// (opensles.c tipsy_audio_test_door_*). No PCM leaves C.
type doorStats struct {
	Callbacks    uint32
	PCMCallbacks uint32 // completions that delivered non-silent data
	Opens        uint32
	Closes       uint32
	Reads        uint32
	Queued       uint32
	StreamOpen   bool // the fake host stream is currently held by the recorder
}

func doorFixtureStart(bytes uint32) error {
	if C.tipsy_audio_test_door_start(C.uint32_t(bytes)) != 0 {
		return errors.New("door fixture did not start")
	}
	return nil
}

func doorFixtureEnqueue() error {
	if C.tipsy_audio_test_door_enqueue() != 0 {
		return errors.New("door fixture enqueue failed")
	}
	return nil
}

func doorFixtureSetRecording(recording bool) error {
	v := C.int(0)
	if recording {
		v = 1
	}
	if C.tipsy_audio_test_door_set_recording(v) != 0 {
		return errors.New("door fixture state change failed")
	}
	return nil
}

func doorFixtureStats() doorStats {
	var s C.tipsy_audio_door_stats
	C.tipsy_audio_test_door_stats(&s)
	return doorStats{
		Callbacks:    uint32(s.callbacks),
		PCMCallbacks: uint32(s.pcm_callbacks),
		Opens:        uint32(s.opens),
		Closes:       uint32(s.closes),
		Reads:        uint32(s.reads),
		Queued:       uint32(s.queued),
		StreamOpen:   s.stream_open != 0,
	}
}

// doorFixtureCloseAfterNextRead arms the fake host so the persisted switch
// flips closed (no sweep) right after its next capture read: the recorder must
// then wipe and withhold that buffer.
func doorFixtureCloseAfterNextRead() {
	C.tipsy_audio_test_door_arm_close_after_read()
}

func doorFixtureStop() {
	C.tipsy_audio_test_door_stop()
}

// resetMicrophoneFileDoor puts the C-side persisted switch back to its default
// (allowed) so one test's file state cannot leak into the next.
func resetMicrophoneFileDoor() {
	C.tipsy_audio_set_microphone_file_door(1)
}

func microphoneEnvDecision() int {
	return int(C.tipsy_audio_microphone_env_decision())
}

func microphoneFileDoor() bool {
	return C.tipsy_audio_microphone_file_door() != 0
}
