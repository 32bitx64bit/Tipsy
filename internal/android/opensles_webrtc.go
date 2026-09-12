// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

// webRtcShapeResult is what the WebRTC-shaped OpenSL probe observed.
type webRtcShapeResult struct {
	playCallbacks    uint32
	captureCallbacks uint32
	playerVoice      bool
	recorderVoice    bool
	clearedCount     uint32
	clearedIndex     uint32
	rc               int
}

// audioTestHostVoicePlayback opens the real host playback endpoint through a
// player tagged as WebRTC's voice stream (voice-sized Pulse target buffer).
func audioTestHostVoicePlayback(rate, channels, bytes uint32) (uint64, uint32, int) {
	var written C.uint64_t
	var callbacks C.uint32_t
	rc := C.tipsy_audio_test_host_voice_playback(C.uint32_t(rate), C.uint32_t(channels), C.uint32_t(bytes), &written, &callbacks)
	return uint64(written), uint32(callbacks), int(rc)
}

// probeShapeResult is what the capability-probe sequence observed.
type probeShapeResult struct {
	failed              uint32
	recorderIIDs        uint32
	mixDevice           uint32
	configUnknownResult uint32
	configPerfResult    uint32
	rc                  int
}

// audioTestProbeShape runs the pre-stream capability probes (interface
// census, extensions, OutputMix destinations, refused interfaces,
// configuration keys) against the fake host.
func audioTestProbeShape() probeShapeResult {
	var failed, recorderIIDs, mixDevice, unknownResult, perfResult C.uint32_t
	rc := C.tipsy_audio_test_probe_shape(&failed, &recorderIIDs, &mixDevice, &unknownResult, &perfResult)
	return probeShapeResult{
		failed:              uint32(failed),
		recorderIIDs:        uint32(recorderIIDs),
		mixDevice:           uint32(mixDevice),
		configUnknownResult: uint32(unknownResult),
		configPerfResult:    uint32(perfResult),
		rc:                  int(rc),
	}
}

// audioTestWebRtcShape drives Tipsy's OpenSL bridge through the exact call
// sequence of WebRTC's legacy Android AudioDeviceModule (OpenSLESPlayer +
// OpenSLESRecorder) against the deterministic fake host.
func audioTestWebRtcShape() webRtcShapeResult {
	var playCB, capCB, clearedCount, clearedIndex C.uint32_t
	var playerVoice, recorderVoice C.int
	rc := C.tipsy_audio_test_webrtc_shape(&playCB, &capCB, &playerVoice, &recorderVoice, &clearedCount, &clearedIndex)
	return webRtcShapeResult{
		playCallbacks:    uint32(playCB),
		captureCallbacks: uint32(capCB),
		playerVoice:      playerVoice != 0,
		recorderVoice:    recorderVoice != 0,
		clearedCount:     uint32(clearedCount),
		clearedIndex:     uint32(clearedIndex),
		rc:               int(rc),
	}
}
