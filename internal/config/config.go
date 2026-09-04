// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	DataDir       string   `json:"dataDir,omitempty"`
	LogLevel      string   `json:"logLevel,omitempty"`
	LogCategories []string `json:"logCategories,omitempty"`
}

// Layout is the XDG directory set for Tipsy.
// Named separately from func Paths because Go forbids a type and func sharing an identifier.
type Layout struct {
	ConfigDir  string `json:"configDir"`
	DataDir    string `json:"dataDir"`
	CacheDir   string `json:"cacheDir"`
	StateDir   string `json:"stateDir"`
	ConfigFile string `json:"configFile"`
	LogDir     string `json:"logDir"`
}

func Paths() Layout {
	configDir := xdgDir("XDG_CONFIG_HOME", ".config")
	dataDir := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
	cacheDir := xdgDir("XDG_CACHE_HOME", ".cache")
	stateDir := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	return Layout{
		ConfigDir:  configDir,
		DataDir:    dataDir,
		CacheDir:   cacheDir,
		StateDir:   stateDir,
		ConfigFile: filepath.Join(configDir, "config.json"),
		LogDir:     stateDir,
	}
}

func xdgDir(env, homeFallback string) string {
	if v := os.Getenv(env); v != "" {
		return filepath.Join(v, "tipsy")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, homeFallback, "tipsy")
}

func Load() (*Config, error) {
	p := Paths()
	data, err := os.ReadFile(p.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func Save(c *Config) error {
	if c == nil {
		c = &Config{}
	}
	p := Paths()
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(p.ConfigFile, data, 0o600)
}
