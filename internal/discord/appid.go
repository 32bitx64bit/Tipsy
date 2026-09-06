// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"os"
	"strings"
)

// ApplicationID is Tipsy's public Discord application id. It is not a secret.
// Create an application named "Tipsy" at https://discord.com/developers/applications,
// enable Rich Presence, and upload tipsy.png as asset keys "tipsy" and "tipsy_large".
// Override at runtime with TIPSY_DISCORD_APPLICATION_ID.
const ApplicationID = "1546068043614916629"

const (
	assetTipsy      = "tipsy"
	assetTipsyLarge = "tipsy_large"
	envApplication  = "TIPSY_DISCORD_APPLICATION_ID"
)

// ResolvedApplicationID returns the runtime Discord application id.
func ResolvedApplicationID() string {
	if v := strings.TrimSpace(os.Getenv(envApplication)); v != "" {
		return v
	}
	return strings.TrimSpace(ApplicationID)
}
