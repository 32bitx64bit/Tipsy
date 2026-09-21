// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

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
// file means defaults, a malformed one yields defaults plus an error.
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
	return config.UpdateJSON(func(data []byte) ([]byte, error) {
		return mergeMicrophoneSettings(data, settings)
	})
}

// saveMicrophoneSettingsAt overlays enabled onto the "microphone" section
// of path, preserving every other top-level key and any existing source
// pin. A malformed existing file is never overwritten.
func saveMicrophoneSettingsAt(path string, settings guimodel.MicrophoneSettings) error {
	merged, err := mergeMicrophoneSettingsFromPath(path, settings)
	if err != nil {
		return err
	}
	return config.AtomicWriteFile(path, merged, 0o600)
}

// mergeMicrophoneSettings updates only the microphone section in one
// complete config document. The canonical caller runs it under
// config.UpdateJSON's cross-process lock.
func mergeMicrophoneSettings(data []byte, settings guimodel.MicrophoneSettings) ([]byte, error) {
	cfg, err := mic.ParseMicSection(data)
	if err != nil {
		return nil, fmt.Errorf("microphone settings not saved: existing settings file is invalid (%v)", err)
	}
	cfg.Enabled = settings.Enabled
	merged, err := mic.UpsertMicSection(data, cfg)
	if err != nil {
		return nil, fmt.Errorf("microphone settings not saved: existing settings file is invalid (%v)", err)
	}
	merged = append(merged, '\n')
	return merged, nil
}

func mergeMicrophoneSettingsFromPath(path string, settings guimodel.MicrophoneSettings) ([]byte, error) {
	var data []byte
	if existing, err := os.ReadFile(path); err == nil {
		data = existing
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return mergeMicrophoneSettings(data, settings)
}

// microphoneEffectiveEnabled mirrors the mic-owned kill-switch:
// TIPSY_MICROPHONE=0|off|false|no (and the DISABLE alias) closes the door
// regardless of the file. Widget-off still wins; env never forces it on.
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
