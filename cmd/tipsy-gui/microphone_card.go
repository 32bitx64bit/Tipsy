// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

const microphoneConsentLabel = "Allow Roblox to use your microphone"

func microphoneToggleText(enabled bool) string {
	if enabled {
		return "✓ Microphone access allowed"
	}
	return "Microphone access disabled"
}

// microphoneUnreadableToggleText labels the toggle while the settings file
// cannot be read: the box shows the default, not a saved choice, so it must
// not claim "allowed" or "disabled".
const microphoneUnreadableToggleText = "Microphone access — settings file unreadable (default shown)"

const microphoneUnreadableNote = "The settings file could not be read, so the toggle above shows the default and is locked. Fix or remove the file, then return to this page."

// microphoneHintDefault is the card's resting hint.
const microphoneHintDefault = "Enabling this toggle allows Roblox to use the host microphone; switching it off stops capture right away, even in a running game. TIPSY_MICROPHONE=0 overrides this setting. Host mute is still pavucontrol or wpctl."

func (w *mainWindow) buildMicrophoneCard() *qt.QFrame {
	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Microphone").QWidget)

	intro := qt.NewQLabel3("Voice chat uses the host microphone. Enabling this setting allows Roblox to use it; capture still starts only inside an experience.")
	intro.SetWordWrap(true)
	setObjectName(intro.QObject, "mutedText")
	cardLayout.AddWidget(intro.QWidget)

	w.microphoneEnable = qt.NewQCheckBox3(microphoneToggleText(false))
	setObjectName(w.microphoneEnable.QObject, "inputToggle")
	w.microphoneEnable.SetAccessibleName(microphoneConsentLabel)
	w.microphoneEnable.SetAccessibleDescription("Microphone consent for Roblox. Default on. Switching off stops host capture right away, even in a running game. TIPSY_MICROPHONE=0 or off disables capture regardless of this setting.")
	w.microphoneEnable.SetToolTip("Default on. Takes effect live. TIPSY_MICROPHONE=0|off disables capture regardless of this setting.")
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

	w.microphoneHint = qt.NewQLabel3(microphoneHintDefault)
	w.microphoneHint.SetWordWrap(true)
	setObjectName(w.microphoneHint.QObject, "noticeInfo")
	cardLayout.AddWidget(w.microphoneHint.QWidget)

	w.bindMicrophoneSettings(w.microphoneSettings)
	if w.microphoneLoadErr != nil {
		setNotice(w.microphoneHint, "noticeWarning", w.microphoneLoadErr.Error())
	}
	return card
}

func (w *mainWindow) bindMicrophoneSettings(settings guimodel.MicrophoneSettings) {
	w.microphoneSyncing = true
	defer func() { w.microphoneSyncing = false }()
	if w.microphoneEnable != nil {
		// While the settings file cannot be read the card shows a default it
		// cannot save, so the toggle is locked rather than left to claim a choice.
		usable := w.microphoneLoadErr == nil
		w.microphoneEnable.SetChecked(settings.Enabled)
		if usable {
			w.microphoneEnable.SetText(microphoneToggleText(settings.Enabled))
		} else {
			w.microphoneEnable.SetText(microphoneUnreadableToggleText)
		}
		w.microphoneEnable.SetEnabled(usable)
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
// microphone section. Graphics settings above are never touched. If the save
// fails the toggle snaps back to the persisted state, so it never claims a
// choice that is not saved.
func (w *mainWindow) persistMicrophoneSettings() {
	if w.microphoneSyncing || w.microphoneEnable == nil {
		return
	}
	settings := w.readMicrophoneWidgets()
	if err := saveMicrophoneSettings(settings); err != nil {
		w.reloadMicrophoneSettings()
		message := "Could not save microphone settings: " + err.Error()
		setNotice(w.microphoneHint, "noticeError", message)
		w.showStatus(message)
		return
	}
	w.microphoneSettings = settings
	w.microphoneEnable.SetText(microphoneToggleText(settings.Enabled))
	setNotice(w.microphoneHint, "noticeSuccess", "Microphone settings saved.")
	w.updateMicrophoneStateNote()
	w.refreshMicrophoneStatus()
}

func (w *mainWindow) updateMicrophoneStateNote() {
	if w.microphoneStateNote == nil || w.microphoneEnable == nil {
		return
	}
	settings := w.readMicrophoneWidgets()
	effective := microphoneEffectiveEnabled(settings)
	switch {
	case w.microphoneLoadErr != nil:
		w.microphoneStateNote.SetText(microphoneUnreadableNote)
	case !settings.Enabled && effective:
		w.microphoneStateNote.SetText("Forced on by TIPSY_MICROPHONE=1|on — Roblox may use the host microphone even though this toggle is off.")
	case !settings.Enabled:
		w.microphoneStateNote.SetText("Microphone access is off — Roblox will not use the host microphone.")
	case !effective:
		w.microphoneStateNote.SetText("Disabled by TIPSY_MICROPHONE=0|off or TIPSY_DISABLE_MICROPHONE=1 — capture stays closed regardless of this toggle.")
	default:
		w.microphoneStateNote.SetText("Microphone access is allowed. Roblox still needs an in-experience unmute to start capture.")
	}
}

// refreshMicrophoneStatus re-reads the diagnose audio microphone door
// without opening capture and without listing Pulse source names.
func (w *mainWindow) refreshMicrophoneStatus() {
	w.refreshMicrophoneStatusWithPolicy(true)
}

func (w *mainWindow) refreshMicrophoneStatusAutomatically() {
	w.refreshMicrophoneStatusWithPolicy(false)
}

func (w *mainWindow) refreshMicrophoneStatusWithPolicy(explicit bool) {
	if w.microphoneStatusNote == nil {
		return
	}
	now := w.now()
	if !w.microphoneLookup.allow(now, explicit) {
		w.metrics.optionalDeferred.Add(1)
		w.setLabelText(w.microphoneStatusNote, "Microphone status is temporarily unavailable. Automatic retry is delayed.")
		return
	}
	w.metrics.optionalCalls.Add(1)
	state, err := w.service.MicrophoneStatus(context.Background())
	if err != nil {
		w.microphoneLookup.failed(now)
		w.setLabelText(w.microphoneStatusNote, "Microphone status is unavailable: "+err.Error())
		return
	}
	w.microphoneLookup.succeeded()
	w.setLabelText(w.microphoneStatusNote, microphoneStatusText(state))
}
