// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/updatecheck"
	"github.com/tipsy-linux/tipsy/internal/version"
)

// updateCheckView is the quiet presentation state of the Settings Updates
// card. The zero value is "not checked yet", which is what a window shows
// before its first Settings visit.
type updateCheckView int

const (
	updateNotChecked updateCheckView = iota
	updateChecking
	updateUpToDate
	updateAvailable
	updateDevBuild
	updateFailed
)

// updateCheckOutcome is one completed check. It is built entirely on the
// worker goroutine and read only on the GUI thread.
type updateCheckOutcome struct {
	release updatecheck.Release
	method  updatecheck.Method
	message string
	command string
	url     string
	err     error
}

// updateCheckState holds the one release check a window performs. started is
// set on the GUI thread before the goroutine starts, so later Settings visits
// reuse the result instead of checking again.
type updateCheckState struct {
	started bool
	pending bool
	result  chan updateCheckOutcome
	view    updateCheckView
	outcome updateCheckOutcome

	fetch  func(context.Context) (updatecheck.Release, error)
	detect func() updatecheck.Method
	local  string
}

// updateCheckFetch is the default release fetch every new window starts with.
// Tests replace it once in TestMain so no GUI test reaches the network;
// production keeps fetchLatestRelease.
var updateCheckFetch = fetchLatestRelease

func newUpdateCheckState() *updateCheckState {
	return &updateCheckState{
		fetch:  updateCheckFetch,
		detect: updatecheck.DetectInstall,
		local:  version.Version,
	}
}

// fetchLatestRelease is the only production network path for the update
// check. It runs on a worker goroutine, never on the Qt thread.
func fetchLatestRelease(ctx context.Context) (updatecheck.Release, error) {
	client := updatecheck.Client{
		BaseURL:   updatecheck.DefaultBaseURL,
		HTTP:      http.DefaultClient,
		Timeout:   10 * time.Second,
		UserAgent: "Tipsy-Linux",
	}
	return client.Latest(ctx)
}

// startUpdateCheck starts the single background release check. It never
// blocks: the worker publishes its outcome and queues an owner-thread wakeup,
// and pollUpdateCheck renders the result.
func (w *mainWindow) startUpdateCheck() {
	state := w.updateCheck
	if state == nil || state.started {
		return
	}
	state.started = true
	state.pending = true
	state.view = updateChecking
	w.renderUpdateCheck()

	result := make(chan updateCheckOutcome, 1)
	state.result = result
	fetch, detect, local := state.fetch, state.detect, state.local
	go func() {
		defer w.queueOwnerState()
		release, err := fetch(context.Background())
		outcome := updateCheckOutcome{release: release, err: err}
		switch {
		case err != nil:
			outcome = updateCheckOutcome{err: err}
		case updatecheck.Compare(local, release.TagName) == updatecheck.UpdateAvailable:
			outcome.method = detect()
			outcome.message, outcome.command, outcome.url = updatecheck.Instructions(outcome.method, release)
		}
		result <- outcome
	}()
}

// pollUpdateCheck consumes the worker outcome on the GUI thread.
func (w *mainWindow) pollUpdateCheck() {
	state := w.updateCheck
	if state == nil || !state.pending || state.result == nil {
		return
	}
	select {
	case outcome := <-state.result:
		state.pending = false
		state.result = nil
		state.outcome = outcome
		switch {
		case outcome.err != nil:
			state.view = updateFailed
		case updatecheck.Compare(state.local, outcome.release.TagName) == updatecheck.UpdateAvailable:
			state.view = updateAvailable
		case updatecheck.Compare(state.local, outcome.release.TagName) == updatecheck.DevBuild:
			state.view = updateDevBuild
		default:
			state.view = updateUpToDate
		}
		w.renderUpdateCheck()
	default:
	}
}

// updatePresentation is the plain-text/HTML rendering of one check state.
type updatePresentation struct {
	summary string
	detail  string
	command string
	link    string
	notice  string
}

