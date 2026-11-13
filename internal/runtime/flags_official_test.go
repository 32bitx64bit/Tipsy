// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/graphics"
)

func TestWithFlogOverridesIsNoopInOfficialRelease(t *testing.T) {
	base := map[string]any{"Existing": "kept"}
	merged, groups, lua := withFlogOverrides(base, "VoiceChatLogs=Verbose,7", "debug:Voice")
	if groups != 0 || lua {
		t.Fatalf("groups = %d lua = %v, want 0 false", groups, lua)
	}
	if len(merged) != 1 || merged["Existing"] != "kept" {
		t.Fatalf("merged = %v, want the input unchanged", merged)
	}
	if _, leaked := merged["FLogVoiceChatLogs"]; leaked {
		t.Fatalf("diagnostic override leaked into settings: %v", merged)
	}
}

func TestLoadAndroidAppOverridesIgnoresDiagnosticEnvInOfficialRelease(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TIPSY_FLOG_OVERRIDES", "VoiceChatLogs=Verbose,7")
	t.Setenv("TIPSY_LUA_LOG", "debug:Voice")

	got, err := loadAndroidAppOverrides(context.Background(), filepath.Join(t.TempDir(), "ClientAppSettings.json"), false, graphics.ProbeRendererCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	for key := range got.values {
		if strings.HasPrefix(key, "FLog") || strings.HasPrefix(key, "DFLog") ||
			key == "FStringDebugLuaLogLevel" || key == "FStringDebugLuaLogPattern" {
			t.Fatalf("official_release applied diagnostic env override %q: %v", key, got.values)
		}
	}
	if strings.Contains(got.external, "FLog") || strings.Contains(got.external, "FStringDebugLuaLog") {
		t.Fatalf("official_release leaked diagnostic env into external layer: %s", got.external)
	}
}
