// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

// Microphone settings card backend. The GUI owns widgets only: it binds to
// the existing diagnose audio report (via guimodel.MicrophoneState) and
// persists the lean microphone section. It owns no Pulse, PCM, or JNI
// semantics — those belong to Audio/JNI.
//
// The canonical home is the existing settings file
// ($XDG_CONFIG_HOME/tipsy/config.json) under the "microphone" section
// shaped exactly as mic.MicrophoneConfig. CLI/mic owns that shape and its
// merge/defaults/env semantics; this file is a thin adapter.
// Missing file/key = microphone defaults (allowed). Persist enabled only;
// an existing source pin is left as-is.

import (
	"fmt"
	"os"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/mic"
)

func canonicalMicrophonePath() string { return config.Paths().ConfigFile }

func loadMicrophoneSettings() (guimodel.MicrophoneSettings, error) {
	return loadMicrophoneSettingsAt(canonicalMicrophonePath())
}

// loadMicrophoneSettingsAt reads the canonical section from path; a missing
// file means defaults. A malformed section (or a malformed file) yields
// defaults plus an honest error and leaves the file untouched.
func loadMicrophoneSettingsAt(path string) (guimodel.MicrophoneSettings, error) {
	defaults := guimodel.DefaultMicrophoneSettings()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults, nil
		}
		return defaults, err
	}
	cfg, parseErr := mic.ParseMicSection(data)
	if parseErr != nil {
		return defaults, fmt.Errorf("microphone settings are invalid (%v); using defaults, file left untouched", parseErr)
	}
	return guimodel.MicrophoneSettings{Enabled: cfg.Enabled}, nil
}

func saveMicrophoneSettings(settings guimodel.MicrophoneSettings) error {
	return saveMicrophoneSettingsAt(canonicalMicrophonePath(), settings)
}

// saveMicrophoneSettingsAt overlays enabled onto the "microphone" section
// of path, preserving every other top-level key and any existing source
// pin. A malformed existing file is never overwritten.
func saveMicrophoneSettingsAt(path string, settings guimodel.MicrophoneSettings) error {
	var data []byte
	if existing, err := os.ReadFile(path); err == nil {
		data = existing
	} else if !os.IsNotExist(err) {
		return err
	}
	cfg, err := mic.ParseMicSection(data)
	if err != nil {
		return fmt.Errorf("microphone settings not saved: existing settings file is invalid (%v)", err)
	}
	cfg.Enabled = settings.Enabled
	merged, err := mic.UpsertMicSection(data, cfg)
	if err != nil {
		return fmt.Errorf("microphone settings not saved: existing settings file is invalid (%v)", err)
	}
	merged = append(merged, '\n')
	return config.AtomicWriteFile(path, merged, 0o600)
}

// microphoneEffectiveEnabled mirrors the mic-owned kill-switch without
// caching: TIPSY_MICROPHONE=0|off|false|no (and the DISABLE alias) closes
// the door however the file is set. Read live so the card always reflects
// the current environment. Widget-off still wins; env is not used to force
// the checkbox on.
func microphoneEffectiveEnabled(settings guimodel.MicrophoneSettings) bool {
	if !settings.Enabled {
		return false
	}
	if enabled, set := mic.ParseMicrophoneEnv(os.Getenv("TIPSY_MICROPHONE")); set {
		return enabled
	}
	if disabled, set := mic.ParseDisableMicrophoneEnv(os.Getenv("TIPSY_DISABLE_MICROPHONE")); set && disabled {
		return false
	}
	return true
}

func microphoneCaptureCountText(n *int) string {
	if n == nil {
		return "not probed"
	}
	if *n == 1 {
		return "1 capture source"
	}
	return fmt.Sprintf("%d capture sources", *n)
}

func microphonePinText(pinned bool) string {
	if pinned {
		return "source pin: pinned"
	}
	return "source pin: default"
}

// microphoneStatusText renders the diagnose bind: door control, capture
// count, and pin boolean. Never a Pulse source name.
func microphoneStatusText(state guimodel.MicrophoneState) string {
	control := strings.TrimSpace(state.Control)
	if control == "" {
		control = mic.MicrophoneControlDefault
	}
	text := "Door: " + control + " · " + microphoneCaptureCountText(state.CaptureSources) + " · " + microphonePinText(state.SourcePinned)
	if note := strings.TrimSpace(state.Note); note != "" {
		text += " · " + note
	}
	return text
}
