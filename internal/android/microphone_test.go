// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import "testing"

func TestCaptureMutedDefaultOff(t *testing.T) {
	SetCaptureMuted(false)
	if CaptureMuted() {
		t.Fatal("capture mute should default off")
	}
	SetCaptureMuted(true)
	if !CaptureMuted() {
		t.Fatal("SetCaptureMuted(true) did not stick")
	}
	SetCaptureMuted(false)
	if CaptureMuted() {
		t.Fatal("SetCaptureMuted(false) did not clear")
	}
}

func TestMicrophoneDisabledEnv(t *testing.T) {
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	if MicrophoneDisabled() {
		t.Fatal("empty mic env should allow capture")
	}
	t.Setenv("TIPSY_MICROPHONE", "1")
	if MicrophoneDisabled() {
		t.Fatal("TIPSY_MICROPHONE=1 should allow capture")
	}
	for _, off := range []string{"0", "off", "false", "no"} {
		t.Setenv("TIPSY_MICROPHONE", off)
		if !MicrophoneDisabled() {
			t.Fatalf("TIPSY_MICROPHONE=%s should disable capture", off)
		}
	}
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "1")
	if !MicrophoneDisabled() {
		t.Fatal("TIPSY_DISABLE_MICROPHONE=1 should disable capture")
	}
}
