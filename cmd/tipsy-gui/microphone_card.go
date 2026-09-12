// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

// Microphone settings card. Widgets bind to the existing diagnose audio
// report and persist the lean microphone section. No Pulse, PCM, or JNI
// logic lives here. This toggle is the RECORD_AUDIO consent surface.

import (
	"context"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

const microphoneConsentLabel = "Allow Roblox to use your microphone"

func (w *mainWindow) buildMicrophoneCard() *qt.QFrame {
	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Microphone").QWidget)

	intro := qt.NewQLabel3("Voice chat uses the host microphone. Enabling this setting allows Roblox to use it; capture still starts only inside an experience.")
	intro.SetWordWrap(true)
	setObjectName(intro.QObject, "mutedText")
	cardLayout.AddWidget(intro.QWidget)

	w.microphoneEnable = qt.NewQCheckBox3(microphoneConsentLabel)
	w.microphoneEnable.SetAccessibleName(microphoneConsentLabel)
	w.microphoneEnable.SetAccessibleDescription("Microphone consent for Roblox. Default on. TIPSY_MICROPHONE=0 or off disables capture regardless of this setting.")
	w.microphoneEnable.SetToolTip("Default on. TIPSY_MICROPHONE=0|off disables capture regardless of this setting.")
	w.microphoneEnable.OnToggled(func(bool) { w.persistMicrophoneSettings() })
	cardLayout.AddWidget(w.microphoneEnable.QWidget)

	w.microphoneStateNote = qt.NewQLabel3("")
	w.microphoneStateNote.SetWordWrap(true)
	w.microphoneStateNote.SetAccessibleName("Microphone effective state")
	setObjectName(w.microphoneStateNote.QObject, "mutedText")
	cardLayout.AddWidget(w.microphoneStateNote.QWidget)

	w.microphoneStatusNote = qt.NewQLabel3("Open Settings to refresh microphone status from diagnose audio.")
	w.microphoneStatusNote.SetWordWrap(true)
	w.microphoneStatusNote.SetAccessibleName("Microphone diagnose status")
	w.microphoneStatusNote.SetTextInteractionFlags(qt.TextSelectableByMouse)
	setObjectName(w.microphoneStatusNote.QObject, "mutedText")
	cardLayout.AddWidget(w.microphoneStatusNote.QWidget)

	w.microphoneHint = qt.NewQLabel3("Enabling this toggle allows Roblox to use the host microphone. TIPSY_MICROPHONE=0 overrides this setting. Host mute is still pavucontrol or wpctl.")
	w.microphoneHint.SetWordWrap(true)
	setObjectName(w.microphoneHint.QObject, "noticeInfo")
	cardLayout.AddWidget(w.microphoneHint.QWidget)

	w.bindMicrophoneSettings(w.microphoneSettings)
	if w.microphoneLoadErr != nil {
		w.microphoneHint.SetText(w.microphoneLoadErr.Error())
		setObjectName(w.microphoneHint.QObject, "noticeWarning")
		refreshStyle(w.microphoneHint.QWidget)
	}
	return card
}

func (w *mainWindow) bindMicrophoneSettings(settings guimodel.MicrophoneSettings) {
	w.microphoneSyncing = true
	defer func() { w.microphoneSyncing = false }()
	if w.microphoneEnable != nil {
		w.microphoneEnable.SetChecked(settings.Enabled)
	}
	w.updateMicrophoneStateNote()
}

func (w *mainWindow) readMicrophoneWidgets() guimodel.MicrophoneSettings {
	settings := w.microphoneSettings
	if w.microphoneEnable != nil {
		settings.Enabled = w.microphoneEnable.IsChecked()
	}
	return settings
}

// persistMicrophoneSettings saves the current widget state to the canonical
// microphone section. Graphics settings above are never touched.
func (w *mainWindow) persistMicrophoneSettings() {
	if w.microphoneSyncing || w.microphoneEnable == nil {
		return
	}
	settings := w.readMicrophoneWidgets()
	if err := saveMicrophoneSettings(settings); err != nil {
		w.microphoneHint.SetText("Could not save microphone settings: " + err.Error())
		setObjectName(w.microphoneHint.QObject, "noticeError")
		refreshStyle(w.microphoneHint.QWidget)
		return
	}
	w.microphoneSettings = settings
	w.microphoneHint.SetText("Microphone settings saved.")
	setObjectName(w.microphoneHint.QObject, "noticeSuccess")
	refreshStyle(w.microphoneHint.QWidget)
	w.updateMicrophoneStateNote()
	w.refreshMicrophoneStatus()
}

func (w *mainWindow) updateMicrophoneStateNote() {
	if w.microphoneStateNote == nil || w.microphoneEnable == nil {
		return
	}
	switch {
	case !w.microphoneEnable.IsChecked():
		w.microphoneStateNote.SetText("Microphone access is off — Roblox will not use the host microphone.")
	case !microphoneEffectiveEnabled(w.readMicrophoneWidgets()):
		w.microphoneStateNote.SetText("Disabled by TIPSY_MICROPHONE=0|off — capture stays closed regardless of this toggle.")
	default:
		w.microphoneStateNote.SetText("Microphone access is allowed. Roblox still needs an in-experience unmute to start capture.")
	}
}

// refreshMicrophoneStatus re-reads the diagnose audio microphone door
// without opening capture and without listing Pulse source names.
func (w *mainWindow) refreshMicrophoneStatus() {
	if w.microphoneStatusNote == nil {
		return
	}
	state, err := w.service.MicrophoneStatus(context.Background())
	if err != nil {
		w.microphoneStatusNote.SetText("Microphone status is unavailable: " + err.Error())
		return
	}
	w.microphoneStatusNote.SetText(microphoneStatusText(state))
}
