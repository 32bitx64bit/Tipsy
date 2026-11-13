// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package runtime

import (
	"os"
	"strings"
	"unicode"
)

// flogOverridesEnv is a diagnostics-only knob: a space- or `;`-separated list
// of FastLog groups and levels (e.g. `VoiceChatLogs=Verbose,7`) that raises the
// matching FLog/DFLog levels. It only changes how much the client logs; it never
// sets an FFlag/FInt/FString or alters engine behavior.
const flogOverridesEnv = "TIPSY_FLOG_OVERRIDES"

// flogLevelNames are the named FastLog severities the official settings use.
// A level value is `<number>`, `<Severity>`, or `<Severity>,<number>`.
var flogLevelNames = map[string]bool{"error": true, "warning": true, "info": true, "debug": true, "verbose": true, "trace": true}

// flogFilterSuffixes are the per-place / per-datacenter companions Roblox ships
// next to a group. Raising a group must drop them, or the filter pins the group
// to its shipped value elsewhere. A nil override value means delete.
var flogFilterSuffixes = []string{"_PlaceFilter", "_DataCenterFilter"}

// flogOverrides parses a flogOverridesEnv value, skipping malformed entries
// (the group must be an identifier; the level a small integer or a known name).
// Each group yields FLog and DFLog keys, both with non-nil values.
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

// luaLogEnv is a diagnostics-only knob for Roblox's Lua Logger: `level` or
// `level:pattern` (e.g. `debug:Voice`). It sets only the two logging fast
// strings, so Lua-side decisions print into the Player log.
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

// withFlogOverrides merges the flogOverridesEnv groups and luaLogEnv logger
// settings into overrides, returning the count of FastLog groups raised and
// whether the Lua logger was configured.
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

// applyDiagnosticOverrides reads the process diagnostics knobs. It is the only
// place that names them, and it is absent from official_release binaries.
func applyDiagnosticOverrides(overrides map[string]any) (map[string]any, int, bool) {
	return withFlogOverrides(overrides, os.Getenv(flogOverridesEnv), os.Getenv(luaLogEnv))
}
