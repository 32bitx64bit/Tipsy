// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
)

// Development builds accept arbitrary custom Fast Flags. The gate pair still
// wins in the persistent layer while an unrelated custom flag stays in the
// settings blob.
func TestLoadAndroidAppOverridesCustomFastFlagsWinInPersistentLayer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TIPSY_FLOG_OVERRIDES", "")
	t.Setenv("TIPSY_LUA_LOG", "")
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
	want := map[string]any{gateKeys[0]: "False", gateKeys[1]: "2500"}
	if ext := decodeExternalOverrides(t, got.external); !reflect.DeepEqual(ext, want) {
		t.Fatalf("custom external layer=%v want %v", ext, want)
	}
	for _, key := range gateKeys {
		if _, exists := got.values[key]; exists {
			t.Fatalf("custom persistent key %q duplicated in settings blob: %v", key, got.values)
		}
	}
	if got.values["FFlagCustomOther"] != "True" {
		t.Fatalf("unrelated custom flag lost from settings blob: %v", got.values)
	}
	// Settings blob must not contain the gate keys either.
	js, _, err := applicationSettingsFromResponseWithOverrides([]byte(`{"applicationSettings":{"Official":"kept"}}`), got.values)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range gateKeys {
		if strings.Contains(js, key) {
			t.Fatalf("arg0 settings JSON carries persistent key %q: %s", key, js)
		}
	}
}
