// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package clientsettings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/graphics"
)

func testService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return &Service{
		Path:    filepath.Join(root, "config", "client-settings.json"),
		XMLPath: filepath.Join(root, "data", "GlobalBasicSettings_13.xml"),
		now:     func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
	}
}

func settingsXML(value string) []byte {
	return []byte(`<?xml version="1.0"?>
<roblox version="4">
	<!-- preserve this comment -->
	<Item class="Other"><Properties><int name="FramerateCap">77</int></Properties></Item>
	<Item class="UserGameSettings" referent="RBX-test">
		<Properties>
			<string name="UnknownField">keep-me</string>
			<int name="FramerateCap">` + value + `</int>
		</Properties>
	</Item>
</roblox>
`)
}

func writeXML(t *testing.T, s *Service, value string) []byte {
	t.Helper()
	raw := settingsXML(value)
	if err := os.MkdirAll(filepath.Dir(s.XMLPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.XMLPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDefaultsAndRestartSurvival(t *testing.T) {
	s := testService(t)
	writeXML(t, s, "-1")
	got, err := s.Load(context.Background())
	if err != nil || got != Default() {
		t.Fatalf("defaults=%+v err=%v", got, err)
	}
	want := Settings{Renderer: RendererOpenGL, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 144}, Display: DisplayPrimary}
	result, err := s.Apply(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RestartRequired || !result.FrameRateApplied || result.Settings != want {
		t.Fatalf("result=%+v", result)
	}
	reloaded, err := (&Service{Path: s.Path, XMLPath: s.XMLPath}).Load(context.Background())
	if err != nil || reloaded != want {
		t.Fatalf("reload=%+v err=%v", reloaded, err)
	}
	st, err := os.Stat(s.Path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
	result, err = s.Apply(context.Background(), want)
	if err != nil || result.RestartRequired {
		t.Fatalf("no-op result=%+v err=%v", result, err)
	}
}

func TestLowTextureModeDefaultsOffAndEmitsAllowlistedOverrides(t *testing.T) {
	s := testService(t)
	writeXML(t, s, "-1")
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"renderer":"auto","frameRate":{"mode":"auto"}}`)
	if err := os.WriteFile(s.Path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil || got.LowTextureMode {
		t.Fatalf("old config migration=%+v err=%v", got, err)
	}
	unchanged, err := os.ReadFile(s.Path)
	if err != nil || !bytes.Equal(unchanged, old) {
		t.Fatalf("load rewrote old config=%q err=%v", unchanged, err)
	}

	high, err := Overrides(got)
	if err != nil {
		t.Fatalf("default high texture overrides err=%v", err)
	}
	assertHighTextureOverrides(t, high)

	got.LowTextureMode = true
	result, err := s.Apply(context.Background(), got)
	if err != nil || !result.RestartRequired || !result.Settings.LowTextureMode {
		t.Fatalf("enable low texture mode result=%+v err=%v", result, err)
	}
	reloaded, err := s.Load(context.Background())
	if err != nil || !reloaded.LowTextureMode {
		t.Fatalf("reloaded LowTextureMode=%+v err=%v", reloaded, err)
	}
	low, err := Overrides(reloaded)
	if err != nil {
		t.Fatalf("low texture overrides err=%v", err)
	}
	assertLowTextureOverrides(t, low)
	reset, err := s.Reset(context.Background())
	if err != nil || reset.LowTextureMode || reset != Default() {
		t.Fatalf("reset LowTextureMode=%+v err=%v", reset, err)
	}
	restored, err := Overrides(reset)
	if err != nil {
		t.Fatalf("reset texture overrides err=%v", err)
	}
	assertHighTextureOverrides(t, restored)
}

func TestVSyncDefaultsOffAndMigratesExistingSettings(t *testing.T) {
	s := testService(t)
	writeXML(t, s, "-1")
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"renderer":"auto","frameRate":{"mode":"auto"}}`)
	if err := os.WriteFile(s.Path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil || got.VSync {
		t.Fatalf("old config migration=%+v err=%v", got, err)
	}
	unchanged, err := os.ReadFile(s.Path)
	if err != nil || !bytes.Equal(unchanged, old) {
		t.Fatalf("load rewrote old config=%q err=%v", unchanged, err)
	}

	got.VSync = true
	result, err := s.Apply(context.Background(), got)
	if err != nil || !result.RestartRequired || !result.Settings.VSync {
		t.Fatalf("enable VSync result=%+v err=%v", result, err)
	}
	reloaded, err := s.Load(context.Background())
	if err != nil || !reloaded.VSync {
		t.Fatalf("reloaded VSync=%+v err=%v", reloaded, err)
	}
	reset, err := s.Reset(context.Background())
	if err != nil || reset.VSync || reset.Display != DisplayPrimary {
		t.Fatalf("reset VSync=%+v err=%v", reset, err)
	}
}

func TestDisplayDefaultsToPrimaryAndPointerDoesNotRestart(t *testing.T) {
	s := testService(t)
	writeXML(t, s, "-1")
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"renderer":"auto","frameRate":{"mode":"auto"}}`)
	if err := os.WriteFile(s.Path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil || got.Display != DisplayPrimary {
		t.Fatalf("old config display=%+v err=%v", got, err)
	}
	unchanged, err := os.ReadFile(s.Path)
	if err != nil || !bytes.Equal(unchanged, old) {
		t.Fatalf("load rewrote old config=%q err=%v", unchanged, err)
	}

	got.Display = DisplayPointer
	result, err := s.Apply(context.Background(), got)
	if err != nil || result.RestartRequired || result.Settings.Display != DisplayPointer {
		t.Fatalf("pointer display result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.FrameRateNote, "follow the mouse") {
		t.Fatalf("pointer note=%q", result.FrameRateNote)
	}
	reloaded, err := s.Load(context.Background())
	if err != nil || reloaded.Display != DisplayPointer {
		t.Fatalf("reloaded display=%+v err=%v", reloaded, err)
	}

	named := reloaded
	named.Display = "DP-1"
	result, err = s.Apply(context.Background(), named)
	if err != nil || result.RestartRequired || result.Settings.Display != "DP-1" {
		t.Fatalf("named display result=%+v err=%v", result, err)
	}

	_, err = s.Apply(context.Background(), Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateAuto}, Display: "bad/name"})
	if err == nil {
		t.Fatal("path-like monitor name accepted")
	}
	reset, err := s.Reset(context.Background())
	if err != nil || reset.Display != DisplayPrimary {
		t.Fatalf("reset display=%+v err=%v", reset, err)
	}
}

func TestDiscordPresenceDefaultsOnAndJoinOffWithoutRestart(t *testing.T) {
	s := testService(t)
	writeXML(t, s, "-1")
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"renderer":"auto","frameRate":{"mode":"auto"}}`)
	if err := os.WriteFile(s.Path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil || !got.DiscordRichPresence || got.DiscordJoinButton {
		t.Fatalf("old config discord=%+v err=%v", got, err)
	}
	unchanged, err := os.ReadFile(s.Path)
	if err != nil || !bytes.Equal(unchanged, old) {
		t.Fatalf("load rewrote old config=%q err=%v", unchanged, err)
	}
	if d := Default(); !d.DiscordRichPresence || d.DiscordJoinButton {
		t.Fatalf("Default discord=%+v", d)
	}

	got.DiscordJoinButton = true
	result, err := s.Apply(context.Background(), got)
	if err != nil || result.RestartRequired || !result.Settings.DiscordJoinButton || !strings.Contains(result.FrameRateNote, "Join button") {
		t.Fatalf("enable join result=%+v err=%v", result, err)
	}
	got.DiscordRichPresence = false
	result, err = s.Apply(context.Background(), got)
	if err != nil || result.RestartRequired || result.Settings.DiscordRichPresence || !strings.Contains(result.FrameRateNote, "is off") {
		t.Fatalf("disable presence result=%+v err=%v", result, err)
	}
	reloaded, err := s.Load(context.Background())
	if err != nil || reloaded.DiscordRichPresence || !reloaded.DiscordJoinButton {
		t.Fatalf("reloaded discord=%+v err=%v", reloaded, err)
	}
	reset, err := s.Reset(context.Background())
	if err != nil || !reset.DiscordRichPresence || reset.DiscordJoinButton || reset != Default() {
		t.Fatalf("reset discord=%+v err=%v", reset, err)
	}
}

func TestFrameRateBoundsUnlimitedAndResetOwnership(t *testing.T) {
	s := testService(t)
	original := writeXML(t, s, "-1")
	for _, fps := range []int{-1, 0, 1, 29, 241, 1000} {
		wanted := Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: fps}}
		if _, err := s.Apply(context.Background(), wanted); err == nil {
			t.Fatalf("fps %d accepted", fps)
		}
	}
	for _, fps := range []int{30, 60, 120, 240} {
		wanted := Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: fps}}
		if _, err := s.Apply(context.Background(), wanted); err != nil {
			t.Fatalf("fps %d: %v", fps, err)
		}
	}
	unlimited := Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateUnlimited}}
	result, err := s.Apply(context.Background(), unlimited)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(result.FrameRateNote), "experimental") {
		t.Fatalf("unlimited note=%q", result.FrameRateNote)
	}
	if !strings.Contains(result.FrameRateNote, unlimitedFPSValue) ||
		!strings.Contains(result.FrameRateNote, "may impose another limit") {
		t.Fatalf("unlimited warning must state the finite request and residual limits: %q", result.FrameRateNote)
	}
	raw, _ := os.ReadFile(s.XMLPath)
	if !bytes.Contains(raw, []byte(`name="FramerateCap">9999</int>`)) {
		t.Fatalf("unlimited XML=%s", raw)
	}
	got, err := s.Reset(context.Background())
	if err != nil || got != Default() {
		t.Fatalf("reset=%+v err=%v", got, err)
	}
	raw, _ = os.ReadFile(s.XMLPath)
	if !bytes.Equal(raw, original) {
		t.Fatalf("auto did not byte-restore original\nwant=%s\ngot=%s", original, raw)
	}
}

