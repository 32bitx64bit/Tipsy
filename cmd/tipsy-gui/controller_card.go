// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/gamepad"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

// controllerAppliesLiveText says when a saved controller change reaches a
// running Roblox. It mirrors the JNI side: the settings file is re-checked
// (rate-limited, no polling) whenever the pad produces input, and read fresh at
// every launch and whenever the Roblox window regains focus. A pad that is
// idle, or controllers that were off when Roblox started, are picked up at the
// focus gain.
const controllerAppliesLiveText = "on your next controller input or when its window regains focus"

func controllerToggleText(enabled bool) string {
	if enabled {
		return "✓ Controller input enabled"
	}
	return "Controller input disabled"
}

// controllerUnreadableToggleText labels the toggle while the settings file
// cannot be read: the box shows defaults, not a saved choice, so it must not
// claim "enabled" or "disabled".
const controllerUnreadableToggleText = "Controller input — settings file unreadable (defaults shown)"

const controllerUnreadableNote = "The settings file could not be read, so the controls below show defaults and are locked. Fix or remove the file, then return to this page."

// controllerHintDefault is the card's resting hint.
const controllerHintDefault = "Xbox is the default face-button layout. Choose Switch if A/B or X/Y feel reversed on a Switch-labelled controller. Deadzone applies to both sticks (0.00–0.50, default 0.00 = device flat). Changes save immediately and reach a running Roblox " + controllerAppliesLiveText + ", and every later launch; they never touch the graphics settings above."

func (w *mainWindow) buildControllerCard() *qt.QFrame {
	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Controller").QWidget)

	intro := qt.NewQLabel3("Linux gamepads feed controller-enabled experiences. There are no per-game profiles.")
	intro.SetWordWrap(true)
	setObjectName(intro.QObject, "mutedText")
	cardLayout.AddWidget(intro.QWidget)

	w.controllerEnable = qt.NewQCheckBox3(controllerToggleText(false))
	setObjectName(w.controllerEnable.QObject, "inputToggle")
	w.controllerEnable.SetAccessibleName("Enable controller input")
	w.controllerEnable.SetAccessibleDescription("Default on. Switching off disconnects the controller from a running Roblox " + controllerAppliesLiveText + ". TIPSY_GAMEPAD=0 or off disables pads regardless of this setting.")
	w.controllerEnable.SetToolTip("Default on. Off disconnects the controller from a running Roblox " + controllerAppliesLiveText + ". The TIPSY_GAMEPAD=0|off kill-switch disables pads regardless.")
	w.controllerEnable.OnToggled(func(bool) { w.persistControllerSettings() })
	cardLayout.AddWidget(w.controllerEnable.QWidget)

	w.controllerStateNote = qt.NewQLabel3("")
	w.controllerStateNote.SetWordWrap(true)
	w.controllerStateNote.SetAccessibleName("Controller effective state")
	setObjectName(w.controllerStateNote.QObject, "mutedText")
	cardLayout.AddWidget(w.controllerStateNote.QWidget)

	padHead := qt.NewQHBoxLayout2()
	padHead.AddWidget(sectionLabel("Connected pads").QWidget)
	padHead.AddStretch()
	refresh := qt.NewQPushButton3("Refresh")
	setObjectName(refresh.QObject, "secondaryButton")
	refresh.SetAccessibleName("Refresh connected pads")
	refresh.SetAccessibleDescription("Re-read the local pad enumeration without opening pads for input")
	refresh.OnClicked(w.refreshControllerPads)
	padHead.AddWidget(refresh.QWidget)
	cardLayout.AddLayout(padHead.QLayout)

	w.controllerPadRows = qt.NewQVBoxLayout2()
	cardLayout.AddLayout(w.controllerPadRows.QLayout)

	w.controllerPadNote = qt.NewQLabel3("Press Refresh to list connected pads. Names and mapping choices are content-free; no input values are shown.")
	w.controllerPadNote.SetWordWrap(true)
	w.controllerPadNote.SetAccessibleName("Connected pads status")
	setObjectName(w.controllerPadNote.QObject, "mutedText")
	cardLayout.AddWidget(w.controllerPadNote.QWidget)

	form := qt.NewQFormLayout2()
	form.SetHorizontalSpacing(24)
	form.SetVerticalSpacing(16)
	form.SetRowWrapPolicy(qt.QFormLayout__WrapLongRows)
	form.SetFieldGrowthPolicy(qt.QFormLayout__AllNonFixedFieldsGrow)

	w.controllerFaceLayout = qt.NewQComboBox2()
	w.controllerFaceLayout.AddItems([]string{
		"Xbox (A bottom, X left)",
		"Switch (B bottom, Y left)",
	})
	w.controllerFaceLayout.SetAccessibleName("Controller face button layout")
	w.controllerFaceLayout.SetAccessibleDescription("Choose Xbox or Switch face-button labels. Xbox is the default. Switch swaps A/B and X/Y so Switch-labelled controllers match their printed buttons.")
	w.controllerFaceLayout.SetToolTip("Xbox is the default. Switch swaps A/B and X/Y.")
	w.controllerFaceLayout.OnCurrentIndexChanged(func(int) { w.persistControllerSettings() })
	form.AddRow3("Face button layout", w.controllerFaceLayout.QWidget)

	w.controllerDeadL, w.controllerDeadLValue = w.newDeadzoneRow("Stick deadzone")
	form.AddRow3("Deadzone", w.controllerDeadL.QWidget)
	form.AddRow3("", w.controllerDeadLValue.QWidget)
	cardLayout.AddLayout(form.QLayout)

	w.controllerHint = qt.NewQLabel3(controllerHintDefault)
	w.controllerHint.SetWordWrap(true)
	setObjectName(w.controllerHint.QObject, "noticeInfo")
	cardLayout.AddWidget(w.controllerHint.QWidget)

	resetRow := qt.NewQHBoxLayout2()
	resetRow.AddStretch()
	w.controllerReset = qt.NewQPushButton3("Reset controller defaults")
	setObjectName(w.controllerReset.QObject, "secondaryButton")
	w.controllerReset.SetAccessibleName("Reset controller defaults")
	w.controllerReset.OnClicked(w.resetControllerSettings)
	resetRow.AddWidget(w.controllerReset.QWidget)
	cardLayout.AddLayout(resetRow.QLayout)

	w.bindControllerSettings(w.controllerSettings)
	if w.controllerLoadErr != nil {
		setNotice(w.controllerHint, "noticeWarning", w.controllerLoadErr.Error())
	}
	return card
}

