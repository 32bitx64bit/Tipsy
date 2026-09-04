// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "testing"

func TestDesktopPlatformSystemFeatures(t *testing.T) {
	t.Setenv("TIPSY_INPUT_DEVICE", "")
	ResetPointerDeviceMode()
	t.Cleanup(ResetPointerDeviceMode)
	tests := []struct {
		name string
		want bool
	}{
		{name: "android.hardware.type.pc", want: true},
		{name: "android.hardware.touchscreen", want: false},
		{name: "android.hardware.microphone", want: false},
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

func TestTouchDiagnosticPlatformSystemFeatures(t *testing.T) {
	t.Setenv("TIPSY_INPUT_DEVICE", "touch")
	ResetPointerDeviceMode()
	t.Cleanup(ResetPointerDeviceMode)

	if platformSystemFeature(androidHardwareTypePC) {
		t.Fatal("touch diagnostic profile advertises android.hardware.type.pc")
	}
	if !platformSystemFeature(androidHardwareTouchscreen) {
		t.Fatal("touch diagnostic profile does not advertise android.hardware.touchscreen")
	}
	if platformSystemFeature("android.hardware.microphone") {
		t.Fatal("unverified microphone feature was advertised")
	}
}

func TestHasSystemFeatureMethodIsImplemented(t *testing.T) {
	if !isImplementedMethod("hasSystemFeature", "(Ljava/lang/String;)Z") {
		t.Fatal("PackageManager.hasSystemFeature is not registered")
	}
}