func TestVSyncPresentationPolicy(t *testing.T) {
	tests := []struct {
		name  string
		value Settings
		want  bool
	}{
		{name: "default is unthrottled", value: Default(), want: true},
		{name: "off is independent of low fixed cap", value: Settings{FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 30}}, want: true},
		{name: "off is independent of unlimited", value: Settings{FrameRate: FrameRate{Mode: FrameRateUnlimited}}, want: true},
		{name: "on synchronizes low fixed cap", value: Settings{FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 30}, VSync: true}, want: false},
		{name: "on synchronizes unlimited", value: Settings{FrameRate: FrameRate{Mode: FrameRateUnlimited}, VSync: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.value.NeedsUnthrottledPresentation(); got != tt.want {
				t.Fatalf("NeedsUnthrottledPresentation()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestAutoPreservesClientChangeOutsideTipsyOwnership(t *testing.T) {
	s := testService(t)
	writeXML(t, s, "-1")
	wanted := Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 60}}
	if _, err := s.Apply(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.XMLPath)
	external, _, err := updateFramerateCap(raw, "75")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.XMLPath, external, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(context.Background(), Default()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s.XMLPath)
	if !bytes.Contains(raw, []byte(`name="FramerateCap">75</int>`)) {
		t.Fatalf("Auto overwrote client-owned change: %s", raw)
	}
}

func TestFrameRatePendingUntilRobloxCreatesXML(t *testing.T) {
	s := testService(t)
	wanted := Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 90}}
	result, err := s.Apply(context.Background(), wanted)
	if err != nil {
		t.Fatal(err)
	}
	if result.FrameRateApplied || !strings.Contains(result.FrameRateNote, "not created") {
		t.Fatalf("pending result=%+v", result)
	}
	writeXML(t, s, "-1")
	if err := s.ReconcileWhileClientLocked(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.XMLPath)
	if !bytes.Contains(raw, []byte(`name="FramerateCap">90</int>`)) {
		t.Fatalf("reconcile XML=%s", raw)
	}
}

