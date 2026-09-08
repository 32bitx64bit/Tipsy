// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package rbxuri parses official Roblox website and protocol-handler launch
// URIs. Secret fields are never included in logs or error text.
package rbxuri

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Request is a validated website or protocol-handler launch.
// Ticket is a one-time authentication ticket from the official website and
// must never be logged.
type Request struct {
	Scheme             string
	LaunchMode         string
	PlaceID            int64
	UserID             int64
	ReferredByPlayerID int64
	GameInstanceID     string
	AccessCode         string
	ReservedServerCode string
	LinkCode           string
	ShareCode          string
	ShareType          string
	LaunchData         string
	ReferralPage       string
	BrowserTrackerID   string
	PlaceLauncherURL   string
	AndroidDeepLink    string
	Original           string
	Ticket             string
	TicketRedeemed     bool
	hasWebsiteLaunch   bool
}

var (
	errUnsupported  = errors.New("unsupported Roblox URI")
	errStudio       = errors.New("Roblox Studio URIs are not supported")
	errMissingPlace = errors.New("Roblox URI does not name an experience")
)

// LooksLike reports whether s is a Roblox website or protocol URI Tipsy may own.
func LooksLike(s string) bool {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return false
	case strings.HasPrefix(strings.ToLower(s), "roblox-player:"):
		return true
	case strings.HasPrefix(strings.ToLower(s), "roblox-studio:"):
		return true
	case strings.HasPrefix(strings.ToLower(s), "roblox:"):
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return isRobloxWebURL(u)
}

// Parse decodes an official website Play URI, Android deep link, or Roblox
// experience HTTPS URL. An empty string is a no-op request.
func Parse(raw string) (Request, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Request{}, nil
	}
	lower := strings.ToLower(raw)
	var (
		req Request
		err error
	)
	switch {
	case strings.HasPrefix(lower, "roblox-studio:"):
		return Request{}, errStudio
	case strings.HasPrefix(lower, "roblox-player:"):
		req, err = parsePlayerProtocol(raw)
	case strings.HasPrefix(lower, "roblox:"):
		req, err = parseRobloxDeepLink(raw)
	default:
		u, parseErr := url.Parse(raw)
		if parseErr != nil || !isRobloxWebURL(u) {
			return Request{}, errUnsupported
		}
		req, err = parseWebURL(u)
	}
	if err != nil {
		return Request{}, err
	}
	req.Original = raw
	return req, nil
}

// Empty reports whether the request carries no website launch.
func (r Request) Empty() bool {
	return !r.hasWebsiteLaunch && r.PlaceID == 0 && r.Ticket == "" && r.AndroidDeepLink == ""
}

// HasTicket reports whether the website supplied a one-time auth ticket.
func (r Request) HasTicket() bool {
	return r.Ticket != ""
}

// IsPrivateServerShare reports whether r is Roblox's current private-server
// share-link route without requiring callers to inspect its opaque code.
func (r Request) IsPrivateServerShare() bool {
	return r.ShareCode != "" && strings.EqualFold(r.ShareType, "Server")
}

// WebLoginURI is the string handed to JNIWebLoginProtocol. After a successful
// Go redeem, that is the Android deep link (the ticket is one-shot and must
// not be replayed). If redeem failed, the original roblox-player: URI is kept
// so the official client can try the same ticket.
func (r Request) WebLoginURI() string {
	if r.HasTicket() && !r.TicketRedeemed && r.Scheme == "roblox-player" && r.Original != "" {
		return r.Original
	}
	return r.AndroidDeepLink
}

// PlacePageURL is the public Roblox website page for a place. It never includes
// job ids, access codes, tickets, or user identities.
func PlacePageURL(placeID int64) string {
	if placeID <= 0 {
		return ""
	}
	return "https://www.roblox.com/games/" + strconv.FormatInt(placeID, 10)
}

// Summary is safe for logs: identities only, never ticket or cookie values.
func (r Request) Summary() string {
	if r.Empty() {
		return "none"
	}
	var b strings.Builder
	if r.Scheme != "" {
		b.WriteString(r.Scheme)
	} else {
		b.WriteString("roblox")
	}
	if r.LaunchMode != "" {
		b.WriteString(" mode=")
		b.WriteString(r.LaunchMode)
	}
	if r.PlaceID != 0 {
		b.WriteString(" place=")
		b.WriteString(strconv.FormatInt(r.PlaceID, 10))
	}
	if r.HasTicket() {
		b.WriteString(" ticket=present")
	}
	if r.GameInstanceID != "" {
		b.WriteString(" instance=present")
	}
	if r.AccessCode != "" || r.ReservedServerCode != "" || r.LinkCode != "" || r.ShareCode != "" {
		b.WriteString(" private-server=present")
	}
	return b.String()
}

func isRobloxWebURL(u *url.URL) bool {
	if u == nil || strings.ToLower(u.Scheme) != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "www.roblox.com", "web.roblox.com", "roblox.com":
	default:
		return false
	}
	path := strings.ToLower(u.EscapedPath())
	return strings.HasPrefix(path, "/games") || strings.HasPrefix(path, "/experiences") || path == "/share"
}

