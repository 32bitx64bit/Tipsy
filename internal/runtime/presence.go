// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"os"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/discord"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

func startDiscordPresence(ctx context.Context, load func(context.Context) (clientsettings.Settings, error), placeID int64, logsDir string) *discord.Hub {
	hub := discord.Start(ctx, discord.Options{
		ApplicationID: discord.ResolvedApplicationID(),
		PID:           os.Getpid(),
		Load: func(ctx context.Context) (discord.Settings, error) {
			if load == nil {
				return discord.Settings{Enabled: true}, nil
			}
			s, err := load(ctx)
			if err != nil {
				return discord.Settings{}, err
			}
			return discord.Settings{Enabled: s.DiscordRichPresence, JoinButton: s.DiscordJoinButton}, nil
		},
	})
	jni.SetPlaceIDListener(hub.SetPlaceID)
	android.SetLogTextObserver(func(text string) {
		id, ok := parseOnGameLoadedPlaceID([]byte(text))
		if ok {
			hub.SetLoadedPlaceID(id)
		}
	})
	hub.Seed(placeID)
	if logsDir == "" {
		logsDir = filepath.Join(AppStorage().FilesDir, "appData", "logs")
	}
	go watchPlayerLogs(ctx, logsDir, 0, hub.SetLoadedPlaceID)
	if discord.ResolvedApplicationID() == "" {
		logging.Logger(logging.CatRuntime).Info("discord rich presence idle; set TIPSY_DISCORD_APPLICATION_ID after creating the Tipsy Discord application")
	}
	return hub
}

func stopDiscordPresence(hub *discord.Hub) {
	jni.SetPlaceIDListener(nil)
	android.SetLogTextObserver(nil)
	if hub != nil {
		hub.Close()
	}
}
