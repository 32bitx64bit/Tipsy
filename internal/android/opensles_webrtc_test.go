// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// The bridge must answer the capability probes a client runs before it
// creates a stream the way Android's OpenSL ES (frameworks/wilhelm) does:
// QueryNumSupportedInterfaces / QuerySupportedInterfaces enumerate exactly
// the interfaces GetInterface hands out, unknown object classes and
// extensions are refused honestly, OutputMix reports the single default
// output device, an interface the class lacks is FEATURE_UNSUPPORTED with a
// cleared out pointer, and an unknown configuration key is
// PARAMETER_INVALID instead of a blind success. Every refused request is
// logged once (deduplicated) so the live trace shows what the client probes.
func TestOpenSLProbeShape(t *testing.T) {
	allowMicrophone(t)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := audioTestProbeShape()
	if r.rc != 0 {
		t.Fatalf("probe sequence failed bits=%#x: %+v", r.failed, r)
	}
	if r.recorderIIDs != 4 {
		t.Fatalf("recorder interface census = %d, want 4 (RECORD, BUFFERQUEUE, ANDROIDSIMPLEBUFFERQUEUE, ANDROIDCONFIGURATION)", r.recorderIIDs)
	}
	if r.mixDevice != 0xffffffff {
		t.Fatalf("OutputMix destination = %#x, want SL_DEFAULTDEVICEID_AUDIOOUTPUT", r.mixDevice)
	}
	if r.configPerfResult != 0 {
		t.Fatalf("androidPerformanceMode SetConfiguration = %d, want SUCCESS", r.configPerfResult)
	}
	if r.configUnknownResult != 2 {
		t.Fatalf("unknown SetConfiguration key = %d, want SL_RESULT_PARAMETER_INVALID (2)", r.configUnknownResult)
	}
	logs := buf.String()
	for _, want := range []string{
		"engine GetInterface(PLAY): FEATURE_UNSUPPORTED",
		"QueryNumSupportedInterfaces(recorder): 4",
		"QueryNumSupportedInterfaces(objectID=0x1006): FEATURE_UNSUPPORTED",
		"IsExtensionSupported(ANDROID_SDK_LEVEL_23): false",
		"GetDestinationOutputDeviceIDs count query: 1 device",
		"recorder GetInterface(VOLUME): FEATURE_UNSUPPORTED",
		"recorder: IODEVICE type=1 id=0xffffffff, 2 buffers, PCM ch=1 rate=48000.000 bits=16",
		"tipsyUnknownKey=7): unknown key, PARAMETER_INVALID",
		"androidPerformanceMode=1): accepted",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("probe diagnostics missing %q:\n%s", want, logs)
		}
	}
	// The engine PLAY probe ran four times; the once-table keeps one line.
	if got := strings.Count(logs, "engine GetInterface(PLAY)"); got != 1 {
		t.Fatalf("refused-interface diagnostic logged %d times, want 1", got)
	}
}

// The bridge must serve WebRTC's legacy Android ADM exactly as upstream
// opensles_player.cc / opensles_recorder.cc drive OpenSL ES: engine option
// + no interfaces, OutputMix sink with a NULL format, required
// {ANDROIDCONFIGURATION, BUFFERQUEUE, VOLUME} on the player and
// {ANDROIDSIMPLEBUFFERQUEUE, ANDROIDCONFIGURATION} on the recorder,
// configuration before Realize, two buffers primed before the state change,
// re-enqueue from the callback, and a stop sequence whose Clear leaves the
// queue at count 0 / index 0.
func TestOpenSLWebRtcShape(t *testing.T) {
	allowMicrophone(t)
	r := audioTestWebRtcShape()
	if r.rc != 0 {
		t.Fatalf("WebRTC-shaped sequence failed: %+v", r)
	}
	if r.playCallbacks != 4 || r.captureCallbacks != 4 {
		t.Fatalf("callbacks play=%d capture=%d, want 4/4 (two primed + two re-enqueued): %+v",
			r.playCallbacks, r.captureCallbacks, r)
	}
	if !r.playerVoice || !r.recorderVoice {
		t.Fatalf("voice stream type / recording preset not recorded: %+v", r)
	}
	if r.clearedCount != 0 || r.clearedIndex != 0 {
		t.Fatalf("Clear left count=%d index=%d, want 0/0 (upstream StopPlayout DCHECKs both): %+v",
			r.clearedCount, r.clearedIndex, r)
	}
}

// The voice-tagged player asks Pulse for a 50 ms target buffer instead of
// the server default; the real endpoint must accept that attribute.
func TestOpenSLHostVoicePlayback(t *testing.T) {
	if os.Getenv("TIPSY_AUDIO_HOST_TEST") != "1" {
		t.Skip("set TIPSY_AUDIO_HOST_TEST=1 to open the host playback device")
	}
	const bytes = 960 // one WebRTC 10 ms block, 48 kHz mono S16LE
	written, callbacks, rc := audioTestHostVoicePlayback(48000, 1, bytes)
	if rc != 0 || written != bytes || callbacks != 1 {
		t.Fatalf("host voice playback rc=%d written=%d callbacks=%d", rc, written, callbacks)
	}
}
