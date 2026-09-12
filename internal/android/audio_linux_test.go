// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"testing"
)

func allowMicrophone(t *testing.T) {
	t.Helper()
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	SetCaptureMuted(false)
}

func TestOpenSLPlaybackBufferQueue(t *testing.T) {
	allowMicrophone(t)
	written, callbacks, rc := audioTestPlayback(48000, 2, 3840)
	if rc != 0 {
		t.Fatalf("playback bridge rc=%d", rc)
	}
	if written != 3840 || callbacks != 1 {
		t.Fatalf("playback written=%d callbacks=%d, want 3840/1", written, callbacks)
	}
}

func TestOpenSLCaptureStartsOnlyWhenRecording(t *testing.T) {
	allowMicrophone(t)
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
	allowMicrophone(t)
	opens, writes, callbacks, rc := audioTestRetry()
	if rc != 0 {
		t.Fatalf("retry bridge rc=%d", rc)
	}
	if opens < 2 || writes < 2 || callbacks != 1 {
		t.Fatalf("opens=%d writes=%d callbacks=%d, want >=2/>=2/1", opens, writes, callbacks)
	}
}

func TestOpenSLCaptureReopensAfterHostFailure(t *testing.T) {
	allowMicrophone(t)
	opens, reads, callbacks, rc := audioTestCaptureRetry()
	if rc != 0 {
		t.Fatalf("capture retry bridge rc=%d", rc)
	}
	if opens < 2 || reads < 2 || callbacks != 1 {
		t.Fatalf("opens=%d reads=%d callbacks=%d, want >=2/>=2/1", opens, reads, callbacks)
	}
}

func TestOpenSLDuplex(t *testing.T) {
	allowMicrophone(t)
	written, playCB, read, capCB, rc := audioTestDuplex(48000, 2, 19200)
	if rc != 0 {
		t.Fatalf("duplex bridge rc=%d", rc)
	}
	if written != 19200 || playCB != 1 || read != 19200 || capCB != 1 {
		t.Fatalf("duplex written=%d playCB=%d read=%d capCB=%d, want 19200/1/19200/1",
			written, playCB, read, capCB)
	}
}

func TestOpenSLCaptureMuteSilencesBuffer(t *testing.T) {
	allowMicrophone(t)
	read, callbacks, hadNonzero, rc := audioTestCaptureMuted()
	if rc != 0 {
		t.Fatalf("muted capture rc=%d", rc)
	}
	if read != 960 || callbacks != 1 {
		t.Fatalf("muted capture read=%d callbacks=%d, want 960/1", read, callbacks)
	}
	if hadNonzero != 0 {
		t.Fatal("muted capture delivered host samples")
	}
	if CaptureMuted() {
		t.Fatal("mute probe left the process muted")
	}
}

func TestOpenSLCaptureDisabled(t *testing.T) {
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "1")
	t.Setenv("TIPSY_MICROPHONE", "")
	SetCaptureMuted(false)
	if rc := audioTestCaptureRefused(); rc != 0 {
		t.Fatalf("disabled capture was not refused, rc=%d", rc)
	}
}

func TestOpenSLCaptureMicrophoneEnvOff(t *testing.T) {
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	for _, off := range []string{"0", "off", "false", "no"} {
		t.Setenv("TIPSY_MICROPHONE", off)
		SetCaptureMuted(false)
		if rc := audioTestCaptureRefused(); rc != 0 {
			t.Fatalf("TIPSY_MICROPHONE=%s did not refuse capture, rc=%d", off, rc)
		}
	}
}

func TestOpenSLCaptureMidstreamDisable(t *testing.T) {
	allowMicrophone(t)
	callbacks, reads, rc := audioTestCaptureMidstreamDisable()
	if rc != 0 {
		t.Fatalf("mid-stream disable rc=%d callbacks=%d reads=%d", rc, callbacks, reads)
	}
	if callbacks != 1 || reads != 1 {
		t.Fatalf("mid-stream disable callbacks=%d reads=%d, want 1/1", callbacks, reads)
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

// FMOD's OpenSL ES output plug-in loads the library at runtime:
// dlopen("libOpenSLES.so") then dlsym of slCreateEngine and the SL_IID_*
// tables (libroblox.so binds none of them at relocation time). The handle
// must resolve exactly what the in-process provider serves, a symbol the
// provider lacks must stay NULL, and a library Tipsy does not provide
// (libaaudio.so: SDK 26, no AAudio) must still fail to open.
func TestOpenSLDlopenHandle(t *testing.T) {
	h := openRegistered("libOpenSLES.so")
	if h == nil {
		t.Fatalf("dlopen(libOpenSLES.so) failed: %s", getDLError())
	}
	for _, name := range []string{
		"slCreateEngine", "SL_IID_ENGINE", "SL_IID_PLAY", "SL_IID_RECORD",
		"SL_IID_ANDROIDSIMPLEBUFFERQUEUE", "SL_IID_ANDROIDCONFIGURATION", "SL_IID_VOLUME",
	} {
		addr := lookupHandle(h, name)
		want, err := Provider().Lookup("libOpenSLES.so", name)
		if err != nil || addr == 0 || addr != want {
			t.Fatalf("dlsym(libOpenSLES.so, %s) = %#x, provider %#x err=%v", name, addr, want, err)
		}
	}
	if addr := lookupHandle(h, "SL_IID_TIPSY_NOT_A_THING"); addr != 0 {
		t.Fatalf("dlsym of an unknown OpenSL symbol returned %#x, want NULL", addr)
	}
	if again := openRegistered("/system/lib64/libOpenSLES.so"); again != h {
		t.Fatalf("second dlopen returned a different handle: %p vs %p", again, h)
	}
	if h := openRegistered("libaaudio.so"); h != nil {
		t.Fatal("dlopen(libaaudio.so) succeeded; Tipsy does not provide AAudio")
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

func TestOpenSLHostCapture(t *testing.T) {
	if os.Getenv("TIPSY_AUDIO_HOST_TEST") != "1" {
		t.Skip("set TIPSY_AUDIO_HOST_TEST=1 to open the host capture device")
	}
	if MicrophoneDisabled() {
		t.Fatal("host capture cannot run while the microphone kill-switch is on")
	}
	const bytes = 9600 // 100 ms at 48 kHz mono S16LE
	for i := 0; i < 2; i++ {
		read, callbacks, rc := audioTestHostCapture(48000, 1, bytes)
		if rc != 0 || read != bytes || callbacks != 1 {
			t.Fatalf("host capture #%d rc=%d read=%d callbacks=%d", i+1, rc, read, callbacks)
		}
	}
}

func TestOpenSLHostDuplex(t *testing.T) {
	if os.Getenv("TIPSY_AUDIO_HOST_TEST") != "1" {
		t.Skip("set TIPSY_AUDIO_HOST_TEST=1 to open host playback and capture together")
	}
	if MicrophoneDisabled() {
		t.Fatal("host duplex cannot run while the microphone kill-switch is on")
	}
	written, playCB, read, capCB, rc := audioTestHostDuplex(48000, 2, 19200)
	if rc != 0 || written != 19200 || playCB != 1 || read != 19200 || capCB != 1 {
		t.Fatalf("host duplex rc=%d written=%d playCB=%d read=%d capCB=%d",
			rc, written, playCB, read, capCB)
	}
}
