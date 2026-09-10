// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatDoctor(t *testing.T) {
	t.Parallel()
	r := &DoctorReport{
		Version: "0.0.0-dev",
		System: SystemInfo{
			OS:           "Arch Linux",
			Kernel:       "6.8.0",
			Architecture: "x86_64",
		},
		Display: DisplayInfo{
			Session: "X11",
			DISPLAY: ":0",
			XRandR:  "OK",
			XInput2: "OK",
		},
		GPU: GPUInfo{
			Vendor: "AMD",
			Driver: "Mesa (amdgpu)",
			EGL:    "OK",
			GLES:   "OK",
			Vulkan: "OK",
		},
		Audio: AudioInfo{
			PipeWire: "OK",
			Pulse:    "OK",
		},
		Qt: QtInfo{Widgets: "OK (6.11.2)"},
		Roblox: RobloxInfo{
			DataDir:        "/home/user/.local/share/tipsy",
			DataDirPresent: false,
			RuntimeFiles:   "not present",
			Note:           "Package cookies and tokens are not read.",
		},
		Runtime: RuntimeInfo{
			NativeSymbols: "official x86_64 client maps and runs (JNI_OnLoad and initializeNativeCode complete)",
			JNIMethods:    "Android/JNI compatibility surface is active",
			Note:          "The official logged-out login UI renders on X11 with EGL/GLES presentation.",
		},
		Issues:  []string{"DISPLAY is unset"},
		Summary: "1 issue found.",
	}
	got := FormatDoctor(r)
	for _, want := range []string{
		"Tipsy Doctor",
		"System",
		"OS: Arch Linux",
		"Kernel: 6.8.0",
		"Architecture: x86_64",
		"Display",
		"Session: X11",
		"DISPLAY: :0",
		"XRandR: OK",
		"XInput2: OK",
		"GPU",
		"Vendor: AMD",
		"Driver: Mesa (amdgpu)",
		"EGL: OK",
		"OpenGL ES: OK",
		"Vulkan: OK",
		"Audio",
		"PipeWire: OK",
		"Qt",
		"Roblox",
		"Runtime",
		"Result",
		"1 issue found.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatDoctor missing %q in:\n%s", want, got)
		}
	}
}

func TestFormatDoctorRedactsSecrets(t *testing.T) {
	t.Parallel()
	r := &DoctorReport{
		Version: "0.0.0-dev",
		Display: DisplayInfo{
			DISPLAY: ":0 .ROBLOSECURITY=super-secret-cookie",
		},
		Roblox: RobloxInfo{
			Note: "password=hunter2 leaked",
		},
	}
	got := FormatDoctor(r)
	for _, secret := range []string{"super-secret-cookie", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Fatalf("FormatDoctor leaked %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker:\n%s", got)
	}
}

func TestFormatDoctorJSON(t *testing.T) {
	t.Parallel()
	r := &DoctorReport{
		Version: "0.0.0-dev",
		System:  SystemInfo{OS: "Arch Linux", Architecture: "x86_64"},
	}
	b, err := FormatDoctorJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("invalid JSON: %s", b)
	}
	if !strings.Contains(string(b), "Arch Linux") {
		t.Fatalf("missing OS: %s", b)
	}
}

func TestFormatDoctorJSONRedactsSecrets(t *testing.T) {
	t.Parallel()
	r := &DoctorReport{
		Roblox: RobloxInfo{Note: "Authorization: Bearer eyJsecret"},
	}
	b, err := FormatDoctorJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "eyJsecret") {
		t.Fatalf("JSON leaked token: %s", b)
	}
}

func TestDoctorSmoke(t *testing.T) {
	r := Doctor(context.Background())
	if r == nil {
		t.Fatal("Doctor returned nil")
	}
	if r.System.Architecture == "" {
		t.Fatal("missing architecture")
	}
	text := FormatDoctor(r)
	if !strings.Contains(text, "Tipsy Doctor") {
		t.Fatalf("unexpected doctor text:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), ".roblosecurity=") && !strings.Contains(text, "[REDACTED]") {
		t.Fatal("possible cookie leak")
	}
}

func TestDiagnoseUnknown(t *testing.T) {
	t.Parallel()
	r := Diagnose(context.Background(), "not-a-subsystem")
	if r.Status != "unknown" {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestDiagnoseImplementedSubsystems(t *testing.T) {
	t.Parallel()
	for _, sub := range DiagnoseSubsystems {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()
			r := Diagnose(context.Background(), sub)
			if r.Status != "active" || r.Milestone != "" {
				t.Fatalf("status=%q milestone=%q", r.Status, r.Milestone)
			}
			for _, text := range []string{r.Status, r.Message} {
				if strings.Contains(text, "not implemented yet") {
					t.Fatalf("%s still claims unimplemented: %q", sub, text)
				}
			}
			if sub == "x11" || sub == "graphics" || sub == "roblox" {
				if !strings.Contains(r.Message, "login UI") {
					t.Fatalf("message omits the verified login UI state: %q", r.Message)
				}
			}
			if text := FormatSubsystem(r); strings.Contains(text, "not implemented yet") {
				t.Fatalf("format still claims unimplemented:\n%s", text)
			}
		})
	}
}

func TestDiagnoseMeasuredState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sub   string
		wants []string
	}{
		{"jni", []string{"JNI_OnLoad returns 0x10006", "initializeNativeCode returns a non-zero handle"}},
		{"loader", []string{"libroblox.so maps", "relocations applied"}},
		{"roblox", []string{"login UI renders on X11"}},
		{"auth", []string{"authenticated restart is user-confirmed"}},
		{"audio", []string{"user-confirmed audible"}},
	}
	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			t.Parallel()
			r := Diagnose(context.Background(), tt.sub)
			text := FormatSubsystem(r)
			if strings.Contains(text, "not implemented yet") {
				t.Fatalf("%s still claims unimplemented:\n%s", tt.sub, text)
			}
			for _, want := range tt.wants {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing measured state %q:\n%s", tt.sub, want, text)
				}
			}
		})
	}
}

func TestDiagnoseAudioBridge(t *testing.T) {
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "true")
	r := Diagnose(context.Background(), "audio")
	if r.Status != "active" || r.Milestone != "" {
		t.Fatalf("audio status=%q milestone=%q", r.Status, r.Milestone)
	}
	text := FormatSubsystem(r)
	for _, want := range []string{"FMOD AudioTrack", "OpenSL ES", "PulseAudio", "disabled by TIPSY_DISABLE_MICROPHONE", "does not play sound", "microphone capture"} {
		if !strings.Contains(text, want) {
			t.Fatalf("audio diagnostics missing %q:\n%s", want, text)
		}
	}
}

func TestParseOSRelease(t *testing.T) {
	t.Parallel()
	vars := parseOSRelease(`NAME="Arch Linux"
PRETTY_NAME="Arch Linux"
ID=arch
`)
	if vars["PRETTY_NAME"] != "Arch Linux" {
		t.Fatalf("%v", vars)
	}
}
