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
	if p.LogDir != p.StateDir {
		t.Fatalf("LogDir = %s, want StateDir %s", p.LogDir, p.StateDir)
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
	if c.DataDir != "" || c.LogLevel != "" || len(c.LogCategories) != 0 {
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
