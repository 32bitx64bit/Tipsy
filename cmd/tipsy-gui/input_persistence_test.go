// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/config"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

// TestInputSettingsConcurrentCanonicalSavesReloadBoth covers two independent
// Settings windows saving each section at once. Both values must survive the
// shared config.json write and hydrate a fresh UI load; an unmodeled key is
// included to prove the narrow writers do not discard future settings.
func TestInputSettingsConcurrentCanonicalSavesReloadBoth(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	p := config.Paths()
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 20; round++ {
		if err := config.AtomicWriteFile(p.ConfigFile, []byte(`{"future":{"keep":true},"gamepad":{"enabled":false},"microphone":{"enabled":false}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errCh := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errCh <- saveControllerSettings(guimodel.ControllerSettings{Enabled: true, Deadzone: 0.2})
		}()
		go func() {
			defer wg.Done()
			<-start
			errCh <- saveMicrophoneSettings(guimodel.MicrophoneSettings{Enabled: true})
		}()
		close(start)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			if err != nil {
				t.Fatalf("round %d save: %v", round, err)
			}
		}
		controller, err := loadControllerSettings()
		if err != nil {
			t.Fatal(err)
		}
		microphone, err := loadMicrophoneSettings()
		if err != nil {
			t.Fatal(err)
		}
		if !controller.Enabled || controller.Deadzone != 0.2 || !microphone.Enabled {
			t.Fatalf("round %d reload controller=%+v microphone=%+v", round, controller, microphone)
		}
	}

	raw, err := os.ReadFile(p.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["future"]) != `{"keep":true}` {
		t.Fatalf("unmodeled config field was lost: %s", raw)
	}
}

func TestInputToggleUIReloadsEnabledState(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	t.Setenv("TIPSY_GAMEPAD", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	if err := saveControllerSettings(guimodel.ControllerSettings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := saveMicrophoneSettings(guimodel.MicrophoneSettings{Enabled: false}); err != nil {
		t.Fatal(err)
	}

	app := qt.NewQApplication([]string{"tipsy-input-persistence-test"})
	defer app.Delete()
	first := newMainWindow(visualService{}, brandIcon())
	defer first.win.Delete()
	if first.controllerEnable.IsChecked() || first.microphoneEnable.IsChecked() {
		t.Fatal("first UI did not hydrate its persisted off state")
	}
	if first.controllerEnable.Text() != controllerToggleText(false) || first.microphoneEnable.Text() != microphoneToggleText(false) {
		t.Fatalf("off state labels controller=%q microphone=%q", first.controllerEnable.Text(), first.microphoneEnable.Text())
	}
	if first.controllerEnable.ObjectName() != "inputToggle" || first.microphoneEnable.ObjectName() != "inputToggle" {
		t.Fatal("input toggles are missing the explicit checked/unchecked presentation")
	}
	first.controllerEnable.SetChecked(true)
	first.microphoneEnable.SetChecked(true)
	qt.QCoreApplication_ProcessEvents()
	first.win.Close()

	second := newMainWindow(visualService{}, brandIcon())
	defer second.win.Delete()
	if !second.controllerEnable.IsChecked() || !second.microphoneEnable.IsChecked() {
		t.Fatalf("reloaded UI lost enabled state: controller=%v microphone=%v", second.controllerEnable.IsChecked(), second.microphoneEnable.IsChecked())
	}
	if second.controllerEnable.Text() != controllerToggleText(true) || second.microphoneEnable.Text() != microphoneToggleText(true) {
		t.Fatalf("reloaded on state labels controller=%q microphone=%q", second.controllerEnable.Text(), second.microphoneEnable.Text())
	}
	second.win.Close()
}
