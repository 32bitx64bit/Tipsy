// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package rbxuri

import (
	"strings"
	"testing"
)

func TestParseRobloxPlayerURI(t *testing.T) {
	t.Parallel()
	raw := "roblox-player:1+launchmode:play+gameinfo:SYNTHETIC-TICKET+launchtime:1710000000000+placelauncherurl:https%3A%2F%2Fassetgame.roblox.com%2Fgame%2FPlaceLauncher.ashx%3Frequest%3DRequestGame%26placeId%3D1818+browsertrackerid:99+robloxLocale:en_us+gameLocale:en_us"
	req, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Scheme != "roblox-player" || req.LaunchMode != "play" || req.PlaceID != 1818 {
		t.Fatalf("request=%+v", req)
	}
	if req.Ticket != "SYNTHETIC-TICKET" || !req.HasTicket() {
		t.Fatal("missing ticket")
	}
	if req.BrowserTrackerID != "99" {
		t.Fatalf("browser tracker=%q", req.BrowserTrackerID)
	}
	if !strings.Contains(req.AndroidDeepLink, "placeId=1818") {
		t.Fatalf("android deep link=%q", req.AndroidDeepLink)
	}
	if strings.Contains(req.AndroidDeepLink, "SYNTHETIC-TICKET") {
		t.Fatal("android deep link replayed the website ticket")
	}
	if strings.Contains(req.Summary(), "SYNTHETIC-TICKET") || strings.Contains(req.Summary(), "gameinfo") {
		t.Fatalf("summary leaked ticket: %s", req.Summary())
	}
	if !strings.Contains(req.Summary(), "ticket=present") || !strings.Contains(req.Summary(), "place=1818") {
		t.Fatalf("summary=%s", req.Summary())
	}
}

func TestParseRobloxPlayerURIKeepsPlusInTicket(t *testing.T) {
	t.Parallel()
	raw := "roblox-player:1+launchmode:play+gameinfo:AAA%2BBBB+CCC+placelauncherurl:https%3A%2F%2Fassetgame.roblox.com%2Fgame%2FPlaceLauncher.ashx%3Frequest%3DRequestGame%26placeId%3D1818+browsertrackerid:99"
	req, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.PlaceID != 1818 {
		t.Fatalf("place=%d", req.PlaceID)
	}
	if req.Ticket != "AAA+BBB+CCC" {
		t.Fatalf("ticket was split or query-unescaped")
	}
	if req.Original != raw {
		t.Fatal("original URI was not retained")
	}
	if req.WebLoginURI() != raw {
		t.Fatal("unredeemed web login URI should keep roblox-player")
	}
	req.TicketRedeemed = true
	if strings.Contains(req.WebLoginURI(), "gameinfo:") {
		t.Fatal("redeemed web login URI replayed the website ticket")
	}
	if strings.Contains(req.Summary(), "AAA") || strings.Contains(req.Summary(), "BBB") {
		t.Fatalf("summary leaked ticket: %s", req.Summary())
	}
}

func TestParseRobloxDeepLinkAndWebURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw    string
		place  int64
		scheme string
	}{
		{raw: "roblox://experiences/start?placeId=920587237&gameInstanceId=job", place: 920587237, scheme: "roblox"},
		{raw: "roblox://placeId=1818", place: 1818, scheme: "roblox"},
		{raw: "https://www.roblox.com/games/920587237/Natural-Disaster-Survival", place: 920587237, scheme: "https"},
		{raw: "https://www.roblox.com/games/start?placeid=1818", place: 1818, scheme: "https"},
		{raw: "https://web.roblox.com/experiences/start?placeId=1818&linkCode=abc", place: 1818, scheme: "https"},
	}
	for _, test := range cases {
		req, err := Parse(test.raw)
		if err != nil {
			t.Fatalf("%s: %v", test.raw, err)
		}
		if req.PlaceID != test.place || req.Scheme != test.scheme {
			t.Fatalf("%s: place=%d scheme=%s", test.raw, req.PlaceID, req.Scheme)
		}
		if req.HasTicket() {
			t.Fatalf("%s: unexpected ticket", test.raw)
		}
		if !strings.Contains(req.AndroidDeepLink, "placeId="+itoa(test.place)) {
			t.Fatalf("%s: deep link=%q", test.raw, req.AndroidDeepLink)
		}
	}
}

