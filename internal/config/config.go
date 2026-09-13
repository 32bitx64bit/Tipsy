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

type Config struct {
	DataDir       string   `json:"dataDir,omitempty"`
	LogLevel      string   `json:"logLevel,omitempty"`
	LogCategories []string `json:"logCategories,omitempty"`
	// Gamepad carries the persisted controller section verbatim. Input owns
	// its shape/defaults/merge rules (see internal/gamepad GamepadConfig
	// under the "gamepad" key); this package only preserves the bytes
	// across atomic read-modify-write cycles. Missing or null means
	// defaults (on, device-flat deadzone baseline, rumble
	// on-if-supported). Never edited by hand for engine behavior: it
	// shapes host stick calibration only, never GlobalBasicSettings_13.xml.
	Gamepad json.RawMessage `json:"gamepad,omitempty"`
	// Microphone carries the persisted capture-door section verbatim.
	// CLI/mic owns its shape/defaults/merge rules (see internal/mic
	// MicrophoneConfig under the "microphone" key); this package only
	// preserves the bytes across atomic read-modify-write cycles. Missing
	// or null means defaults (allowed; OpenSL still lazy-opens). Never
	// PCM, and never a Pulse source name in this package.
	Microphone         json.RawMessage     `json:"microphone,omitempty"`
	DevelopmentConsent *DevelopmentConsent `json:"developmentConsent,omitempty"`
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
	// The XDG base directory specification requires ignoring relative paths.
	if v := os.Getenv(env); filepath.IsAbs(v) {
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

// Update applies fn to the current config and saves the result while holding
// the cross-process config lock. Callers that need a read-modify-write cycle
// must use Update instead of a bare Load followed by Save.
func Update(fn func(*Config) error) error {
	if fn == nil {
		return errors.New("config update function is nil")
	}
	release, err := acquireConfigLock()
	if err != nil {
		return err
	}
	defer release()
	c, err := Load()
	if err != nil {
		return err
	}
	if err := fn(c); err != nil {
		return err
	}
	return saveLocked(c)
}

// UpdateJSON applies fn to the complete config JSON document while holding
// the cross-process config lock. Unlike Update, it deliberately preserves
// top-level fields that Config does not model yet. It is for owners of a
// namespaced section (for example gamepad or microphone) that must update
// that section without clobbering another Settings window's newer write.
//
// A missing document is supplied as nil. Existing documents must be
// owner-private JSON objects; malformed, null, array, and scalar documents
// are refused rather than being replaced with a partial settings file.
func UpdateJSON(fn func([]byte) ([]byte, error)) error {
	if fn == nil {
		return errors.New("config JSON update function is nil")
	}
	release, err := acquireConfigLock()
	if err != nil {
		return err
	}
	defer release()

	p := Paths()
	data, err := readConfigJSONObject(p.ConfigFile)
	if err != nil {
		return err
	}
	updated, err := fn(append([]byte(nil), data...))
	if err != nil {
		return err
	}
	if err := requireJSONObject(updated); err != nil {
		return fmt.Errorf("updated config must be a JSON object: %w", err)
	}
	return AtomicWriteFile(p.ConfigFile, updated, 0o600)
}

func Save(c *Config) error {
	release, err := acquireConfigLock()
	if err != nil {
		return err
	}
	defer release()
	return saveLocked(c)
}

func saveLocked(c *Config) error {
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

// readConfigJSONObject is the raw counterpart to Load for section writers.
// It performs the same ownership checks but retains every encoded field so a
// narrow update can preserve forward-compatible and specialist-owned keys.
func readConfigJSONObject(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("config file is not an owner-private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := requireJSONObject(data); err != nil {
		return nil, fmt.Errorf("config file is not a JSON object: %w", err)
	}
	return data, nil
}

func requireJSONObject(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("expected an object")
	}
	return nil
}