func TestRendererOverridesAndVulkanAvailability(t *testing.T) {
	got, err := Overrides(Settings{Renderer: RendererOpenGL, FrameRate: FrameRate{Mode: FrameRateUnlimited}})
	if err != nil || got[flagPreferOpenGL] != "True" {
		t.Fatalf("OpenGL overrides=%v err=%v", got, err)
	}
	if got[flagTextureQualityOverrideEnabled] != "True" || got[intTextureQualityOverride] != textureQualityHigh {
		t.Fatalf("OpenGL high texture overrides=%v", got)
	}
	assertHighTextureOverrides(t, got)
	if got[flagGameBasicSettingsFramerateCap] != "True" ||
		got[flagTaskSchedulerLimitFPS240] != "False" {
		t.Fatalf("unlimited FPS overrides=%v", got)
	}
	if _, ok := got[intTaskSchedulerTargetFPS]; ok {
		t.Fatalf("unlimited must not enter the client's legacy 240-clamped integer path: %v", got)
	}
	if _, ok := got[flagPreferVulkan]; ok {
		t.Fatalf("conflicting Vulkan override: %v", got)
	}
	limited, err := Overrides(Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 144}})
	if err != nil || limited[intTaskSchedulerTargetFPS] != "144" {
		t.Fatalf("limited FPS overrides=%v err=%v", limited, err)
	}
	if _, ok := limited[flagTaskSchedulerLimitFPS240]; ok {
		t.Fatalf("limited must preserve downloaded 240-limit policy: %v", limited)
	}
	auto, err := Overrides(Default())
	if err != nil {
		t.Fatalf("auto overrides err=%v", err)
	}
	if auto[flagGameBasicSettingsFramerateCap] != "True" {
		t.Fatalf("auto overrides=%v", auto)
	}
	if auto[flagTextureQualityOverrideEnabled] != "True" || auto[intTextureQualityOverride] != textureQualityHigh {
		t.Fatalf("default high texture overrides=%v", auto)
	}
	assertHighTextureOverrides(t, auto)
	if _, ok := auto[intTaskSchedulerTargetFPS]; ok {
		t.Fatalf("auto must preserve scheduler target ownership: %v", auto)
	}
	if _, ok := auto[flagTaskSchedulerLimitFPS240]; ok {
		t.Fatalf("auto must preserve scheduler limit ownership: %v", auto)
	}
	caps := graphics.ProbeRendererCapabilities()
	if _, ok := auto[flagPreferVulkan]; ok != caps.Vulkan.Available {
		t.Fatalf("auto PreferVulkan=%v vulkanAvailable=%v overrides=%v", ok, caps.Vulkan.Available, auto)
	}
	if _, ok := auto[flagPreferOpenGL]; ok {
		t.Fatalf("auto must not emit PreferOpenGL: %v", auto)
	}
	wantAutoLen := 14 // framerate-cap gate + thirteen high-quality texture keys
	if caps.Vulkan.Available {
		wantAutoLen++
	}
	if len(auto) != wantAutoLen {
		t.Fatalf("auto overrides=%v want %d keys err=%v", auto, wantAutoLen, err)
	}
	low, err := Overrides(Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateAuto}, LowTextureMode: true})
	if err != nil {
		t.Fatalf("low texture overrides err=%v", err)
	}
	assertLowTextureOverrides(t, low)
	withVSync, err := Overrides(Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateUnlimited}, VSync: true})
	if err != nil || withVSync[flagTaskSchedulerLimitFPS240] != "False" ||
		withVSync[flagGameBasicSettingsFramerateCap] != "True" {
		t.Fatalf("VSync changed independent FPS overrides=%v err=%v", withVSync, err)
	}
	if _, ok := withVSync[intTaskSchedulerTargetFPS]; ok {
		t.Fatalf("VSync changed Unlimited scheduler ownership=%v", withVSync)
	}
	_, err = Overrides(Settings{Renderer: RendererVulkan, FrameRate: FrameRate{Mode: FrameRateAuto}})
	if caps.Vulkan.Available {
		if err != nil {
			t.Fatalf("Vulkan available but Overrides failed: %v", err)
		}
	} else {
		var unsupported *UnsupportedRendererError
		if !errors.As(err, &unsupported) {
			t.Fatalf("Vulkan error=%T %v", err, err)
		}
	}
	opts := RendererOptions()
	if len(opts) != 3 || opts[2].Renderer != RendererVulkan || opts[2].Available != caps.Vulkan.Available {
		t.Fatalf("renderer options=%+v vulkanAvailable=%v", opts, caps.Vulkan.Available)
	}
}

