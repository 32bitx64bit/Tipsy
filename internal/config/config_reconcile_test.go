// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestGamepadSectionRoundTrip proves the reconciled home: the "gamepad"
// section survives a Save/Load cycle verbatim while every other key is
// preserved.
func TestGamepadSectionRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	section := json.RawMessage(`{"enabled":false,"deadzone":0.2,"deadzoneRight":0.3,"invertYRight":true,"rumble":false}`)
	in := &Config{LogLevel: "debug", Gamepad: section}
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
	if err := json.Unmarshal(out.Gamepad, &got); err != nil {
		t.Fatalf("gamepad section unreadable: %v", err)
	}
	if err := json.Unmarshal(section, &want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("gamepad section = %s, want %s", out.Gamepad, section)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("gamepad[%s] = %v, want %v (section %s)", k, got[k], v, out.Gamepad)
		}
	}
}

// TestGamepadSectionMissingIsNil proves missing JSON = no section (callers
// fall back to gamepad defaults), and that omitempty keeps the key out of
// fresh files.
func TestGamepadSectionMissingIsNil(t *testing.T) {
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
	if _, ok := top["gamepad"]; ok {
		t.Fatalf("fresh config must omit the gamepad key: %s", raw)
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Gamepad) != 0 {
		t.Fatalf("missing section must load as nil, got %s", out.Gamepad)
	}
}

// TestGamepadSectionSurvivesUpdate proves a read-modify-write cycle for an
// unrelated key keeps the gamepad section bytes intact.
func TestGamepadSectionSurvivesUpdate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p := Paths()
	if err := os.MkdirAll(filepath.Dir(p.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	section := `{"enabled":false,"deadzone":0.2,"rumble":false}`
	if err := os.WriteFile(p.ConfigFile,
		[]byte("{\"logLevel\":\"debug\",\"gamepad\":"+section+"}\n"), 0o600); err != nil {
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
	if err := json.Unmarshal(out.Gamepad, &got); err != nil {
		t.Fatalf("gamepad section unreadable: %v", err)
	}
	if err := json.Unmarshal([]byte(section), &want); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("gamepad[%s] = %v, want %v", k, got[k], v)
		}
	}
}
