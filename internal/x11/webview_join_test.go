// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

func TestClassifyWebViewURLOmitsQuery(t *testing.T) {
	c := ClassifyWebViewURL("https://www.roblox.com/games/start?placeid=1818&gameInstanceId=SYNTHETIC-JOB-ID")
	if !c.HasURL || c.Scheme != "https" || c.Host != "www.roblox.com" || c.PathClass != "/games/start" {
		t.Fatalf("class=%+v", c)
	}
}

func TestParseWebViewJoinURIKeepsSyntheticJob(t *testing.T) {
	const fakeJob = "SYNTHETIC-JOB-ID"
	req, ok := ParseWebViewJoinURI("roblox://experiences/start?placeId=1818&gameInstanceId=" + fakeJob)
	if !ok || req.PlaceID != 1818 || req.GameInstanceID != fakeJob {
		t.Fatalf("req=%+v ok=%v", req, ok)
	}
	if strings.Contains(req.Summary(), fakeJob) {
		t.Fatalf("summary leaked job: %s", req.Summary())
	}
}

func TestParseWebViewJoinURIRobloxMobileAndPlayer(t *testing.T) {
	const fakeJob = "SYNTHETIC-JOB-ID"
	req, ok := ParseWebViewJoinURI("robloxmobile://experiences/start?placeId=1818&gameInstanceId=" + fakeJob)
	if !ok || req.GameInstanceID != fakeJob || req.PlaceID != 1818 {
		t.Fatalf("mobile req=%+v ok=%v", req, ok)
	}
	req, ok = ParseWebViewJoinURI("https://www.roblox.com/games/start?placeid=1818&gameInstanceId=" + fakeJob)
	if !ok || req.GameInstanceID != fakeJob {
		t.Fatalf("https start req=%+v ok=%v", req, ok)
	}
	if _, listing := ParseWebViewJoinURI("https://www.roblox.com/games/1818/Classic"); listing {
		t.Fatal("listing page must not start a join")
	}
	req, err := rbxuri.Parse("roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+placelauncherurl:https%3A%2F%2Fassetgame.roblox.com%2Fgame%2FPlaceLauncher.ashx%3Frequest%3DRequestGameJob%26placeId%3D1818%26gameId%3D" + fakeJob)
	if err != nil {
		t.Fatal(err)
	}
	if req.GameInstanceID != fakeJob {
		t.Fatalf("player job=%q", req.GameInstanceID)
	}
}

func TestParseWebViewJoinURILPOpenURL(t *testing.T) {
	const fakeJob = "SYNTHETIC-JOB-ID"
	raw := `lp:openUrl{"url":"roblox://experiences/start?placeId=1818&gameInstanceId=` + fakeJob + `"}`
	req, ok := ParseWebViewJoinURI(raw)
	if !ok || req.PlaceID != 1818 || req.GameInstanceID != fakeJob {
		t.Fatalf("lp req=%+v ok=%v", req, ok)
	}
}

func TestIsWebViewCloseCommand(t *testing.T) {
	if !IsWebViewCloseCommand("bs:command:close") {
		t.Fatal("close")
	}
	if IsWebViewCloseCommand("https://www.roblox.com/games/1818") {
		t.Fatal("games page is not a close command")
	}
}

func TestHandleWebViewPolicyURIJoinCallsStartGame(t *testing.T) {
	const fakeJob = "SYNTHETIC-JOB-ID"
	var got rbxuri.Request
	SetWebViewStartGame(func(req rbxuri.Request) { got = req })
	t.Cleanup(func() { SetWebViewStartGame(nil) })
	if !HandleWebViewPolicyURI("roblox://experiences/start?placeId=1818&gameInstanceId=" + fakeJob) {
		t.Fatal("join should be handled")
	}
	if got.PlaceID != 1818 || got.GameInstanceID != fakeJob {
		t.Fatalf("started %+v", got)
	}
}

