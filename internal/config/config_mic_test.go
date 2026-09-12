// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMicrophoneSectionRoundTrip proves the reconciled home: the
// "microphone" section survives a Save/Load cycle verbatim while every
// other key is preserved.
func TestMicrophoneSectionRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	section := json.RawMessage(`{"enabled":false,"source":"alsa_input.pci","gain":0.9}`)
	in := &Config{LogLevel: "debug", Microphone: section}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.LogLevel != "debug" {
		t.Fatalf("other keys lost: %+v", out)
	}
	var got, want map[string]any
	if err := json.Unmarshal(out.Microphone, &got); err != nil {
		t.Fatalf("microphone section unreadable: %v", err)
	}
	if err := json.Unmarshal(section, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("microphone section = %s, want %s", out.Microphone, section)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("microphone[%s] = %v, want %v (section %s)", k, got[k], v, out.Microphone)
		}
	}
}

// TestMicrophoneSectionMissingIsNil proves missing JSON = no section
// (callers fall back to mic defaults), and that omitempty keeps the key
// out of fresh files.
func TestMicrophoneSectionMissingIsNil(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := Save(&Config{LogLevel: "info"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["microphone"]; ok {
		t.Fatalf("fresh config must omit the microphone key: %s", raw)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Microphone) != 0 {
		t.Fatalf("missing section must load as nil, got %s", out.Microphone)
	}
}

// TestMicrophoneSectionSurvivesUpdate proves a read-modify-write cycle
// for an unrelated key keeps the microphone section bytes intact.
func TestMicrophoneSectionSurvivesUpdate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p := Paths()
	if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	section := `{"enabled":false,"source":"alsa_input.pci"}`
	if err := os.WriteFile(p.ConfigFile,
		[]byte("{\"logLevel\":\"debug\",\"microphone\":"+section+"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(c *Config) error {
		c.LogLevel = "warn"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.LogLevel != "warn" {
		t.Fatalf("unrelated key not updated: %+v", out)
	}
	var got, want map[string]any
	if err := json.Unmarshal(out.Microphone, &got); err != nil {
		t.Fatalf("microphone section unreadable: %v", err)
	}
	if err := json.Unmarshal([]byte(section), &want); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("microphone[%s] = %v, want %v", k, got[k], v)
		}
	}
}

// TestMicrophoneAndGamepadSectionsCoexist proves an Update that touches
// neither section keeps both passthrough blobs intact.
func TestMicrophoneAndGamepadSectionsCoexist(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	pad := json.RawMessage(`{"enabled":true,"deadzone":0.1}`)
	mic := json.RawMessage(`{"enabled":false}`)
	if err := Save(&Config{LogLevel: "info", Gamepad: pad, Microphone: mic}); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(c *Config) error {
		c.LogLevel = "debug"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Gamepad) == "" || string(out.Microphone) == "" {
		t.Fatalf("passthrough sections lost: gamepad=%s microphone=%s", out.Gamepad, out.Microphone)
	}
	var gotPad, wantPad, gotMic, wantMic map[string]any
	if err := json.Unmarshal(out.Gamepad, &gotPad); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pad, &wantPad); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Microphone, &gotMic); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mic, &wantMic); err != nil {
		t.Fatal(err)
	}
	if gotPad["deadzone"] != wantPad["deadzone"] || gotMic["enabled"] != wantMic["enabled"] {
		t.Fatalf("section drift: gamepad=%s microphone=%s", out.Gamepad, out.Microphone)
	}
}
