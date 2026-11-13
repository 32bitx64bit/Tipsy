// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/android"
)

// Production wiring lives in internal/runtime/launch.go; jni tests wire the
// same android gates so they exercise the real door and mute.
func init() {
	SetMicrophoneHooks(MicrophoneHooks{
		DoorOpen:        android.MicrophoneDoorOpen,
		CaptureMuted:    android.CaptureMuted,
		SetCaptureMuted: android.SetCaptureMuted,
	})
}

func TestMicrophoneHooksUnsetFailClosed(t *testing.T) {
	prev := microphoneHooks.Load()
	microphoneHooks.Store(nil)
	t.Cleanup(func() { microphoneHooks.Store(prev) })
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")

	if microphoneDoorOpen() {
		t.Fatal("door open without hooks")
	}
	if got := evaluatePermission(recordAudioPermissionName); got != permissionDenied {
		t.Fatalf("RECORD_AUDIO without hooks = %d, want denied", got)
	}
	if !captureMuted() {
		t.Fatal("capture not muted without hooks")
	}
	setCaptureMuted(false) // must not panic or reach android
	if !captureMuted() {
		t.Fatal("setCaptureMuted(false) unmuted capture without hooks")
	}
	// Nil members behave the same as an unset struct.
	SetMicrophoneHooks(MicrophoneHooks{})
	if microphoneDoorOpen() || !captureMuted() {
		t.Fatal("empty hooks must act as door closed and capture muted")
	}
}
