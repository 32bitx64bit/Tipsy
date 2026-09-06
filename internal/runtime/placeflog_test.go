// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestParseOnGameLoadedPlaceID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		id   int64
		ok   bool
	}{
		{line: "Info [FLog::DataModelBindings] onGameLoaded: placeId:0.", id: 0, ok: true},
		{line: "Info [FLog::DataModelBindings] onGameLoaded: placeId:18667984660.", id: 18667984660, ok: true},
		{line: "Info [FLog::NativeDM] dataModelBindings_onGameLoaded: placeId = 18667984660.", id: 18667984660, ok: true},
		{line: "https://games.roblox.com/v1/games?placeIds=1818", ok: false},
		{line: "roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+placelauncherurl:https://example/PlaceLauncher.ashx?placeId=1818", ok: false},
		{line: "[FLog::Output] ! Joining game 'not-a-ticket' place 18667984660 at 10.41.2.218", ok: false},
		{line: "placeId:1818", ok: false},
	}
	for _, test := range tests {
		got, ok := parseOnGameLoadedPlaceID([]byte(test.line))
		if ok != test.ok || got != test.id {
			t.Fatalf("line %q -> (%d, %v), want (%d, %v)", test.line, got, ok, test.id, test.ok)
		}
	}
}

func TestParseOnGameLoadedPlaceIDReturnsOnlyID(t *testing.T) {
	t.Parallel()
	id, ok := parseOnGameLoadedPlaceID([]byte("gameinfo:SYNTHETIC-TICKET onGameLoaded: placeId:1818."))
	if !ok || id != 1818 {
		t.Fatalf("got (%d, %v)", id, ok)
	}
}

func TestWatchPlayerLogsIgnoresHistoryAndFollowsJoin(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "2.736.0.1408_20260101T000000Z_Player_old_last.log")
	if err := os.WriteFile(old, []byte("Info [FLog::DataModelBindings] onGameLoaded: placeId:999.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var got []int64
	go watchPlayerLogs(ctx, dir, 15*time.Millisecond, func(id int64) {
		mu.Lock()
		got = append(got, id)
		mu.Unlock()
	})

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n != 0 {
			t.Fatalf("historical place leaked: %v", snapshotIDs(&mu, &got))
		}
		time.Sleep(15 * time.Millisecond)
	}

	f, err := os.OpenFile(old, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("Info [FLog::DataModelBindings] onGameLoaded: placeId:1818.\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	waitPlaceIDs(t, &mu, &got, 1818)

	cur := filepath.Join(dir, "2.736.0.1408_20260906T081258Z_Player_new_last.log")
	body := "Info [FLog::DataModelBindings] onGameLoaded: placeId:0.\n" +
		"Info [FLog::DataModelBindings] onGameLoaded: placeId:18667984660.\n"
	if err := os.WriteFile(cur, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	waitPlaceIDs(t, &mu, &got, 18667984660)
}

func snapshotIDs(mu *sync.Mutex, got *[]int64) []int64 {
	mu.Lock()
	defer mu.Unlock()
	out := make([]int64, len(*got))
	copy(out, *got)
	return out
}

func waitPlaceIDs(t *testing.T, mu *sync.Mutex, got *[]int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, id := range *got {
			if id == want {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing %d in %v", want, snapshotIDs(mu, got))
}
