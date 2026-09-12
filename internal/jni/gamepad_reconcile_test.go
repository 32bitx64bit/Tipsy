// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeGamepadConfigFile plants one canonical settings file with a gamepad
// section under an isolated XDG_CONFIG_HOME. Production resolves the same
// path via internal/config; tests must never touch the real home.
func writeGamepadConfigFile(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tipsy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))
}

// selectGamepadConfigFile combines one file body with one calibration
// environment and resets the cached effective config.
func selectGamepadConfigFile(t *testing.T, body string, env map[string]string) {
	t.Helper()
	writeGamepadConfigFile(t, body)
	t.Setenv("TIPSY_GAMEPAD_PATH", "direct")
	t.Setenv("TIPSY_GAMEPAD", "")
	t.Setenv("TIPSY_GAMEPAD_DEADZONE", "")
	for k, v := range env {
		t.Setenv(k, v)
	}
	ResetGamepadInputPath()
	t.Cleanup(ResetGamepadInputPath)
}

// TestGamepadEffectiveConfigFilePrecedence proves the live rule: persisted
// file under env, unset env keeping the file value. Removed v1 file keys
// are ignored, not honored.
func TestGamepadEffectiveConfigFilePrecedence(t *testing.T) {
	selectGamepadConfigFile(t,
		`{"gamepad":{"deadzone":0.1,"deadzoneRight":0.3,"invertYRight":true}}`,
		map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.2"})
	cfg := gamepadCalibration()
	if cfg.Deadzone != 0.2 {
		t.Fatalf("env must win over file, got %+v", cfg)
	}
	if cfg.EffectiveDeadzone() != 0.2 {
		t.Fatalf("effective floor must be the single global, got %+v", cfg)
	}
}

// TestGamepadEffectiveConfigFileInvalidEnvIgnored proves a bad override
// never clobbers the persisted choice.
func TestGamepadEffectiveConfigFileInvalidEnvIgnored(t *testing.T) {
	selectGamepadConfigFile(t,
		`{"gamepad":{"deadzone":0.1}}`,
		map[string]string{"TIPSY_GAMEPAD_DEADZONE": "huge"})
	if cfg := gamepadCalibration(); cfg.Deadzone != 0.1 {
		t.Fatalf("invalid env must be ignored, got %+v", cfg)
	}
}

// TestGamepadFileDisabledGate proves the persisted switch gates the frame
// path exactly like the kill-switch: zero emissions, one honest drop.
func TestGamepadFileDisabledGate(t *testing.T) {
	selectGamepadConfigFile(t, `{"gamepad":{"enabled":false}}`, nil)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectPadForTest(t, 1, 3)
	testDirectGamepadRecReset()

	before := RobloxDirectGamepadStats()
	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("file-disabled frame path emitted %d calls, want 0", n)
	}
	if RobloxDirectGamepadStats().Dropped == before.Dropped {
		t.Fatal("file-disabled frame was not counted as dropped")
	}
}

// TestGamepadFileDisabledPumpRefusal proves the pump never starts while the
// persisted switch is off.
func TestGamepadFileDisabledPumpRefusal(t *testing.T) {
	selectGamepadConfigFile(t, `{"gamepad":{"enabled":false}}`, nil)
	if StartRobloxDirectGamepadPumpDir(t.TempDir()) {
		StopRobloxDirectGamepadPump()
		t.Fatal("pump must refuse to start while gamepad.enabled=false")
	}
}

// TestGamepadFileDeadzonePipeline proves the persisted floor reaches the
// wire: with deadzone 0.2 in config.json the ≈0.09 X deflection floors to 0
// while the ≈0.5 Y deflection passes through — the same application order
// (ApplyCalibration with the effective config, then MapFrame, then the
// focus-gated handler) the pump runs.
func TestGamepadFileDeadzonePipeline(t *testing.T) {
	selectGamepadConfigFile(t, `{"gamepad":{"deadzone":0.2}}`, nil)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectPadForTest(t, 1, 3)
	testDirectGamepadRecReset()

	f, m, infos := calibrationPumpFrame(t)
	pumpFrameCalibrated(f, m, infos)
	axes := recordedAxisTriples()
	left0, ok := axes[0]
	if !ok {
		t.Fatal("file-calibrated left stick did not emit AXIS_X")
	}
	if left0[0] != 0 {
		t.Fatalf("file-calibrated AXIS_X f1 = %v, want floored 0", left0[0])
	}
	if math.Abs(float64(left0[1])+0.5) > 0.002 || left0[2] != 0 {
		t.Fatalf("file-calibrated left pack = %v, want (0,-0.5,0)", left0)
	}
	if axes[1] != left0 {
		t.Fatalf("AXIS_Y pack = %v, want the same pair as AXIS_X %v", axes[1], left0)
	}
}
