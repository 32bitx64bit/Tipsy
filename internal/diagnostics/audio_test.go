// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/mic"
)

func stubCaptureSources(t *testing.T, fn func(context.Context) (int, string, bool)) {
	t.Helper()
	old := captureSourceProbe
	captureSourceProbe = fn
	t.Cleanup(func() { captureSourceProbe = old })
}

func TestDiagnoseAudioMicrophoneControlAndNoSourceName(t *testing.T) {
	isolateGamepadConfig(t, `{"microphone":{"enabled":true,"source":"alsa_input.usb-SerialDEADBEEF"}}`)
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE_SOURCE", "hw:Serial1234,0")
	stubCaptureSources(t, func(context.Context) (int, string, bool) {
		return 2, "pactl", true
	})
	text := FormatSubsystem(Diagnose(context.Background(), "audio"))
	for _, want := range []string{
		"Microphone: enabled (config file)",
		"Source pin: pinned (name not reported)",
		"Capture sources: 2 (pactl",
		"JNI feature follows the env mic door",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("audio facts missing %q:\n%s", want, text)
		}
	}
	for _, leak := range []string{"SerialDEADBEEF", "Serial1234", "alsa_input", "hw:"} {
		if strings.Contains(text, leak) {
			t.Errorf("audio report leaked source name %q:\n%s", leak, text)
		}
	}
}

func TestDiagnoseAudioFileOff(t *testing.T) {
	isolateGamepadConfig(t, `{"microphone":{"enabled":false}}`)
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	stubCaptureSources(t, func(context.Context) (int, string, bool) {
		return 1, "pactl", true
	})
	text := FormatSubsystem(Diagnose(context.Background(), "audio"))
	if !strings.Contains(text, "Microphone: disabled (config file)") {
		t.Fatalf("file-off door missing:\n%s", text)
	}
}

func TestDoctorMicrophoneDisabledIssue(t *testing.T) {
	isolateGamepadConfig(t, "")
	t.Setenv("TIPSY_MICROPHONE", "off")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	stubCaptureSources(t, func(context.Context) (int, string, bool) {
		return 1, "pactl", true
	})
	rep := Doctor(context.Background())
	var found string
	for _, issue := range rep.Issues {
		if strings.Contains(issue, "Microphone input is disabled") {
			found = issue
		}
	}
	if found == "" {
		t.Fatalf("doctor issues omit mic disabled: %v", rep.Issues)
	}
	if !strings.Contains(found, "TIPSY_MICROPHONE") {
		t.Errorf("doctor hint missing control: %q", found)
	}
	text := FormatDoctor(rep)
	if !strings.Contains(text, "Microphone: disabled (TIPSY_MICROPHONE)") {
		t.Errorf("doctor text omits mic door:\n%s", text)
	}
	if !strings.Contains(text, "JNI feature follows the env mic door") {
		t.Errorf("doctor text missing JNI leftover:\n%s", text)
	}
}

func TestDoctorNoCaptureSourceIssue(t *testing.T) {
	isolateGamepadConfig(t, "")
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	stubCaptureSources(t, func(context.Context) (int, string, bool) {
		return 0, "pactl", true
	})
	rep := Doctor(context.Background())
	var found string
	for _, issue := range rep.Issues {
		if strings.Contains(issue, "no capture source found") {
			found = issue
		}
	}
	if found == "" {
		t.Fatalf("doctor issues omit missing capture source: %v", rep.Issues)
	}
	if !strings.Contains(found, "pavucontrol") {
		t.Errorf("doctor hint missing host-permission note: %q", found)
	}
}

func TestDoctorJNIFeatureNotAnIssue(t *testing.T) {
	isolateGamepadConfig(t, "")
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	stubCaptureSources(t, func(context.Context) (int, string, bool) {
		return 1, "pactl", true
	})
	rep := Doctor(context.Background())
	for _, issue := range rep.Issues {
		if strings.Contains(strings.ToLower(issue), "jni feature") ||
			strings.Contains(issue, "android.hardware.microphone") {
			t.Fatalf("JNI feature leftover must not be a doctor issue: %q", issue)
		}
	}
}

func TestDoctorAudioJSONOmitsSourceName(t *testing.T) {
	isolateGamepadConfig(t, `{"microphone":{"enabled":true,"source":"alsa_input.usb-SecretMic"}}`)
	t.Setenv("TIPSY_MICROPHONE_SOURCE", "secret-source-name")
	stubCaptureSources(t, func(context.Context) (int, string, bool) {
		return 3, "pactl", true
	})
	rep := Doctor(context.Background())
	b, err := FormatDoctorJSON(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("invalid JSON: %s", b)
	}
	raw := string(b)
	for _, leak := range []string{"SecretMic", "secret-source-name", "alsa_input"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("doctor JSON leaked source name %q:\n%s", leak, raw)
		}
	}
	if !strings.Contains(raw, `"microphoneEnabled"`) || !strings.Contains(raw, `"sourcePinned"`) {
		t.Fatalf("doctor JSON missing mic fields:\n%s", raw)
	}
	if !strings.Contains(raw, `"captureSources": 3`) && !strings.Contains(raw, `"captureSources":3`) {
		t.Fatalf("doctor JSON missing capture count:\n%s", raw)
	}
	if strings.Contains(raw, `"source":`) {
		t.Fatalf("doctor JSON must not include a source name field:\n%s", raw)
	}
}

func TestCountPulseSourcesShortSkipsMonitors(t *testing.T) {
	out := "0\talsa_input.pci-0000.analog-stereo\talsa\ts16le 2ch 48000Hz\tRUNNING\n" +
		"1\talso_output.pci-0000.analog-stereo.monitor\talsa\ts16le 2ch 48000Hz\tIDLE\n" +
		"2\tbluez_input.AA_BB.headset\tbluez\ts16le 1ch 16000Hz\tSUSPENDED\n"
	if n := countPulseSourcesShort(out); n != 2 {
		t.Fatalf("count = %d, want 2 (monitors excluded)", n)
	}
}

func TestCountPipeWireAudioSources(t *testing.T) {
	out := "id 40, type PipeWire:Interface:Node/3\n" +
		"    media.class = \"Audio/Sink\"\n" +
		"id 45, type PipeWire:Interface:Node/3\n" +
		"    media.class = \"Audio/Source\"\n" +
		"    node.name = \"alsa_input.pci\"\n" +
		"id 50, type PipeWire:Interface:Node/3\n" +
		"    media.class = \"Audio/Source\"\n" +
		"    node.name = \"alsa_output.pci.monitor\"\n"
	if n := countPipeWireAudioSources(out); n != 1 {
		t.Fatalf("count = %d, want 1 (monitor skipped)", n)
	}
}

func TestMicrophoneControlConstantsStayStable(t *testing.T) {
	if mic.MicrophoneControlEnv != "TIPSY_MICROPHONE" ||
		mic.MicrophoneControlDisable != "TIPSY_DISABLE_MICROPHONE" ||
		mic.MicrophoneControlFile != "config file" {
		t.Fatalf("diagnose control strings drifted")
	}
}
