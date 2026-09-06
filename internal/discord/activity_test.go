// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildActivityHomeOmitsJoinAndSmallBadge(t *testing.T) {
	t.Parallel()
	act := BuildActivity(Place{}, true, time.Unix(1, 0).UTC())
	if act.Details != "Home - Tipsy" {
		t.Fatalf("details=%q", act.Details)
	}
	if act.Assets == nil || act.Assets.LargeImage != assetTipsyLarge || act.Assets.SmallImage != "" {
		t.Fatalf("home assets=%+v", act.Assets)
	}
	if len(act.Buttons) != 0 {
		t.Fatalf("home join buttons=%+v", act.Buttons)
	}
	raw, err := json.Marshal(act)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "userId") || strings.Contains(string(raw), "ticket") || strings.Contains(string(raw), "gameinfo") {
		t.Fatalf("activity leaked secrets: %s", raw)
	}
}

func TestBuildActivityExperienceUsesWebToAppJoinURL(t *testing.T) {
	t.Parallel()
	act := BuildActivity(Place{
		ID:      1818,
		Name:    "Crossroads",
		IconURL: "https://tr.rbxcdn.com/icon.png",
	}, true, time.UnixMilli(1_700_000_000_000))
	if act.Details != "Crossroads - Tipsy" {
		t.Fatalf("details=%q", act.Details)
	}
	if act.Assets == nil || act.Assets.LargeImage != "https://tr.rbxcdn.com/icon.png" || act.Assets.SmallImage != assetTipsy {
		t.Fatalf("experience assets=%+v", act.Assets)
	}
	if act.Assets.SmallText != "Tipsy" {
		t.Fatalf("small text=%q", act.Assets.SmallText)
	}
	if len(act.Buttons) != 1 || act.Buttons[0].Label != "Join" || act.Buttons[0].URL != "https://www.roblox.com/games/start?placeId=1818" {
		t.Fatalf("buttons=%+v", act.Buttons)
	}
	if !act.Instance {
		t.Fatal("experience activity omitted instance")
	}
	if strings.Contains(act.Buttons[0].URL, "job") || strings.Contains(act.Buttons[0].URL, "accessCode") {
		t.Fatalf("join url is not a public place page: %s", act.Buttons[0].URL)
	}
	if act.Timestamps == nil || act.Timestamps.Start != 1_700_000_000_000 {
		t.Fatalf("timestamps=%+v", act.Timestamps)
	}
}

func TestPublicJoinURLRejectsNonPositivePlaceIDs(t *testing.T) {
	t.Parallel()
	if publicJoinURL(0) != "" || publicJoinURL(-1) != "" {
		t.Fatal("non-positive place ids produced a join URL")
	}
}

func TestBuildActivityRejectsNonHTTPSIconsAndMissingNames(t *testing.T) {
	t.Parallel()
	act := BuildActivity(Place{ID: 92, Name: "\x01", IconURL: "http://evil.example/icon.png"}, false, time.Time{})
	if act.Details != "Roblox - Tipsy" {
		t.Fatalf("fallback details=%q", act.Details)
	}
	if act.Assets == nil || act.Assets.LargeImage != assetTipsyLarge {
		t.Fatalf("fallback large image=%+v", act.Assets)
	}
	if len(act.Buttons) != 0 {
		t.Fatalf("join disabled still added buttons=%+v", act.Buttons)
	}
}

func TestSanitizeNameFitsDiscordDetails(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("N", 200)
	act := BuildActivity(Place{ID: 1, Name: long}, false, time.Time{})
	if got := []rune(act.Details); len(got) > detailsMaxRunes {
		t.Fatalf("details length %d", len(got))
	}
	if !strings.HasSuffix(act.Details, suffixTipsy) {
		t.Fatalf("details=%q", act.Details)
	}
}