func (w *mainWindow) newDeadzoneRow(accessible string) (*qt.QSlider, *qt.QLabel) {
	slider := qt.NewQSlider3(qt.Horizontal)
	slider.SetRange(0, int(guimodel.MaxControllerDeadzone*100))
	slider.SetSingleStep(1)
	slider.SetPageStep(5)
	slider.SetAccessibleName(accessible)
	slider.SetToolTip("Stick deadzone from 0.00 to 0.50; default 0.00 (device flat)")
	value := qt.NewQLabel3("0.00")
	setObjectName(value.QObject, "mutedText")
	slider.OnValueChanged(func(v int) {
		value.SetText(fmt.Sprintf("%.2f", float64(v)/100))
		// Persist on every change so keyboard adjustments save too.
		w.persistControllerSettings()
	})
	return slider, value
}

func (w *mainWindow) bindControllerSettings(settings guimodel.ControllerSettings) {
	w.controllerSyncing = true
	defer func() { w.controllerSyncing = false }()
	// While the settings file cannot be read the card shows defaults it cannot
	// save, so the controls are locked rather than left to claim a choice.
	usable := w.controllerLoadErr == nil
	if w.controllerEnable != nil {
		w.controllerEnable.SetChecked(settings.Enabled)
		if usable {
			w.controllerEnable.SetText(controllerToggleText(settings.Enabled))
		} else {
			w.controllerEnable.SetText(controllerUnreadableToggleText)
		}
		w.controllerEnable.SetEnabled(usable)
	}
	if w.controllerDeadL != nil {
		w.controllerDeadL.SetValue(deadzoneSliderValue(settings.Deadzone))
		w.controllerDeadL.SetEnabled(usable)
	}
	if w.controllerDeadLValue != nil {
		w.controllerDeadLValue.SetText(fmt.Sprintf("%.2f", float64(deadzoneSliderValue(settings.Deadzone))/100))
	}
	if w.controllerFaceLayout != nil {
		w.setComboIndex(w.controllerFaceLayout, controllerFaceButtonLayoutIndex(w.controllerFaceButtonLayout))
		w.controllerFaceLayout.SetEnabled(usable)
	}
	if w.controllerReset != nil {
		w.controllerReset.SetEnabled(usable)
	}
	w.updateControllerStateNote()
}