func TestHandleHybridExecuteRobloxLaunchGame(t *testing.T) {
	const fakeJob = "SYNTHETIC-JOB-ID"
	var got rbxuri.Request
	var signaled string
	SetWebViewStartGame(func(req rbxuri.Request) { got = req })
	SetWebViewJavascriptSignal(func(cmd string) { signaled = cmd })
	t.Cleanup(func() {
		SetWebViewStartGame(nil)
		SetWebViewJavascriptSignal(nil)
	})
	raw := `{"moduleID":"Game","functionName":"launchGame","params":{"request":{"requestType":"RequestGame","placeId":1818,"gameInstanceId":"` + fakeJob + `"}}}`
	if !HandleHybridExecuteRoblox(raw) {
		t.Fatal("hybrid launchGame should be handled")
	}
	if got.PlaceID != 1818 || got.GameInstanceID != fakeJob {
		t.Fatalf("started %+v", got)
	}
	if signaled != raw {
		t.Fatal("signalJavascriptCallback was not invoked with the command")
	}
	if strings.Contains(got.Summary(), fakeJob) {
		t.Fatalf("summary leaked job: %s", got.Summary())
	}
}

func TestHandleHybridExecuteRobloxOverlayClose(t *testing.T) {
	var closed bool
	SetWebViewUserClosed(func() { closed = true })
	t.Cleanup(func() { SetWebViewUserClosed(nil) })
	if !HandleHybridExecuteRoblox(`{"moduleID":"Overlay","functionName":"close"}`) {
		t.Fatal("overlay close should be handled")
	}
	if !closed {
		t.Fatal("Overlay.close must publish handleWindowClose")
	}
}

func TestSoupCookieDomainPrefixesRegistrableDomain(t *testing.T) {
	if got := SoupCookieDomain("roblox.com", false); got != ".roblox.com" {
		t.Fatalf("domain cookie=%q", got)
	}
	if got := SoupCookieDomain(".roblox.com", false); got != ".roblox.com" {
		t.Fatalf("already dotted=%q", got)
	}
	if got := SoupCookieDomain("www.roblox.com", true); got != "www.roblox.com" {
		t.Fatalf("host-only=%q", got)
	}
}

func TestWebViewWebsiteThemeDefaultsDark(t *testing.T) {
	if WebViewWebsiteTheme("") != "Dark" || WebViewWebsiteTheme("dark") != "Dark" {
		t.Fatal("empty/client Dark must stay Dark")
	}
	if WebViewWebsiteTheme("Light") != "Light" {
		t.Fatal("Light must stay Light")
	}
}

func TestEnsureWebViewThemeCookieUsesAndroidOverride(t *testing.T) {
	original := []WebViewCookie{
		{Name: "session-fixture", Value: "opaque-fixture", Domain: "roblox.com", HTTPOnly: true},
		{Name: "RBXThemeOverride", Value: "light", Domain: "www.roblox.com", Path: "/", HostOnly: true},
	}
	for _, theme := range []string{"", "Dark", "Light"} {
		got := EnsureWebViewThemeCookie(original, theme)
		if len(got) != 2 || got[0] != original[0] {
			t.Fatal("host theme must preserve other cookie scopes and values")
		}
		want := "dark"
		if theme == "Light" {
			want = "light"
		}
		c := got[1]
		if c.Name != "RBXThemeOverride" || c.Value != want || c.Domain != "www.roblox.com" || !c.HostOnly || c.Path != "/" {
			t.Fatal("theme override must follow Android's host cookie contract")
		}
	}
	if original[1].Value != "light" {
		t.Fatal("snapshot was mutated")
	}
	got := EnsureWebViewThemeCookie(nil, "Dark")
	if len(got) != 1 || got[0].Value != "dark" {
		t.Fatal("missing override was not seeded")
	}
}

func TestHandleHybridExecuteRobloxRejectsUnsupportedRequestType(t *testing.T) {
	var started bool
	SetWebViewStartGame(func(rbxuri.Request) { started = true })
	t.Cleanup(func() { SetWebViewStartGame(nil) })
	raw := `{"moduleID":"Game","functionName":"launchGame","params":{"request":{"requestType":"Unsupported","placeId":1818}}}`
	if !HandleHybridExecuteRoblox(raw) {
		t.Fatal("hybrid JSON is still consumed")
	}
	if started {
		t.Fatal("unsupported requestType must not StartGame")
	}
}
