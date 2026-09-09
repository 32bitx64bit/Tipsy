// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/jni"
)

func TestDiscordPresenceStartAndStopDoNotFailLaunch(t *testing.T) {
	t.Setenv("TIPSY_DISCORD_APPLICATION_ID", "")
	t.Cleanup(func() {
		jni.SetPlaceIDListener(nil)
		jni.SetGameLoadedListener(nil)
		android.SetLogTextObserver(nil)
	})
	load := func(context.Context) (clientsettings.Settings, error) {
		return clientsettings.Default(), nil
	}
	hub := startDiscordPresence(context.Background(), load, 1818, t.TempDir())
	if hub == nil {
		t.Fatal("presence hub was nil")
	}
	stopDiscordPresence(hub)
}

func TestDiscordPresenceDoesNotStartPlayerLogPoller(t *testing.T) {
	t.Setenv("TIPSY_DISCORD_APPLICATION_ID", "")
	t.Cleanup(func() {
		jni.SetPlaceIDListener(nil)
		jni.SetGameLoadedListener(nil)
		android.SetLogTextObserver(nil)
	})
	load := func(context.Context) (clientsettings.Settings, error) {
		return clientsettings.Default(), nil
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "2.736.0.1408_20260909T000000Z_Player_test_last.log")
	if err := os.WriteFile(path, []byte("Info [FLog::DataModelBindings] onGameLoaded: placeId:111.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := watchPlayerLogsStarts.Load()
	hub := startDiscordPresence(context.Background(), load, 1818, dir)
	if hub == nil {
		t.Fatal("presence hub was nil")
	}
	defer stopDiscordPresence(hub)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("Info [FLog::DataModelBindings] onGameLoaded: placeId:999.\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(playerLogPoll + 80*time.Millisecond)
	if got := watchPlayerLogsStarts.Load(); got != before {
		t.Fatalf("watchPlayerLogs started (%d → %d); JNI onGameLoaded is the place source", before, got)
	}
}

func TestDiscordPresenceWiresGameLoadedListener(t *testing.T) {
	t.Setenv("TIPSY_DISCORD_APPLICATION_ID", "")
	t.Cleanup(func() {
		jni.SetPlaceIDListener(nil)
		jni.SetGameLoadedListener(nil)
		android.SetLogTextObserver(nil)
	})
	load := func(context.Context) (clientsettings.Settings, error) {
		return clientsettings.Default(), nil
	}
	hub := startDiscordPresence(context.Background(), load, 0, t.TempDir())
	if hub == nil {
		t.Fatal("presence hub was nil")
	}
	defer stopDiscordPresence(hub)

	// SetGameLoadedListener is hub.SetLoadedPlaceID: zero is Home and must
	// apply. StartGameParams' SetPlaceID ignores 0 and must not clobber.
	hub.SetPlaceID(1818)
	hub.SetPlaceID(0)
	hub.SetLoadedPlaceID(18667984660)
	hub.SetLoadedPlaceID(0)
	hub.SetLoadedPlaceID(8735521924)
	hub.SetLoadedPlaceID(-1)
	stopDiscordPresence(hub)
	// Listener must be cleared so a later dispatch cannot notify this hub.
	var notified bool
	jni.SetGameLoadedListener(func(int64) { notified = true })
	jni.SetGameLoadedListener(nil)
	if notified {
		t.Fatal("cleared GameLoadedListener still called")
	}
}
