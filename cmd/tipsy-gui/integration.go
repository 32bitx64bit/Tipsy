// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/desktop"
	"github.com/tipsy-linux/tipsy/internal/version"
)

// integrationView is what the Settings page shows about the desktop
// launcher: who provides this build's identity and the one action the
// running install can take. All decisions come from internal/desktop; this
// only turns them into words.
type integrationView struct {
	summary string
	detail  string
	// button is the action label, or "" when there is nothing to do.
	button string
	action desktop.Action
	// notice is the QLabel object name (noticeInfo/noticeSuccess/noticeWarning).
	notice string
}

func describeIntegration(status desktop.Status, medium desktop.Medium, origin string) integrationView {
	id := status.Identity
	self := describeSelf(medium, origin)
	view := integrationView{action: desktop.Plan(status, medium, origin), notice: "noticeInfo"}
	owner := status.Owner
	switch view.action {
	case desktop.ActionNone:
		view.notice = "noticeSuccess"
		view.summary = fmt.Sprintf("%s launcher: this %s.", id.Name, self)
		if id.Development() {
			view.detail = "Development builds appear in menus as Tipsy-Dev and leave the roblox:// handler to the stable Tipsy install."
		} else if status.HandledByIdentity {
			view.detail = "Menu entries and roblox:// links open this installation."
		} else {
			view.detail = "Menu entries open this installation. roblox:// links are handled by " + handlerName(status) + "."
		}
	case desktop.ActionRelease:
		view.notice = "noticeWarning"
		view.summary = fmt.Sprintf("%s launcher: %s, not this %s.", id.Name, providerPhrase(owner), self)
		view.detail = "A user-level launcher entry is shadowing this installation in menus and for roblox:// links. Removing it hands both back to this installation."
		view.button = fmt.Sprintf("Use this %s", shortSelf(medium))
	case desktop.ActionAdopt:
		if owner == nil {
			view.summary = fmt.Sprintf("%s launcher: not in your application menu yet.", id.Name)
			view.detail = fmt.Sprintf("Add menu entries for this %s", self)
			if !id.Development() {
				view.detail += " and make it the roblox:// handler"
			}
			view.detail += "."
			view.button = "Add to menu"
		} else {
			view.notice = "noticeWarning"
			view.summary = fmt.Sprintf("%s launcher: %s.", id.Name, providerPhrase(owner))
			view.detail = fmt.Sprintf("This %s is not the one your menu opens. Taking over writes user-level entries that point here", self)
			if owner.Scope == desktop.ScopeSystem {
				view.detail += " and shadow the installed copy until you remove them (tipsy desktop release)"
			}
			view.detail += "."
			view.button = fmt.Sprintf("Use this %s instead", shortSelf(medium))
		}
	}
	return view
}

func describeSelf(medium desktop.Medium, origin string) string {
	switch medium {
	case desktop.MediumAppImage:
		return "AppImage (" + filepath.Base(origin) + ")"
	case desktop.MediumFlatpak:
		return "Flatpak"
	case desktop.MediumSystem:
		return "system package"
	case desktop.MediumSource:
		return "source build (" + origin + ")"
	default:
		return "installation"
	}
}

func shortSelf(medium desktop.Medium) string {
	switch medium {
	case desktop.MediumAppImage:
		return "AppImage"
	case desktop.MediumFlatpak:
		return "Flatpak"
	case desktop.MediumSystem:
		return "package"
	case desktop.MediumSource:
		return "build"
	default:
		return "installation"
	}
}

func providerPhrase(p *desktop.Provider) string {
	if p == nil {
		return "nothing provides it"
	}
	switch p.Medium {
	case desktop.MediumAppImage:
		return "provided by the AppImage " + filepath.Base(p.Origin)
	case desktop.MediumFlatpak:
		return "provided by the Flatpak"
	case desktop.MediumSource:
		return "provided by a source build (" + p.Origin + ")"
	default:
		if p.Scope == desktop.ScopeUser {
			return "provided by a user-level install"
		}
		return "provided by the system package"
	}
}

func handlerName(status desktop.Status) string {
	switch status.Handler {
	case "":
		return "no default application"
	case desktop.Stable.File(desktop.Play):
		return "Tipsy"
	case desktop.Development.File(desktop.Play):
		return "Tipsy-Dev"
	default:
		return status.Handler
	}
}

// buildIntegrationCard renders the launcher-ownership row on Settings.
func (w *mainWindow) buildIntegrationCard() *qt.QFrame {
	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Desktop integration").QWidget)

	w.integrationStatus = qt.NewQLabel3("")
	w.integrationStatus.SetWordWrap(true)
	w.integrationStatus.SetAccessibleName("Desktop launcher status")
	cardLayout.AddWidget(w.integrationStatus.QWidget)

	w.integrationDetail = qt.NewQLabel3("")
	w.integrationDetail.SetWordWrap(true)
	setObjectName(w.integrationDetail.QObject, "mutedText")
	cardLayout.AddWidget(w.integrationDetail.QWidget)

	actions := qt.NewQHBoxLayout2()
	build := qt.NewQLabel3(fmt.Sprintf("This build: %s %s (%s channel)", desktop.Current().Name, version.String(), version.Channel))
	setObjectName(build.QObject, "mutedText")
	actions.AddWidget(build.QWidget)
	actions.AddStretch()
	w.integrationAction = qt.NewQPushButton3("")
	setObjectName(w.integrationAction.QObject, "secondaryButton")
	w.integrationAction.SetAccessibleName("Desktop launcher action")
	w.integrationAction.OnClicked(w.applyIntegrationAction)
	actions.AddWidget(w.integrationAction.QWidget)
	cardLayout.AddLayout(actions.QLayout)

	w.refreshIntegration()
	return card
}

func (w *mainWindow) refreshIntegration() {
	env := desktop.EnvFromOS()
	status := desktop.Resolve(env, desktop.Current())
	medium, origin := desktop.CurrentMedium()
	view := describeIntegration(status, medium, origin)
	w.integrationView = view
	w.integrationStatus.SetText(view.summary)
	setObjectName(w.integrationStatus.QObject, view.notice)
	refreshStyle(w.integrationStatus.QWidget)
	w.integrationDetail.SetText(view.detail)
	w.integrationAction.SetText(view.button)
	w.integrationAction.SetVisible(view.button != "")
	w.integrationAction.SetEnabled(view.button != "")
}

func (w *mainWindow) applyIntegrationAction() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	env := desktop.EnvFromOS()
	id := desktop.Current()
	medium, origin := desktop.CurrentMedium()
	var err error
	switch w.integrationView.action {
	case desktop.ActionAdopt:
		var launcher desktop.Launcher
		launcher, err = desktop.LauncherFor(medium, origin)
		if err == nil {
			_, err = desktop.Adopt(ctx, env, desktop.AdoptOptions{
				Identity:   id,
				Launcher:   launcher,
				IconSource: desktop.IconSource(medium),
				Handler:    !id.Development(),
			})
		}
	case desktop.ActionRelease:
		_, err = desktop.Release(ctx, env, id, nil)
	default:
		return
	}
	if err != nil {
		w.integrationStatus.SetText("Desktop integration failed: " + err.Error())
		setObjectName(w.integrationStatus.QObject, "noticeError")
		refreshStyle(w.integrationStatus.QWidget)
		return
	}
	w.refreshIntegration()
}
