// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/gamepad"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func controllerTestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}

func TestControllerSettingsMissingFileMeansDefaults(t *testing.T) {
	t.Parallel()
	got, err := loadControllerSettingsAt(controllerTestPath(t))
	if err != nil {
		t.Fatalf("missing file err=%v", err)
	}
	if want := guimodel.DefaultControllerSettings(); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	want := controllerSettingsFromGamepad(gamepad.DefaultGamepadConfig())
	if got != want {
		t.Fatalf("defaults diverge from canonical gamepad defaults: got=%+v want=%+v", got, want)
	}
}

func TestControllerSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	want := guimodel.ControllerSettings{Enabled: false, Deadzone: 0.25}
	if err := saveControllerSettingsAt(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadControllerSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"gamepad"`) {
		t.Fatalf("canonical home must be the gamepad section: %s", raw)
	}
}

func TestControllerSettingsPartialSectionKeepsDefaults(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	if err := os.WriteFile(path, []byte("{\"gamepad\":{\"deadzone\": 0.2}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadControllerSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	want := guimodel.ControllerSettings{Enabled: true, Deadzone: 0.2}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

// TestControllerSettingsIgnoresRemovedKeys pins the honest downgrade: v1
// files with per-stick/invert/rumble/legacy keys load their lean subset.
func TestControllerSettingsIgnoresRemovedKeys(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	body := "{\"gamepad\":{\"enabled\": true, \"deadzone\": 0.15, \"deadzoneLeft\": 0.4, \"invertY\": true, \"rumble\": false}}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadControllerSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	want := guimodel.ControllerSettings{Enabled: true, Deadzone: 0.15}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestControllerSettingsMalformedSectionLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	bad := []byte("{\"dataDir\":\"/keep\",\"gamepad\":{\"deadzone\":\"huge\"}}\n")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadControllerSettingsAt(path)
	if err == nil {
		t.Fatal("malformed section accepted")
	}
	if want := guimodel.DefaultControllerSettings(); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	// The shared settings file is never backed up or rewritten on a read.
	kept, _ := os.ReadFile(path)
	if string(kept) != string(bad) {
		t.Fatalf("shared file touched: %s", kept)
	}
	if matches, _ := filepath.Glob(path + ".invalid-*"); len(matches) != 0 {
		t.Fatalf("backup written for shared file: %v", matches)
	}
}

func TestControllerSettingsMalformedFileMeansDefaultsAndError(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadControllerSettingsAt(path)
	if err == nil {
		t.Fatal("malformed file accepted")
	}
	if want := guimodel.DefaultControllerSettings(); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestControllerSettingsSavePreservesOtherKeys(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	if err := os.WriteFile(path, []byte("{\"dataDir\":\"/keep\",\"logLevel\":\"debug\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := guimodel.DefaultControllerSettings()
	want.Deadzone = 0.2
	if err := saveControllerSettingsAt(path, want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"/keep"`) || !strings.Contains(string(raw), `"gamepad"`) {
		t.Fatalf("other keys lost: %s", raw)
	}
	got, err := loadControllerSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestControllerSettingsSaveRefusesCorruptFile(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	bad := []byte("{not json")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveControllerSettingsAt(path, guimodel.DefaultControllerSettings()); err == nil {
		t.Fatal("save overwrote a corrupt shared file")
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != string(bad) {
		t.Fatalf("corrupt file touched: %s", kept)
	}
}

func TestControllerSettingsSaveRejectsInvalid(t *testing.T) {
	t.Parallel()
	path := controllerTestPath(t)
	bad := guimodel.DefaultControllerSettings()
	bad.Deadzone = -0.1
	if err := saveControllerSettingsAt(path, bad); err == nil {
		t.Fatal("invalid settings saved")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid save left a file: %v", err)
	}
}

func TestControllerEffectiveEnabledHonorsFileAndKillSwitch(t *testing.T) {
	t.Setenv("TIPSY_GAMEPAD", "")
	on := guimodel.DefaultControllerSettings()
	if !controllerEffectiveEnabled(on) {
		t.Fatal("default settings report disabled without kill-switch")
	}
	off := on
	off.Enabled = false
	if controllerEffectiveEnabled(off) {
		t.Fatal("file-disabled settings report enabled")
	}
	for _, kill := range []string{"0", "off", "false", "no", "OFF"} {
		t.Setenv("TIPSY_GAMEPAD", kill)
		if controllerEffectiveEnabled(on) {
			t.Fatalf("kill-switch %q ignored", kill)
		}
	}
}

func TestControllerPadRowsAreContentFree(t *testing.T) {
	t.Parallel()
	pad := guimodel.ControllerPad{
		Path: "/dev/input/event5", Name: "Xbox 360 Controller", Vendor: "045e",
		Product: "028e", Mapping: "xpad",
		Caps: "abs=X,Y buttons=15 androidKeys=[96] androidAxes=[0]",
	}
	if got := controllerPadTitle(pad); got != "Xbox 360 Controller" {
		t.Fatalf("title=%q", got)
	}
	detail := controllerPadDetail(pad)
	for _, want := range []string{"event5", "vendor=045e", "product=028e", "mapping xpad", "androidKeys=[96]"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q misses %q", detail, want)
		}
	}
	empty := guimodel.ControllerPad{Path: "/dev/input/event9"}
	if got := controllerPadTitle(empty); got != "Unknown pad" {
		t.Fatalf("empty title=%q", got)
	}
	if got := controllerPadDetail(empty); !strings.Contains(got, "spec-default") {
		t.Fatalf("empty mapping=%q", got)
	}
}

