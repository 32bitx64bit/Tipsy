// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package mic is the host microphone door: persisted settings, env overlay,
// and Allowed() for JNI/diagnose/GUI. It never opens Pulse streams, never
// logs PCM, and never prints Pulse source names.
package mic

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const (
	// MicrophoneConfigSectionKey is the reserved key for this struct under
	// the existing settings file. Wiring it into config.json belongs to
	// the config owner; this package only defines the section shape,
	// defaults, and merge rules. Missing key/file = defaults.
	MicrophoneConfigSectionKey = "microphone"

	// Control names for diagnose/doctor: which input set the door.
	MicrophoneControlDefault = "default"
	MicrophoneControlFile    = "config file"
	MicrophoneControlEnv     = "TIPSY_MICROPHONE"
	MicrophoneControlDisable = "TIPSY_DISABLE_MICROPHONE"
)

// MicrophoneConfig is the persisted capture-door section. Enabled means
// allowed, not always-open: OpenSL still lazy-opens only when the client
// records. Source is an optional Pulse source name pin (empty = host
// default). Diagnose must never print Source; use SourcePinned().
type MicrophoneConfig struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source,omitempty"`
}

// DefaultMicrophoneConfig returns the missing-JSON defaults (allowed).
func DefaultMicrophoneConfig() MicrophoneConfig {
	return MicrophoneConfig{Enabled: true}
}

// Normalize trims the optional source pin. Empty after trim is host default.
func (c *MicrophoneConfig) Normalize() {
	c.Source = strings.TrimSpace(c.Source)
}

// SetEnabled sets the whole-input switch (default on).
func (c *MicrophoneConfig) SetEnabled(v bool) { c.Enabled = v }

// SetSource sets the optional Pulse source pin (empty = host default).
func (c *MicrophoneConfig) SetSource(v string) { c.Source = strings.TrimSpace(v) }

// Allowed reports whether capture is allowed after whatever overlay
// produced c. True iff enabled. This is the canonical Go door; JNI still
// duplicates env-only parsing this slice and should later call Allowed()
// / EffectiveConfig. OpenSL C getenv remains the native kill-switch.
func (c MicrophoneConfig) Allowed() bool { return c.Enabled }

// SourcePinned reports whether a non-default source pin is set. Diagnose
// and logs must use this boolean rather than printing the name.
func (c MicrophoneConfig) SourcePinned() bool {
	return strings.TrimSpace(c.Source) != ""
}

// ParseMicSection merges one settings-file body over the defaults: the
// "microphone" key, when present, overlays only its stated fields. Empty or
// whitespace-only input, or a body without the key, yields defaults with
// no error. Unknown keys are ignored. Malformed JSON or a mistyped section
// is an honest error, never silent defaults.
func ParseMicSection(fileJSON []byte) (MicrophoneConfig, error) {
	cfg := DefaultMicrophoneConfig()
	if len(strings.TrimSpace(string(fileJSON))) == 0 {
		return cfg, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(fileJSON, &top); err != nil {
		return cfg, err
	}
	raw, ok := top[MicrophoneConfigSectionKey]
	if !ok || len(strings.TrimSpace(string(raw))) == 0 || string(raw) == "null" {
		return cfg, nil
	}
	merged := DefaultMicrophoneConfig()
	if err := json.Unmarshal(raw, &merged); err != nil {
		return cfg, err
	}
	merged.Normalize()
	return merged, nil
}

// LoadMicrophoneConfigFile reads one settings-file path and merges its
// microphone section over the defaults. A missing file is not an error
// (missing JSON = defaults). Any other read or parse failure is returned
// honestly.
func LoadMicrophoneConfigFile(path string) (MicrophoneConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultMicrophoneConfig(), nil
		}
		return DefaultMicrophoneConfig(), err
	}
	return ParseMicSection(data)
}

