// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/config"
)

// inputSyncEnv isolates every XDG root and returns the (not yet written)
// config.json path. Nothing here may ever reach the owner's real settings.
func inputSyncEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	t.Setenv("TIPSY_GAMEPAD", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	p := config.Paths()
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return p.ConfigFile
}

func writeInputConfig(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

const inputsOffConfig = `{"gamepad":{"enabled":false,"deadzone":0.25,"faceButtonLayout":"switch"},"microphone":{"enabled":false}}`
const inputsOnConfig = `{"gamepad":{"enabled":true,"deadzone":0.1,"faceButtonLayout":"xbox"},"microphone":{"enabled":true}}`

// requireCards asserts both input cards show the given persisted state
// consistently: checkbox, label, state note, slider, and layout.
func requireCards(t *testing.T, w *mainWindow, controllerOn, micOn bool, deadzone, face int) {
	t.Helper()
	if w.controllerEnable.IsChecked() != controllerOn || w.controllerEnable.Text() != controllerToggleText(controllerOn) || !w.controllerEnable.IsEnabled() {
		t.Fatalf("controller card checked=%v text=%q enabled=%v, want on=%v", w.controllerEnable.IsChecked(), w.controllerEnable.Text(), w.controllerEnable.IsEnabled(), controllerOn)
	}
	wantNote := "Controller input is on."
	if !controllerOn {
		wantNote = "Controller input is off"
	}
	if !strings.HasPrefix(w.controllerStateNote.Text(), wantNote) {
		t.Fatalf("controller state note=%q, want prefix %q", w.controllerStateNote.Text(), wantNote)
	}
	if w.microphoneEnable.IsChecked() != micOn || w.microphoneEnable.Text() != microphoneToggleText(micOn) || !w.microphoneEnable.IsEnabled() {
		t.Fatalf("microphone card checked=%v text=%q enabled=%v, want on=%v", w.microphoneEnable.IsChecked(), w.microphoneEnable.Text(), w.microphoneEnable.IsEnabled(), micOn)
	}
	wantNote = "Microphone access is allowed."
	if !micOn {
		wantNote = "Microphone access is off"
	}
	if !strings.HasPrefix(w.microphoneStateNote.Text(), wantNote) {
		t.Fatalf("microphone state note=%q, want prefix %q", w.microphoneStateNote.Text(), wantNote)
	}
	if w.controllerDeadL.Value() != deadzone || w.controllerFaceLayout.CurrentIndex() != face {
		t.Fatalf("controller deadzone=%d face=%d, want %d/%d", w.controllerDeadL.Value(), w.controllerFaceLayout.CurrentIndex(), deadzone, face)
	}
}

// Divergence reproduced before the fix: the cards bound the config once at
// window build, so a settings file changed afterwards (hand edit, another
// Tipsy window) left them showing the old state even after re-entering
// Settings.
func TestSettingsEntryReloadsPersistedInputToggles(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-entry"})
	defer app.Delete()

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()
	requireCards(t, win, true, true, 0, 0)

	writeInputConfig(t, cfg, inputsOffConfig, 0o600)
	win.selectPage(settingsPageIndex)
	qt.QCoreApplication_ProcessEvents()
	requireCards(t, win, false, false, 25, 1)
	if got := win.controllerDeadLValue.Text(); got != "0.25" {
		t.Fatalf("deadzone label=%q after reload", got)
	}
	if hint := win.controllerHint.Text(); !strings.Contains(hint, "changed outside this window") {
		t.Fatalf("controller hint does not say the card was reloaded: %q", hint)
	}

	// Reloading must not re-persist (the syncing guard): the file is unchanged.
	raw, err := os.ReadFile(cfg)
	if err != nil || string(raw) != inputsOffConfig {
		t.Fatalf("reload rewrote the settings file: %q err=%v", raw, err)
	}

	// And back: another round trip through a different page.
	win.selectPage(0)
	writeInputConfig(t, cfg, inputsOnConfig, 0o600)
	win.selectPage(settingsPageIndex)
	requireCards(t, win, true, true, 10, 0)

	// A file that disappears means defaults, still shown consistently.
	win.selectPage(0)
	if err := os.Remove(cfg); err != nil {
		t.Fatal(err)
	}
	win.selectPage(settingsPageIndex)
	requireCards(t, win, true, true, 0, 0)
	win.win.Close()
}

// A visible Settings page whose window is raised again re-reads the file;
// a hidden window or another page does no settings work at all.
func TestInputCardsReloadWhenWindowIsReturnedTo(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-return"})
	defer app.Delete()

	service := &optionalLookupService{}
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()

	// Hidden window: no work, state stays what it was.
	writeInputConfig(t, cfg, inputsOffConfig, 0o600)
	win.selectPage(settingsPageIndex)
	requireCards(t, win, false, false, 25, 1) // selecting the page is itself a refresh
	writeInputConfig(t, cfg, inputsOnConfig, 0o600)
	win.reloadInputSettingsOnReturn()
	requireCards(t, win, false, false, 25, 1)

	win.Show()
	qt.QCoreApplication_ProcessEvents()
	// Showing the window on the Settings page is a return.
	requireCards(t, win, true, true, 10, 0)

	// Another page: nothing is re-read until Settings is entered.
	win.selectPage(0)
	writeInputConfig(t, cfg, inputsOffConfig, 0o600)
	win.reloadInputSettingsOnReturn()
	requireCards(t, win, true, true, 10, 0)

	// The real activation event path, on the Settings page. Offscreen Qt may not
	// grant activation, so fall back to driving the same handler directly.
	win.selectPage(settingsPageIndex)
	writeInputConfig(t, cfg, inputsOnConfig, 0o600)
	win.win.ActivateWindow()
	qt.QCoreApplication_ProcessEvents()
	event := qt.NewQEvent(qt.QEvent__ActivationChange)
	qt.QCoreApplication_SendEvent(win.win.QObject, event)
	event.Delete()
	if win.win.IsActiveWindow() {
		requireCards(t, win, true, true, 10, 0)
	} else {
		t.Log("offscreen platform did not activate the window; exercising the handler directly")
		win.reloadInputSettingsOnReturn()
		requireCards(t, win, true, true, 10, 0)
	}

	// A change found on return also refreshes that card's diagnose note; an
	// unchanged return does not touch diagnostics at all.
	service.controllerCalls.Store(0)
	service.microphoneCalls.Store(0)
	win.reloadInputSettingsOnReturn()
	if service.controllerCalls.Load() != 0 || service.microphoneCalls.Load() != 0 {
		t.Fatalf("unchanged return ran diagnostics: controller=%d microphone=%d", service.controllerCalls.Load(), service.microphoneCalls.Load())
	}
	writeInputConfig(t, cfg, inputsOffConfig, 0o600)
	win.reloadInputSettingsOnReturn()
	requireCards(t, win, false, false, 25, 1)
	if service.controllerCalls.Load() != 1 || service.microphoneCalls.Load() != 1 {
		t.Fatalf("changed return did not refresh diagnose notes once: controller=%d microphone=%d", service.controllerCalls.Load(), service.microphoneCalls.Load())
	}
	win.selectPage(0)
	win.win.Close()
}

// Divergence reproduced before the fix: with a config.json that Tipsy refuses
// to write (not owner-private, e.g. hand-made 0644 or a symlinked dotfile) the
// toggle stayed flipped while nothing was saved, and its label and state note
// kept describing the old state.
func TestFailedInputSaveRevertsToPersistedState(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-failed-save"})
	defer app.Delete()
	writeInputConfig(t, cfg, inputsOnConfig, 0o644)

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()
	requireCards(t, win, true, true, 10, 0)

	win.controllerEnable.SetChecked(false)
	qt.QCoreApplication_ProcessEvents()
	requireCards(t, win, true, true, 10, 0)
	if hint := win.controllerHint.Text(); !strings.HasPrefix(hint, "Could not save controller settings") {
		t.Fatalf("controller hint=%q", hint)
	}
	if win.controllerHint.ObjectName() != "noticeError" {
		t.Fatalf("controller hint style=%q", win.controllerHint.ObjectName())
	}
	if !strings.HasPrefix(win.status.CurrentMessage(), "Could not save controller settings") {
		t.Fatalf("status bar=%q", win.status.CurrentMessage())
	}

	win.microphoneEnable.SetChecked(false)
	qt.QCoreApplication_ProcessEvents()
	requireCards(t, win, true, true, 10, 0)
	if hint := win.microphoneHint.Text(); !strings.HasPrefix(hint, "Could not save microphone settings") || win.microphoneHint.ObjectName() != "noticeError" {
		t.Fatalf("microphone hint=%q style=%q", hint, win.microphoneHint.ObjectName())
	}

	win.controllerDeadL.SetValue(40)
	win.controllerFaceLayout.SetCurrentIndex(1)
	qt.QCoreApplication_ProcessEvents()
	requireCards(t, win, true, true, 10, 0)
	if got := win.controllerDeadLValue.Text(); got != "0.10" {
		t.Fatalf("deadzone label=%q after failed save", got)
	}

	raw, err := os.ReadFile(cfg)
	if err != nil || string(raw) != inputsOnConfig {
		t.Fatalf("a refused save changed the settings file: %q err=%v", raw, err)
	}

	// Once the file is writable again the same click saves and sticks.
	if err := os.Chmod(cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	win.controllerEnable.SetChecked(false)
	win.microphoneEnable.SetChecked(false)
	qt.QCoreApplication_ProcessEvents()
	requireCards(t, win, false, false, 10, 0)
	fresh := newMainWindow(visualService{}, brandIcon())
	defer fresh.win.Delete()
	requireCards(t, fresh, false, false, 10, 0)
	fresh.win.Close()
	win.win.Close()
}

// A settings file Tipsy cannot read must not be presented as a saved "on"
// choice: the cards say so, lock their controls, and recover when the file is
// fixed.
func TestUnreadableSettingsFileIsNotShownAsAChoice(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-unreadable"})
	defer app.Delete()
	writeInputConfig(t, cfg, `{"gamepad":{"enabled":false},"microphone":{"enabled":false}`, 0o600)

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()
	requireUnreadable := func() {
		t.Helper()
		if win.controllerEnable.IsEnabled() || win.controllerDeadL.IsEnabled() || win.controllerFaceLayout.IsEnabled() || win.controllerReset.IsEnabled() || win.microphoneEnable.IsEnabled() {
			t.Fatal("controls stayed interactive while the settings file is unreadable")
		}
		if win.controllerEnable.Text() != controllerUnreadableToggleText || win.microphoneEnable.Text() != microphoneUnreadableToggleText {
			t.Fatalf("labels claim a state: %q / %q", win.controllerEnable.Text(), win.microphoneEnable.Text())
		}
		if win.controllerStateNote.Text() != controllerUnreadableNote || win.microphoneStateNote.Text() != microphoneUnreadableNote {
			t.Fatalf("state notes still describe a state: %q / %q", win.controllerStateNote.Text(), win.microphoneStateNote.Text())
		}
		if win.controllerHint.ObjectName() != "noticeWarning" || win.microphoneHint.ObjectName() != "noticeWarning" ||
			!strings.Contains(win.controllerHint.Text(), "invalid") || !strings.Contains(win.microphoneHint.Text(), "invalid") {
			t.Fatalf("warnings missing: %q / %q", win.controllerHint.Text(), win.microphoneHint.Text())
		}
	}
	requireUnreadable()
	win.selectPage(settingsPageIndex)
	requireUnreadable()
	raw, _ := os.ReadFile(cfg)
	if !strings.HasSuffix(string(raw), `"enabled":false}`) {
		t.Fatalf("an unreadable file was rewritten: %q", raw)
	}

	// Fixing the file recovers the real saved state and unlocks the controls.
	win.selectPage(0)
	writeInputConfig(t, cfg, inputsOffConfig, 0o600)
	win.selectPage(settingsPageIndex)
	requireCards(t, win, false, false, 25, 1)
	if !win.controllerDeadL.IsEnabled() || !win.controllerFaceLayout.IsEnabled() || !win.controllerReset.IsEnabled() {
		t.Fatal("controls stayed locked after the file was fixed")
	}
	if win.controllerHint.ObjectName() == "noticeWarning" || win.microphoneHint.ObjectName() == "noticeWarning" {
		t.Fatal("the unreadable-file warning outlived the fix")
	}
	win.win.Close()
}

// Only the controller section being mistyped locks only the controller card.
func TestMistypedControllerSectionLocksOnlyControllerCard(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-mistyped"})
	defer app.Delete()
	writeInputConfig(t, cfg, `{"gamepad":{"enabled":"no"},"microphone":{"enabled":false}}`, 0o600)

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()
	if win.controllerEnable.IsEnabled() || win.controllerEnable.Text() != controllerUnreadableToggleText {
		t.Fatalf("controller card enabled=%v text=%q", win.controllerEnable.IsEnabled(), win.controllerEnable.Text())
	}
	if win.microphoneEnable.IsChecked() || !win.microphoneEnable.IsEnabled() || win.microphoneEnable.Text() != microphoneToggleText(false) {
		t.Fatalf("microphone card checked=%v enabled=%v text=%q", win.microphoneEnable.IsChecked(), win.microphoneEnable.IsEnabled(), win.microphoneEnable.Text())
	}
	win.win.Close()
}

// Divergence reproduced before the fix: any control on the Controller card
// wrote the whole section from its widgets, so a card that was stale relative
// to the file silently reverted the controls the user never touched (nudging
// the deadzone re-enabled a controller turned off elsewhere).
func TestControllerSaveOverlaysOnlyTheChangedControl(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-overlay"})
	defer app.Delete()

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()
	requireCards(t, win, true, true, 0, 0)

	// Changed elsewhere after the window was built; the card is now stale.
	writeInputConfig(t, cfg, inputsOffConfig, 0o600)
	win.controllerDeadL.SetValue(40)
	qt.QCoreApplication_ProcessEvents()
	raw, _ := os.ReadFile(cfg)
	want := `{"gamepad":{"enabled":false,"deadzone":0.4,"faceButtonLayout":"switch"},"microphone":{"enabled":false}}`
	if strings.TrimSpace(string(raw)) != want {
		t.Fatalf("a deadzone nudge overwrote untouched fields:\n got %s\nwant %s", raw, want)
	}
	// The card now shows what is on disk, including the fields it was stale on.
	if win.controllerEnable.IsChecked() || win.controllerFaceLayout.CurrentIndex() != 1 || win.controllerDeadL.Value() != 40 {
		t.Fatalf("card did not adopt the persisted state: enabled=%v face=%d deadzone=%d", win.controllerEnable.IsChecked(), win.controllerFaceLayout.CurrentIndex(), win.controllerDeadL.Value())
	}
	if win.controllerEnable.Text() != controllerToggleText(false) || !strings.HasPrefix(win.controllerStateNote.Text(), "Controller input is off") {
		t.Fatalf("label/note disagree with the persisted state: %q / %q", win.controllerEnable.Text(), win.controllerStateNote.Text())
	}

	// A hand-edited finer deadzone is not rounded away by an unrelated toggle.
	writeInputConfig(t, cfg, `{"gamepad":{"enabled":true,"deadzone":0.255,"faceButtonLayout":"switch"}}`, 0o600)
	win.selectPage(settingsPageIndex)
	win.controllerEnable.SetChecked(false)
	raw, _ = os.ReadFile(cfg)
	if !strings.Contains(string(raw), `"deadzone":0.255`) || !strings.Contains(string(raw), `"enabled":false`) {
		t.Fatalf("toggle rewrote the deadzone: %s", raw)
	}
	win.win.Close()
}

// The graphics "Reset defaults" must not rebind or rewrite the input cards;
// "Reset controller defaults" resets only the Controller card and leaves the
// microphone alone. Neither leaves a card disagreeing with the file.
func TestResetsKeepInputCardsConsistentWithFile(t *testing.T) {
	cfg := inputSyncEnv(t)
	app := qt.NewQApplication([]string{"tipsy-input-sync-reset"})
	defer app.Delete()
	writeInputConfig(t, cfg, inputsOffConfig, 0o600)

	win := newMainWindow(visualService{}, brandIcon())
	defer win.win.Delete()
	requireCards(t, win, false, false, 25, 1)

	win.resetSettings()
	requireCards(t, win, false, false, 25, 1)
	raw, _ := os.ReadFile(cfg)
	if string(raw) != inputsOffConfig {
		t.Fatalf("graphics reset touched the input settings: %s", raw)
	}

	win.resetControllerSettings()
	requireCards(t, win, true, false, 0, 0)
	if got := win.controllerEnable.Text(); got != controllerToggleText(true) {
		t.Fatalf("controller label after reset=%q", got)
	}
	controller, layout, err := loadControllerSettingsAndFaceButtonLayout()
	if err != nil || !controller.Enabled || controller.Deadzone != 0 || layout != "xbox" {
		t.Fatalf("persisted controller after reset: %+v %q err=%v", controller, layout, err)
	}
	microphone, err := loadMicrophoneSettings()
	if err != nil || microphone.Enabled {
		t.Fatalf("controller reset changed the microphone: %+v err=%v", microphone, err)
	}
	win.win.Close()
}
