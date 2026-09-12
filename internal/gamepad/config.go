// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
)

// Lean stick calibration (simplified 2026-09-12): one global deadzone floor
// over the honest device ranges. The evdev flat from EVIOCGABS is the
// baseline (reader.go), capped at DefaultDeadzone when the device reports a
// larger stick flat (GuliKit gyro-to-stick); the global floor only ever
// RAISES the stick floor above that baseline, never lowers it and never
// touches triggers or hats. It never edits GlobalBasicSettings_13.xml or
// any engine state: it shapes the normalized Frame before android.go
// translates it.
//
// Downgrades vs controller v1 (honest list, see
// gamepad-simplify-2026-09-12.md): per-stick deadzoneLeft/deadzoneRight and
// invertY/invertYLeft/invertYRight are gone (one floor for both sticks, Y
// never inverted); the rumble preference is gone (no vibration door was
// ever observed); unknown section keys (including those removed keys) are
// parsed-but-ignored so old files still load their enabled/deadzone.
const (
	// MaxUserDeadzone caps the global stick floor (TIPSY_GAMEPAD_DEADZONE
	// range is 0.0-0.5 in normalized axis units).
	MaxUserDeadzone = 0.5
	// GamepadConfigSectionKey is the reserved key for this struct under
	// the existing settings file. Wiring it into config.json belongs to
	// the config owner; this package only defines the section shape,
	// defaults, and merge rules. Missing key/file = defaults.
	GamepadConfigSectionKey = "gamepad"
)

// GamepadConfig is the persisted gamepad section: enable switch plus one
// global stick floor. Defaults (missing JSON = these): enabled, zero user
// floor (the small 0.08 reader fallback still applies when a device reports
// flat=0, so the default effective feel is unchanged from v1).
type GamepadConfig struct {
	Enabled  bool    `json:"enabled"`
	Deadzone float64 `json:"deadzone"`
}

// DefaultGamepadConfig returns the missing-JSON defaults.
func DefaultGamepadConfig() GamepadConfig {
	return GamepadConfig{Enabled: true}
}

// Normalize clamps the floor into its honest domain 0..MaxUserDeadzone.
func (c *GamepadConfig) Normalize() { c.Deadzone = clampDeadzone(c.Deadzone) }

func clampDeadzone(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > MaxUserDeadzone {
		return MaxUserDeadzone
	}
	return v
}

// SetDeadzone sets the global stick floor (0.0-0.5, clamped).
func (c *GamepadConfig) SetDeadzone(v float64) { c.Deadzone = clampDeadzone(v) }

// SetEnabled sets the whole-subsystem switch (default on).
func (c *GamepadConfig) SetEnabled(v bool) { c.Enabled = v }

// EffectiveDeadzone resolves the user floor both sticks share.
func (c GamepadConfig) EffectiveDeadzone() float64 { return clampDeadzone(c.Deadzone) }

// ParseGamepadSection merges one settings-file body over the defaults: the
// "gamepad" key, when present, overlays only its stated fields. Empty or
// whitespace-only input, or a body without the key, yields defaults with no
// error. Unknown keys (including removed v1 per-stick/invert/rumble keys)
// are ignored so old files keep their enabled/deadzone. Malformed JSON or a
// mistyped section is an honest error, never silent defaults.
func ParseGamepadSection(fileJSON []byte) (GamepadConfig, error) {
	cfg := DefaultGamepadConfig()
	if len(strings.TrimSpace(string(fileJSON))) == 0 {
		return cfg, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(fileJSON, &top); err != nil {
		return cfg, err
	}
	raw, ok := top[GamepadConfigSectionKey]
	if !ok || len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return cfg, nil
	}
	merged := DefaultGamepadConfig()
	if err := json.Unmarshal(raw, &merged); err != nil {
		return cfg, err
	}
	merged.Normalize()
	return merged, nil
}

