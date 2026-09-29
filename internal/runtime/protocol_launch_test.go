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

func TestWebViewProtocolJNIExports(t *testing.T) {
	if messageBusDoSubscribeRawSym != "Java_com_roblox_universalapp_messagebus_MessageBus_doSubscribeRaw" {
		t.Fatalf("doSubscribeRaw=%q", messageBusDoSubscribeRawSym)
	}
	if messageBusGetMessageIdSym != "Java_com_roblox_universalapp_messagebus_MessageBus_getMessageId" {
		t.Fatalf("getMessageId=%q", messageBusGetMessageIdSym)
	}
	if webViewInitializeSym != "Java_com_roblox_protocols_webview_WebViewProtocol_initializeAndroidWebViewProtocol" {
		t.Fatalf("initialize=%q", webViewInitializeSym)
	}
	if messageBusPublishRawSym != "Java_com_roblox_universalapp_messagebus_MessageBus_publishRaw" {
		t.Fatalf("publishRaw=%q", messageBusPublishRawSym)
	}
	if webViewSignalJavascriptSym != "Java_com_roblox_protocols_webview_WebViewProtocol_signalJavascriptCallback" {
		t.Fatalf("signalJavascript=%q", webViewSignalJavascriptSym)
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

func TestSpecificServerJoinKeepsJobInStartGameRequest(t *testing.T) {
	const fakeJob = "SYNTHETIC-JOB-ID"
	req, err := rbxuri.Parse("roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+placelauncherurl:https%3A%2F%2Fassetgame.roblox.com%2Fgame%2FPlaceLauncher.ashx%3Frequest%3DRequestGameJob%26placeId%3D1818%26gameId%3D" + fakeJob)
	if err != nil {
		t.Fatal(err)
	}
	if req.PlaceID != 1818 || req.GameInstanceID != fakeJob {
		t.Fatalf("place=%d instance=%q", req.PlaceID, req.GameInstanceID)
	}
	if got := appStarterPlace(req); got != "1818" {
		t.Fatalf("appStarterPlace=%q", got)
	}
	req.TicketRedeemed = true
	if got := req.WebLoginURI(); !strings.Contains(got, "gameInstanceId="+fakeJob) || !strings.Contains(got, "placeId=1818") || strings.Contains(got, "gameinfo:") {
		t.Fatalf("redeemed uri=%q", got)
	}
	if req.JoinRequestType() != rbxuri.JoinRequestGameInstance {
		t.Fatalf("joinType=%d, want specific instance", req.JoinRequestType())
	}
}

func TestFollowUserJoinKeepsUserInStartGameRequest(t *testing.T) {
	req, err := rbxuri.Parse("roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+placelauncherurl:https%3A%2F%2Fassetgame.roblox.com%2Fgame%2FPlaceLauncher.ashx%3Frequest%3DRequestFollowUser%26userId%3D123456")
	if err != nil {
		t.Fatal(err)
	}
	if req.PlaceID != 0 || req.UserID != 123456 {
		t.Fatalf("place=%d user=%d", req.PlaceID, req.UserID)
	}
	if got := appStarterPlace(req); got != "" {
		t.Fatalf("appStarterPlace=%q, follow-user has no place", got)
	}
	req.TicketRedeemed = true
	if got := req.WebLoginURI(); got != "roblox://experiences/start?userId=123456" {
		t.Fatalf("redeemed uri=%q", got)
	}
	if req.JoinRequestType() != rbxuri.JoinRequestFollowUser {
		t.Fatalf("joinType=%d, want follow user", req.JoinRequestType())
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

func TestStartGameFieldsTypePrivateServerJoins(t *testing.T) {
	const fakeCode = "SYNTHETIC-ACCESS-CODE"
	req, err := rbxuri.Parse("roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+placeid:1818+accesscode:" + fakeCode)
	if err != nil {
		t.Fatal(err)
	}
	fields := startGameFields(req)
	if got := fields["joinRequestType"]; got != rbxuri.JoinRequestPrivateServer {
		t.Fatalf("joinRequestType=%v, want private server", got)
	}
	if fields["accessCode"] != fakeCode || fields["placeId"] != int64(1818) {
		t.Fatalf("fields=%v", fields)
	}
	if fields["joinAttemptOrigin"] != "Website" {
		t.Fatalf("origin=%v, want Website for a roblox-player launch", fields["joinAttemptOrigin"])
	}
}

func TestStartGameShapeNeverIncludesValues(t *testing.T) {
	const (
		fakeJob  = "SYNTHETIC-JOB-ID"
		fakeCode = "SYNTHETIC-ACCESS-CODE"
		fakeData = "SYNTHETIC-LAUNCH-DATA"
	)
	shape := startGameShape(startGameFields(rbxuri.Request{
		Scheme:         "roblox",
		PlaceID:        1818,
		GameInstanceID: fakeJob,
		AccessCode:     fakeCode,
		LaunchData:     fakeData,
		ReferralPage:   "WebView",
	}))
	for _, leaked := range []string{fakeJob, fakeCode, fakeData, "1818", "WebView"} {
		if strings.Contains(shape, leaked) {
			t.Fatalf("shape leaked %q: %s", leaked, shape)
		}
	}
	for _, want := range []string{"joinRequestType=2", "placeId=set", "gameId=16", "accessCode=21",
		"launchData=21", "referralPage=7", "joinAttemptId=0", "joinAttemptOrigin=Deeplink", "userId=0"} {
		if !strings.Contains(shape, want) {
			t.Fatalf("shape missing %q: %s", want, shape)
		}
	}
}
