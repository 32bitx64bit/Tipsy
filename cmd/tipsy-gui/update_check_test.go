// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/updatecheck"
)

// fakeReleaseFetch is the test default installed by TestMain. Tests that need
// a result replace the per-window fetch; none reaches the release API.
func fakeReleaseFetch(context.Context) (updatecheck.Release, error) {
	return updatecheck.Release{}, errors.New("release fetch disabled in GUI tests")
}

// TestGUITestWindowsUseFakeReleaseFetch guards the TestMain seam: a new window
// must start with the fake default, not the production network fetch.
func TestGUITestWindowsUseFakeReleaseFetch(t *testing.T) {
	got := reflect.ValueOf(newUpdateCheckState().fetch).Pointer()
	if want := reflect.ValueOf(fakeReleaseFetch).Pointer(); got != want {
		t.Fatal("GUI test windows default to the production release fetch")
	}
}

func waitForUpdateCheck(t *testing.T, win *mainWindow) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		win.pollUpdateCheck()
		if !win.updateCheck.pending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("update check did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUpdateCheckStartsOnceAndReusesResult(t *testing.T) {
	win := &mainWindow{updateCheck: newUpdateCheckState(), now: time.Now}
	state := win.updateCheck
	var calls atomic.Int32
	release := make(chan struct{})
	state.local = "0.2.0"
	state.fetch = func(context.Context) (updatecheck.Release, error) {
		calls.Add(1)
		<-release
		return updatecheck.Release{TagName: "v0.3.0"}, nil
	}
	state.detect = func() updatecheck.Method { return updatecheck.Flatpak }

	win.startUpdateCheck()
	win.startUpdateCheck()
	if state.view != updateChecking {
		t.Fatalf("view during check = %v, want checking", state.view)
	}
	close(release)
	waitForUpdateCheck(t, win)

	if calls.Load() != 1 {
		t.Fatalf("fetch calls = %d, want 1", calls.Load())
	}
	if state.view != updateAvailable {
		t.Fatalf("view = %v, want update available", state.view)
	}
	if state.outcome.method != updatecheck.Flatpak || !strings.Contains(state.outcome.command, "flatpak update") {
		t.Fatalf("outcome = %+v, want flatpak instructions", state.outcome)
	}

	// A later Settings open reuses the finished result and never fetches again.
	win.startUpdateCheck()
	waitForUpdateCheck(t, win)
	if calls.Load() != 1 || state.view != updateAvailable {
		t.Fatalf("second open fetch calls = %d view = %v", calls.Load(), state.view)
	}
}

func TestUpdateCheckFailureIsQuiet(t *testing.T) {
	win := &mainWindow{updateCheck: newUpdateCheckState(), now: time.Now}
	state := win.updateCheck
	state.local = "0.2.0"
	state.fetch = func(context.Context) (updatecheck.Release, error) {
		return updatecheck.Release{}, errors.New("offline")
	}
	state.detect = func() updatecheck.Method { return updatecheck.Source }

	win.startUpdateCheck()
	waitForUpdateCheck(t, win)

	if state.view != updateFailed {
		t.Fatalf("view = %v, want failed", state.view)
	}
	presentation := updateCheckPresentation(state.view, state.outcome, state.local)
	if presentation.command != "" || presentation.link != "" {
		t.Fatalf("failure presentation = %+v, want no instructions", presentation)
	}
	if !strings.Contains(presentation.summary, "keeps working") {
		t.Fatalf("failure summary = %q, want quiet wording", presentation.summary)
	}
}

