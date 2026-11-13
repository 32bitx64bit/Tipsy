// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
)

// Official releases accept only the curated Fast Flag allowlist. An allowed
// custom value still wins over the gate defaults in the persistent layer.
func TestLoadAndroidAppOverridesAllowlistedCustomFastFlagsWinInPersistentLayer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svc := clientsettings.New()
	if err := os.MkdirAll(filepath.Dir(svc.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"fastFlags":[{"name":"FFlagRenderPipelineVersioningFlag4","value":"False"},{"name":"FIntEnableRenderPipelineHundredthsPercent","value":"2500"}]}`
	if err := os.WriteFile(svc.Path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadAndroidAppOverrides(context.Background(), filepath.Join(t.TempDir(), "ClientAppSettings.json"), false, graphics.ProbeRendererCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{gateKeys[0]: "False", gateKeys[1]: "2500"}
	if ext := decodeExternalOverrides(t, got.external); !reflect.DeepEqual(ext, want) {
		t.Fatalf("allowlisted custom external layer=%v want %v", ext, want)
	}
	for _, key := range gateKeys {
		if _, exists := got.values[key]; exists {
			t.Fatalf("custom persistent key %q duplicated in settings blob: %v", key, got.values)
		}
	}
	if backups, err := filepath.Glob(svc.Path + ".invalid-*"); err != nil || len(backups) != 0 {
		t.Fatalf("allowlisted settings document was recovered (backups=%v err=%v)", backups, err)
	}
}

// An unallowlisted custom Fast Flag makes the whole document malformed under
// official_release. loadDocument must move it aside and fall back to safe
// defaults rather than applying any of it, including the gate pair.
func TestLoadAndroidAppOverridesUnallowlistedFastFlagRecoversToDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svc := clientsettings.New()
	if err := os.MkdirAll(filepath.Dir(svc.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"fastFlags":[{"name":"FFlagRenderPipelineVersioningFlag4","value":"False"},{"name":"FIntEnableRenderPipelineHundredthsPercent","value":"2500"},{"name":"FFlagCustomOther","value":"True"}]}`
	if err := os.WriteFile(svc.Path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadAndroidAppOverrides(context.Background(), filepath.Join(t.TempDir(), "ClientAppSettings.json"), false, graphics.ProbeRendererCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{gateKeys[0]: "True", gateKeys[1]: "10000"}
	if ext := decodeExternalOverrides(t, got.external); !reflect.DeepEqual(ext, want) {
		t.Fatalf("recovered external layer=%v want safe defaults %v", ext, want)
	}
	for _, key := range gateKeys {
		if _, exists := got.values[key]; exists {
			t.Fatalf("recovered settings blob kept persistent key %q: %v", key, got.values)
		}
	}
	if _, exists := got.values["FFlagCustomOther"]; exists {
		t.Fatalf("recovered settings blob kept unallowlisted flag: %v", got.values)
	}
	backups, err := filepath.Glob(svc.Path + ".invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("malformed settings backups=%v want exactly one", backups)
	}
	raw, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != doc {
		t.Fatalf("malformed settings backup lost the original document: %q", raw)
	}
	rewritten, err := os.ReadFile(svc.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(rewritten) == doc || len(rewritten) == 0 {
		t.Fatalf("malformed settings document was not replaced with defaults: %q", rewritten)
	}
}
