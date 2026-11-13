// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package runtime

// withFlogOverrides is a no-op in official_release builds: the diagnostic
// TIPSY_FLOG_OVERRIDES and TIPSY_LUA_LOG knobs are not compiled in, so no
// spec can alter client settings.
func withFlogOverrides(overrides map[string]any, _, _ string) (map[string]any, int, bool) {
	return overrides, 0, false
}

// applyDiagnosticOverrides never reads the diagnostic environment in
// official_release builds.
func applyDiagnosticOverrides(overrides map[string]any) (map[string]any, int, bool) {
	return withFlogOverrides(overrides, "", "")
}
