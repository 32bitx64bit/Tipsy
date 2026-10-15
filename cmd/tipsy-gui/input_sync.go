// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	qt "github.com/mappu/miqt/qt6"
)

// settingsPageIndex is the stacked-widget index of the Settings page.
const settingsPageIndex = 2

// setNotice shows text in one of the card hint labels with the named
// notice style (noticeInfo, noticeSuccess, noticeWarning, noticeError).
func setNotice(label *qt.QLabel, style, text string) {
	if label == nil {
		return
	}
	label.SetText(text)
	setObjectName(label.QObject, style)
	refreshStyle(label.QWidget)
}

// showStatus puts a short message in the always-visible status bar. The
// card hints sit at the bottom of a long scrolling page, so a failed save
// would otherwise be easy to miss.
func (w *mainWindow) showStatus(message string) {
	if w.status != nil {
		w.status.ShowMessage2(message, 7000)
	}
}

// reloadInputSettings re-reads the persisted Controller and Microphone
// sections and re-binds their widgets, so the cards show what is on disk now
// rather than what was on disk when the window was built. It does file reads
// only: no timers, no polling, nothing that runs while the window is hidden.
// It reports which cards changed what they show.
func (w *mainWindow) reloadInputSettings() (controllerChanged, microphoneChanged bool) {
	return w.reloadControllerSettings(), w.reloadMicrophoneSettings()
}

// reloadControllerSettings re-binds the enable toggle, deadzone slider and
// face-button layout from one config-file snapshot and reports whether the
// card now shows something different from before.
func (w *mainWindow) reloadControllerSettings() bool {
	if w.controllerEnable == nil {
		return false
	}
	settings, layout, err := loadControllerSettingsAndFaceButtonLayout()
	wasUnreadable := w.controllerLoadErr != nil
	differs := err == nil && (w.controllerEnable.IsChecked() != settings.Enabled ||
		w.controllerDeadL.Value() != deadzoneSliderValue(settings.Deadzone) ||
		controllerFaceButtonLayoutIndex(w.readControllerFaceButtonLayout()) != controllerFaceButtonLayoutIndex(layout))
	w.controllerLoadErr = err
	w.controllerSettings, w.controllerFaceButtonLayout = settings, layout
	w.bindControllerSettings(settings)
	switch {
	case err != nil:
		setNotice(w.controllerHint, "noticeWarning", err.Error())
	case wasUnreadable:
		setNotice(w.controllerHint, "noticeInfo", controllerHintDefault)
	case differs:
		setNotice(w.controllerHint, "noticeInfo", "Controller settings changed outside this window; showing the saved values.")
	}
	return differs || wasUnreadable != (err != nil)
}

// reloadMicrophoneSettings re-binds the microphone toggle from the persisted
// section and reports whether the card now shows something different.
func (w *mainWindow) reloadMicrophoneSettings() bool {
	if w.microphoneEnable == nil {
		return false
	}
	settings, err := loadMicrophoneSettings()
	wasUnreadable := w.microphoneLoadErr != nil
	differs := err == nil && w.microphoneEnable.IsChecked() != settings.Enabled
	w.microphoneLoadErr = err
	w.microphoneSettings = settings
	w.bindMicrophoneSettings(settings)
	switch {
	case err != nil:
		setNotice(w.microphoneHint, "noticeWarning", err.Error())
	case wasUnreadable:
		setNotice(w.microphoneHint, "noticeInfo", microphoneHintDefault)
	case differs:
		setNotice(w.microphoneHint, "noticeInfo", "Microphone settings changed outside this window; showing the saved value.")
	}
	return differs || wasUnreadable != (err != nil)
}

// reloadInputSettingsOnReturn refreshes the Controller and Microphone cards
// when the user comes back to a visible Settings page (the window was raised
// or re-shown): the settings file may have been edited elsewhere while the
// window sat untouched. Entering Settings goes through selectPage, which
// reloads too. A card whose state changed also re-reads its diagnose note.
func (w *mainWindow) reloadInputSettingsOnReturn() {
	if w.stack == nil || w.win == nil || !w.win.IsVisible() || w.stack.CurrentIndex() != settingsPageIndex {
		return
	}
	controllerChanged, microphoneChanged := w.reloadInputSettings()
	if controllerChanged && w.controllerPadRows != nil {
		w.refreshControllerPadsAutomatically()
	}
	if microphoneChanged && w.microphoneStatusNote != nil {
		w.refreshMicrophoneStatusAutomatically()
	}
}
