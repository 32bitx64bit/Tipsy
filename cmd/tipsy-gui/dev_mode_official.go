// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package main

import (
	"fmt"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func isDevelopmentConsentError(error) bool {
	return false
}

func allowGUIDevelopmentApproval(approveDevelopment bool) error {
	if approveDevelopment {
		return fmt.Errorf("development launch authorization is disabled in official release builds")
	}
	return nil
}

func validatePreparedGUIAuthority(state guimodel.LaunchAuthority) error {
	if state.DevelopmentConsentRequired {
		return fmt.Errorf("authorized GUI launch session returned incomplete authority")
	}
	if state.Mode != string(setupsvc.OfficialVerified) {
		return fmt.Errorf("authorized GUI launch session requires OfficialVerified authority")
	}
	if state.Warning != "" {
		return fmt.Errorf("official GUI launch returned an unexpected warning")
	}
	return nil
}

func (w *mainWindow) handleDevelopmentConsentRequired() {
	w.launchPreparing = false
	w.launchRequest = guimodel.LaunchRequest{}
	w.showLaunchFailure(safeLaunchFailure)
}

func (w *mainWindow) updateExternalProgressAuthority(guimodel.LaunchAuthority) {}

func (w *mainWindow) setLaunchAuthority(authority guimodel.LaunchAuthority) {
	if w.playAuthority == nil {
		return
	}
	switch {
	case authority.Mode == string(setupsvc.OfficialVerified):
		w.playAuthority.SetText("OfficialVerified — release authority and the active Roblox generation are authenticated.")
		setObjectName(w.playAuthority.QObject, "noticeSuccess")
		w.playAuthority.SetVisible(false)
	case authority.DevelopmentConsentRequired || authority.Mode != "":
		w.playAuthority.SetText("Rejected — launch requires OfficialVerified release authority.")
		setObjectName(w.playAuthority.QObject, "noticeError")
		w.playAuthority.SetVisible(true)
	default:
		w.playAuthority.SetText("Launch authority will be verified before Roblox starts.")
		setObjectName(w.playAuthority.QObject, "noticeInfo")
		w.playAuthority.SetVisible(false)
	}
	refreshStyle(w.playAuthority.QWidget)
}
