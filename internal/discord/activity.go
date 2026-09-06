// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"net/url"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

const (
	detailsMaxRunes = 128
	suffixTipsy     = " - Tipsy"
	homeLabel       = "Home"
	fallbackName    = "Roblox"
	joinLabel       = "Join"
)

// Place is the public identity shown in Rich Presence. It must never carry
// tickets, cookies, user ids, job ids, or private-server codes.
type Place struct {
	ID      int64
	Name    string
	IconURL string
}

type activityPayload struct {
	Details    string      `json:"details,omitempty"`
	Assets     *assets     `json:"assets,omitempty"`
	Timestamps *timestamps `json:"timestamps,omitempty"`
	Buttons    []button    `json:"buttons,omitempty"`
	Instance   bool        `json:"instance,omitempty"`
}

type assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"`
	SmallText  string `json:"small_text,omitempty"`
}

type timestamps struct {
	Start int64 `json:"start,omitempty"`
}

type button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// BuildActivity constructs a SET_ACTIVITY payload. join adds a public Roblox
// place-page button only when a positive place id is present.
func BuildActivity(place Place, join bool, started time.Time) *activityPayload {
	name := displayName(place)
	act := &activityPayload{
		Details: clipDetails(name + suffixTipsy),
	}
	if !started.IsZero() {
		act.Timestamps = &timestamps{Start: started.UnixMilli()}
	}
	if place.ID > 0 {
		// Large image is the experience's public square icon (the display art).
		// Small image is the Tipsy corner badge. Missing icons fall back to
		// tipsy_large without dropping the corner badge.
		large := publicHTTPS(place.IconURL)
		if large == "" {
			large = assetTipsyLarge
		}
		act.Assets = &assets{
			LargeImage: large,
			LargeText:  clipDetails(name),
			SmallImage: assetTipsy,
			SmallText:  "Tipsy",
		}
		act.Instance = true
		if join {
			if page := rbxuri.PlacePageURL(place.ID); page != "" {
				act.Buttons = []button{{Label: joinLabel, URL: page}}
			}
		}
		return act
	}
	act.Assets = &assets{
		LargeImage: assetTipsyLarge,
		LargeText:  "Tipsy",
	}
	return act
}

func displayName(place Place) string {
	if place.ID <= 0 {
		return homeLabel
	}
	if name := sanitizeName(place.Name); name != "" {
		return name
	}
	return fallbackName
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	max := detailsMaxRunes - len([]rune(suffixTipsy))
	runes := []rune(s)
	if max < 1 {
		max = 1
	}
	if len(runes) > max {
		s = strings.TrimSpace(string(runes[:max]))
	}
	return s
}

func clipDetails(s string) string {
	runes := []rune(s)
	if len(runes) <= detailsMaxRunes {
		return s
	}
	return strings.TrimSpace(string(runes[:detailsMaxRunes]))
}

func publicHTTPS(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return u.String()
}
