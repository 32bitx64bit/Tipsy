// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestUpsertGamepadSectionRoundTrip proves the write path: the section
// stores normalized and parses back identically.
func TestUpsertGamepadSectionRoundTrip(t *testing.T) {
	t.Parallel()
	cfg := DefaultGamepadConfig()
	cfg.SetEnabled(false)
	cfg.SetDeadzone(0.15)
	out, err := UpsertGamepadSection(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseGamepadSection(out)
	if err != nil {
		t.Fatalf("upserted body must parse: %v\n%s", err, out)
	}
	if back != cfg {
		t.Fatalf("round trip = %+v, want %+v", back, cfg)
	}
}

// TestUpsertGamepadSectionPreservesOtherKeys proves the GUI save never
// destroys unrelated settings-file keys.
func TestUpsertGamepadSectionPreservesOtherKeys(t *testing.T) {
	t.Parallel()
	existing := []byte(`{"dataDir":"/opt/tipsy-data","logLevel":"debug","gamepad":{"deadzone":0.9}}`)
	cfg := DefaultGamepadConfig()
	cfg.SetDeadzone(0.2)
	out, err := UpsertGamepadSection(existing, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["dataDir"]) != `"/opt/tipsy-data"` || string(top["logLevel"]) != `"debug"` {
		t.Fatalf("unrelated keys lost: %s", out)
	}
	back, err := ParseGamepadSection(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Deadzone != 0.2 {
		t.Fatalf("section lost: %+v", back)
	}
}

// TestUpsertGamepadSectionNormalizes proves clamping happens at write time,
// so readers never see an out-of-range floor.
func TestUpsertGamepadSectionNormalizes(t *testing.T) {
	t.Parallel()
	cfg := DefaultGamepadConfig()
	cfg.Deadzone = 0.9
	out, err := UpsertGamepadSection([]byte("{}"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseGamepadSection(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Deadzone != MaxUserDeadzone {
		t.Fatalf("unnormalized write: %+v", back)
	}
}

// TestUpsertGamepadSectionRejectsCorruptFile proves a malformed existing
// file is never overwritten with a gamepad-only document.
func TestUpsertGamepadSectionRejectsCorruptFile(t *testing.T) {
	t.Parallel()
	if out, err := UpsertGamepadSection([]byte("{not json"), DefaultGamepadConfig()); err == nil || out != nil {
		t.Fatalf("corrupt file must fail honestly, got %s err=%v", out, err)
	}
}

// TestEffectiveConfigPrecedence proves file < env: set env wins, unset or
// invalid env leaves the file value standing.
func TestEffectiveConfigPrecedence(t *testing.T) {
	t.Parallel()
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		}
	}
	file := []byte(`{"gamepad":{"enabled":true,"deadzone":0.1,"deadzoneRight":0.3,"invertYRight":true,"rumble":false}}`)
	got, err := EffectiveConfig(file, lookup(map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.2"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Deadzone != 0.2 {
		t.Fatalf("env must win over file: %+v", got)
	}
	// Removed v1 file keys are ignored, not honored.
	if got.EffectiveDeadzone() != 0.2 {
		t.Fatalf("effective floor must be the single global: %+v", got)
	}
	// Invalid env never clobbers the file value.
	again, err := EffectiveConfig(file, lookup(map[string]string{"TIPSY_GAMEPAD_DEADZONE": "huge"}))
	if err != nil {
		t.Fatal(err)
	}
	if again.Deadzone != 0.1 {
		t.Fatalf("invalid env must be ignored: %+v", again)
	}
	// Missing file bytes = defaults, env still applies.
	def, err := EffectiveConfig(nil, lookup(map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.25"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := DefaultGamepadConfig().WithEnv(lookup(map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.25"})); def != want {
		t.Fatalf("missing file must equal defaults+env: %+v", def)
	}
	// A corrupt section is an honest error alongside usable defaults.
	if _, err := EffectiveConfig([]byte(`{"gamepad":{"deadzone":"huge"}}`), lookup(nil)); err == nil {
		t.Fatal("corrupt section must error honestly")
	}
}

// TestEffectiveConfigFileRoundTrip proves the headless pump's exact read:
// LoadGamepadConfigFile over a real config.json, then env.
func TestEffectiveConfigFileRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"dataDir":"/tmp/x","gamepad":{"enabled":false,"deadzone":0.15}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGamepadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.Deadzone != 0.15 {
		t.Fatalf("file section wrong: %+v", cfg)
	}
	eff := cfg.WithEnv(func(k string) (string, bool) { return "", false })
	if eff.Enabled {
		t.Fatalf("file-off must survive empty env: %+v", eff)
	}
}