func TestUpdateCheckPresentationCoversEveryState(t *testing.T) {
	tests := []struct {
		name    string
		view    updateCheckView
		outcome updateCheckOutcome
		want    []string
	}{
		{name: "checking", view: updateChecking, want: []string{"Checking for updates"}},
		{name: "up to date", view: updateUpToDate, outcome: updateCheckOutcome{release: updatecheck.Release{TagName: "v0.2.0"}}, want: []string{"up to date", "v0.2.0"}},
		{
			name: "available flatpak",
			view: updateAvailable,
			outcome: updateCheckOutcome{
				release: updatecheck.Release{TagName: "v0.3.0"},
				method:  updatecheck.Flatpak,
				message: "A newer Tipsy release is available. Update it with Flatpak.",
				command: "flatpak update " + updatecheck.FlatpakID,
			},
			want: []string{"Update available", "v0.3.0", "Flatpak", "flatpak update"},
		},
		{
			name: "available appimage",
			view: updateAvailable,
			outcome: updateCheckOutcome{
				release: updatecheck.Release{TagName: "v0.3.0"},
				method:  updatecheck.AppImage,
				message: "A newer Tipsy release is available. Download the latest AppImage.",
				url:     "https://github.com/32bitx64bit/Tipsy/releases/download/v0.3.0/Tipsy-v0.3.0-x86_64.AppImage",
			},
			want: []string{"Download the latest AppImage", "x86_64.AppImage"},
		},
		{name: "dev build", view: updateDevBuild, want: []string{"Development build"}},
		{name: "failed", view: updateFailed, want: []string{"Could not check"}},
		{name: "not checked", view: updateNotChecked, want: []string{"not been checked"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presentation := updateCheckPresentation(test.view, test.outcome, "0.2.0")
			combined := presentation.summary + "\n" + presentation.detail + "\n" + presentation.command + "\n" + presentation.link
			for _, want := range test.want {
				if !strings.Contains(combined, want) {
					t.Fatalf("presentation = %q, want %q", combined, want)
				}
			}
		})
	}

	// A dev build is described quietly: it never claims a check was skipped.
	dev := updateCheckPresentation(updateDevBuild, updateCheckOutcome{}, "0.3.0-dev")
	if strings.Contains(strings.ToLower(dev.summary), "skip") {
		t.Fatalf("dev build summary = %q, want quiet wording", dev.summary)
	}

	// The AppImage link is clickable HTML and the package command stays plain.
	appImage := updateCheckPresentation(updateAvailable, updateCheckOutcome{
		release: updatecheck.Release{TagName: "v0.3.0"},
		method:  updatecheck.AppImage,
		message: "A newer Tipsy release is available. Download the latest AppImage.",
		url:     "https://github.com/32bitx64bit/Tipsy/releases/download/v0.3.0/Tipsy-v0.3.0-x86_64.AppImage",
	}, "0.2.0")
	if !strings.Contains(appImage.detail, `<a href="https://github.com/32bitx64bit/Tipsy/releases/download/v0.3.0/Tipsy-v0.3.0-x86_64.AppImage">`) {
		t.Fatalf("appimage detail = %q, want a link", appImage.detail)
	}
	// Release JSON from an arbitrary host must never render as a link.
	foreign := updateCheckPresentation(updateAvailable, updateCheckOutcome{
		release: updatecheck.Release{TagName: "v0.3.0"},
		method:  updatecheck.AppImage,
		message: "A newer Tipsy release is available. Download the latest AppImage.",
		url:     "https://attacker.invalid/Tipsy.AppImage",
	}, "0.2.0")
	if foreign.link != "" || strings.Contains(foreign.detail, "<a href=") {
		t.Fatalf("foreign release host produced a link: %+v", foreign)
	}
	flatpak := updateCheckPresentation(updateAvailable, updateCheckOutcome{
		release: updatecheck.Release{TagName: "v0.3.0"},
		method:  updatecheck.Flatpak,
		command: "flatpak update " + updatecheck.FlatpakID,
	}, "0.2.0")
	if flatpak.link != "" || flatpak.detail != "" {
		t.Fatalf("flatpak presentation = %+v, want command only", flatpak)
	}
}

