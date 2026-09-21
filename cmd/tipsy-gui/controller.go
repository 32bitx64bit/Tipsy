// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

// controllerSettingsFromGamepad renders the effective calibration as widget
// state: the single global deadzone floor both sticks share.
func controllerSettingsFromGamepad(cfg gamepad.GamepadConfig) guimodel.ControllerSettings {
	return guimodel.ControllerSettings{
		Enabled:  cfg.Enabled,
		Deadzone: cfg.EffectiveDeadzone(),
	}
}

// gamepadConfigFromController bakes widget state and the selected layout
// into the config.
func gamepadConfigFromController(settings guimodel.ControllerSettings, layout gamepad.FaceButtonLayout) gamepad.GamepadConfig {
	cfg := gamepad.DefaultGamepadConfig()
	cfg.Enabled = settings.Enabled
	cfg.Deadzone = settings.Deadzone
	cfg.FaceButtonLayout = layout
	cfg.Normalize()
	return cfg
}

// canonicalControllerPath is the one persisted home: the "gamepad" section
// of the existing settings file.
func canonicalControllerPath() string { return config.Paths().ConfigFile }

func loadControllerSettings() (guimodel.ControllerSettings, error) {
	return loadControllerSettingsAt(canonicalControllerPath())
}

// loadControllerSettingsAndFaceButtonLayout reads both controller widget
// surfaces from one shared config-file snapshot.
func loadControllerSettingsAndFaceButtonLayout() (guimodel.ControllerSettings, gamepad.FaceButtonLayout, error) {
	return loadControllerSettingsAndFaceButtonLayoutAt(canonicalControllerPath())
}

// loadControllerSettingsAt reads the canonical section from path; a missing
// file means defaults, a malformed one yields defaults plus an error.
func loadControllerSettingsAt(path string) (guimodel.ControllerSettings, error) {
	settings, _, err := loadControllerSettingsAndFaceButtonLayoutAt(path)
	return settings, err
}

func loadControllerSettingsAndFaceButtonLayoutAt(path string) (guimodel.ControllerSettings, gamepad.FaceButtonLayout, error) {
	defaults := controllerSettingsFromGamepad(gamepad.DefaultGamepadConfig())
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults, gamepad.FaceButtonLayoutXbox, nil
		}
		return defaults, gamepad.FaceButtonLayoutXbox, err
	}
	cfg, parseErr := gamepad.ParseGamepadSection(data)
	if parseErr != nil {
		return defaults, gamepad.FaceButtonLayoutXbox, fmt.Errorf("controller settings are invalid (%v); using defaults, file left untouched", parseErr)
	}
	return controllerSettingsFromGamepad(cfg), cfg.FaceButtonLayout, nil
}

func saveControllerSettings(settings guimodel.ControllerSettings) error {
	return config.UpdateJSON(func(data []byte) ([]byte, error) {
		return mergeControllerSettings(data, settings)
	})
}

func saveControllerSettingsWithFaceButtonLayout(settings guimodel.ControllerSettings, layout gamepad.FaceButtonLayout) error {
	return config.UpdateJSON(func(data []byte) ([]byte, error) {
		return mergeControllerSettingsWithFaceButtonLayout(data, settings, layout)
	})
}

// saveControllerSettingsAt overlays the "gamepad" section onto path,
// preserving every other top-level key. A malformed existing file is never
// overwritten, so unrelated keys cannot be destroyed by a gamepad write.
func saveControllerSettingsAt(path string, settings guimodel.ControllerSettings) error {
	merged, err := mergeControllerSettingsFromPath(path, settings)
	if err != nil {
		return err
	}
	return config.AtomicWriteFile(path, merged, 0o600)
}

// mergeControllerSettings updates only the gamepad section in one complete
// config document. The canonical caller runs it under config.UpdateJSON's
// cross-process lock.
func mergeControllerSettings(data []byte, settings guimodel.ControllerSettings) ([]byte, error) {
	// This compatibility helper preserves an already selected layout when a
	// caller only changes the pre-existing enable/deadzone controls.
	cfg, err := gamepad.ParseGamepadSection(data)
	if err != nil {
		return nil, fmt.Errorf("controller settings not saved: existing settings file is invalid (%v)", err)
	}
	return mergeControllerSettingsWithFaceButtonLayout(data, settings, cfg.FaceButtonLayout)
}

func mergeControllerSettingsWithFaceButtonLayout(data []byte, settings guimodel.ControllerSettings, layout gamepad.FaceButtonLayout) ([]byte, error) {
	if err := guimodel.ValidateControllerSettings(settings); err != nil {
		return nil, err
	}
	merged, err := gamepad.UpsertGamepadSection(data, gamepadConfigFromController(settings, layout))
	if err != nil {
		return nil, fmt.Errorf("controller settings not saved: existing settings file is invalid (%v)", err)
	}
	merged = append(merged, '\n')
	return merged, nil
}

func mergeControllerSettingsFromPath(path string, settings guimodel.ControllerSettings) ([]byte, error) {
	var data []byte
	if existing, err := os.ReadFile(path); err == nil {
		data = existing
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return mergeControllerSettings(data, settings)
}

// controllerEffectiveEnabled mirrors the Input-owned kill-switch:
// TIPSY_GAMEPAD=0|off|false|no disables pads regardless of the file.
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

// controllerProbe is a stub: the card never opens a pad, so it stays nil and
// stopControllerTest is a no-op.
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
