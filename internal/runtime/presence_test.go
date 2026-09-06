// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/jni"
)

func TestDiscordPresenceStartAndStopDoNotFailLaunch(t *testing.T) {
	t.Setenv("TIPSY_DISCORD_APPLICATION_ID", "")
	t.Cleanup(func() { jni.SetPlaceIDListener(nil) })
	load := func(context.Context) (clientsettings.Settings, error) {
		return clientsettings.Default(), nil
	}
	hub := startDiscordPresence(context.Background(), load, 1818, t.TempDir())
	if hub == nil {
		t.Fatal("presence hub was nil")
	}
	stopDiscordPresence(hub)
}