// controllerChangeFromWidgets reports which controls differ from the last
// state this card knows to be on disk.
func (w *mainWindow) controllerChangeFromWidgets() controllerChange {
	var change controllerChange
	if w.controllerEnable != nil {
		change.enabled = w.controllerEnable.IsChecked() != w.controllerSettings.Enabled
	}
	if w.controllerDeadL != nil {
		change.deadzone = w.controllerDeadL.Value() != deadzoneSliderValue(w.controllerSettings.Deadzone)
	}
	if w.controllerFaceLayout != nil {
		change.layout = controllerFaceButtonLayoutIndex(w.readControllerFaceButtonLayout()) != controllerFaceButtonLayoutIndex(w.controllerFaceButtonLayout)
	}
	return change
}

func controllerFaceButtonLayoutIndex(layout gamepad.FaceButtonLayout) int {
	if gamepad.NormalizeFaceButtonLayout(layout) == gamepad.FaceButtonLayoutSwitch {
		return 1
	}
	return 0
}

func (w *mainWindow) readControllerFaceButtonLayout() gamepad.FaceButtonLayout {
	if w.controllerFaceLayout != nil && w.controllerFaceLayout.CurrentIndex() == 1 {
		return gamepad.FaceButtonLayoutSwitch
	}
	return gamepad.FaceButtonLayoutXbox
}

func deadzoneSliderValue(deadzone float64) int {
	v := int(deadzone*100 + 0.5)
	if v < 0 {
		return 0
	}
	if v > int(guimodel.MaxControllerDeadzone*100) {
		return int(guimodel.MaxControllerDeadzone * 100)
	}
	return v
}

func (w *mainWindow) readControllerWidgets() guimodel.ControllerSettings {
	settings := w.controllerSettings
	if w.controllerEnable != nil {
		settings.Enabled = w.controllerEnable.IsChecked()
	}
	if w.controllerDeadL != nil {
		settings.Deadzone = float64(w.controllerDeadL.Value()) / 100
	}
	return settings
}

// persistControllerSettings saves the control the user just changed to the
// canonical gamepad section and then shows what is on disk. Graphics settings
// above are never touched. If the save fails the card snaps back to the
// persisted state, so it never claims a choice that is not saved.
func (w *mainWindow) persistControllerSettings() {
	if w.controllerSyncing || w.controllerEnable == nil {
		return
	}
	change := w.controllerChangeFromWidgets()
	if !change.any() {
		return
	}
	saved, layout, err := saveControllerChange(w.readControllerWidgets(), w.readControllerFaceButtonLayout(), change)
	if err != nil {
		w.reloadControllerSettings()
		message := "Could not save controller settings: " + err.Error()
		setNotice(w.controllerHint, "noticeError", message)
		w.showStatus(message)
		return
	}
	w.controllerSettings = saved
	w.controllerFaceButtonLayout = layout
	w.bindControllerSettings(saved)
	setNotice(w.controllerHint, "noticeSuccess", "Controller settings saved. A running Roblox picks them up "+controllerAppliesLiveText+".")
}

func (w *mainWindow) updateControllerStateNote() {
	if w.controllerStateNote == nil || w.controllerEnable == nil {
		return
	}
	switch {
	case w.controllerLoadErr != nil:
		w.controllerStateNote.SetText(controllerUnreadableNote)
	case !w.controllerEnable.IsChecked():
		w.controllerStateNote.SetText("Controller input is off — the engine sees zero pads.")
	case !controllerEffectiveEnabled(w.readControllerWidgets()):
		w.controllerStateNote.SetText("Disabled by TIPSY_GAMEPAD=0|off — the engine sees zero pads regardless of this card.")
	default:
		w.controllerStateNote.SetText("Controller input is on. Pads stay quiet while the Roblox window is unfocused.")
	}
}