func TestUnavailableRendererRemainsPersistedButCannotApply(t *testing.T) {
	s := testService(t)
	want := persistedSettings{Settings: Settings{
		Renderer:  RendererVulkan,
		FrameRate: FrameRate{Mode: FrameRateAuto},
		Display:   DisplayPrimary,
	}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background())
	if err != nil || got != want.Settings {
		t.Fatalf("load unavailable renderer=%+v err=%v", got, err)
	}
	_, err = s.Apply(context.Background(), got)
	if graphics.ProbeRendererCapabilities().Vulkan.Available {
		if err != nil {
			t.Fatalf("Vulkan is selectable; Apply should persist it: %v", err)
		}
		return
	}
	var unsupported *UnsupportedRendererError
	if !errors.As(err, &unsupported) {
		t.Fatalf("apply unavailable renderer error=%v", err)
	}
	after, err := os.ReadFile(s.Path)
	if err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("unsupported apply changed settings: %q err=%v", after, err)
	}
}

func TestXMLUpdateChangesOnlyTargetText(t *testing.T) {
	raw := settingsXML(" 240 ")
	got, prior, err := updateFramerateCap(raw, "120")
	if err != nil || prior != "240" {
		t.Fatalf("prior=%q err=%v", prior, err)
	}
	want := bytes.Replace(raw, []byte("> 240 </int>"), []byte("> 120 </int>"), 1)
	if !bytes.Equal(got, want) {
		t.Fatalf("structure changed\nwant=%s\ngot=%s", want, got)
	}
}

