// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
)

func TestRuntimeDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	want := filepath.Join(tmp, "tipsy", "runtime")
	if got := RuntimeDir(); got != want {
		t.Fatalf("RuntimeDir()=%s want %s", got, want)
	}
}

func TestSetupNoPaths(t *testing.T) {
	_, err := Setup(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "pass official APK/dir") {
		t.Fatalf("err=%v", err)
	}
	_, err = Setup(context.Background(), []string{})
	if err == nil || !strings.Contains(err.Error(), "pass official APK/dir") {
		t.Fatalf("err=%v", err)
	}
}

func TestSetupExtractsIntoRuntimeDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache-home"))
	src := t.TempDir()
	lib := []byte("runtime-setup-lib")
	apkPath := writeZip(t, filepath.Join(src, "base.apk"), map[string][]byte{
		"lib/x86_64/libdummy.so": lib,
		"assets/hello.txt":       []byte("hi"),
	})

	res, err := Setup(context.Background(), []string{apkPath})
	if err != nil {
		t.Fatal(err)
	}
	wantDest := RuntimeDir()
	if res.DestDir != wantDest {
		t.Fatalf("DestDir=%s want %s", res.DestDir, wantDest)
	}
	if len(res.Libraries) != 1 {
		t.Fatalf("libraries=%d", len(res.Libraries))
	}
	got, err := os.ReadFile(res.Libraries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, lib) {
		t.Fatal("extracted lib mismatch")
	}
	if _, err := os.Stat(filepath.Join(wantDest, "apk", "base.apk")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wantDest, "assets", "hello.txt")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta apk.Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Libraries) != 1 || meta.Libraries[0].Name != "libdummy.so" {
		t.Fatalf("meta %+v", meta)
	}
}

func TestSetupUpdateReplacesRuntimeButPreservesPersistentFiles(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data-home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache-home"))
	src := t.TempDir()
	v1 := writeZip(t, filepath.Join(src, "v1.apk"), map[string][]byte{
		"lib/x86_64/libdummy.so": []byte("runtime-v1"),
		"assets/version.txt":     []byte("v1"),
	})
	if _, err := Setup(context.Background(), []string{v1}); err != nil {
		t.Fatal(err)
	}
	layout := AppStorage()
	sentinel := filepath.Join(layout.FilesDir, "appData", "synthetic-account-state.bin")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0xff, 'o', 'p', 'a', 'q', 'u', 'e'}
	if err := os.WriteFile(sentinel, want, 0o600); err != nil {
		t.Fatal(err)
	}

	v2 := writeZip(t, filepath.Join(src, "v2.apk"), map[string][]byte{
		"lib/x86_64/libdummy.so": []byte("runtime-v2"),
		"assets/version.txt":     []byte("v2"),
	})
	if _, err := Setup(context.Background(), []string{v2}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("persistent sentinel after update=%x err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(RuntimeDir(), "lib", "x86_64", "libdummy.so")); err != nil || string(got) != "runtime-v2" {
		t.Fatalf("runtime library=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(RuntimeDir(), "assets", "version.txt")); err != nil || string(got) != "v2" {
		t.Fatalf("runtime asset=%q err=%v", got, err)
	}
	assertMode(t, layout.FilesDir, 0o700)
	assertMode(t, sentinel, 0o600)
}

func TestSetupContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Setup(ctx, []string{"ignored.apk"})
	if err == nil {
		t.Fatal("expected context error")
	}
}