func updateCheckPresentation(view updateCheckView, outcome updateCheckOutcome, localVersion string) updatePresentation {
	presentation := updatePresentation{notice: "noticeInfo"}
	switch view {
	case updateChecking:
		presentation.summary = "<b>Checking for updates…</b>"
	case updateUpToDate:
		presentation.summary = "Tipsy is up to date."
		presentation.notice = "noticeSuccess"
		if tag := strings.TrimSpace(outcome.release.TagName); tag != "" {
			presentation.detail = "Latest release: " + html.EscapeString(tag)
		}
	case updateAvailable:
		presentation.notice = "noticeWarning"
		latest := strings.TrimSpace(outcome.release.TagName)
		shown := latest
		if shown != "" {
			shown = "Tipsy " + html.EscapeString(shown)
			presentation.summary = "<b>Update available — " + shown + ".</b>"
		} else {
			presentation.summary = "<b>Update available.</b>"
		}
		if local := strings.TrimSpace(localVersion); local != "" {
			presentation.summary += " You have " + html.EscapeString(local) + " installed."
		}
		presentation.detail = html.EscapeString(outcome.message)
		presentation.command = outcome.command
		if safe := safeUpdateLink(outcome.url); safe != "" {
			presentation.link = safe
			text := "Open the latest Tipsy release"
			if outcome.method == updatecheck.AppImage {
				text = "Download the latest AppImage"
			}
			presentation.detail += fmt.Sprintf(`<br><a href="%s">%s</a>`, html.EscapeString(safe), text)
		}
	case updateDevBuild:
		presentation.summary = "Development build — no update is offered for this build."
	case updateFailed:
		presentation.summary = "Could not check for updates. Tipsy keeps working normally."
	default:
		presentation.summary = "Updates have not been checked yet."
	}
	return presentation
}

// safeUpdateLink accepts only absolute https URLs from GitHub metadata.
func safeUpdateLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

// buildUpdateCard builds the Settings Updates area. It holds the status, the
// install-method instructions and the release link; the text comes from
// renderUpdateCheck so a rebuild shows the already-known result.
func (w *mainWindow) buildUpdateCard() *qt.QFrame {
	card, layout := newVerticalCard("subtleCard")
	layout.AddWidget(sectionLabel("Updates").QWidget)

	w.updateSummary = qt.NewQLabel3("")
	w.updateSummary.SetTextFormat(qt.RichText)
	w.updateSummary.SetWordWrap(true)
	w.updateSummary.SetTextInteractionFlags(qt.TextBrowserInteraction)
	w.updateSummary.SetAccessibleName("Tipsy update status")
	w.updateSummary.SetAccessibleDescription("Shows whether a newer Tipsy release is available and how to install it.")
	layout.AddWidget(w.updateSummary.QWidget)

	w.updateDetail = qt.NewQLabel3("")
	w.updateDetail.SetTextFormat(qt.RichText)
	w.updateDetail.SetWordWrap(true)
	w.updateDetail.SetTextInteractionFlags(qt.TextBrowserInteraction)
	w.updateDetail.SetOpenExternalLinks(true)
	w.updateDetail.SetAccessibleName("Tipsy update instructions")
	layout.AddWidget(w.updateDetail.QWidget)

	w.updateCommand = qt.NewQLabel3("")
	w.updateCommand.SetTextFormat(qt.PlainText)
	w.updateCommand.SetWordWrap(true)
	w.updateCommand.SetTextInteractionFlags(qt.TextSelectableByMouse)
	w.updateCommand.SetAccessibleName("Tipsy update command")
	layout.AddWidget(w.updateCommand.QWidget)

	w.renderUpdateCheck()
	return card
}

func (w *mainWindow) renderUpdateCheck() {
	if w.updateSummary == nil {
		return
	}
	state := w.updateCheck
	view, outcome, local := updateNotChecked, updateCheckOutcome{}, ""
	if state != nil {
		view, outcome, local = state.view, state.outcome, state.local
	}
	presentation := updateCheckPresentation(view, outcome, local)
	w.updateSummary.SetText(presentation.summary)
	setObjectName(w.updateSummary.QObject, presentation.notice)
	refreshStyle(w.updateSummary.QWidget)
	if w.updateDetail != nil {
		w.updateDetail.SetText(presentation.detail)
		w.updateDetail.SetVisible(strings.TrimSpace(presentation.detail) != "")
	}
	if w.updateCommand != nil {
		w.updateCommand.SetText(presentation.command)
		w.updateCommand.SetVisible(strings.TrimSpace(presentation.command) != "")
	}
}
