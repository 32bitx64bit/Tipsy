// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package main

import (
	"errors"
	"fmt"

	qt "github.com/mappu/miqt/qt6"
	"github.com/tipsy-linux/tipsy/internal/app"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

var confirmDevelopmentLaunch = func(parent *qt.QWidget) bool {
	buttons := qt.QMessageBox__StandardButton(int(qt.QMessageBox__Yes) | int(qt.QMessageBox__No))
	dialog := qt.NewQMessageBox6(
		qt.QMessageBox__Warning,
		"Authorize development launch",
		"This local build cannot be authenticated as an official Tipsy release. Continue in DevelopmentUnrestricted mode?\n\nThis records your explicit choice in Tipsy's owner-private configuration. The official Roblox APK and active generation will still be verified, but this Tipsy session must not be represented as OfficialVerified.",
		buttons,
		parent,
	)
	dialog.SetDefaultButtonWithButton(qt.QMessageBox__No)
	dialog.SetEscapeButtonWithButton(qt.QMessageBox__No)
	result := dialog.Exec()
	dialog.Delete()
	return result == int(qt.QMessageBox__Yes)
}

func isDevelopmentConsentError(err error) bool {
	return errors.Is(err, app.ErrDevelopmentConsentRequired)
}

func allowGUIDevelopmentApproval(bool) error {
	return nil
}

func validatePreparedGUIAuthority(state guimodel.LaunchAuthority) error {
	if state.DevelopmentConsentRequired {
		return fmt.Errorf("authorized GUI launch session returned incomplete authority")
	}
	switch state.Mode {
	case string(setupsvc.DevelopmentUnrestricted):
		if state.Warning != app.DevelopmentUnrestrictedWarning {
			return fmt.Errorf("development GUI launch omitted its required warning")
		}
		return nil
	case string(setupsvc.OfficialVerified):
		if state.Warning != "" {
			return fmt.Errorf("official GUI launch returned an unexpected warning")
		}
		return nil
	default:
		return fmt.Errorf("authorized GUI launch session returned an unknown authority mode")
	}
}

func (w *mainWindow) handleDevelopmentConsentRequired() {
	if w.launchConsentRequested {
		w.launchPreparing = false
		w.launchRequest = guimodel.LaunchRequest{}
		w.showLaunchFailure(safeLaunchFailure)
		return
	}
	w.launchConsentRequested = true
	parent := w.win.QWidget
	if w.launchOrigin == launchFromExternal {
		parent = w.externalProgress.dialog.QWidget
		w.externalProgress.detail.SetText("Your approval is required to continue.")
	}
	if !confirmDevelopmentLaunch(parent) {
		w.launchPreparing = false
		w.launchRequest = guimodel.LaunchRequest{}
		w.showLaunchFailure("Launch was not authorized. Open Tipsy when you are ready to review the development-build approval.")
		return
	}
	w.prepareLaunch(true)
}

func (w *mainWindow) updateExternalProgressAuthority(authority guimodel.LaunchAuthority) {
	if authority.Mode == string(setupsvc.DevelopmentUnrestricted) {
		w.externalProgress.authority.SetText("DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session.")
		w.externalProgress.authority.Show()
	}
}

func (w *mainWindow) setLaunchAuthority(authority guimodel.LaunchAuthority) {
	if w.playAuthority == nil {
		return
	}
	switch {
	case authority.DevelopmentConsentRequired:
		w.playAuthority.SetText("Approval required — local builds cannot launch as OfficialVerified.")
		setObjectName(w.playAuthority.QObject, "noticeWarning")
	case authority.Mode == string(setupsvc.DevelopmentUnrestricted):
		warning := authority.Warning
		if warning == "" {
			warning = "DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session."
		}
		w.playAuthority.SetText(warning)
		setObjectName(w.playAuthority.QObject, "noticeWarning")
	case authority.Mode == string(setupsvc.OfficialVerified):
		w.playAuthority.SetText("OfficialVerified — release authority and the active Roblox generation are authenticated.")
		setObjectName(w.playAuthority.QObject, "noticeSuccess")
	default:
		w.playAuthority.SetText("Launch authority will be verified before Roblox starts.")
		setObjectName(w.playAuthority.QObject, "noticeInfo")
	}
	w.playAuthority.SetVisible(authority.DevelopmentConsentRequired || authority.Mode == string(setupsvc.DevelopmentUnrestricted))
	refreshStyle(w.playAuthority.QWidget)
}
