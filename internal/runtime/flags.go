// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Official public client-settings endpoint the Android app uses.
const androidAppSettingsURL = "https://clientsettingscdn.roblox.com/v2/settings/application/AndroidApp"

const desktopAppPolicyFlag = "FStringAppConfigurationOverrideAppPolicy"

// applicationSettingsFromResponse extracts the flag map as a JSON object.
// Does not log flag names or values.
func applicationSettingsFromResponse(body []byte) (string, int, error) {
	return applicationSettingsFromResponseWithOverrides(body, nil)
}

func applicationSettingsFromResponseWithOverrides(body []byte, overrides map[string]any) (string, int, error) {
	var wrap struct {
		ApplicationSettings map[string]any `json:"applicationSettings"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return "", 0, err
	}
	m := wrap.ApplicationSettings
	if len(m) == 0 {
		var direct map[string]any
		if err := json.Unmarshal(body, &direct); err == nil {
			if _, has := direct["applicationSettings"]; !has {
				m = direct
			}
		}
	}
	n := len(m)
	if n == 0 {
		m = map[string]any{}
	} else {
		m = cloneFlagMap(m)
	}
	// Explicit renderer choices own this narrow conflict set. Auto emits
	// PreferVulkan only when the Vulkan WSI path is the resolved platform.
	if _, ok := overrides["FFlagDebugGraphicsPreferOpenGL"]; ok {
		delete(m, "FFlagDebugGraphicsPreferVulkan")
		delete(m, "FFlagDebugGraphicsDisableVulkan")
	}
	if _, ok := overrides["FFlagDebugGraphicsPreferVulkan"]; ok {
		delete(m, "FFlagDebugGraphicsPreferOpenGL")
		delete(m, "FFlagDebugGraphicsDisableVulkan")
	}
	for k, v := range overrides {
		if v == nil {
			// A nil override removes a shipped key (used for the
			// `_PlaceFilter` companions of raised FastLog groups).
			delete(m, k)
			continue
		}
		m[k] = v
	}
	// 7a14858 / ShadowFValuesEnabled defaults false and is absent from CDN.
	// Commit 0x2e77680 runs apply (store+0x10 list → shadow maps) and sets
	// 7a148d8 only when that C++ byte is already 1. JSON overlay does not
	// set the byte in time; launch pokes it. JNI nativeGetFFlag still reads
	// a different map (store+0x1a8).
	m["ShadowFValuesEnabled"] = "True"
	raw, err := json.Marshal(map[string]any{
		"applicationSettings": m,
		"ClientAppSettings":   m,
	})
	if err != nil {
		return "", 0, err
	}
	return string(raw), n, nil
}

func cloneFlagMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// flogOverridesEnv is a diagnostics-only knob: a space- or `;`-separated
// list of official FastLog groups and levels, e.g.
// `VoiceChatLogs=Verbose,7 SoundTrace=Verbose`. Each entry raises the
// `FLog<Group>` and `DFLog<Group>` levels in the client settings handed to
// Roblox. It can only change how much the official client logs; it never
// sets an FFlag/FInt/FString and never alters engine behavior.
const flogOverridesEnv = "TIPSY_FLOG_OVERRIDES"

// flogLevelNames are the named FastLog severities the official settings use
// (`FLogAudio = Info`, `FLogX = Verbose,6`, `..._PlaceFilter = Verbose;<placeId>`).
// A value is `<number>`, `<Severity>`, or `<Severity>,<number>`.
var flogLevelNames = map[string]bool{"error": true, "warning": true, "info": true, "debug": true, "verbose": true, "trace": true}

// flogFilterSuffixes are the per-place / per-datacenter companions Roblox
// ships next to a group (`FLogVoiceChatLogs_PlaceFilter = Verbose;1111…`).
// Raising a group must drop them, or the filter pins the group back to its
// shipped value in every other place. A nil override value means delete.
var flogFilterSuffixes = []string{"_PlaceFilter", "_DataCenterFilter"}

// flogOverrides parses a flogOverridesEnv value. Malformed entries are
// skipped: the group must be an identifier and the level a small integer or
// one of flogLevelNames. The returned map counts one group as len/2 keys
// with non-nil values.
func flogOverrides(spec string) map[string]any {
	out := map[string]any{}
	items := strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || unicode.IsSpace(r) })
	for _, item := range items {
		group, level, ok := strings.Cut(item, "=")
		if !ok || !isFlogGroupName(group) || !isFlogLevel(level) {
			continue
		}
		for _, prefix := range []string{"FLog", "DFLog"} {
			out[prefix+group] = level
			for _, suffix := range flogFilterSuffixes {
				out[prefix+group+suffix] = nil
			}
		}
	}
	return out
}

// flogGroupCount counts the groups in a flogOverrides result.
func flogGroupCount(m map[string]any) int {
	n := 0
	for k, v := range m {
		if v != nil && strings.HasPrefix(k, "FLog") {
			n++
		}
	}
	return n
}

func isFlogGroupName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func isFlogLevel(s string) bool {
	name, number, hasNumber := strings.Cut(s, ",")
	if flogLevelNames[strings.ToLower(name)] {
		return !hasNumber || isFlogNumber(number)
	}
	return !hasNumber && isFlogNumber(name)
}

func isFlogNumber(s string) bool {
	if s == "" || len(s) > 3 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// luaLogEnv is the companion diagnostics knob for Roblox's own Lua Logger:
// `level` or `level:pattern`, e.g. `debug:Voice`. It sets only the two
// logging fast strings the official CoreScripts Logger reads
// (`FStringDebugLuaLogLevel`, `FStringDebugLuaLogPattern`) so Lua-side
// decisions print into the Player log as `[Name (level)] - message`.
const luaLogEnv = "TIPSY_LUA_LOG"

const (
	luaLogLevelFlag   = "FStringDebugLuaLogLevel"
	luaLogPatternFlag = "FStringDebugLuaLogPattern"
)

var luaLogLevels = map[string]bool{"trace": true, "debug": true, "info": true, "warning": true, "error": true}

// luaLogOverrides parses a luaLogEnv value; anything but a known level and
// a short printable pattern yields nothing.
func luaLogOverrides(spec string) map[string]any {
	level, pattern, _ := strings.Cut(strings.TrimSpace(spec), ":")
	level = strings.ToLower(strings.TrimSpace(level))
	pattern = strings.TrimSpace(pattern)
	if !luaLogLevels[level] || len(pattern) > 64 {
		return nil
	}
	for _, r := range pattern {
		if r < 0x21 || r > 0x7e {
			return nil
		}
	}
	out := map[string]any{luaLogLevelFlag: level}
	if pattern != "" {
		out[luaLogPatternFlag] = pattern
	}
	return out
}

// withFlogOverrides merges the flogOverridesEnv groups and the luaLogEnv
// logger settings into overrides. It returns the number of FastLog groups
// raised and whether the Lua logger was configured.
func withFlogOverrides(overrides map[string]any, spec, luaSpec string) (map[string]any, int, bool) {
	extra := flogOverrides(spec)
	lua := luaLogOverrides(luaSpec)
	if len(extra) == 0 && len(lua) == 0 {
		return overrides, 0, false
	}
	overrides = cloneFlagMap(overrides)
	for k, v := range extra {
		overrides[k] = v
	}
	for k, v := range lua {
		overrides[k] = v
	}
	return overrides, flogGroupCount(extra), len(lua) > 0
}

func loadAndroidAppSettings(ctx context.Context, cachePath, version string) (string, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	overrides, overrideErr := clientsettings.New().LoadOverrides(ctx)
	if overrideErr != nil {
		// A settings-permission/symlink failure must not prevent use of the
		// official client settings; the rejected override is simply not applied.
		overrides = nil
	}
	// The named AppConfiguration override is a terminal whole-policy response,
	// not a field merge. The OS-specific loader therefore returns a complete
	// cached policy or nothing; any malformed, ambiguous, or touch-mode input
	// fails closed and leaves Roblox's ordinary policy path intact.
	var policyApplied bool
	overrides, policyApplied, overrideErr = withDesktopAppPolicyOverride(cachePath, overrides)
	if overrideErr != nil {
		logging.Logger(logging.CatGameActivity).Info("desktop app policy override omitted", "err", overrideErr)
	} else if policyApplied {
		logging.Logger(logging.CatGameActivity).Info("desktop app policy override applied", "presentation_fields", 4)
	}
	var (
		flogGroups int
		luaLog     bool
	)
	overrides, flogGroups, luaLog = withFlogOverrides(overrides, os.Getenv(flogOverridesEnv), os.Getenv(luaLogEnv))
	if flogGroups > 0 || luaLog {
		logging.Logger(logging.CatGameActivity).Info("log level overrides applied", "flogGroups", flogGroups, "luaLogger", luaLog)
	}
	body, err := fetchAndroidAppSettings(ctx, version)
	if err != nil {
		if cachePath != "" {
			if cached, rerr := os.ReadFile(cachePath); rerr == nil && len(cached) > 2 {
				return applicationSettingsFromResponseWithOverrides(cached, overrides)
			}
		}
		return "", 0, err
	}
	js, n, err := applicationSettingsFromResponseWithOverrides(body, overrides)
	if err != nil {
		return "", 0, err
	}
	if cachePath != "" && n > 0 {
		_ = os.WriteFile(cachePath, body, 0o600)
	}
	return js, n, nil
}

func withDesktopAppPolicyOverride(cachePath string, overrides map[string]any) (map[string]any, bool, error) {
	policy, err := desktopAppPolicyOverride(filepath.Dir(cachePath))
	if err != nil {
		return overrides, false, err
	}
	if policy == "" {
		return overrides, false, nil
	}
	overrides = cloneFlagMap(overrides)
	overrides[desktopAppPolicyFlag] = policy
	return overrides, true, nil
}

func fetchAndroidAppSettings(ctx context.Context, version string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// The bounded timeout stays; the launch context additionally lets a
	// cancelled launch abort this startup fetch immediately instead of
	// blocking the path for the full window.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, androidAppSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", robloxUserAgent(version))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client settings HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
