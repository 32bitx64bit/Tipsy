// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

// Controller settings card (lean, simplified 2026-09-12). Widgets bind to
// the existing diagnose gamepad report and persist the lean gamepad section.
// No mapping, deadzone, rumble, or feed-in logic lives here.
//
// Deleted vs controller v1 (honest list, see
// gamepad-simplify-2026-09-12.md): per-stick deadzone sliders (one global
// slider), Y-invert checkboxes, the disabled rumble checkbox, and the local
// test-input readout (start/stop, stick bars, button lamps). The card shows
// the connected-pad enumeration and two controls: enable + deadzone.

import (
	"context"
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func (w *mainWindow) buildControllerCard() *qt.QFrame {
	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Controller").QWidget)

	intro := qt.NewQLabel3("Linux gamepads feed controller-enabled experiences. There are no per-game profiles.")
	intro.SetWordWrap(true)
	setObjectName(intro.QObject, "mutedText")
	cardLayout.AddWidget(intro.QWidget)

	w.controllerEnable = qt.NewQCheckBox3("Enable controller input")
	w.controllerEnable.SetAccessibleName("Enable controller input")
	w.controllerEnable.SetAccessibleDescription("Default on. TIPSY_GAMEPAD=0 or off disables pads regardless of this setting.")
	w.controllerEnable.SetToolTip("Default on. The TIPSY_GAMEPAD=0|off kill-switch disables pads regardless.")
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

	w.controllerDeadL, w.controllerDeadLValue = w.newDeadzoneRow("Stick deadzone")
	form.AddRow3("Deadzone", w.controllerDeadL.QWidget)
	form.AddRow3("", w.controllerDeadLValue.QWidget)
	cardLayout.AddLayout(form.QLayout)

	w.controllerHint = qt.NewQLabel3("Deadzone applies to both sticks (0.00–0.50, default 0.00 = device flat). Changes save immediately and never touch the graphics settings above.")
	w.controllerHint.SetWordWrap(true)
	setObjectName(w.controllerHint.QObject, "noticeInfo")
	cardLayout.AddWidget(w.controllerHint.QWidget)

	resetRow := qt.NewQHBoxLayout2()
	resetRow.AddStretch()
	reset := qt.NewQPushButton3("Reset controller defaults")
	setObjectName(reset.QObject, "secondaryButton")
	reset.SetAccessibleName("Reset controller defaults")
	reset.OnClicked(w.resetControllerSettings)
	resetRow.AddWidget(reset.QWidget)
	cardLayout.AddLayout(resetRow.QLayout)

	w.bindControllerSettings(w.controllerSettings)
	if w.controllerLoadErr != nil {
		w.controllerHint.SetText(w.controllerLoadErr.Error())
		setObjectName(w.controllerHint.QObject, "noticeWarning")
		refreshStyle(w.controllerHint.QWidget)
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
		// Persist on every change (not just slider release) so keyboard
		// adjustments save too. Each write is one small atomic file.
		w.persistControllerSettings()
	})
	return slider, value
}

func (w *mainWindow) bindControllerSettings(settings guimodel.ControllerSettings) {
	w.controllerSyncing = true
	defer func() { w.controllerSyncing = false }()
	if w.controllerEnable != nil {
		w.controllerEnable.SetChecked(settings.Enabled)
	}
	if w.controllerDeadL != nil {
		w.controllerDeadL.SetValue(deadzoneSliderValue(settings.Deadzone))
	}
	if w.controllerDeadLValue != nil {
		w.controllerDeadLValue.SetText(fmt.Sprintf("%.2f", settings.Deadzone))
	}
	w.updateControllerStateNote()
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

// persistControllerSettings saves the current widget state to the canonical
// gamepad section. Graphics settings above are never touched.
func (w *mainWindow) persistControllerSettings() {
	if w.controllerSyncing || w.controllerEnable == nil {
		return
	}
	settings := w.readControllerWidgets()
	if err := saveControllerSettings(settings); err != nil {
		w.controllerHint.SetText("Could not save controller settings: " + err.Error())
		setObjectName(w.controllerHint.QObject, "noticeError")
		refreshStyle(w.controllerHint.QWidget)
		return
	}
	w.controllerSettings = settings
	w.controllerHint.SetText("Controller settings saved.")
	setObjectName(w.controllerHint.QObject, "noticeSuccess")
	refreshStyle(w.controllerHint.QWidget)
	w.updateControllerStateNote()
}

func (w *mainWindow) updateControllerStateNote() {
	if w.controllerStateNote == nil || w.controllerEnable == nil {
		return
	}
	switch {
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
	if err := saveControllerSettings(settings); err != nil {
		w.controllerHint.SetText("Could not reset controller settings: " + err.Error())
		setObjectName(w.controllerHint.QObject, "noticeError")
		refreshStyle(w.controllerHint.QWidget)
		return
	}
	w.controllerSettings = settings
	w.bindControllerSettings(settings)
	w.controllerHint.SetText("Controller settings reset to defaults (on, device-flat deadzone).")
	setObjectName(w.controllerHint.QObject, "noticeSuccess")
	refreshStyle(w.controllerHint.QWidget)
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
	if w.controllerPadRows == nil {
		return
	}
	clearControllerRows(w.controllerPadRows)
	state, err := w.service.ControllerPads(context.Background())
	if err != nil {
		w.controllerPadNote.SetText("Pad list is unavailable: " + err.Error())
		return
	}
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
	w.controllerPadNote.SetText(strings.Join(lines, "\n"))
}

// stopControllerTest is kept for window-lifetime compatibility: the lean
// card never opens a pad, so this only resets button text/state when called
// while leaving the Settings page.
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