// LoadGamepadConfigFile reads one settings-file path and merges its gamepad
// section over the defaults. A missing file is not an error (missing JSON =
// defaults). Any other read or parse failure is returned honestly.
func LoadGamepadConfigFile(path string) (GamepadConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultGamepadConfig(), nil
		}
		return DefaultGamepadConfig(), err
	}
	return ParseGamepadSection(data)
}

// UpsertGamepadSection overlays cfg onto the "gamepad" section of one
// settings-file body, preserving every other top-level key value.
// Malformed top-level JSON is an honest error so callers never overwrite an
// unrelated corrupt file. cfg is normalized before it is stored.
func UpsertGamepadSection(fileJSON []byte, cfg GamepadConfig) ([]byte, error) {
	cfg.Normalize()
	section, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	top := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(fileJSON))) != 0 {
		if err := json.Unmarshal(fileJSON, &top); err != nil {
			return nil, err
		}
	}
	top[GamepadConfigSectionKey] = section
	return json.Marshal(top)
}

// EffectiveConfig resolves the live calibration: the persisted section over
// fileJSON, then TIPSY_GAMEPAD_DEADZONE over that (env wins; unset or
// invalid env leaves the file/default value standing).
func EffectiveConfig(fileJSON []byte, lookup func(string) (string, bool)) (GamepadConfig, error) {
	cfg, err := ParseGamepadSection(fileJSON)
	return cfg.WithEnv(lookup), err
}

// ParseDeadzoneEnv parses one TIPSY_GAMEPAD_DEADZONE value: surrounding
// whitespace tolerated, numeric values clamped to 0..MaxUserDeadzone and
// reported set. Empty or non-numeric input reports unset (ignored).
func ParseDeadzoneEnv(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return clampDeadzone(v), true
}

// WithEnv overlays TIPSY_GAMEPAD_DEADZONE over c and returns the result.
// Env wins over file/defaults; unset or invalid env leaves it untouched.
// (Removed v1 keys TIPSY_GAMEPAD_DEADZONE_LEFT/RIGHT, TIPSY_GAMEPAD_INVERT_*
// and TIPSY_GAMEPAD_RUMBLE are parsed nowhere: setting them is ignored.
// The TIPSY_GAMEPAD kill-switch keeps living in the JNI feed-in.)
func (c GamepadConfig) WithEnv(lookup func(string) (string, bool)) GamepadConfig {
	if lookup == nil {
		return c
	}
	if v, ok := lookup("TIPSY_GAMEPAD_DEADZONE"); ok {
		if d, set := ParseDeadzoneEnv(v); set {
			c.Deadzone = d
		}
	}
	return c
}

// ApplyCalibration shapes a normalized reader Frame in place with the single
// user stick floor: |v| <= floor → 0, else passthrough unrescaled (matching
// the engine's own |v|<=flat gate). Only the four stick codes move: ABS_X/Y
// (left) and m.RightX/m.RightY (right, skipped when NoAxis); both sticks
// share the floor (uniform, no per-stick override, Y never inverted).
// Trigger-shaped axes and hats are untouched (honest device flat only).
// Codes absent from f.Axes stay absent (never zero-filled). A nil frame or a
// zero floor is a no-op. Callers apply this between Reader.Feed and MapFrame;
// MapFrame itself stays pure translation so golden tests pin each layer once.
func ApplyCalibration(f *Frame, m Mapping, cfg GamepadConfig) {
	floor := clampDeadzone(cfg.Deadzone)
	if f == nil || len(f.Axes) == 0 || floor <= 0 {
		return
	}
	seen := make(map[uint16]bool, 4)
	for _, code := range []uint16{AbsX, AbsY, m.RightX, m.RightY} {
		if code == NoAxis || seen[code] {
			continue
		}
		seen[code] = true
		if v, ok := f.Axes[code]; ok && absF(v) <= floor {
			f.Axes[code] = 0
		}
	}
}
