// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "testing"

func TestDesktopPlatformSystemFeatures(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_INPUT_DEVICE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	ResetPointerDeviceMode()
	t.Cleanup(ResetPointerDeviceMode)
	tests := []struct {
		name string
		want bool
	}{
		{name: "android.hardware.type.pc", want: true},
		{name: "android.hardware.touchscreen", want: false},
		{name: androidHardwareMicrophone, want: true},
		{name: "android.hardware.camera", want: false},
		{name: "", want: false},
		{name: "android.hardware.type.pc.extra", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := platformSystemFeature(tt.name); got != tt.want {
				t.Fatalf("platformSystemFeature(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestDesktopPlatformSystemFeaturesMicrophoneDoor(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_INPUT_DEVICE", "")
	ResetPointerDeviceMode()
	t.Cleanup(ResetPointerDeviceMode)

	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "0")
	if platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("TIPSY_MICROPHONE=0 advertised android.hardware.microphone")
	}
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "1")
	if platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("TIPSY_DISABLE_MICROPHONE=1 advertised android.hardware.microphone")
	}
}

func TestDesktopPlatformSystemFeaturesMicrophoneFileDoor(t *testing.T) {
	t.Setenv("TIPSY_INPUT_DEVICE", "")
	ResetPointerDeviceMode()
	t.Cleanup(ResetPointerDeviceMode)

	writeMicrophoneConfigFile(t, `{"microphone":{"enabled":false}}`)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	if platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("file enabled:false advertised android.hardware.microphone")
	}

	writeMicrophoneConfigFile(t, `{"microphone":{"enabled":true}}`)
	t.Setenv("TIPSY_MICROPHONE", "0")
	if platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("file enabled:true with TIPSY_MICROPHONE=0 advertised microphone")
	}

	writeMicrophoneConfigFile(t, `{"microphone":{"enabled":false}}`)
	t.Setenv("TIPSY_MICROPHONE", "1")
	if !platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("TIPSY_MICROPHONE=1 must win over file enabled:false")
	}

	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	if !platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("missing file and empty env must advertise android.hardware.microphone")
	}
}

func TestTouchDiagnosticPlatformSystemFeatures(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_INPUT_DEVICE", "touch")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	ResetPointerDeviceMode()
	t.Cleanup(ResetPointerDeviceMode)

	if platformSystemFeature(androidHardwareTypePC) {
		t.Fatal("touch diagnostic profile advertises android.hardware.type.pc")
	}
	if !platformSystemFeature(androidHardwareTouchscreen) {
		t.Fatal("touch diagnostic profile does not advertise android.hardware.touchscreen")
	}
	if !platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("open mic door must advertise microphone even in touch mode")
	}
	t.Setenv("TIPSY_MICROPHONE", "0")
	if platformSystemFeature(androidHardwareMicrophone) {
		t.Fatal("closed mic door advertised microphone in touch mode")
	}
	if platformSystemFeature("android.hardware.camera") {
		t.Fatal("unverified camera feature was advertised")
	}
}

func TestHasSystemFeatureMethodIsImplemented(t *testing.T) {
	if !isImplementedMethod("hasSystemFeature", "(Ljava/lang/String;)Z") {
		t.Fatal("PackageManager.hasSystemFeature is not registered")
	}
}