func TestDeadzoneSliderValueClamps(t *testing.T) {
	t.Parallel()
	if got := deadzoneSliderValue(0.08); got != 8 {
		t.Fatalf("0.08→%d", got)
	}
	if got := deadzoneSliderValue(-1); got != 0 {
		t.Fatalf("negative→%d", got)
	}
	if got := deadzoneSliderValue(9); got != int(guimodel.MaxControllerDeadzone*100) {
		t.Fatalf("overflow→%d", got)
	}
}

func TestControllerCardBuildsBindsAndPersistsOffscreen(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	t.Setenv("TIPSY_GAMEPAD", "")
	app := qt.NewQApplication([]string{"tipsy-controller-card-test"})
	defer app.Delete()

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()

	// Lean card: enable + one global deadzone slider, no per-stick
	// sliders, no invert toggles, no rumble box, no test readout.
	if win.controllerEnable == nil || win.controllerDeadL == nil || win.controllerDeadLValue == nil {
		t.Fatal("lean controller card widgets are incomplete")
	}
	if !win.controllerEnable.IsChecked() {
		t.Fatal("controller input is not enabled by default")
	}
	if win.controllerDeadLValue.Text() != "0.00" {
		t.Fatalf("deadzone label=%q", win.controllerDeadLValue.Text())
	}
	// Other cards untouched: graphics controls still standing.
	if win.settingsFPS == nil || win.settingsApply == nil || win.settingsRenderer == nil {
		t.Fatal("graphics card widgets are missing after the controller card landed")
	}
	// Opening Settings refreshes the honest no-pad enumeration.
	win.selectPage(2)
	qt.QCoreApplication_ProcessEvents()
	if !strings.Contains(win.controllerPadNote.Text(), "No gamepad found") {
		t.Fatalf("pad note=%q", win.controllerPadNote.Text())
	}
	// Toggling Enable persists to the isolated config only.
	win.controllerEnable.SetChecked(false)
	qt.QCoreApplication_ProcessEvents()
	if !strings.Contains(win.controllerHint.Text(), "saved") {
		t.Fatalf("save hint=%q", win.controllerHint.Text())
	}
	saved, err := loadControllerSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled {
		t.Fatalf("toggle did not persist: %+v", saved)
	}
	// Slider changes persist (every change, so keyboard adjustments save too).
	win.controllerDeadL.SetValue(20)
	qt.QCoreApplication_ProcessEvents()
	saved, err = loadControllerSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Deadzone != 0.2 {
		t.Fatalf("slider did not persist: %+v", saved)
	}
	if win.controllerDeadLValue.Text() != "0.20" {
		t.Fatalf("slider label=%q", win.controllerDeadLValue.Text())
	}
	win.selectPage(0)
	win.win.Close()
}