func TestMalformedFilesRecoveredWithoutPayloadLeak(t *testing.T) {
	s := testService(t)
	secret := "synthetic-secret-that-must-not-leak"
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, []byte(`{"renderer":`+secret), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background())
	if err != nil || got != Default() {
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked malformed payload: %v", err)
		}
		t.Fatalf("recovery=%+v err=%v", got, err)
	}
	backup := s.Path + ".invalid-20260904T120000.000000000Z"
	raw, err := os.ReadFile(backup)
	if err != nil || !strings.Contains(string(raw), secret) {
		t.Fatalf("backup missing original: err=%v", err)
	}
	raw, err = os.ReadFile(s.Path)
	if err != nil || !json.Valid(raw) || strings.Contains(string(raw), secret) {
		t.Fatalf("replacement=%q err=%v", raw, err)
	}

	if err := os.MkdirAll(filepath.Dir(s.XMLPath), 0o700); err != nil {
		t.Fatal(err)
	}
	badXML := []byte(`<roblox><Item class="UserGameSettings">` + secret)
	if err := os.WriteFile(s.XMLPath, badXML, 0o600); err != nil {
		t.Fatal(err)
	}
	wanted := Settings{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: 60}}
	_, err = s.Apply(context.Background(), wanted)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed XML err=%v", err)
	}
	kept, _ := os.ReadFile(s.XMLPath)
	if !bytes.Equal(kept, badXML) {
		t.Fatal("malformed XML was modified")
	}
	xmlBackup := s.XMLPath + ".invalid-20260904T120000.000000000Z"
	if copied, err := os.ReadFile(xmlBackup); err != nil || !bytes.Equal(copied, badXML) {
		t.Fatalf("XML backup err=%v copied=%q", err, copied)
	}
}

func TestSettingsSymlinkAndCancellationRejected(t *testing.T) {
	s := testService(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"renderer":"auto"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, s.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background()); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if err := os.Remove(s.Path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Apply(ctx, Default()); err == nil {
		t.Fatal("expected cancellation")
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("settings file written after cancellation: %v", err)
	}
}

func TestClientLockBlocksApply(t *testing.T) {
	s := testService(t)
	release, err := AcquireClientLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := s.Apply(context.Background(), Default()); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("lock error=%v", err)
	}
}

