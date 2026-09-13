// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/gamepad"
)

// stubGamepadScan replaces the evdev enumeration for one test. Production
// always calls gamepad.Scan; the stub never synthesizes input content.
func stubGamepadScan(t *testing.T, fn func(dir string) (gamepad.ScanResult, error)) {
	t.Helper()
	old := gamepadScanFunc
	gamepadScanFunc = fn
	t.Cleanup(func() { gamepadScanFunc = old })
}

// xboxTestPad is an Xbox-class topology: RX/RY right stick, asymmetric
// Z/RZ analog triggers, HAT0 + DPAD buttons. Mapping is left empty so the
// report resolves it through the public ResolveMappingForDevice path.
func xboxTestPad() gamepad.DeviceInfo {
	stick := gamepad.AbsInfo{Minimum: -32768, Maximum: 32767, Flat: 128}
	trigger := gamepad.AbsInfo{Minimum: 0, Maximum: 255}
	hat := gamepad.AbsInfo{Minimum: -1, Maximum: 1}
	return gamepad.DeviceInfo{
		Path: "/dev/input/event0",
		Name: "Microsoft X-Box 360 pad",
		ID:   gamepad.DeviceID{BusType: 0x03, Vendor: 0x045e, Product: 0x028e},
		Abs: map[uint16]gamepad.AbsInfo{
			gamepad.AbsX: stick, gamepad.AbsY: stick,
			gamepad.AbsRX: stick, gamepad.AbsRY: stick,
			gamepad.AbsZ: trigger, gamepad.AbsRZ: trigger,
			gamepad.AbsHat0X: hat, gamepad.AbsHat0Y: hat,
		},
		HasKey: map[uint16]bool{
			gamepad.BtnSouth: true, gamepad.BtnEast: true,
			gamepad.BtnNorth: true, gamepad.BtnWest: true,
			gamepad.BtnTL: true, gamepad.BtnTR: true,
			gamepad.BtnTL2: true, gamepad.BtnTR2: true,
			gamepad.BtnStart: true, gamepad.BtnSelect: true,
			gamepad.BtnMode:   true,
			gamepad.BtnThumbl: true, gamepad.BtnThumbr: true,
			gamepad.BtnDpadUp: true, gamepad.BtnDpadDown: true,
			gamepad.BtnDpadLeft: true, gamepad.BtnDpadRight: true,
		},
		HasAbs: map[uint16]bool{
			gamepad.AbsX: true, gamepad.AbsY: true,
			gamepad.AbsRX: true, gamepad.AbsRY: true,
			gamepad.AbsZ: true, gamepad.AbsRZ: true,
			gamepad.AbsHat0X: true, gamepad.AbsHat0Y: true,
		},
	}
}

func TestDiagnoseGamepadNoPad(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	r := Diagnose(context.Background(), "gamepad")
	if r.Subsystem != "gamepad" {
		t.Fatalf("subsystem = %q", r.Subsystem)
	}
	if r.Status != "active" {
		t.Fatalf("no-pad status = %q, want active (honest empty is not a failure)", r.Status)
	}
	text := FormatSubsystem(r)
	for _, want := range []string{
		"zero devices is the honest state",
		"TIPSY_GAMEPAD=",
		"TIPSY_GAMEPAD_PATH=",
		"missing JSON",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no-pad report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "pad0") {
		t.Errorf("no-pad report fabricated a pad:\n%s", text)
	}
	if strings.Contains(text, "not implemented yet") {
		t.Errorf("report claims unimplemented:\n%s", text)
	}
}

func TestDiagnoseGamepadAliases(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	for _, alias := range []string{"pad", "controller", "GAMEPAD", " Pad "} {
		r := Diagnose(context.Background(), alias)
		if r.Subsystem != "gamepad" {
			t.Errorf("alias %q subsystem = %q, want gamepad", alias, r.Subsystem)
		}
		if r.Status != "active" {
			t.Errorf("alias %q status = %q, want active", alias, r.Status)
		}
	}
}

// TestDiagnoseGamepadOnePadCaps pins the one caps line per pad: content-free
// name, identity, mapping choice, and honest capabilities.
func TestDiagnoseGamepadOnePadCaps(t *testing.T) {
	pad := xboxTestPad()
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{Pads: []gamepad.DeviceInfo{pad}}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	r := Diagnose(context.Background(), "gamepad")
	if r.Status != "active" {
		t.Fatalf("one-pad status = %q, want active", r.Status)
	}
	text := FormatSubsystem(r)
	for _, want := range []string{
		"X-Box 360 pad",
		"vendor=045e",
		"product=028e",
		"mapping=xpad",
		"abs=X,Y,Z,RX,RY,RZ,HAT0X,HAT0Y",
		"buttons=17",
		"androidKeys=[19,20,21,22,96,",
		"androidAxes=[0,1,11,12,13,14,15,16,17,18]",
		"/dev/input/event0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("one-pad report missing %q:\n%s", want, text)
		}
	}
}

