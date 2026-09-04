// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"testing"
)

func TestOpenSLPlaybackBufferQueue(t *testing.T) {
	written, callbacks, rc := audioTestPlayback(48000, 2, 3840)
	if rc != 0 {
		t.Fatalf("playback bridge rc=%d", rc)
	}
	if written != 3840 || callbacks != 1 {
		t.Fatalf("playback written=%d callbacks=%d, want 3840/1", written, callbacks)
	}
}

func TestOpenSLCaptureStartsOnlyWhenRecording(t *testing.T) {
	read, callbacks, rc := audioTestCapture(48000, 1, 960)
	if rc != 0 {
		t.Fatalf("capture bridge rc=%d", rc)
	}
	if read != 960 || callbacks != 1 {
		t.Fatalf("capture read=%d callbacks=%d, want 960/1", read, callbacks)
	}
}

func TestOpenSLRejectsUnsupportedPCM(t *testing.T) {
	if rc := audioTestInvalidFormat(); rc != 0 {
		t.Fatalf("invalid PCM format was accepted, rc=%d", rc)
	}
}

func TestOpenSLReopensAfterHostFailure(t *testing.T) {
	opens, writes, callbacks, rc := audioTestRetry()
	if rc != 0 {
		t.Fatalf("retry bridge rc=%d", rc)
	}
	if opens < 2 || writes < 2 || callbacks != 1 {
		t.Fatalf("opens=%d writes=%d callbacks=%d, want >=2/>=2/1", opens, writes, callbacks)
	}
}

func TestOpenSLSymbolSurface(t *testing.T) {
	for _, name := range []string{
		"slCreateEngine", "SL_IID_ENGINE", "SL_IID_PLAY", "SL_IID_RECORD",
		"SL_IID_BUFFERQUEUE", "SL_IID_ANDROIDSIMPLEBUFFERQUEUE", "SL_IID_VOLUME",
		"SL_IID_ANDROIDCONFIGURATION", "SL_IID_OUTPUTMIX",
	} {
		addr, err := Provider().Lookup("libOpenSLES.so", name)
		if err != nil || addr == 0 {
			t.Fatalf("Lookup libOpenSLES.so %s: addr=%#x err=%v", name, addr, err)
		}
	}
}

func TestOpenSLHostPlayback(t *testing.T) {
	if os.Getenv("TIPSY_AUDIO_HOST_TEST") != "1" {
		t.Skip("set TIPSY_AUDIO_HOST_TEST=1 to open the host playback device")
	}
	written, callbacks, rc := audioTestHostPlayback(48000, 2, 19200)
	if rc != 0 || written != 19200 || callbacks != 1 {
		t.Fatalf("host playback rc=%d written=%d callbacks=%d", rc, written, callbacks)
	}
}
