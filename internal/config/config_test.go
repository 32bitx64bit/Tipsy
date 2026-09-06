// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPathsRespectsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	p := Paths()
	if p.ConfigDir != filepath.Join(tmp, "cfg", "tipsy") {
		t.Fatalf("ConfigDir = %s", p.ConfigDir)
	}
	if p.DataDir != filepath.Join(tmp, "data", "tipsy") {
		t.Fatalf("DataDir = %s", p.DataDir)
	}
	if p.CacheDir != filepath.Join(tmp, "cache", "tipsy") {
		t.Fatalf("CacheDir = %s", p.CacheDir)
	}
	if p.StateDir != filepath.Join(tmp, "state", "tipsy") {
		t.Fatalf("StateDir = %s", p.StateDir)
	}
	if p.ConfigFile != filepath.Join(tmp, "cfg", "tipsy", "config.json") {
		t.Fatalf("ConfigFile = %s", p.ConfigFile)
	}
	if p.ClientSettingsFile != filepath.Join(tmp, "cfg", "tipsy", "client-settings.json") {
		t.Fatalf("ClientSettingsFile = %s", p.ClientSettingsFile)
	}
	if p.LogDir != p.StateDir {
		t.Fatalf("LogDir = %s, want StateDir %s", p.LogDir, p.StateDir)
	}
}

func TestSaveIsAtomicPrivateAndRejectsSymlink(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	if err := Save(&Config{LogLevel: "debug"}); err != nil {
		t.Fatal(err)
	}
	path := Paths().ConfigFile
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".tipsy-config-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v", leftovers)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "outside")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := Save(&Config{LogLevel: "info"}); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if got, _ := os.ReadFile(target); string(got) != "untouched" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestPathsFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	p := Paths()
	if p.ConfigDir != filepath.Join(home, ".config", "tipsy") {
		t.Fatalf("ConfigDir = %s", p.ConfigDir)
	}
	if p.DataDir != filepath.Join(home, ".local", "share", "tipsy") {
		t.Fatalf("DataDir = %s", p.DataDir)
	}
	if p.CacheDir != filepath.Join(home, ".cache", "tipsy") {
		t.Fatalf("CacheDir = %s", p.CacheDir)
	}
	if p.StateDir != filepath.Join(home, ".local", "state", "tipsy") {
		t.Fatalf("StateDir = %s", p.StateDir)
	}
}

func TestLoadMissingReturnsDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "" || c.LogLevel != "" || len(c.LogCategories) != 0 || c.DevelopmentApproved() {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	in := &Config{
		DataDir:       "/opt/tipsy-data",
		LogLevel:      "debug",
		LogCategories: []string{"apk", "elf"},
	}
	in.ApproveDevelopment()
	if err := Save(in); err != nil {
		t.Fatal(err)
	}

	cfgPath := Paths().ConfigFile
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("config is not JSON: %s", raw)
	}

	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if out.DataDir != in.DataDir || out.LogLevel != in.LogLevel {
		t.Fatalf("got %+v want %+v", out, in)
	}
	if len(out.LogCategories) != 2 || out.LogCategories[0] != "apk" || out.LogCategories[1] != "elf" {
		t.Fatalf("LogCategories = %v", out.LogCategories)
	}
	if !out.DevelopmentApproved() {
		t.Fatal("explicit development consent was not preserved")
	}
}

func TestDevelopmentConsentRequiresExactSchemaAndAcknowledgement(t *testing.T) {
	for _, consent := range []*DevelopmentConsent{
		nil,
		{Schema: DevelopmentConsentSchema},
		{Schema: "unknown", Acknowledged: true},
	} {
		c := &Config{DevelopmentConsent: consent}
		if c.DevelopmentApproved() {
			t.Fatalf("invalid consent was accepted: %+v", consent)
		}
	}
	c := &Config{}
	c.ApproveDevelopment()
	if !c.DevelopmentApproved() {
		t.Fatal("explicit consent was not accepted")
	}
}

func TestLoadRejectsNonPrivateConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p := Paths()
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("non-private config was accepted")
	}
}

func TestLoadRejectsSymlinkConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p := Paths()
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "outside.json")
	if err := os.WriteFile(target, []byte(`{"developmentConsent":{"schema":"tipsy.development-consent.v1","acknowledged":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.ConfigFile); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("symlink config granted development consent")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	p := Paths()
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
