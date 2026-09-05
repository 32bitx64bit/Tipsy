// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/jni"
)

func completeTestAppPolicy() map[string]any {
	policy := make(map[string]any, minCompleteAppPolicyFields+8)
	for i := 0; i < minCompleteAppPolicyFields; i++ {
		policy["OfficialField"+strings.Repeat("x", i/26)+string(rune('a'+i%26))] = i
	}
	policy["PlatformGroup"] = "Tablet"
	policy["SystemBarPlacement"] = "Bottom"
	policy["ShowUncheckedBadge"] = true
	policy["DevicePreferencesPersistentPresenceVariant"] = ""
	policy["NestedOfficialValue"] = map[string]any{"kept": []any{true, "value", 7}}
	return policy
}

func writeTestAppPolicyCache(t *testing.T, policies map[string]any) (string, string) {
	t.Helper()
	filesDir := filepath.Join(t.TempDir(), "files")
	dir := filepath.Join(filesDir, "appData", "LocalStorage")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := make(map[string]any, len(policies)+1)
	for key, policy := range policies {
		raw, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		configuration[key] = string(raw)
	}
	configuration["GUAC:ignored:app-patch"] = `{"not":"a policy"}`
	configurationRaw, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := json.Marshal(map[string]any{"AppConfiguration": string(configurationRaw)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "appStorage.json")
	if err := os.WriteFile(path, outer, 0o600); err != nil {
		t.Fatal(err)
	}
	return filesDir, path
}

func resetDesktopInputMode(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("TIPSY_INPUT_DEVICE", mode)
	jni.ResetPointerDeviceMode()
	t.Cleanup(jni.ResetPointerDeviceMode)
}

func decodePolicyOverride(t *testing.T, override string) map[string]any {
	t.Helper()
	var policy map[string]any
	if err := json.Unmarshal([]byte(override), &policy); err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestDesktopAppPolicyExactPresentationOverlay(t *testing.T) {
	resetDesktopInputMode(t, "")
	original := completeTestAppPolicy()
	filesDir, _ := writeTestAppPolicyCache(t, map[string]any{
		"GUAC:-1:app-policy":   original,
		"GUAC:auth:app-policy": original,
	})
	override, err := desktopAppPolicyOverride(filesDir)
	if err != nil {
		t.Fatal(err)
	}
	got := decodePolicyOverride(t, override)
	originalJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	want := decodePolicyOverride(t, string(originalJSON))
	want["UseGridHomePage"] = true
	want["UseGridPageLayout"] = true
	want["SystemBarPlacement"] = "Left"
	want["ShouldSystemBarUsuallyBePresent"] = true
	if !reflect.DeepEqual(got, want) {
		t.Fatal("desktop app policy changed fields outside the approved presentation overlay")
	}
	if got["PlatformGroup"] != "Tablet" || got["ShowUncheckedBadge"] != true || got["DevicePreferencesPersistentPresenceVariant"] != "" {
		t.Fatalf("identity or status fields changed: %v", got)
	}
}

func TestDesktopAppPolicyGuestOnlyAndIdenticalSelection(t *testing.T) {
	resetDesktopInputMode(t, "")
	for _, policies := range []map[string]any{
		{"GUAC:-1:app-policy": completeTestAppPolicy()},
		{"GUAC:-1:app-policy": completeTestAppPolicy(), "GUAC:auth:app-policy": completeTestAppPolicy()},
	} {
		filesDir, _ := writeTestAppPolicyCache(t, policies)
		if got, err := desktopAppPolicyOverride(filesDir); err != nil || got == "" {
			t.Fatalf("complete policy rejected: value=%q err=%v", got, err)
		}
	}
}

func TestDesktopAppPolicySelectionUsesJSONSemanticEquality(t *testing.T) {
	left := completeTestAppPolicy()
	right := completeTestAppPolicy()
	left["NumericValue"] = json.Number("1.0")
	right["NumericValue"] = json.Number("1")
	filesDir, _ := writeTestAppPolicyCache(t, map[string]any{
		"GUAC:-1:app-policy":   left,
		"GUAC:auth:app-policy": right,
	})
	resetDesktopInputMode(t, "")
	if got, err := desktopAppPolicyOverride(filesDir); err != nil || got == "" {
		t.Fatalf("semantically identical policies rejected: value=%q err=%v", got, err)
	}
}

func TestDesktopAppPolicyFailsClosedOnAmbiguityOrMalformedData(t *testing.T) {
	resetDesktopInputMode(t, "")
	different := completeTestAppPolicy()
	different["OfficialFielda"] = "different"
	filesDir, _ := writeTestAppPolicyCache(t, map[string]any{
		"GUAC:-1:app-policy":   completeTestAppPolicy(),
		"GUAC:auth:app-policy": different,
	})
	if got, err := desktopAppPolicyOverride(filesDir); err == nil || got != "" {
		t.Fatalf("differing policies accepted: value=%q err=%v", got, err)
	}

	partial := map[string]any{
		"PlatformGroup": "Tablet", "SystemBarPlacement": "Bottom",
		"ShowUncheckedBadge": true, "DevicePreferencesPersistentPresenceVariant": "",
	}
	filesDir, path := writeTestAppPolicyCache(t, map[string]any{"GUAC:-1:app-policy": partial})
	if got, err := desktopAppPolicyOverride(filesDir); err == nil || got != "" {
		t.Fatalf("partial policy accepted: value=%q err=%v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"AppConfiguration":{"nested":"not a string"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := desktopAppPolicyOverride(filesDir); err == nil || got != "" {
		t.Fatalf("nested malformed cache accepted: value=%q err=%v", got, err)
	}
}

func TestDesktopAppPolicyTouchModeOmitsOverride(t *testing.T) {
	resetDesktopInputMode(t, "touch")
	filesDir, _ := writeTestAppPolicyCache(t, map[string]any{"GUAC:-1:app-policy": completeTestAppPolicy()})
	if got, err := desktopAppPolicyOverride(filesDir); err != nil || got != "" {
		t.Fatalf("touch mode received desktop policy: value=%q err=%v", got, err)
	}
}

func TestDesktopAppPolicyRejectsSymlinkAndOversizeCache(t *testing.T) {
	resetDesktopInputMode(t, "")
	filesDir, path := writeTestAppPolicyCache(t, map[string]any{"GUAC:-1:app-policy": completeTestAppPolicy()})
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.Rename(path, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if got, err := desktopAppPolicyOverride(filesDir); err == nil || got != "" {
		t.Fatalf("symlink cache accepted: value=%q err=%v", got, err)
	}

	filesDir, path = writeTestAppPolicyCache(t, map[string]any{"GUAC:-1:app-policy": completeTestAppPolicy()})
	if err := os.WriteFile(path, make([]byte, maxDesktopAppPolicyCacheSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := desktopAppPolicyOverride(filesDir); err == nil || got != "" {
		t.Fatalf("oversize cache accepted: value=%q err=%v", got, err)
	}
}

func TestDesktopAppPolicyAppearsAsFullFastStringInBothEnvelopes(t *testing.T) {
	resetDesktopInputMode(t, "")
	filesDir, _ := writeTestAppPolicyCache(t, map[string]any{"GUAC:-1:app-policy": completeTestAppPolicy()})
	policy, err := desktopAppPolicyOverride(filesDir)
	if err != nil {
		t.Fatal(err)
	}
	overrides, applied, err := withDesktopAppPolicyOverride(filepath.Join(filesDir, "ClientAppSettings.json"), map[string]any{"ExistingOverride": "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if !applied || overrides[desktopAppPolicyFlag] != policy || overrides["ExistingOverride"] != "kept" {
		t.Fatalf("desktop app-policy integration missing: applied=%t overrides=%v", applied, overrides)
	}
	raw, _, err := applicationSettingsFromResponseWithOverrides([]byte(`{"applicationSettings":{"OfficialOnly":"kept"}}`), overrides)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"applicationSettings", "ClientAppSettings"} {
		got, ok := envelope[name][desktopAppPolicyFlag].(string)
		if !ok || got != policy || len(decodePolicyOverride(t, got)) < minCompleteAppPolicyFields {
			t.Fatalf("%s missing complete app-policy FastString", name)
		}
	}
}
