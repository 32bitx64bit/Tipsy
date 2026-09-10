// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package desktop owns Tipsy's freedesktop launcher integration: the
// .desktop entries every packaging medium ships, and the rules for which
// installed copy of Tipsy (AppImage, Flatpak, distro package, source build)
// provides the launcher and the roblox:// URI handler on a given machine.
//
// One desktop-file ID is one identity. Stable builds share
// io.github.tipsy_linux.Tipsy; development builds use a separate
// io.github.tipsy_linux.Tipsy.Dev identity so a developer's daily install is
// never shadowed by a work-in-progress AppImage.
package desktop

import "github.com/tipsy-linux/tipsy/internal/version"

// Base is the AppStream component ID and the prefix of every desktop-file ID.
const Base = "io.github.tipsy_linux.Tipsy"

// Identity names one launcher family: its desktop-file IDs, display name,
// and theme icon.
type Identity struct {
	// ID is the desktop-file ID prefix (Base or Base + ".Dev").
	ID string `json:"id"`
	// Name is the human-readable product name in menus ("Tipsy", "Tipsy-Dev").
	Name string `json:"name"`
	// Channel is the release channel this identity belongs to.
	Channel string `json:"channel"`
}

// Stable is the identity official builds integrate under.
var Stable = Identity{ID: Base, Name: "Tipsy", Channel: version.ChannelStable}

// Development is the identity development builds integrate under.
var Development = Identity{ID: Base + ".Dev", Name: "Tipsy-Dev", Channel: version.ChannelDevelopment}

// Current is the identity of the running binary, decided by the release
// channel stamped at link time.
func Current() Identity {
	if version.Development() {
		return Development
	}
	return Stable
}

// ForChannel maps a channel name to its identity; anything but stable is dev.
func ForChannel(channel string) Identity {
	if channel == version.ChannelStable {
		return Stable
	}
	return Development
}

// Kind is one of the two launchers each identity ships.
type Kind string

const (
	// Play launches the official client (and handles roblox:// URIs).
	Play Kind = "Play"
	// Settings opens the setup and settings window.
	Settings Kind = "Settings"
)

// Kinds lists every launcher in the order they are written.
var Kinds = []Kind{Play, Settings}

// File is the desktop-file ID (also its basename) for one launcher.
func (id Identity) File(kind Kind) string {
	return id.ID + "." + string(kind) + ".desktop"
}

// IconName is the theme icon name user-scope entries install under. System
// packages keep the short "tipsy" theme icon from the shared templates.
func (id Identity) IconName() string {
	return id.ID
}

// Development reports whether this is the development identity.
func (id Identity) Development() bool {
	return id.Channel != version.ChannelStable
}

// Title is the menu name for one launcher ("Tipsy - Play").
func (id Identity) Title(kind Kind) string {
	return id.Name + " - " + string(kind)
}