func parsePlayerProtocol(raw string) (Request, error) {
	_, payload, ok := strings.Cut(raw, ":")
	if !ok {
		return Request{}, errUnsupported
	}
	values := parsePlayerFields(payload)
	mode := strings.ToLower(values["launchmode"])
	if isStudioMode(mode) {
		return Request{}, errStudio
	}
	if mode == "" {
		mode = "play"
	}
	req := Request{
		Scheme:             "roblox-player",
		LaunchMode:         mode,
		Ticket:             values["gameinfo"],
		BrowserTrackerID:   firstValue(values, "browsertrackerid"),
		PlaceLauncherURL:   firstValue(values, "placelauncherurl", "placeLauncherUrl"),
		LaunchData:         firstValue(values, "launchdata", "launchData"),
		AccessCode:         firstValue(values, "accesscode"),
		ReservedServerCode: firstValue(values, "reservedserveraccesscode"),
		LinkCode:           firstValue(values, "linkcode", "privateserverlinkcode"),
		GameInstanceID:     firstValue(values, "gameinstanceid", "gameid"),
		ReferralPage:       firstValue(values, "referralpage"),
		hasWebsiteLaunch:   true,
	}
	if id, ok := parseInt64(firstValue(values, "placeid")); ok {
		req.PlaceID = id
	}
	if id, ok := parseInt64(firstValue(values, "userid")); ok {
		req.UserID = id
	}
	if id, ok := parseInt64(firstValue(values, "referredbyplayerid")); ok {
		req.ReferredByPlayerID = id
	}
	if req.PlaceID == 0 && req.PlaceLauncherURL != "" {
		req.PlaceID = placeIDFromQuery(req.PlaceLauncherURL)
	}
	if req.PlaceID == 0 && req.Ticket == "" && req.PlaceLauncherURL == "" {
		return Request{}, errMissingPlace
	}
	req.AndroidDeepLink = req.androidDeepLink()
	return req, nil
}

func parseRobloxDeepLink(raw string) (Request, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Request{}, errUnsupported
	}
	req := Request{Scheme: "roblox", LaunchMode: "play", hasWebsiteLaunch: true}
	q := u.Query()
	host := strings.ToLower(u.Host)
	path := strings.ToLower(strings.Trim(u.Path, "/"))
	opaque := strings.ToLower(u.Opaque)
	switch {
	case host == "navigation" && path == "share_links":
		return privateServerShareRequest("roblox", q)
	case host == "experiences" && (path == "start" || strings.HasPrefix(path, "start/")):
	case path == "experiences/start" || strings.HasPrefix(path, "experiences/start/"):
	case strings.Contains(host, "placeid=") || strings.HasPrefix(opaque, "placeid="):
		combined := u.Host
		if combined == "" {
			combined = u.Opaque
		}
		if u.RawQuery != "" {
			combined += "&" + u.RawQuery
		}
		q, _ = url.ParseQuery(strings.ReplaceAll(combined, "?", "&"))
	default:
		if len(q) == 0 {
			return Request{}, errUnsupported
		}
	}
	req.PlaceID, _ = parseInt64(firstQuery(q, "placeid", "placeId"))
	req.UserID, _ = parseInt64(firstQuery(q, "userid", "userId"))
	req.ReferredByPlayerID, _ = parseInt64(firstQuery(q, "referredbyplayerid", "referredByPlayerId"))
	req.GameInstanceID = firstQuery(q, "gameinstanceid", "gameInstanceId", "gameid")
	req.AccessCode = firstQuery(q, "accesscode", "accessCode")
	req.ReservedServerCode = firstQuery(q, "reservedserveraccesscode", "reservedServerAccessCode")
	req.LinkCode = firstQuery(q, "linkcode", "linkCode", "privateserverlinkcode", "privateServerLinkCode")
	req.LaunchData = firstQuery(q, "launchdata", "launchData")
	req.ReferralPage = firstQuery(q, "referralpage", "referralPage")
	if req.PlaceID == 0 {
		return Request{}, errMissingPlace
	}
	req.AndroidDeepLink = req.androidDeepLink()
	return req, nil
}

func parseWebURL(u *url.URL) (Request, error) {
	if strings.EqualFold(u.EscapedPath(), "/share") {
		return privateServerShareRequest("https", u.Query())
	}
	req := Request{Scheme: "https", LaunchMode: "play", hasWebsiteLaunch: true}
	q := u.Query()
	req.PlaceID, _ = parseInt64(firstQuery(q, "placeid", "placeId"))
	req.GameInstanceID = firstQuery(q, "gameinstanceid", "gameInstanceId")
	req.AccessCode = firstQuery(q, "accesscode", "accessCode")
	req.ReservedServerCode = firstQuery(q, "reservedserveraccesscode", "reservedServerAccessCode")
	req.LinkCode = firstQuery(q, "linkcode", "linkCode", "privateserverlinkcode", "privateServerLinkCode")
	req.LaunchData = firstQuery(q, "launchdata", "launchData")
	if req.PlaceID == 0 {
		req.PlaceID = placeIDFromPath(u.Path)
	}
	if req.PlaceID == 0 {
		return Request{}, errMissingPlace
	}
	req.AndroidDeepLink = req.androidDeepLink()
	return req, nil
}

