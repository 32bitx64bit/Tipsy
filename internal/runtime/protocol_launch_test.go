// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

func TestWebsiteLaunchJNIExports(t *testing.T) {
	if webLoginInitSym != "Java_com_roblox_universalapp_linking_JNIWebLoginProtocol_init" {
		t.Fatalf("webLoginInitSym=%q", webLoginInitSym)
	}
	if webLoginColdStartSym != "Java_com_roblox_universalapp_linking_JNIWebLoginProtocol_maybeHandleColdStartProtocolLaunch" {
		t.Fatalf("webLoginColdStartSym=%q", webLoginColdStartSym)
	}
	if baseURLInitSym != "Java_com_roblox_universalapp_linking_JNIBaseUrlProtocol_init" {
		t.Fatalf("baseURLInitSym=%q", baseURLInitSym)
	}
	if baseURLColdStartSym != "Java_com_roblox_universalapp_linking_JNIBaseUrlProtocol_maybeHandleColdStartProtocolLaunch" {
		t.Fatalf("baseURLColdStartSym=%q", baseURLColdStartSym)
	}
	if startGameSym != "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2StartGameWithParam" {
		t.Fatalf("startGameSym=%q", startGameSym)
	}
}

func TestAppStarterPlaceFromWebsiteRequest(t *testing.T) {
	if got := appStarterPlace(rbxuri.Request{}); got != "" {
		t.Fatalf("empty request place=%q", got)
	}
	req, err := rbxuri.Parse("https://www.roblox.com/games/1818/Classic-Crossroads")
	if err != nil {
		t.Fatal(err)
	}
	if got := appStarterPlace(req); got != "1818" {
		t.Fatalf("place=%q", got)
	}
}

func TestWebLoginURIOmitsTicketAfterRedeem(t *testing.T) {
	req, err := rbxuri.Parse("roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+placelauncherurl:https%3A%2F%2Fassetgame.roblox.com%2Fgame%2FPlaceLauncher.ashx%3Frequest%3DRequestGame%26placeId%3D1818")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.WebLoginURI(), "roblox-player:") {
		t.Fatal("unredeemed ticket should stay on the original player URI")
	}
	req.TicketRedeemed = true
	if got := req.WebLoginURI(); !strings.Contains(got, "placeId=1818") || strings.Contains(got, "gameinfo:") {
		t.Fatalf("redeemed uri=%q", got)
	}
}

func TestPrivateServerShareUsesOfficialNavigationHandoff(t *testing.T) {
	const fakeCode = "SYNTHETIC-PRIVATE-SERVER-CODE"
	req, err := rbxuri.Parse("roblox://navigation/share_links?code=" + fakeCode + "&type=Server")
	if err != nil {
		t.Fatal(err)
	}
	if req.PlaceID != 0 {
		t.Fatalf("share link place=%d, want no guessed place", req.PlaceID)
	}
	if got, want := req.WebLoginURI(), "roblox://navigation/share_links?code="+fakeCode+"&type=Server"; got != want {
		t.Fatalf("native handoff=%q want %q", got, want)
	}
}
