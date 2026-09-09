// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"os"

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
	// Place-id sources, in the order the official client produces them:
	//   1. launch URI seed (below);
	//   2. JNI StartGameParams.placeId for website/protocol joins (ignores 0);
	//   3. JNI NativeHelper.gameActivity_onGameLoaded(J)V — the engine's own
	//      per-DataModel announcement. This is the only source that fires for
	//      in-client Home → Play joins (no StartGameParams) and for leaving
	//      back to Home (0). Live sessions show the matching FLog
	//      `onGameLoaded: placeId:N` line is written to the Player log file
	//      only and never crosses liblog, so the liblog observer below is a
	//      secondary path, not the one that carries the transition.
	// The 250 ms Player-log file follower is not started. logsDir remains on
	// the signature so launch.go's call site is unchanged.
	jni.SetPlaceIDListener(hub.SetPlaceID)
	jni.SetGameLoadedListener(hub.SetLoadedPlaceID)
	android.SetLogTextObserver(func(text string) {
		id, ok := parseOnGameLoadedPlaceID([]byte(text))
		if ok {
			hub.SetLoadedPlaceID(id)
		}
	})
	hub.Seed(placeID)
	_ = logsDir
	if discord.ResolvedApplicationID() == "" {
		logging.Logger(logging.CatRuntime).Info("discord rich presence idle; set TIPSY_DISCORD_APPLICATION_ID after creating the Tipsy Discord application")
	}
	return hub
}

func stopDiscordPresence(hub *discord.Hub) {
	jni.SetPlaceIDListener(nil)
	jni.SetGameLoadedListener(nil)
	android.SetLogTextObserver(nil)
	if hub != nil {
		hub.Close()
	}
}
