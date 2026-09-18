// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
)

var webViewStartGame struct {
	sync.Mutex
	fn func(rbxuri.Request)
}

var webViewJavascriptSignal struct {
	sync.Mutex
	fn func(string)
}

var webViewUserClosed struct {
	sync.Mutex
	fn func()
}

// SetWebViewStartGame registers the in-session StartGame door used when the
// in-window overlay intercepts an official join URI. fn runs through the
// existing nativeAppBridgeV2StartGameWithParam path. A nil fn parks joins.
func SetWebViewStartGame(fn func(rbxuri.Request)) {
	webViewStartGame.Lock()
	webViewStartGame.fn = fn
	webViewStartGame.Unlock()
}

func dispatchWebViewStartGame(req rbxuri.Request) {
	webViewStartGame.Lock()
	fn := webViewStartGame.fn
	webViewStartGame.Unlock()
	if fn != nil {
		fn(req)
	}
}

// handoffWebViewJoin completes the browser transition before entering
// StartGame, which may span the experience lifetime. Ordinary URL forwarding
// closes the browser route; Hybrid launchGame only hides its host content.
func handoffWebViewJoin(req rbxuri.Request, leaveBrowser func(), start func(rbxuri.Request)) {
	if leaveBrowser != nil {
		leaveBrowser()
	}
	if start != nil {
		start(req)
	}
}

func dismissWebViewOverlay() {
	// Consume visibility before publication because the MessageBus callback
	// can re-enter the host close path. One presentation emits one close.
	if hideWebViewOverlay() {
		notifyWebViewUserClosed()
	}
}

// SetWebViewJavascriptSignal registers the official
// WebViewProtocol.signalJavascriptCallback door. cmd is the Hybrid JSON
// string; callers must never log it.
func SetWebViewJavascriptSignal(fn func(string)) {
	webViewJavascriptSignal.Lock()
	webViewJavascriptSignal.fn = fn
	webViewJavascriptSignal.Unlock()
}

func signalWebViewJavascript(cmd string) {
	webViewJavascriptSignal.Lock()
	fn := webViewJavascriptSignal.fn
	webViewJavascriptSignal.Unlock()
	if fn != nil {
		fn(cmd)
	}
}

// SetWebViewUserClosed registers the MessageBus handleWindowClose publisher
// used when the user dismisses the overlay, including an ordinary join URL.
func SetWebViewUserClosed(fn func()) {
	webViewUserClosed.Lock()
	webViewUserClosed.fn = fn
	webViewUserClosed.Unlock()
}

func notifyWebViewUserClosed() {
	webViewUserClosed.Lock()
	fn := webViewUserClosed.fn
	webViewUserClosed.Unlock()
	if fn != nil {
		fn()
	}
}

// WebViewURLClass is a log-safe classification of an overlay URL. Query
// strings (tickets, job ids, user ids) are never included.
type WebViewURLClass struct {
	Scheme    string
	Host      string
	PathClass string
	HasURL    bool
}

// ClassifyWebViewURL reports scheme/host/path class only.
func ClassifyWebViewURL(raw string) WebViewURLClass {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return WebViewURLClass{}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return WebViewURLClass{HasURL: true, PathClass: "opaque"}
	}
	path := strings.ToLower(u.EscapedPath())
	class := "other"
	switch {
	case strings.HasPrefix(path, "/games/start"):
		class = "/games/start"
	case strings.HasPrefix(path, "/games"):
		class = "/games"
	case strings.HasPrefix(path, "/experiences/start"):
		class = "/experiences/start"
	case strings.HasPrefix(path, "/experiences"):
		class = "/experiences"
	case path == "" || path == "/":
		class = "/"
	}
	return WebViewURLClass{
		Scheme:    strings.ToLower(u.Scheme),
		Host:      strings.ToLower(u.Hostname()),
		PathClass: class,
		HasURL:    true,
	}
}