func privateServerShareRequest(scheme string, q url.Values) (Request, error) {
	code := firstQuery(q, "code")
	if code == "" || !strings.EqualFold(firstQuery(q, "type"), "Server") {
		return Request{}, errors.New("Roblox private server share link is incomplete or unsupported")
	}
	req := Request{
		Scheme:           scheme,
		LaunchMode:       "play",
		ShareCode:        code,
		ShareType:        "Server",
		hasWebsiteLaunch: true,
	}
	req.AndroidDeepLink = req.androidDeepLink()
	return req, nil
}

func (r Request) androidDeepLink() string {
	if r.IsPrivateServerShare() {
		q := url.Values{}
		q.Set("code", r.ShareCode)
		q.Set("type", "Server")
		return "roblox://navigation/share_links?" + q.Encode()
	}
	if r.PlaceID == 0 {
		return ""
	}
	q := url.Values{}
	q.Set("placeId", strconv.FormatInt(r.PlaceID, 10))
	if r.GameInstanceID != "" {
		q.Set("gameInstanceId", r.GameInstanceID)
	}
	if r.AccessCode != "" {
		q.Set("accessCode", r.AccessCode)
	}
	if r.ReservedServerCode != "" {
		q.Set("reservedServerAccessCode", r.ReservedServerCode)
	}
	if r.LinkCode != "" {
		q.Set("linkCode", r.LinkCode)
	}
	if r.LaunchData != "" {
		q.Set("launchData", r.LaunchData)
	}
	if r.UserID != 0 {
		q.Set("userId", strconv.FormatInt(r.UserID, 10))
	}
	if r.ReferredByPlayerID != 0 {
		q.Set("referredByPlayerId", strconv.FormatInt(r.ReferredByPlayerID, 10))
	}
	return "roblox://experiences/start?" + q.Encode()
}

func parsePlayerFields(payload string) map[string]string {
	values := make(map[string]string)
	rest := payload
	if i := strings.IndexByte(rest, '+'); i >= 0 && !strings.Contains(rest[:i], ":") {
		rest = rest[i+1:]
	}
	for rest != "" {
		key, after, ok := strings.Cut(rest, ":")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		end := nextPlayerField(after)
		var value string
		if end < 0 {
			value = after
			rest = ""
		} else {
			value = after[:end]
			rest = after[end+1:]
		}
		values[key] = pathUnescape(value)
	}
	return values
}

func nextPlayerField(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '+' {
			continue
		}
		j := i + 1
		for j < len(s) && isPlayerKeyByte(s[j]) {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == ':' && isPlayerFieldKey(s[i+1:j]) {
			return i
		}
	}
	return -1
}

func isPlayerKeyByte(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isPlayerFieldKey(key string) bool {
	_, ok := playerFieldKeys[strings.ToLower(key)]
	return ok
}

func pathUnescape(value string) string {
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return decoded
}

// playerFieldKeys are the named roblox-player: fields. gameinfo values may
// contain '+' (and must not be query-unescaped, which would turn '+' into space).
var playerFieldKeys = map[string]struct{}{
	"launchmode":               {},
	"gameinfo":                 {},
	"launchtime":               {},
	"placelauncherurl":         {},
	"browsertrackerid":         {},
	"robloxlocale":             {},
	"gamelocale":               {},
	"channel":                  {},
	"launchexp":                {},
	"placeid":                  {},
	"userid":                   {},
	"universeid":               {},
	"gameid":                   {},
	"gameinstanceid":           {},
	"accesscode":               {},
	"linkcode":                 {},
	"privateserverlinkcode":    {},
	"launchdata":               {},
	"referredbyplayerid":       {},
	"referralpage":             {},
	"reservedserveraccesscode": {},
	"trackerid":                {},
}

func isStudioMode(mode string) bool {
	switch mode {
	case "edit", "plugin", "asset", "avatar", "thumbnail":
		return true
	default:
		return false
	}
}

func firstValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := values[strings.ToLower(key)]; v != "" {
			return v
		}
	}
	return ""
}

func firstQuery(q url.Values, keys ...string) string {
	for _, key := range keys {
		if v := q.Get(key); v != "" {
			return v
		}
		for name, vals := range q {
			if strings.EqualFold(name, key) && len(vals) > 0 && vals[0] != "" {
				return vals[0]
			}
		}
	}
	return ""
}

func parseInt64(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func placeIDFromQuery(raw string) int64 {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	id, _ := parseInt64(firstQuery(u.Query(), "placeid", "placeId"))
	return id
}

func placeIDFromPath(path string) int64 {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return 0
	}
	if !strings.EqualFold(parts[0], "games") && !strings.EqualFold(parts[0], "experiences") {
		return 0
	}
	if strings.EqualFold(parts[1], "start") {
		return 0
	}
	id, _ := parseInt64(parts[1])
	return id
}