func TestDiagnoseGamepadEACCESHint(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{Denied: []string{"/dev/input/event0"}}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	r := Diagnose(context.Background(), "gamepad")
	if r.Status != "degraded" {
		t.Fatalf("EACCES status = %q, want degraded", r.Status)
	}
	text := FormatSubsystem(r)
	for _, want := range []string{
		"input group",
		"udev",
		"--device=all",
		"/dev/input/event0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("EACCES report missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "pad0") {
		t.Errorf("EACCES report fabricated a pad:\n%s", text)
	}
}

func TestDiagnoseGamepadDisabled(t *testing.T) {
	called := false
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		called = true
		return gamepad.ScanResult{}, nil
	})
	for _, off := range []string{"0", "off"} {
		t.Setenv("TIPSY_GAMEPAD", off)
		r := Diagnose(context.Background(), "gamepad")
		if r.Status != "disabled" {
			t.Fatalf("TIPSY_GAMEPAD=%q status = %q, want disabled", off, r.Status)
		}
		if !strings.Contains(FormatSubsystem(r), "kill-switch") {
			t.Fatalf("TIPSY_GAMEPAD=%q report omits kill-switch:\n%s", off, FormatSubsystem(r))
		}
	}
	if called {
		t.Fatal("disabled probe must not open evdev nodes")
	}
}

func TestDiagnoseGamepadPathState(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	// Lean build: every selector value is parsed-but-ignored, effective arm
	// always direct.
	for _, raw := range []string{"", "direct", "gameactivity", "both", "bogus"} {
		t.Setenv("TIPSY_GAMEPAD_PATH", raw)
		text := FormatSubsystem(Diagnose(context.Background(), "gamepad"))
		if !strings.Contains(text, "effective: direct") {
			t.Errorf("TIPSY_GAMEPAD_PATH=%q missing %q:\n%s", raw, "effective: direct", text)
		}
		if !strings.Contains(text, "parsed-but-ignored") {
			t.Errorf("TIPSY_GAMEPAD_PATH=%q missing parsed-but-ignored:\n%s", raw, text)
		}
	}
}

func TestDiagnoseGamepadRedactsSecrets(t *testing.T) {
	pad := xboxTestPad()
	pad.Name = "Evil .ROBLOSECURITY=abc123 pad"
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{Pads: []gamepad.DeviceInfo{pad}}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	text := FormatSubsystem(Diagnose(context.Background(), "gamepad"))
	if strings.Contains(text, "abc123") {
		t.Fatalf("pad report leaked secret:\n%s", text)
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("expected redaction marker:\n%s", text)
	}
}

func TestDoctorGamepadEACCESHint(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{Denied: []string{"/dev/input/event0", "/dev/input/event1"}}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	rep := Doctor(context.Background())
	var found string
	for _, issue := range rep.Issues {
		if strings.Contains(issue, "Gamepad") {
			found = issue
		}
	}
	if found == "" {
		t.Fatalf("doctor issues omit gamepad EACCES: %v", rep.Issues)
	}
	for _, want := range []string{"input group", "udev", "--device=all"} {
		if !strings.Contains(found, want) {
			t.Errorf("doctor hint missing %q: %q", want, found)
		}
	}
	text := FormatDoctor(rep)
	if !strings.Contains(text, "Gamepad") || !strings.Contains(text, "--device=all") {
		t.Errorf("doctor text omits gamepad hint:\n%s", text)
	}
}

func TestDoctorGamepadNoPadNoIssue(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	rep := Doctor(context.Background())
	for _, issue := range rep.Issues {
		if strings.Contains(issue, "Gamepad") {
			t.Fatalf("honest empty must not raise an issue: %q", issue)
		}
	}
	if text := FormatDoctor(rep); !strings.Contains(text, "honest empty") {
		t.Errorf("doctor text omits honest empty:\n%s", text)
	}
}

func TestDoctorGamepadEnvReported(t *testing.T) {
	stubGamepadScan(t, func(dir string) (gamepad.ScanResult, error) {
		return gamepad.ScanResult{}, nil
	})
	t.Setenv("TIPSY_GAMEPAD", "1")
	t.Setenv("TIPSY_GAMEPAD_PATH", "both")
	rep := Doctor(context.Background())
	// Lean build: the selector is parsed-but-ignored, effective arm direct.
	if rep.Gamepad.PathSelector != "direct" {
		t.Fatalf("doctor gamepad path = %q, want direct", rep.Gamepad.PathSelector)
	}
	if rep.Env["TIPSY_GAMEPAD_PATH"] != "both" {
		t.Fatalf("doctor env omits TIPSY_GAMEPAD_PATH: %v", rep.Env)
	}
}
