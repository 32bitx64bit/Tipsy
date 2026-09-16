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

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
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

func TestSplitRendererStartupOverridesUsesOnlyOfficialExternalPath(t *testing.T) {
	settings, raw, err := splitRendererStartupOverrides(map[string]any{
		flagPreferOpenGL:    "True",
		flagDisableVulkan:   "True",
		flagDisableVulkan11: "True",
		"FFlagCustom":       "kept-out",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[flagPreferOpenGL] != "True" || got[flagDisableVulkan] != "True" || got[flagDisableVulkan11] != "True" {
		t.Fatalf("renderer startup overrides=%v", got)
	}
	if _, ok := got["FFlagCustom"]; ok {
		t.Fatalf("unrelated flag leaked into renderer startup overrides: %v", got)
	}
	if settings["FFlagCustom"] != "kept-out" {
		t.Fatalf("ordinary override lost from settings envelope: %v", settings)
	}
	for _, key := range []string{flagPreferOpenGL, flagPreferVulkan, flagDisableOpenGL, flagDisableVulkan, flagDisableVulkan11} {
		if _, exists := settings[key]; exists {
			t.Fatalf("renderer key %q remained in settings envelope: %v", key, settings)
		}
	}
	ordinary := map[string]any{"FFlagCustom": "True"}
	if unchanged, empty, err := splitRendererStartupOverrides(ordinary, false); err != nil || empty != "" || !reflect.DeepEqual(unchanged, ordinary) {
		t.Fatalf("ordinary split settings=%v overrides=%q err=%v", unchanged, empty, err)
	}
	ordinaryVulkan := map[string]any{flagPreferVulkan: "True"}
	if unchanged, payload, err := splitRendererStartupOverrides(ordinaryVulkan, false); err != nil || payload != "" || !reflect.DeepEqual(unchanged, ordinaryVulkan) {
		t.Fatalf("ordinary Vulkan split settings=%v overrides=%q err=%v", unchanged, payload, err)
	}
	testInput := map[string]any{flagPreferVulkan: "True", flagDisableOpenGL: "True", "FFlagCustom": "kept"}
	testSettings, testRaw, err := splitRendererStartupOverrides(testInput, true)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal([]byte(testRaw), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[flagPreferOpenGL] != "True" || got[flagDisableVulkan] != "True" || got[flagDisableVulkan11] != "True" || testSettings["FFlagCustom"] != "kept" {
		t.Fatalf("process-only split settings=%v overrides=%v", testSettings, got)
	}
	if testInput[flagPreferVulkan] != "True" || testInput[flagDisableOpenGL] != "True" {
		t.Fatalf("process-only split mutated its input: %v", testInput)
	}
}

func TestLoadAndroidAppOverridesLeavesOrdinaryLaunchInertAndTestOverrideTransient(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv(flogOverridesEnv, "")
	t.Setenv(luaLogEnv, "")
	cachePath := filepath.Join(t.TempDir(), "ClientAppSettings.json")

	ordinary, err := loadAndroidAppOverrides(context.Background(), cachePath, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.renderer != "" {
		t.Fatalf("ordinary renderer preload=%q", ordinary.renderer)
	}
	if ordinary.values[flagPreferOpenGL] != nil || ordinary.values[flagDisableVulkan] != nil || ordinary.values[flagDisableVulkan11] != nil {
		t.Fatalf("ordinary launch invented OpenGL controls: %v", ordinary.values)
	}

	strict, err := loadAndroidAppOverrides(context.Background(), cachePath, true)
	if err != nil {
		t.Fatal(err)
	}
	var renderer map[string]any
	if err := json.Unmarshal([]byte(strict.renderer), &renderer); err != nil {
		t.Fatal(err)
	}
	if len(renderer) != 3 || renderer[flagPreferOpenGL] != "True" || renderer[flagDisableVulkan] != "True" || renderer[flagDisableVulkan11] != "True" {
		t.Fatalf("strict renderer preload=%v", renderer)
	}
	for _, key := range []string{flagPreferOpenGL, flagDisableVulkan, flagDisableVulkan11} {
		if _, exists := strict.values[key]; exists {
			t.Fatalf("strict renderer key %q duplicated in settings envelope: %v", key, strict.values)
		}
	}
	if _, err := os.Stat(clientsettings.New().Path); !os.IsNotExist(err) {
		t.Fatalf("process-only renderer override wrote settings: err=%v", err)
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
