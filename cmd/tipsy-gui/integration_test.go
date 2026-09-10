// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/desktop"
)

func TestDescribeIntegrationOffersOneHonestAction(t *testing.T) {
	t.Parallel()
	appImage := "/home/g/Applications/Tipsy-1.2.0-x86_64.AppImage"
	userPin := desktop.Provider{Path: "/home/g/.local/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop", Scope: desktop.ScopeUser, Medium: desktop.MediumAppImage, Origin: appImage, Marked: true}
	flatpak := desktop.Provider{Path: "/var/lib/flatpak/exports/share/applications/io.github.tipsy_linux.Tipsy.Play.desktop", Scope: desktop.ScopeSystem, Medium: desktop.MediumFlatpak}
	stablePlay := desktop.Stable.File(desktop.Play)

	cases := []struct {
		name       string
		status     desktop.Status
		medium     desktop.Medium
		origin     string
		wantAction desktop.Action
		wantButton string
		wantText   string
	}{
		{
			name:       "flatpak shadowed by an old AppImage pin",
			status:     desktop.Status{Identity: desktop.Stable, Owner: &userPin, Providers: []desktop.Provider{userPin, flatpak}, Handler: stablePlay, HandledByIdentity: true},
			medium:     desktop.MediumFlatpak,
			wantAction: desktop.ActionRelease,
			wantButton: "Use this Flatpak",
			wantText:   "provided by the AppImage Tipsy-1.2.0-x86_64.AppImage, not this Flatpak",
		},
		{
			name:       "the AppImage that owns the pin",
			status:     desktop.Status{Identity: desktop.Stable, Owner: &userPin, Providers: []desktop.Provider{userPin}, Handler: stablePlay, HandledByIdentity: true},
			medium:     desktop.MediumAppImage,
			origin:     appImage,
			wantAction: desktop.ActionNone,
			wantText:   "this AppImage (Tipsy-1.2.0-x86_64.AppImage)",
		},
		{
			name:       "a newer AppImage next to an installed Flatpak",
			status:     desktop.Status{Identity: desktop.Stable, Owner: &flatpak, Providers: []desktop.Provider{flatpak}},
			medium:     desktop.MediumAppImage,
			origin:     "/home/g/Downloads/Tipsy-1.3.0-x86_64.AppImage",
			wantAction: desktop.ActionAdopt,
			wantButton: "Use this AppImage instead",
			wantText:   "shadow the installed copy",
		},
		{
			name:       "dev build not integrated yet",
			status:     desktop.Status{Identity: desktop.Development, Handler: stablePlay},
			medium:     desktop.MediumAppImage,
			origin:     "/home/g/Tipsy-0.0.0-dev-x86_64.AppImage",
			wantAction: desktop.ActionAdopt,
			wantButton: "Add to menu",
			wantText:   "Tipsy-Dev launcher: not in your application menu yet",
		},
		{
			name:       "flatpak with nothing in the way",
			status:     desktop.Status{Identity: desktop.Stable, Handler: stablePlay, HandledByIdentity: true},
			medium:     desktop.MediumFlatpak,
			wantAction: desktop.ActionNone,
			wantText:   "roblox:// links open this installation",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := describeIntegration(tc.status, tc.medium, tc.origin)
			if view.action != tc.wantAction {
				t.Fatalf("action = %s, want %s", view.action, tc.wantAction)
			}
			if view.button != tc.wantButton {
				t.Fatalf("button = %q, want %q", view.button, tc.wantButton)
			}
			if (view.button == "") != (view.action == desktop.ActionNone) {
				t.Fatalf("a button must exist exactly when there is an action: %+v", view)
			}
			if text := view.summary + " " + view.detail; !strings.Contains(text, tc.wantText) {
				t.Fatalf("text %q does not mention %q", text, tc.wantText)
			}
			if view.notice == "" || view.summary == "" {
				t.Fatalf("incomplete view: %+v", view)
			}
		})
	}
	// The dev-build message must never promise the roblox:// handler.
	dev := describeIntegration(desktop.Status{Identity: desktop.Development}, desktop.MediumAppImage, "/x/Tipsy-dev.AppImage")
	if strings.Contains(dev.detail, "roblox:// handler") {
		t.Fatalf("dev adopt must not offer the handler: %q", dev.detail)
	}
}