// UpsertMicSection overlays cfg onto the "microphone" section of one
// settings-file body, preserving every other top-level key value.
// Malformed top-level JSON is an honest error so callers never overwrite an
// unrelated corrupt file. cfg is normalized before it is stored.
func UpsertMicSection(fileJSON []byte, cfg MicrophoneConfig) ([]byte, error) {
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
	top[MicrophoneConfigSectionKey] = section
	return json.Marshal(top)
}

// EffectiveConfig resolves the live door: the persisted section over
// fileJSON, then env over that (env wins per key; unset or invalid env
// leaves the file/default value standing).
func EffectiveConfig(fileJSON []byte, lookup func(string) (string, bool)) (MicrophoneConfig, error) {
	cfg, err := ParseMicSection(fileJSON)
	return cfg.WithEnv(lookup), err
}

// ParseMicrophoneEnv parses one TIPSY_MICROPHONE value: 0|off|false|no
// disable the door; 1|on|true|yes force it on. Surrounding whitespace is
// tolerated. Empty or other tokens report unset (ignored).
func ParseMicrophoneEnv(s string) (enabled bool, set bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "off", "false", "no":
		return false, true
	case "1", "on", "true", "yes":
		return true, true
	default:
		return false, false
	}
}

// ParseDisableMicrophoneEnv parses the deprecated TIPSY_DISABLE_MICROPHONE
// alias: 1|true|yes disable the door and report set. Empty or other tokens
// (including 0|false|no) report unset so they never force the door on.
func ParseDisableMicrophoneEnv(s string) (disabled bool, set bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true, true
	default:
		return false, false
	}
}

// WithEnv overlays TIPSY_MICROPHONE / TIPSY_DISABLE_MICROPHONE /
// TIPSY_MICROPHONE_SOURCE over c and returns the result. Env wins over
// file/defaults; unset or invalid env leaves the field untouched.
// TIPSY_MICROPHONE (newer name) wins over the DISABLE alias when both are
// valid. DISABLE remains a working kill-switch when MICROPHONE is unset.
func (c MicrophoneConfig) WithEnv(lookup func(string) (string, bool)) MicrophoneConfig {
	if lookup == nil {
		return c
	}
	if v, ok := lookup("TIPSY_DISABLE_MICROPHONE"); ok {
		if disabled, set := ParseDisableMicrophoneEnv(v); set && disabled {
			c.Enabled = false
		}
	}
	if v, ok := lookup("TIPSY_MICROPHONE"); ok {
		if enabled, set := ParseMicrophoneEnv(v); set {
			c.Enabled = enabled
		}
	}
	if v, ok := lookup("TIPSY_MICROPHONE_SOURCE"); ok {
		if t := strings.TrimSpace(v); t != "" {
			c.Source = t
		}
	}
	return c
}

// EnabledControl names which input set the effective door: TIPSY_MICROPHONE,
// TIPSY_DISABLE_MICROPHONE, config file, or default. Env that is set-but-
// invalid is ignored, matching WithEnv. A corrupt file does not count as
// "config file" (callers must still surface the parse error).
func EnabledControl(fileJSON []byte, lookup func(string) (string, bool)) string {
	if lookup != nil {
		if v, ok := lookup("TIPSY_MICROPHONE"); ok {
			if _, set := ParseMicrophoneEnv(v); set {
				return MicrophoneControlEnv
			}
		}
		if v, ok := lookup("TIPSY_DISABLE_MICROPHONE"); ok {
			if disabled, set := ParseDisableMicrophoneEnv(v); set && disabled {
				return MicrophoneControlDisable
			}
		}
	}
	if _, err := ParseMicSection(fileJSON); err != nil {
		return MicrophoneControlDefault
	}
	if micSectionPresent(fileJSON) {
		return MicrophoneControlFile
	}
	return MicrophoneControlDefault
}

func micSectionPresent(fileJSON []byte) bool {
	if len(strings.TrimSpace(string(fileJSON))) == 0 {
		return false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(fileJSON, &top); err != nil {
		return false
	}
	raw, ok := top[MicrophoneConfigSectionKey]
	if !ok {
		return false
	}
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}