func (w *mainWindow) resetControllerSettings() {
	settings := guimodel.DefaultControllerSettings()
	if err := saveControllerSettingsWithFaceButtonLayout(settings, gamepad.FaceButtonLayoutXbox); err != nil {
		message := "Could not reset controller settings: " + err.Error()
		setNotice(w.controllerHint, "noticeError", message)
		w.showStatus(message)
		return
	}
	w.controllerSettings = settings
	w.controllerFaceButtonLayout = gamepad.FaceButtonLayoutXbox
	w.bindControllerSettings(settings)
	setNotice(w.controllerHint, "noticeSuccess", "Controller settings reset to defaults (on, Xbox face buttons, device-flat deadzone).")
}

func clearControllerRows(layout *qt.QVBoxLayout) {
	if layout == nil {
		return
	}
	for layout.Count() > 0 {
		item := layout.TakeAt(0)
		if item == nil {
			break
		}
		if wid := item.Widget(); wid != nil {
			wid.Delete()
		}
	}
}

// refreshControllerPads re-reads the diagnose pad enumeration without
// opening any pad for input.
func (w *mainWindow) refreshControllerPads() {
	w.refreshControllerPadsWithPolicy(true)
}

func (w *mainWindow) refreshControllerPadsAutomatically() {
	w.refreshControllerPadsWithPolicy(false)
}

func (w *mainWindow) refreshControllerPadsWithPolicy(explicit bool) {
	if w.controllerPadRows == nil {
		return
	}
	now := w.now()
	if !w.controllerLookup.allow(now, explicit) {
		w.metrics.optionalDeferred.Add(1)
		w.setLabelText(w.controllerPadNote, "Pad list is temporarily unavailable. Automatic retry is delayed; press Refresh to retry now.")
		return
	}
	w.metrics.optionalCalls.Add(1)
	clearControllerRows(w.controllerPadRows)
	state, err := w.service.ControllerPads(context.Background())
	if err != nil {
		w.controllerLookup.failed(now)
		w.setLabelText(w.controllerPadNote, "Pad list is unavailable: "+err.Error())
		return
	}
	w.controllerLookup.succeeded()
	for _, pad := range state.Pads {
		row, rowLayout := newVerticalCard("subtleCard")
		title := qt.NewQLabel3(controllerPadTitle(pad))
		setObjectName(title.QObject, "formLabel")
		title.SetWordWrap(true)
		rowLayout.AddWidget(title.QWidget)
		detail := qt.NewQLabel3(controllerPadDetail(pad))
		detail.SetWordWrap(true)
		detail.SetTextInteractionFlags(qt.TextSelectableByMouse)
		setObjectName(detail.QObject, "mutedText")
		rowLayout.AddWidget(detail.QWidget)
		w.controllerPadRows.AddWidget(row.QWidget)
	}
	var lines []string
	switch {
	case !state.Enabled:
		// Off means nothing opens /dev/input, so there is nothing honest to
		// list; never tell the user to plug a pad in while the switch is off.
		lines = append(lines, "Controller input is off, so no pads are listed and none are opened. Turn it on, then press Refresh.")
	case len(state.Pads) == 0 && len(state.Denied) > 0:
		lines = append(lines, fmt.Sprintf("No accessible gamepad: permission denied on %d node(s). %s. Zero pads is the honest state; no fake pad is shown.", len(state.Denied), state.PermissionHint))
	case len(state.Pads) == 0:
		lines = append(lines, "No gamepad found (zero devices is the honest state); the engine sees zero pads. Plug in a USB pad, then press Refresh.")
	default:
		lines = append(lines, fmt.Sprintf("%d gamepad(s) listed with content-free names and mapping choices.", len(state.Pads)))
	}
	if note := strings.TrimSpace(state.Note); note != "" {
		lines = append(lines, note)
	}
	w.setLabelText(w.controllerPadNote, strings.Join(lines, "\n"))
}

// stopControllerTest only resets button text/state; the card never opens a
// pad.
func (w *mainWindow) stopControllerTest(message string) {
	if w.controllerProbe != nil {
		w.controllerProbe.close()
		w.controllerProbe = nil
	}
	if w.controllerTimer != nil {
		w.controllerTimer.Stop()
	}
	if w.controllerTestButton != nil {
		w.controllerTestButton.SetText("Start test")
	}
	if w.controllerTestState != nil {
		w.controllerTestState.SetText(message)
	}
	w.resetControllerTestWidgets()
}

func (w *mainWindow) resetControllerTestWidgets() {
	for _, bar := range w.controllerBars {
		if bar != nil {
			bar.SetValue(0)
		}
	}
}
