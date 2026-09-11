// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"testing"
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
