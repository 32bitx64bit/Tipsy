// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const DevelopmentConsentSchema = "tipsy.development-consent.v1"

// DevelopmentConsent records the user's explicit choice to run a source build
// outside the official release trust boundary. It is never inferred from a
// missing production root, a development version string, or an unsigned file.
type DevelopmentConsent struct {
	Schema       string `json:"schema"`
	Acknowledged bool   `json:"acknowledged"`
}

// OfficialVerification points at a locally available TUF repository and the
// artifact it authenticates. These locations are inputs, never trust roots by
// themselves: releasemeta must still bind the root and Sigstore evidence to
// compiled production identities before OfficialVerified is possible.
type OfficialVerification struct {
	InitialRootPath string `json:"initialRootPath,omitempty"`
	MetadataDir     string `json:"metadataDir,omitempty"`
	TargetsDir      string `json:"targetsDir,omitempty"`
	ArtifactPath    string `json:"artifactPath,omitempty"`
	TargetPath      string `json:"targetPath,omitempty"`
	Channel         string `json:"channel,omitempty"`
	StateDir        string `json:"stateDir,omitempty"`
}

type Config struct {
	DataDir              string                `json:"dataDir,omitempty"`
	LogLevel             string                `json:"logLevel,omitempty"`
	LogCategories        []string              `json:"logCategories,omitempty"`
	DevelopmentConsent   *DevelopmentConsent   `json:"developmentConsent,omitempty"`
	OfficialVerification *OfficialVerification `json:"officialVerification,omitempty"`
}

func (c *Config) DevelopmentApproved() bool {
	return c != nil && c.DevelopmentConsent != nil &&
		c.DevelopmentConsent.Schema == DevelopmentConsentSchema &&
		c.DevelopmentConsent.Acknowledged
}

func (c *Config) ApproveDevelopment() {
	if c != nil {
		c.DevelopmentConsent = &DevelopmentConsent{Schema: DevelopmentConsentSchema, Acknowledged: true}
	}
}

// Layout is the XDG directory set for Tipsy.
// Named separately from func Paths because Go forbids a type and func sharing an identifier.
type Layout struct {
	ConfigDir          string `json:"configDir"`
	DataDir            string `json:"dataDir"`
	CacheDir           string `json:"cacheDir"`
	StateDir           string `json:"stateDir"`
	ConfigFile         string `json:"configFile"`
	ClientSettingsFile string `json:"clientSettingsFile"`
	LogDir             string `json:"logDir"`
}

func Paths() Layout {
	configDir := xdgDir("XDG_CONFIG_HOME", ".config")
	dataDir := xdgDir("XDG_DATA_HOME", filepath.Join(".local", "share"))
	cacheDir := xdgDir("XDG_CACHE_HOME", ".cache")
	stateDir := xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state"))
	return Layout{
		ConfigDir:          configDir,
		DataDir:            dataDir,
		CacheDir:           cacheDir,
		StateDir:           stateDir,
		ConfigFile:         filepath.Join(configDir, "config.json"),
		ClientSettingsFile: filepath.Join(configDir, "client-settings.json"),
		LogDir:             stateDir,
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
	info, err := os.Lstat(p.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("config file is not an owner-private regular file")
	}
	data, err := os.ReadFile(p.ConfigFile)
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
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return AtomicWriteFile(p.ConfigFile, data, 0o600)
}