func TestClientLockAllowsDiscordOnlyApply(t *testing.T) {
	s := testService(t)
	if _, err := s.Apply(context.Background(), Default()); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireClientLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	wanted := Default()
	wanted.DiscordJoinButton = true
	result, err := s.Apply(context.Background(), wanted)
	if err != nil {
		t.Fatal(err)
	}
	if result.RestartRequired || !strings.Contains(result.FrameRateNote, "Join button") {
		t.Fatalf("discord-only apply=%+v", result)
	}
	got, err := s.Load(context.Background())
	if err != nil || !got.DiscordJoinButton || !got.DiscordRichPresence {
		t.Fatalf("loaded=%+v err=%v", got, err)
	}
	wanted.VSync = true
	if _, err := s.Apply(context.Background(), wanted); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("graphics apply while locked err=%v", err)
	}
}

func assertHighTextureOverrides(t *testing.T, got map[string]any) {
	t.Helper()
	want := map[string]any{
		"DFFlagTextureQualityOverrideEnabled": "True",
		"DFIntTextureQualityOverride":         "3",
		flagUITextureCompressionDesktop:       "True",
		flagTCTextureCompressionDesktop:       "True",
		intRenderTextureTotalBudgetMB:         textureBudgetHighMB,
		intRenderTextureMipBias:               textureMipBiasHigh,
		"DFIntDebugTc1MaxAllowedMemoryBudget": "268435456",
		intRenderForceVideoMemorySize:         videoMemoryHighBytes,
		flagTM2RuntimeTextureDisableStreaming: "False",
		flagTM2SkipMipsForUnstreamable2:       "False",
		flagUseTM1LegacyMipPackForDecal:       "False",
		"FFlagRenderUseTextureManager224":     "False",
		"FFlagNewRenderUseTextureManager2":    "False",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("high texture %s=%v want %v in %v", key, got[key], value, got)
		}
	}
	assertNoForbiddenTextureOverrides(t, got)
}

func assertLowTextureOverrides(t *testing.T, got map[string]any) {
	t.Helper()
	want := map[string]any{
		"DFFlagTextureQualityOverrideEnabled": "True",
		"DFIntTextureQualityOverride":         "1",
		flagUITextureCompressionDesktop:       "False",
		flagTCTextureCompressionDesktop:       "False",
		intRenderTextureTotalBudgetMB:         textureBudgetLowMB,
		intRenderTextureMipBias:               textureMipBiasLow,
		"DFIntDebugTc1MaxAllowedMemoryBudget": "50331648",
		intRenderForceVideoMemorySize:         videoMemoryLowBytes,
		flagTM2RuntimeTextureDisableStreaming: "True",
		flagTM2SkipMipsForUnstreamable2:       "True",
		flagUseTM1LegacyMipPackForDecal:       "True",
		"FFlagRenderUseTextureManager224":     "True",
		"FFlagNewRenderUseTextureManager2":    "True",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("low texture %s=%v want %v in %v", key, got[key], value, got)
		}
	}
	assertNoForbiddenTextureOverrides(t, got)
}

// assertNoForbiddenTextureOverrides keeps keys out of both texture mappings
// that were measured to be harmful or inert on the official Android client:
// the compositor low-res factor (Tipsy's 1 failed to fix the blur and spun
// the compositor; the CDN owns it at 4), the unconsumed avatar texture memory
// name, incorrect compositor-budget aliases, and the temporary compositor
// diagnostic channel. The client's measured cap is the DFIntDebugTc1 key.
func assertNoForbiddenTextureOverrides(t *testing.T, got map[string]any) {
	t.Helper()
	for _, key := range []string{
		"FIntDebugFRMQualityLevelOverride",
		"DFIntRenderForceVideoMemorySize",
		"FIntTextureCompositorLowResFactor",
		"FIntAvatarTextureMemoryMax",
		"FIntRenderTextureCompositorBudget",
		"DFIntRenderTextureCompositorBudget",
		"FLogRenderTextureCompositor",
		"FLogRenderTextureCompositorBudget",
	} {
		if _, ok := got[key]; ok {
			t.Fatalf("forbidden texture override %s in %v", key, got)
		}
	}
}
