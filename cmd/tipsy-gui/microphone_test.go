// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/mic"
)

func microphoneTestPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}

func TestMicrophoneSettingsMissingFileMeansDefaults(t *testing.T) {
	t.Parallel()
	got, err := loadMicrophoneSettingsAt(microphoneTestPath(t))
	if err != nil {
		t.Fatalf("missing file err=%v", err)
	}
	if want := guimodel.DefaultMicrophoneSettings(); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	if !mic.DefaultMicrophoneConfig().Enabled || !got.Enabled {
		t.Fatal("defaults must stay allowed")
	}
}

func TestMicrophoneSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	want := guimodel.MicrophoneSettings{Enabled: false}
	if err := saveMicrophoneSettingsAt(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadMicrophoneSettingsAt(path)
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
	if !strings.Contains(string(raw), `"microphone"`) {
		t.Fatalf("canonical home must be the microphone section: %s", raw)
	}
}

func TestMicrophoneSettingsPartialSectionKeepsDefaults(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	if err := os.WriteFile(path, []byte("{\"microphone\":{\"source\": \"kept-source\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadMicrophoneSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	want := guimodel.MicrophoneSettings{Enabled: true}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestMicrophoneSettingsSavePreservesSourcePin(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	if err := os.WriteFile(path, []byte("{\"microphone\":{\"enabled\": true, \"source\": \"kept-source\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveMicrophoneSettingsAt(path, guimodel.MicrophoneSettings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	got, err := loadMicrophoneSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatalf("enabled not persisted: %+v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"source"`) || !strings.Contains(string(raw), "kept-source") {
		t.Fatalf("existing source pin was wiped: %s", raw)
	}
}

func TestMicrophoneSettingsSaveDoesNotBakeSource(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	if err := saveMicrophoneSettingsAt(path, guimodel.MicrophoneSettings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"source"`) {
		t.Fatalf("GUI save baked a source pin: %s", raw)
	}
}

func TestMicrophoneSettingsMalformedSectionLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	bad := []byte("{\"dataDir\":\"/keep\",\"microphone\":{\"enabled\":\"yes\"}}\n")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadMicrophoneSettingsAt(path)
	if err == nil {
		t.Fatal("malformed section accepted")
	}
	if want := guimodel.DefaultMicrophoneSettings(); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != string(bad) {
		t.Fatalf("shared file touched: %s", kept)
	}
	if matches, _ := filepath.Glob(path + ".invalid-*"); len(matches) != 0 {
		t.Fatalf("backup written for shared file: %v", matches)
	}
}

func TestMicrophoneSettingsMalformedFileMeansDefaultsAndError(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadMicrophoneSettingsAt(path)
	if err == nil {
		t.Fatal("malformed file accepted")
	}
	if want := guimodel.DefaultMicrophoneSettings(); got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestMicrophoneSettingsSavePreservesOtherKeys(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	if err := os.WriteFile(path, []byte("{\"dataDir\":\"/keep\",\"logLevel\":\"debug\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := guimodel.MicrophoneSettings{Enabled: false}
	if err := saveMicrophoneSettingsAt(path, want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"/keep"`) || !strings.Contains(string(raw), `"microphone"`) {
		t.Fatalf("other keys lost: %s", raw)
	}
	got, err := loadMicrophoneSettingsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
}

func TestMicrophoneSettingsSaveRefusesCorruptFile(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	bad := []byte("{not json")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveMicrophoneSettingsAt(path, guimodel.DefaultMicrophoneSettings()); err == nil {
		t.Fatal("save overwrote a corrupt shared file")
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != string(bad) {
		t.Fatalf("corrupt file touched: %s", kept)
	}
}

func TestMicrophoneSettingsSaveRefusesMistypedSection(t *testing.T) {
	t.Parallel()
	path := microphoneTestPath(t)
	bad := []byte("{\"dataDir\":\"/keep\",\"microphone\":{\"enabled\":\"yes\"}}\n")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveMicrophoneSettingsAt(path, guimodel.MicrophoneSettings{Enabled: false}); err == nil {
		t.Fatal("save overwrote a mistyped microphone section")
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != string(bad) {
		t.Fatalf("mistyped section touched: %s", kept)
	}
}

func TestMicrophoneEffectiveEnabledHonorsFileAndKillSwitch(t *testing.T) {
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	on := guimodel.DefaultMicrophoneSettings()
	if !microphoneEffectiveEnabled(on) {
		t.Fatal("default settings report disabled without kill-switch")
	}
	off := on
	off.Enabled = false
	if microphoneEffectiveEnabled(off) {
		t.Fatal("file-disabled settings report enabled")
	}
	for _, kill := range []string{"0", "off", "false", "no", "OFF"} {
		t.Setenv("TIPSY_MICROPHONE", kill)
		if microphoneEffectiveEnabled(on) {
			t.Fatalf("kill-switch %q ignored", kill)
		}
	}
	t.Setenv("TIPSY_MICROPHONE", "")
	for _, kill := range []string{"1", "true", "yes"} {
		t.Setenv("TIPSY_DISABLE_MICROPHONE", kill)
		if microphoneEffectiveEnabled(on) {
			t.Fatalf("DISABLE alias %q ignored", kill)
		}
	}
}

func TestMicrophoneStatusTextNeverListsSourceNames(t *testing.T) {
	t.Parallel()
	n := 2
	got := microphoneStatusText(guimodel.MicrophoneState{
		Enabled:        true,
		Control:        "config file",
		CaptureSources: &n,
		SourcePinned:   true,
		Note:           "synthetic note",
	})
	for _, want := range []string{"Door: config file", "2 capture sources", "source pin: pinned", "synthetic note"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q misses %q", got, want)
		}
	}
	if strings.Contains(got, "alsa_input") || strings.Contains(strings.ToLower(got), "pulse source") {
		t.Fatalf("status listed a source name: %q", got)
	}
	if got := microphoneStatusText(guimodel.MicrophoneState{}); !strings.Contains(got, "not probed") || !strings.Contains(got, "source pin: default") {
		t.Fatalf("unprobed status=%q", got)
	}
	one := 1
	if got := microphoneCaptureCountText(&one); got != "1 capture source" {
		t.Fatalf("singular count=%q", got)
	}
}

func TestMicrophoneCardBuildsBindsAndPersistsOffscreen(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	app := qt.NewQApplication([]string{"tipsy-microphone-card-test"})
	defer app.Delete()

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()

	if win.microphoneEnable == nil || win.microphoneStateNote == nil || win.microphoneStatusNote == nil || win.microphoneHint == nil {
		t.Fatal("microphone card widgets are incomplete")
	}
	if win.microphoneEnable.Text() != microphoneConsentLabel {
		t.Fatalf("label=%q", win.microphoneEnable.Text())
	}
	if !win.microphoneEnable.IsChecked() {
		t.Fatal("microphone consent is not enabled by default")
	}
	if win.settingsFPS == nil || win.settingsApply == nil || win.settingsRenderer == nil {
		t.Fatal("graphics card widgets are missing after the microphone card landed")
	}
	if win.controllerEnable == nil {
		t.Fatal("controller card widgets are missing after the microphone card landed")
	}
	win.selectPage(2)
	qt.QCoreApplication_ProcessEvents()
	if !strings.Contains(win.microphoneStatusNote.Text(), "not probed") {
		t.Fatalf("status note=%q", win.microphoneStatusNote.Text())
	}
	if strings.Contains(win.microphoneStatusNote.Text(), "alsa_input") {
		t.Fatalf("status listed a source name: %q", win.microphoneStatusNote.Text())
	}
	win.microphoneEnable.SetChecked(false)
	qt.QCoreApplication_ProcessEvents()
	if !strings.Contains(win.microphoneHint.Text(), "saved") {
		t.Fatalf("save hint=%q", win.microphoneHint.Text())
	}
	saved, err := loadMicrophoneSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled {
		t.Fatalf("toggle did not persist: %+v", saved)
	}
	win.selectPage(0)
	win.win.Close()
}