func writeZip(t *testing.T, path string, files map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	fixed := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	for _, n := range names {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate, Modified: fixed}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(files[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplicationSettingsFromResponse(t *testing.T) {
	raw := []byte(`{"applicationSettings":{"FFlagFoo":"True","FIntBar":"1"}}`)
	got, n, err := applicationSettingsFromResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count=%d want 2", n)
	}
	if !strings.Contains(got, `"ClientAppSettings"`) || !strings.Contains(got, `"applicationSettings"`) {
		t.Fatalf("missing JSON keys: %s", got)
	}
	if strings.Count(got, `"FFlagFoo":"True"`) != 2 || strings.Count(got, `"FIntBar":"1"`) != 2 {
		t.Fatalf("dual-key envelope did not serialize both maps: %s", got)
	}
	if !strings.Contains(got, `"ShadowFValuesEnabled":"True"`) {
		t.Fatalf("missing commit-gate overlay: %s", got)
	}
	if !strings.Contains(got, `"FFlagFoo":"True"`) || !strings.Contains(got, `"FIntBar":"1"`) {
		t.Fatalf("json=%s", got)
	}
	empty, n, err := applicationSettingsFromResponse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || !strings.Contains(empty, `"ClientAppSettings"`) || !strings.Contains(empty, `"applicationSettings"`) {
		t.Fatalf("empty envelope: n=%d json=%s", n, empty)
	}
}

func decodeExternalOverrides(t *testing.T, raw string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("external override layer %q is not a JSON object: %v", raw, err)
	}
	return got
}

var gateKeys = []string{clientsettings.FlagRenderPipelineVersioning, clientsettings.IntEnableRenderPipelineHundredthsPercent}

func TestSplitStartupOverridesCarriesMeshLodGateAndOfficialExternalPath(t *testing.T) {
	const gateJSON = `{"FFlagRenderPipelineVersioningFlag4":"True","FIntEnableRenderPipelineHundredthsPercent":"10000"}`
	rendererKeys := []string{flagPreferOpenGL, flagPreferVulkan, flagDisableOpenGL, flagDisableVulkan, flagDisableVulkan11}
	cases := []struct {
		name       string
		input      map[string]any
		testOpenGL bool
		wantExt    map[string]any
		wantSet    map[string]any
	}{
		{
			name:    "nil overrides carry only the gate pair",
			input:   nil,
			wantExt: map[string]any{gateKeys[0]: "True", gateKeys[1]: "10000"},
			wantSet: map[string]any{},
		},
		{
			name:    "ordinary launch keeps unrelated flags in the settings blob",
			input:   map[string]any{"FFlagCustom": "True"},
			wantExt: map[string]any{gateKeys[0]: "True", gateKeys[1]: "10000"},
			wantSet: map[string]any{"FFlagCustom": "True"},
		},
		{
			name:    "explicit Vulkan stays in the settings blob",
			input:   map[string]any{flagPreferVulkan: "True"},
			wantExt: map[string]any{gateKeys[0]: "True", gateKeys[1]: "10000"},
			wantSet: map[string]any{flagPreferVulkan: "True"},
		},
		{
			name: "explicit OpenGL shares the object with the gate pair",
			input: map[string]any{
				flagPreferOpenGL:    "True",
				flagDisableVulkan:   "True",
				flagDisableVulkan11: "True",
				"FFlagCustom":       "kept-out",
			},
			wantExt: map[string]any{
				flagPreferOpenGL:    "True",
				flagDisableVulkan:   "True",
				flagDisableVulkan11: "True",
				gateKeys[0]:         "True",
				gateKeys[1]:         "10000",
			},
			wantSet: map[string]any{"FFlagCustom": "kept-out"},
		},
		{
			name:       "process-only OpenGL replaces Vulkan keys",
			input:      map[string]any{flagPreferVulkan: "True", flagDisableOpenGL: "True", "FFlagCustom": "kept"},
			testOpenGL: true,
			wantExt: map[string]any{
				flagPreferOpenGL:    "True",
				flagDisableVulkan:   "True",
				flagDisableVulkan11: "True",
				gateKeys[0]:         "True",
				gateKeys[1]:         "10000",
			},
			wantSet: map[string]any{"FFlagCustom": "kept"},
		},
		{
			name:    "custom Fast Flags win and leave the settings blob",
			input:   map[string]any{gateKeys[0]: "False", gateKeys[1]: "1", "FFlagCustom": "True"},
			wantExt: map[string]any{gateKeys[0]: "False", gateKeys[1]: "1"},
			wantSet: map[string]any{"FFlagCustom": "True"},
		},
		{
			name:    "one custom value keeps the other default",
			input:   map[string]any{gateKeys[1]: "5000"},
			wantExt: map[string]any{gateKeys[0]: "True", gateKeys[1]: "5000"},
			wantSet: map[string]any{},
		},
		{
			name:    "nil custom value omits the key from the external layer and stays a removal in the settings blob",
			input:   map[string]any{gateKeys[0]: nil},
			wantExt: map[string]any{gateKeys[1]: "10000"},
			wantSet: map[string]any{gateKeys[0]: nil},
		},
		{
			name: "custom gate values combine with explicit OpenGL",
			input: map[string]any{
				flagPreferOpenGL: "True",
				gateKeys[0]:      "False",
			},
			wantExt: map[string]any{flagPreferOpenGL: "True", gateKeys[0]: "False", gateKeys[1]: "10000"},
			wantSet: map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var before map[string]any
			if tc.input != nil {
				before = cloneFlagMap(tc.input)
			}
			settings, raw, err := splitStartupOverrides(tc.input, tc.testOpenGL)
			if err != nil {
				t.Fatal(err)
			}
			if got := decodeExternalOverrides(t, raw); !reflect.DeepEqual(got, tc.wantExt) {
				t.Fatalf("external layer=%v want %v", got, tc.wantExt)
			}
			if !reflect.DeepEqual(settings, tc.wantSet) {
				t.Fatalf("settings blob=%v want %v", settings, tc.wantSet)
			}
			if !reflect.DeepEqual(tc.input, before) && !(tc.input == nil && before == nil) {
				t.Fatalf("input mutated: %v want %v", tc.input, before)
			}
			// Output must be deterministic.
			for i := 0; i < 8; i++ {
				_, again, err := splitStartupOverrides(tc.input, tc.testOpenGL)
				if err != nil || again != raw {
					t.Fatalf("non-deterministic external layer: %q vs %q err=%v", again, raw, err)
				}
			}
		})
	}

	// Default layer is exactly the two-key object.
	_, raw, err := splitStartupOverrides(map[string]any{"FFlagCustom": "True"}, false)
	if err != nil || raw != gateJSON {
		t.Fatalf("default external layer=%q err=%v want %q", raw, err, gateJSON)
	}
	// Renderer and gate keys stay out of the settings blob.
	settings, _, err := splitStartupOverrides(map[string]any{flagPreferOpenGL: "True", flagPreferVulkan: "True"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range append(append([]string{}, rendererKeys...), gateKeys...) {
		if _, exists := settings[key]; exists {
			t.Fatalf("key %q remained in settings blob: %v", key, settings)
		}
	}
}

func TestLoadAndroidAppOverridesOrdinaryLaunchCarriesOnlyGatePairAndTestOverrideTransient(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(flogOverridesEnv, "")
	t.Setenv(luaLogEnv, "")
	cachePath := filepath.Join(t.TempDir(), "ClientAppSettings.json")

	ordinary, err := loadAndroidAppOverrides(context.Background(), cachePath, false, graphics.ProbeRendererCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary launch layer holds only the gate pair; the settings blob has neither.
	if got := decodeExternalOverrides(t, ordinary.external); !reflect.DeepEqual(got, map[string]any{gateKeys[0]: "True", gateKeys[1]: "10000"}) {
		t.Fatalf("ordinary external layer=%v", got)
	}
	for _, key := range gateKeys {
		if _, exists := ordinary.values[key]; exists {
			t.Fatalf("ordinary settings blob kept persistent key %q: %v", key, ordinary.values)
		}
	}
	if ordinary.values[flagPreferOpenGL] != nil || ordinary.values[flagDisableVulkan] != nil || ordinary.values[flagDisableVulkan11] != nil {
		t.Fatalf("ordinary launch invented OpenGL controls: %v", ordinary.values)
	}

	strict, err := loadAndroidAppOverrides(context.Background(), cachePath, true, graphics.ProbeRendererCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	renderer := decodeExternalOverrides(t, strict.external)
	wantStrict := map[string]any{flagPreferOpenGL: "True", flagDisableVulkan: "True", flagDisableVulkan11: "True", gateKeys[0]: "True", gateKeys[1]: "10000"}
	if !reflect.DeepEqual(renderer, wantStrict) {
		t.Fatalf("strict external layer=%v want %v", renderer, wantStrict)
	}
	for _, key := range append([]string{flagPreferOpenGL, flagDisableVulkan, flagDisableVulkan11}, gateKeys...) {
		if _, exists := strict.values[key]; exists {
			t.Fatalf("strict renderer key %q duplicated in settings envelope: %v", key, strict.values)
		}
	}
	if _, err := os.Stat(clientsettings.New().Path); !os.IsNotExist(err) {
		t.Fatalf("process-only renderer override wrote settings: err=%v", err)
	}
}

func TestLoadAndroidAppOverridesCustomFastFlagsWinInPersistentLayer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(flogOverridesEnv, "")
	t.Setenv(luaLogEnv, "")
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

func TestSettingsJSONStringAliasesMarshalBacking(t *testing.T) {
	raw := []byte(`{"applicationSettings":{},"ClientAppSettings":{}}`)
	got := settingsJSONString(raw)
	if unsafe.StringData(got) != unsafe.SliceData(raw) {
		t.Fatal("settingsJSONString copied Marshal backing")
	}
	if got != string(raw) {
		t.Fatalf("got %q", got)
	}
	if settingsJSONString(nil) != "" || settingsJSONString([]byte{}) != "" {
		t.Fatal("empty backing")
	}
}

func TestApplicationSettingsUserOverridesMergeWithOfficial(t *testing.T) {
	raw := []byte(`{"applicationSettings":{"OfficialOnly":"kept","FFlagDebugGraphicsPreferOpenGL":"True","FFlagDebugGraphicsDisableVulkan":"True","FFlagDebugGraphicsDisableVulkan11":"True","DFIntTaskSchedulerTargetFps":"60"}}`)
	overrides := map[string]any{
		flagPreferVulkan:              "True",
		"DFIntTaskSchedulerTargetFps": "144",
	}
	got, n, err := applicationSettingsFromResponseWithOverrides(raw, overrides)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("official count=%d want 5", n)
	}
	var envelope map[string]map[string]any
	if err := json.Unmarshal([]byte(got), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"applicationSettings", "ClientAppSettings"} {
		m := envelope[key]
		if m["OfficialOnly"] != "kept" || m["DFIntTaskSchedulerTargetFps"] != "144" || m[flagPreferVulkan] != "True" {
			t.Fatalf("%s merge=%v", key, m)
		}
		if _, ok := m[flagPreferOpenGL]; ok {
			t.Fatalf("%s retained conflicting OpenGL preference: %v", key, m)
		}
		if _, ok := m[flagDisableVulkan]; ok {
			t.Fatalf("%s retained conflicting Vulkan-disable key: %v", key, m)
		}
		if _, ok := m[flagDisableVulkan11]; ok {
			t.Fatalf("%s retained conflicting Vulkan11-disable key: %v", key, m)
		}
	}
}

func TestApplicationSettingsAutoDoesNotReplaceOfficial(t *testing.T) {
	raw := []byte(`{"applicationSettings":{"FFlagDebugGraphicsPreferVulkan":"True","DFIntTaskSchedulerTargetFps":"60"}}`)
	got, _, err := applicationSettingsFromResponseWithOverrides(raw, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"FFlagDebugGraphicsPreferVulkan":"True"`) || !strings.Contains(got, `"DFIntTaskSchedulerTargetFps":"60"`) {
		t.Fatalf("auto replaced official settings: %s", got)
	}
}
