// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultGamepadConfig(t *testing.T) {
	c := DefaultGamepadConfig()
	if !c.Enabled {
		t.Fatal("default gamepad config must be enabled")
	}
	if c.Deadzone != 0 {
		t.Fatalf("default global deadzone = %v, want 0 (device flat + small fallback baseline)", c.Deadzone)
	}
}

func TestParseGamepadSectionMissingIsDefaults(t *testing.T) {
	for _, body := range []string{"", "  \n ", "{}", `{"other":1}`, `{"gamepad":null}`} {
		c, err := ParseGamepadSection([]byte(body))
		if err != nil {
			t.Fatalf("body %q: want defaults without error, got %v", body, err)
		}
		if want := DefaultGamepadConfig(); c != want {
			t.Fatalf("body %q: want defaults %+v, got %+v", body, want, c)
		}
	}
}

func TestParseGamepadSectionPartialMerge(t *testing.T) {
	c, err := ParseGamepadSection([]byte(`{"gamepad":{"deadzone":0.2}}`))
	if err != nil {
		t.Fatalf("partial section: %v", err)
	}
	if c.Deadzone != 0.2 {
		t.Fatalf("partial section must overlay the stated floor, got %+v", c)
	}
	if !c.Enabled {
		t.Fatalf("absent switch must stay default-on, got %+v", c)
	}
}

// TestParseGamepadSectionIgnoresRemovedKeys pins the honest downgrade: old
// v1 files carrying per-stick/invert/rumble keys still load their
// enabled/deadzone; the removed keys are parsed-but-ignored, never an error.
func TestParseGamepadSectionIgnoresRemovedKeys(t *testing.T) {
	c, err := ParseGamepadSection([]byte(`{"gamepad":{"enabled":false,"deadzone":0.15,"deadzoneLeft":0.2,"deadzoneRight":0.3,"invertY":true,"invertYRight":true,"rumble":false}}`))
	if err != nil {
		t.Fatalf("old file with removed keys must still load: %v", err)
	}
	if c.Enabled || c.Deadzone != 0.15 {
		t.Fatalf("kept keys must load, got %+v", c)
	}
	if got := c.EffectiveDeadzone(); got != 0.15 {
		t.Fatalf("effective floor must be the single global, got %v", got)
	}
}

func TestParseGamepadSectionClamps(t *testing.T) {
	c, err := ParseGamepadSection([]byte(`{"gamepad":{"deadzone":0.9}}`))
	if err != nil {
		t.Fatalf("clamp section: %v", err)
	}
	if c.Deadzone != MaxUserDeadzone {
		t.Fatalf("global 0.9 must clamp to %v, got %v", MaxUserDeadzone, c.Deadzone)
	}
}

func TestParseGamepadSectionInvalidIsHonestError(t *testing.T) {
	for _, body := range []string{
		`{not json`,
		`{"gamepad":{"deadzone":"huge"}}`,
		`{"gamepad":{"enabled":"yes"}}`,
	} {
		if _, err := ParseGamepadSection([]byte(body)); err == nil {
			t.Fatalf("body %q must be an honest error, never silent defaults", body)
		}
	}
}

func TestLoadGamepadConfigFileMissingIsDefaults(t *testing.T) {
	c, err := LoadGamepadConfigFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file must not error, got %v", err)
	}
	if c != DefaultGamepadConfig() {
		t.Fatalf("missing file must yield defaults, got %+v", c)
	}
}

func TestLoadGamepadConfigFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := `{"gamepad":{"enabled":true,"deadzone":0.15}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadGamepadConfigFile(path)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if c.Deadzone != 0.15 || !c.Enabled {
		t.Fatalf("round trip wrong: %+v", c)
	}
}

func TestParseDeadzoneEnv(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want float64
		set  bool
	}{
		{"0.2", 0.2, true},
		{" 0.1 ", 0.1, true},
		{"0", 0, true},
		{"0.9", MaxUserDeadzone, true}, // clamped, still set
		{"-0.3", 0, true},              // clamped, still set
		{"", 0, false},
		{"   ", 0, false},
		{"huge", 0, false},
	} {
		got, set := ParseDeadzoneEnv(tc.in)
		if got != tc.want || set != tc.set {
			t.Fatalf("ParseDeadzoneEnv(%q) = (%v,%t), want (%v,%t)", tc.in, got, set, tc.want, tc.set)
		}
	}
}

func envLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestWithEnvOverlay(t *testing.T) {
	base := DefaultGamepadConfig()
	got := base.WithEnv(envLookup(map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.2"}))
	if got.Deadzone != 0.2 {
		t.Fatalf("global deadzone = %v, want 0.2", got.Deadzone)
	}
	// Removed v1 keys are ignored everywhere, even when set.
	ignored := base.WithEnv(envLookup(map[string]string{
		"TIPSY_GAMEPAD_DEADZONE_LEFT":  "0.35",
		"TIPSY_GAMEPAD_DEADZONE_RIGHT": "0.35",
		"TIPSY_GAMEPAD_INVERT_Y":       "1",
		"TIPSY_GAMEPAD_RUMBLE":         "0",
	}))
	if ignored != base {
		t.Fatalf("removed v1 env keys must be ignored, got %+v", ignored)
	}
	// Invalid env never clobbers the file/default value.
	again := got.WithEnv(envLookup(map[string]string{"TIPSY_GAMEPAD_DEADZONE": "huge"}))
	if again.Deadzone != 0.2 {
		t.Fatalf("invalid env must be ignored, got %+v", again)
	}
	// Nil lookup is a no-op.
	if same := base.WithEnv(nil); same != base {
		t.Fatalf("nil lookup must be a no-op, got %+v", same)
	}
}

func TestEffectiveDeadzoneIsGlobal(t *testing.T) {
	c := DefaultGamepadConfig()
	if c.EffectiveDeadzone() != 0 {
		t.Fatal("default effective floor must be 0 (device-flat baseline)")
	}
	c.SetDeadzone(0.25)
	if c.EffectiveDeadzone() != 0.25 {
		t.Fatal("explicit floor must hold")
	}
}

func TestConfigSettersClamp(t *testing.T) {
	c := DefaultGamepadConfig()
	c.SetDeadzone(9)
	if c.Deadzone != MaxUserDeadzone {
		t.Fatalf("setter must clamp, got %v", c.Deadzone)
	}
	c.SetDeadzone(-9)
	if c.Deadzone != 0 {
		t.Fatalf("setter must floor at 0, got %v", c.Deadzone)
	}
}
