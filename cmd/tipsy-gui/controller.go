// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

// Controller settings card backend (lean, simplified 2026-09-12).
//
// The GUI owns widgets only: it binds to the existing diagnose gamepad
// report (via guimodel.ControllerState) and persists the lean gamepad
// section. It owns no mapping, deadzone, rumble, or feed-in semantics —
// those belong to Input/JNI.
//
// The canonical home is the existing settings file
// ($XDG_CONFIG_HOME/tipsy/config.json) under the "gamepad" section shaped
// exactly as gamepad.GamepadConfig: {enabled, deadzone}. Input owns that
// shape and its merge/defaults/env semantics; this file is a thin adapter.
// Missing file/key = gamepad defaults (on, device-flat baseline).
//
// Deleted vs controller v1 (honest list, see
// gamepad-simplify-2026-09-12.md): the legacy controller.json import, the
// local test-input probe (open/poll/bars/lamps), per-stick conversions, and
// button/axis readout labels. Headless parity stays with the environment:
// TIPSY_GAMEPAD=0|off disables the subsystem regardless of the file, and
// TIPSY_GAMEPAD_DEADZONE overrides the file (env wins).

import (
	"fmt"
	"os"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

// controllerSettingsFromGamepad renders the effective calibration as widget
// state: the single global floor both sticks share.
func controllerSettingsFromGamepad(cfg gamepad.GamepadConfig) guimodel.ControllerSettings {
	return guimodel.ControllerSettings{
		Enabled:  cfg.Enabled,
		Deadzone: cfg.EffectiveDeadzone(),
	}
}

// gamepadConfigFromController bakes widget state into calibration.
func gamepadConfigFromController(settings guimodel.ControllerSettings) gamepad.GamepadConfig {
	cfg := gamepad.DefaultGamepadConfig()
	cfg.Enabled = settings.Enabled
	cfg.Deadzone = settings.Deadzone
	cfg.Normalize()
	return cfg
}

// canonicalControllerPath is the one persisted home: the "gamepad" section
// of the existing settings file.
func canonicalControllerPath() string { return config.Paths().ConfigFile }

func loadControllerSettings() (guimodel.ControllerSettings, error) {
	return loadControllerSettingsAt(canonicalControllerPath())
}

// loadControllerSettingsAt reads the canonical section from path; a missing
// file means defaults. A malformed section (or a malformed file) yields
// defaults plus an honest error and leaves the file untouched.
func loadControllerSettingsAt(path string) (guimodel.ControllerSettings, error) {
	defaults := controllerSettingsFromGamepad(gamepad.DefaultGamepadConfig())
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults, nil
		}
		return defaults, err
	}
	cfg, parseErr := gamepad.ParseGamepadSection(data)
	if parseErr != nil {
		return defaults, fmt.Errorf("controller settings are invalid (%v); using defaults, file left untouched", parseErr)
	}
	return controllerSettingsFromGamepad(cfg), nil
}

func saveControllerSettings(settings guimodel.ControllerSettings) error {
	return saveControllerSettingsAt(canonicalControllerPath(), settings)
}

// saveControllerSettingsAt validates, then overlays the "gamepad" section
// onto path, preserving every other top-level key. A malformed existing file
// is never overwritten: the save fails honestly so unrelated keys
// (dataDir, consent, …) cannot be destroyed by a gamepad write.
func saveControllerSettingsAt(path string, settings guimodel.ControllerSettings) error {
	if err := guimodel.ValidateControllerSettings(settings); err != nil {
		return err
	}
	var data []byte
	if existing, err := os.ReadFile(path); err == nil {
		data = existing
	} else if !os.IsNotExist(err) {
		return err
	}
	merged, err := gamepad.UpsertGamepadSection(data, gamepadConfigFromController(settings))
	if err != nil {
		return fmt.Errorf("controller settings not saved: existing settings file is invalid (%v)", err)
	}
	merged = append(merged, '\n')
	return config.AtomicWriteFile(path, merged, 0o600)
}

// controllerEffectiveEnabled mirrors the Input-owned kill-switch without
// caching: TIPSY_GAMEPAD=0|off|false|no disables pads however the file is
// set. Read live so the card always reflects the current environment.
func controllerEffectiveEnabled(settings guimodel.ControllerSettings) bool {
	if !settings.Enabled {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TIPSY_GAMEPAD"))) {
	case "0", "off", "false", "no":
		return false
	default:
		return true
	}
}

// controllerPadTitle renders the per-pad row title: the sanitized device
// name, never input content.
func controllerPadTitle(pad guimodel.ControllerPad) string {
	name := strings.TrimSpace(pad.Name)
	if name == "" {
		name = "Unknown pad"
	}
	return name
}

// controllerPadDetail renders the per-pad status line: node, identity, and
// the mapping choice resolved by the gamepad package.
func controllerPadDetail(pad guimodel.ControllerPad) string {
	node := pad.Path
	if i := strings.LastIndex(node, "/"); i >= 0 {
		node = node[i+1:]
	}
	mapping := pad.Mapping
	if mapping == "" {
		mapping = "spec-default"
	}
	detail := fmt.Sprintf("%s · vendor=%s product=%s · mapping %s", node, pad.Vendor, pad.Product, mapping)
	if strings.TrimSpace(pad.Caps) != "" {
		detail += "\n" + strings.TrimSpace(pad.Caps)
	}
	return detail
}

// controllerProbe is kept as a stub type for window-lifetime compatibility:
// the lean card never opens a pad (no local test readout), so the probe is
// always nil and stopControllerTest is a no-op. The test readout (bars,
// lamps, open/poll) is deleted vs v1.
type controllerProbe struct {
	dev *gamepad.Device
}

func (p *controllerProbe) close() {
	if p == nil || p.dev == nil {
		return
	}
	_ = p.dev.Close()
	p.dev = nil
}