func TestParsePrivateServerLinksPreservesOpaqueJoinFields(t *testing.T) {
	t.Parallel()
	const fakeCode = "SYNTHETIC-PRIVATE-SERVER-CODE"
	cases := []struct {
		name  string
		raw   string
		want  string
		share bool
	}{
		{
			name: "legacy web link code",
			raw:  "https://www.roblox.com/games/1818/Classic-Crossroads?privateServerLinkCode=" + fakeCode,
			want: "roblox://experiences/start?linkCode=" + fakeCode + "&placeId=1818",
		},
		{
			name:  "current browser share link",
			raw:   "https://www.roblox.com/share?code=" + fakeCode + "&type=Server",
			want:  "roblox://navigation/share_links?code=" + fakeCode + "&type=Server",
			share: true,
		},
		{
			name:  "browser protocol handoff",
			raw:   "roblox://navigation/share_links?code=" + fakeCode + "&type=Server",
			want:  "roblox://navigation/share_links?code=" + fakeCode + "&type=Server",
			share: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			req, err := Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := req.AndroidDeepLink; got != test.want {
				t.Fatalf("handoff=%q want %q", got, test.want)
			}
			if strings.Contains(req.Summary(), fakeCode) {
				t.Fatalf("summary leaked private-server code: %s", req.Summary())
			}
			if test.share {
				if !req.IsPrivateServerShare() || req.PlaceID != 0 || req.ShareCode != fakeCode {
					t.Fatalf("share request=%+v", req)
				}
			} else if req.LinkCode != fakeCode || req.PlaceID != 1818 {
				t.Fatalf("legacy request=%+v", req)
			}
		})
	}
}

func TestParseRejectsIncompletePrivateServerShare(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"https://www.roblox.com/share?type=Server",
		"roblox://navigation/share_links?code=SYNTHETIC-PRIVATE-SERVER-CODE&type=Experience",
	} {
		if _, err := Parse(raw); err == nil || strings.Contains(err.Error(), "SYNTHETIC") {
			t.Fatalf("Parse(%q) error=%v", raw, err)
		}
	}
}

func TestParseRejectsStudioAndJunk(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "roblox-studio:1+launchmode:edit+placeId:1", want: "Studio"},
		{raw: "roblox-player:1+launchmode:edit+placelauncherurl:https://www.roblox.com/Game/PlaceLauncher.ashx?placeId=1", want: "Studio"},
		{raw: "https://example.com/games/1", want: "unsupported"},
		{raw: "ftp://www.roblox.com/games/1", want: "unsupported"},
		{raw: "roblox://experiences/start", want: "does not name"},
		{raw: "not-a-uri", want: "unsupported"},
	}
	for _, test := range cases {
		_, err := Parse(test.raw)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: err=%v want %q", test.raw, err, test.want)
		}
		if err != nil && strings.Contains(err.Error(), "launchmode:edit") {
			t.Fatalf("error leaked URI payload: %v", err)
		}
	}
}

func TestParseEmptyAndLooksLike(t *testing.T) {
	t.Parallel()
	req, err := Parse("")
	if err != nil || !req.Empty() {
		t.Fatalf("empty parse=%+v err=%v", req, err)
	}
	if LooksLike("") || LooksLike("https://example.com/") || LooksLike("tipsy") {
		t.Fatal("LooksLike accepted a non-Roblox URI")
	}
	if !LooksLike("roblox-player:1+launchmode:play") || !LooksLike("roblox://experiences/start?placeId=1") {
		t.Fatal("LooksLike rejected a Roblox URI")
	}
	if !LooksLike("https://www.roblox.com/games/1/name") {
		t.Fatal("LooksLike rejected an official experience URL")
	}
	if !LooksLike("https://www.roblox.com/share?code=SYNTHETIC-PRIVATE-SERVER-CODE&type=Server") || !LooksLike("roblox://navigation/share_links?code=SYNTHETIC-PRIVATE-SERVER-CODE&type=Server") {
		t.Fatal("LooksLike rejected an official private-server URI")
	}
}

func TestPlacePageURLIsPublicHTTPS(t *testing.T) {
	t.Parallel()
	if got := PlacePageURL(1818); got != "https://www.roblox.com/games/1818" {
		t.Fatalf("url=%q", got)
	}
	if PlacePageURL(0) != "" || PlacePageURL(-1) != "" {
		t.Fatal("non-positive place ids must not produce a join url")
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