// SoupCookieDomain is the libsoup domain string for a WebView cookie.
// soup_cookie_new treats a domain without a leading '.' as host-only, so
// Domain=.roblox.com must be passed as ".roblox.com" or www.roblox.com
// never receives the session cookie.
func SoupCookieDomain(domain string, hostOnly bool) string {
	d := strings.TrimSpace(strings.TrimPrefix(domain, "."))
	if d == "" {
		return ""
	}
	if hostOnly {
		return d
	}
	return "." + d
}

// WebViewWebsiteTheme maps NativeUser getTheme onto the website cookie
// value. Empty (Android omitted after login) follows the in-client dark
// chrome so the overlay is not a white www.roblox.com default.
func WebViewWebsiteTheme(theme string) string {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "light":
		return "Light"
	default:
		return "Dark"
	}
}

// EnsureWebViewThemeCookie mirrors Android SystemThemeProtocol.A/n:
// RBXThemeOverride=dark|light; Path=/. This override is host theme state,
// refreshed on each open; RBXTheme is not the Android WebView contract.
func EnsureWebViewThemeCookie(cookies []WebViewCookie, theme string) []WebViewCookie {
	out := make([]WebViewCookie, 0, len(cookies)+1)
	for _, c := range cookies {
		if !strings.EqualFold(strings.TrimSpace(c.Name), "RBXThemeOverride") {
			out = append(out, c)
		}
	}
	return append(out, WebViewCookie{
		Name:     "RBXThemeOverride",
		Value:    strings.ToLower(WebViewWebsiteTheme(theme)),
		Domain:   "www.roblox.com",
		HostOnly: true,
		Path:     "/",
		Secure:   true,
	})
}

// NormalizeWebViewURI rewrites in-app Hybrid/Stratus schemes onto URIs
// rbxuri can parse. robloxmobile: is the APK alias of roblox:.
func NormalizeWebViewURI(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "robloxmobile:"):
		return "roblox:" + raw[len("robloxmobile:"):]
	case strings.HasPrefix(lower, "lp:openurl"):
		if inner := extractLPOpenURL(raw); inner != "" {
			return inner
		}
	}
	return raw
}

func extractLPOpenURL(raw string) string {
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, "openurl")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(raw[idx+len("openurl"):])
	rest = strings.TrimLeft(rest, ":")
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "{") {
		var payload struct {
			URL string `json:"url"`
		}
		if json.Unmarshal([]byte(rest), &payload) == nil && strings.TrimSpace(payload.URL) != "" {
			return payload.URL
		}
	}
	if strings.HasPrefix(rest, "?") {
		q, err := url.ParseQuery(rest[1:])
		if err == nil {
			if u := q.Get("url"); u != "" {
				return u
			}
		}
		rest = rest[1:]
	}
	if strings.Contains(rest, "://") {
		return rest
	}
	return ""
}

// IsWebViewCloseCommand reports Stratus/Hybrid close commands that should
// hide the overlay instead of navigating.
func IsWebViewCloseCommand(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(lower, "bs:command") {
		return false
	}
	return strings.Contains(lower, "close") || strings.Contains(lower, "back")
}

// ParseWebViewJoinURI returns a StartGame request when raw is an official
// join URI. Listing pages under /games/<place> are not joins.
func ParseWebViewJoinURI(raw string) (rbxuri.Request, bool) {
	n := NormalizeWebViewURI(raw)
	if n == "" || !isWebViewJoinURI(n) {
		return rbxuri.Request{}, false
	}
	req, err := rbxuri.Parse(n)
	if err != nil || (req.PlaceID == 0 && req.UserID == 0) {
		return rbxuri.Request{}, false
	}
	return req, true
}

func isWebViewJoinURI(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(lower, "roblox:"):
		return true
	case strings.HasPrefix(lower, "roblox-player:"):
		return true
	case strings.HasPrefix(lower, "robloxmobile:"):
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.EscapedPath())
	return strings.HasPrefix(path, "/games/start") || strings.HasPrefix(path, "/experiences/start")
}