func TestSafeUpdateLinkRejectsOtherSchemesAndHosts(t *testing.T) {
	for _, raw := range []string{
		"",
		"javascript:alert(1)",
		"file:///tmp/x",
		"http://github.com/32bitx64bit/Tipsy",
		"not a url",
		"https://example.invalid/Tipsy.AppImage",
		"https://raw.githubusercontent.com/32bitx64bit/Tipsy/main/README.md",
		"https://github.com.evil.example/32bitx64bit/Tipsy/releases",
		"https://evil.example/github.com/32bitx64bit/Tipsy/releases",
		"https://github.com@evil.example/Tipsy/releases",
		"https://user:pass@github.com/32bitx64bit/Tipsy/releases",
		"https://github.com:8443/32bitx64bit/Tipsy/releases",
	} {
		if got := safeUpdateLink(raw); got != "" {
			t.Fatalf("safeUpdateLink(%q) = %q, want empty", raw, got)
		}
	}
	for _, raw := range []string{
		"https://github.com/32bitx64bit/Tipsy/releases",
		"https://github.com/32bitx64bit/Tipsy/releases/download/v0.3.0/Tipsy-v0.3.0-x86_64.AppImage",
		"https://GitHub.com/32bitx64bit/Tipsy/releases",
	} {
		if got := safeUpdateLink(raw); got == "" {
			t.Fatalf("safeUpdateLink(%q) = empty, want a github.com link", raw)
		}
	}
}

func TestUpdateNoticeSettingsAdapterRoundTrip(t *testing.T) {
	backend := clientsettings.Default()
	if !backend.ShowUpdateNotice {
		t.Fatalf("backend default update notice = %+v, want enabled", backend)
	}
	gui := guiSettings(backend)
	if !gui.ShowUpdateNotice {
		t.Fatalf("backend to GUI update notice = %+v, want enabled", gui)
	}
	if got := backendSettings(gui); got != backend {
		t.Fatalf("GUI round trip=%+v want=%+v", got, backend)
	}
	if got := backendSettings(guimodel.DefaultSettings()); !got.ShowUpdateNotice {
		t.Fatalf("GUI default persisted update notice = %+v, want enabled", got)
	}
	backend.ShowUpdateNotice = false
	if got := backendSettings(guiSettings(backend)); got.ShowUpdateNotice {
		t.Fatalf("disabled update notice round trip = %+v", got)
	}
}

type updateNoticeSettingsService struct {
	visualService
	applied []guimodel.Settings
}

func (s *updateNoticeSettingsService) ApplySettings(_ context.Context, settings guimodel.Settings) (guimodel.ApplyResult, error) {
	s.applied = append(s.applied, settings)
	return guimodel.ApplyResult{}, nil
}

func TestSettingsUpdateNoticeCheckboxReflectsAndSaves(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	t.Setenv("TIPSY_GAMEPAD", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	app := qt.NewQApplication([]string{"tipsy-update-notice-test"})
	defer app.Delete()

	service := &updateNoticeSettingsService{}
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()

	if win.settingsUpdateNotice == nil {
		t.Fatal("Settings page has no update notice checkbox")
	}
	if !win.settingsUpdateNotice.IsChecked() || win.settingsUpdateNotice.Text() != updateNoticeToggleText(true) {
		t.Fatalf("update notice default checked=%v text=%q, want enabled", win.settingsUpdateNotice.IsChecked(), win.settingsUpdateNotice.Text())
	}
	win.settingsUpdateNotice.SetChecked(false)
	if win.settingsUpdateNotice.Text() != updateNoticeToggleText(false) || !win.settings.View().Dirty {
		t.Fatalf("update notice edit text=%q dirty=%v", win.settingsUpdateNotice.Text(), win.settings.View().Dirty)
	}
	win.applySettings()
	if len(service.applied) != 1 || service.applied[0].ShowUpdateNotice {
		t.Fatalf("applied settings = %+v, want update notice disabled", service.applied)
	}
}
