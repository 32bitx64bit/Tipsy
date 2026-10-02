// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
)

// isolateGamepadConfig points the settings-file lookup at a temp dir so
// effective-config facts never read the developer's real config.json.
func isolateGamepadConfig(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tipsy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))
}

// TestDiagnoseGamepadCalibrationEnvFacts proves the lean override key is
// reported with its documented default when unset.
func TestDiagnoseGamepadCalibrationEnvFacts(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	isolateGamepadConfig(t, "")
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	t.Setenv("TIPSY_GAMEPAD_DEADZONE", "")
	os.Unsetenv("TIPSY_GAMEPAD_DEADZONE")
	text := FormatSubsystem(Diagnose(context.Background(), "gamepad"))
	for _, want := range []string{
		"TIPSY_GAMEPAD_DEADZONE=unset (default: device flat, 0.08 fallback when flat=0)",
		"Effective: file < env",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("calibration facts missing %q:\n%s", want, text)
		}
	}
}

// TestDiagnoseGamepadEffectiveFactsHonorFileEnvPrecedence proves the
// Effective line reflects the same file < env rule the pump enforces.
func TestDiagnoseGamepadEffectiveFactsHonorFileEnvPrecedence(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	isolateGamepadConfig(t, `{"gamepad":{"deadzone":0.1}}`)
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	t.Setenv("TIPSY_GAMEPAD_DEADZONE", "0.2")
	text := FormatSubsystem(Diagnose(context.Background(), "gamepad"))
	for _, want := range []string{
		"TIPSY_GAMEPAD_DEADZONE=0.2",
		"stick floor=0.20",
		"subsystem=on",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("effective facts missing %q:\n%s", want, text)
		}
	}
}

// TestDiagnoseGamepadEffectiveFactsReportFileOff proves a persisted
// gamepad.enabled=false surfaces as the config-file gate in the facts.
func TestDiagnoseGamepadEffectiveFactsReportFileOff(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	isolateGamepadConfig(t, `{"gamepad":{"enabled":false}}`)
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	t.Setenv("TIPSY_GAMEPAD_DEADZONE", "")
	text := FormatSubsystem(Diagnose(context.Background(), "gamepad"))
	if !strings.Contains(text, "subsystem=off (config file)") {
		t.Errorf("file-off gate missing from facts:\n%s", text)
	}
}

// TestDiagnoseGamepadFileOffIsDisabledAndOpensNothing proves the persisted
// switch is a first-class gate in the probe, like the kill-switch: status
// disabled, the Settings toggle named as the cause, and no evdev node opened.
func TestDiagnoseGamepadFileOffIsDisabledAndOpensNothing(t *testing.T) {
	called := false
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		called = true
		return gamepad.ScanResult{}, nil
	})
	isolateGamepadConfig(t, `{"gamepad":{"enabled":false}}`)
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	t.Setenv("TIPSY_GAMEPAD_DEADZONE", "")
	r := Diagnose(context.Background(), "gamepad")
	if r.Status != "disabled" {
		t.Fatalf("file-off status = %q, want disabled", r.Status)
	}
	if called {
		t.Fatal("file-disabled probe must not open evdev nodes")
	}
	text := FormatSubsystem(r)
	for _, want := range []string{
		"Settings > Controller",
		"TIPSY_GAMEPAD=1 (enabled)", // the env line reports the kill-switch alone
		"subsystem=off (config file)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("file-off report missing %q:\n%s", want, text)
		}
	}
	info := probeGamepad()
	if info.Enabled || info.DisabledBy != "config" {
		t.Fatalf("probe = enabled %v disabledBy %q, want disabled by config", info.Enabled, info.DisabledBy)
	}
}

// TestDiagnoseGamepadFollowsToggleWithoutRestart proves the probe never caches
// the switch: the same process sees each rewrite of the settings file on the
// next call, in both directions, matching what the live JNI pump does.
func TestDiagnoseGamepadFollowsToggleWithoutRestart(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	isolateGamepadConfig(t, `{"gamepad":{"enabled":true}}`)
	t.Setenv("TIPSY_GAMEPAD", "1")
	if r := Diagnose(context.Background(), "gamepad"); r.Status != "active" {
		t.Fatalf("enabled status = %q, want active", r.Status)
	}
	path := config.Paths().ConfigFile
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Diagnose(context.Background(), "gamepad"); r.Status != "disabled" {
		t.Fatalf("after toggle off status = %q, want disabled", r.Status)
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := Diagnose(context.Background(), "gamepad"); r.Status != "active" {
		t.Fatalf("after toggle on status = %q, want active", r.Status)
	}
}

// TestDiagnoseGamepadKillSwitchWinsOverFile pins the attribution when both
// gates are off: the env kill-switch is the reported cause.
func TestDiagnoseGamepadKillSwitchWinsOverFile(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	isolateGamepadConfig(t, `{"gamepad":{"enabled":false}}`)
	t.Setenv("TIPSY_GAMEPAD", "off")
	info := probeGamepad()
	if info.Enabled || info.DisabledBy != "kill-switch" {
		t.Fatalf("probe = enabled %v disabledBy %q, want disabled by kill-switch", info.Enabled, info.DisabledBy)
	}
	if text := FormatSubsystem(Diagnose(context.Background(), "gamepad")); !strings.Contains(text, "kill-switch") {
		t.Fatalf("report omits kill-switch:\n%s", text)
	}
}