// HandleWebViewPolicyURI is the overlay policy decision: join, close, or
// default navigation. Returns true when the URI must not load in WebKit.
func HandleWebViewPolicyURI(raw string) bool {
	if IsWebViewCloseCommand(raw) {
		dismissWebViewOverlay()
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return HandleHybridExecuteRoblox(raw)
	}
	req, ok := ParseWebViewJoinURI(raw)
	if !ok {
		return false
	}
	// Android's ordinary WebView URL callback closes the fragment and
	// publishes handleWindowClose before forwarding the URL to Linking.
	// Hiding alone leaves GenericWebPage over the originating details route.
	logging.Logger(logging.CatX11).Info("WebView join handoff", "route", "uri", "browser", "dismiss")
	handoffWebViewJoin(req, dismissWebViewOverlay, dispatchWebViewStartGame)
	return true
}

type hybridExecute struct {
	ModuleID     string          `json:"moduleID"`
	FunctionName string          `json:"functionName"`
	Params       json.RawMessage `json:"params"`
}

type hybridLaunchParams struct {
	Request hybridLaunchRequest `json:"request"`
}

type hybridLaunchRequest struct {
	RequestType    string          `json:"requestType"`
	PlaceID        json.RawMessage `json:"placeId"`
	UserID         json.RawMessage `json:"userId"`
	GameInstanceID string          `json:"gameInstanceId"`
}

// HandleHybridExecuteRoblox plays Java __globalRobloxAndroidBridge__.executeRoblox.
// Game.launchGame starts the selected place through the existing StartGame
// door. Overlay.close hides the child. The command string is never logged.
func HandleHybridExecuteRoblox(raw string) bool {
	var cmd hybridExecute
	if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.ModuleID == "" {
		return false
	}
	signalWebViewJavascript(raw)
	switch {
	case strings.EqualFold(cmd.ModuleID, "Overlay") && strings.EqualFold(cmd.FunctionName, "close"):
		dismissWebViewOverlay()
		return true
	case strings.EqualFold(cmd.ModuleID, "Game") && strings.EqualFold(cmd.FunctionName, "launchGame"):
		req, ok := parseHybridLaunchGame(cmd.Params)
		if ok {
			// Hybrid posts RequestGame through Android's experience manager;
			// its inspected path does not publish WebView.handleWindowClose.
			logging.Logger(logging.CatX11).Info("WebView join handoff", "route", "hybrid", "browser", "hide")
			handoffWebViewJoin(req, HideWebViewOverlay, dispatchWebViewStartGame)
		}
		return true
	}
	return true
}

func parseHybridLaunchGame(params json.RawMessage) (rbxuri.Request, bool) {
	var p hybridLaunchParams
	if json.Unmarshal(params, &p) != nil {
		return rbxuri.Request{}, false
	}
	rt := strings.TrimSpace(p.Request.RequestType)
	if rt != "" && !isHybridJoinRequestType(rt) {
		return rbxuri.Request{}, false
	}
	place := parseHybridPlaceID(p.Request.PlaceID)
	user := parseHybridPlaceID(p.Request.UserID)
	if strings.EqualFold(rt, "RequestFollowUser") {
		if user == 0 {
			return rbxuri.Request{}, false
		}
	} else if place == 0 {
		return rbxuri.Request{}, false
	}
	return rbxuri.Request{
		Scheme:         "roblox",
		PlaceID:        place,
		UserID:         user,
		GameInstanceID: p.Request.GameInstanceID,
	}, true
}

func isHybridJoinRequestType(rt string) bool {
	switch strings.ToLower(rt) {
	case "requestgame", "requestgamejob", "requestfollowuser":
		return true
	default:
		return false
	}
}

func parseHybridPlaceID(raw json.RawMessage) int64 {
	s := strings.TrimSpace(string(raw))
	s = strings.Trim(s, `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
