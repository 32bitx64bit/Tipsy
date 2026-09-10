// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package desktop

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Env is the XDG base-directory view used to resolve launcher ownership.
type Env struct {
	// DataHome is $XDG_DATA_HOME (user scope; highest precedence).
	DataHome string
	// DataDirs is $XDG_DATA_DIRS in precedence order.
	DataDirs []string
	// ConfigHome is $XDG_CONFIG_HOME, where mimeapps.list lives.
	ConfigHome string
	// Home is the user's home directory.
	Home string
}

// DefaultDataDirs is the specification default for XDG_DATA_DIRS.
var DefaultDataDirs = []string{"/usr/local/share", "/usr/share"}

// EnvFromOS builds the resolution view from the process environment.
//
// Inside a Flatpak sandbox XDG_DATA_HOME points at the app's private
// ~/.var/app/<id>/data, which is not where the desktop looks for launchers.
// The host user's ~/.local/share is used instead (reachable when the manifest
// grants xdg-data/applications), and the host export directories are added
// so a system-level view is at least attempted.
func EnvFromOS() Env {
	home := os.Getenv("HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	env := Env{Home: home}
	inFlatpak := InFlatpak()
	if inFlatpak {
		env.DataHome = filepath.Join(home, ".local", "share")
		env.ConfigHome = filepath.Join(home, ".config")
	} else {
		env.DataHome = os.Getenv("XDG_DATA_HOME")
		if env.DataHome == "" {
			env.DataHome = filepath.Join(home, ".local", "share")
		}
		env.ConfigHome = os.Getenv("XDG_CONFIG_HOME")
		if env.ConfigHome == "" {
			env.ConfigHome = filepath.Join(home, ".config")
		}
	}
	dirs := os.Getenv("XDG_DATA_DIRS")
	if dirs == "" || inFlatpak {
		env.DataDirs = append([]string(nil), DefaultDataDirs...)
		if inFlatpak {
			env.DataDirs = append(env.DataDirs,
				filepath.Join(home, ".local", "share", "flatpak", "exports", "share"),
				"/var/lib/flatpak/exports/share")
		}
	} else {
		for _, dir := range strings.Split(dirs, ":") {
			if dir = strings.TrimSpace(dir); dir != "" {
				env.DataDirs = append(env.DataDirs, filepath.Clean(dir))
			}
		}
	}
	return env
}

// InFlatpak reports whether the process runs inside a Flatpak sandbox.
func InFlatpak() bool {
	if os.Getenv("FLATPAK_ID") != "" {
		return true
	}
	_, err := os.Stat("/.flatpak-info")
	return err == nil
}

// ApplicationsDir is the user-scope applications directory.
func (e Env) ApplicationsDir() string {
	return filepath.Join(e.DataHome, "applications")
}

// IconDir is the user-scope hicolor icon directory for one size.
func (e Env) IconDir(size string) string {
	return filepath.Join(e.DataHome, "icons", "hicolor", size, "apps")
}

// searchDirs lists every applications directory in precedence order, with
// the scope of each.
func (e Env) searchDirs() []scopedDir {
	dirs := []scopedDir{{path: e.ApplicationsDir(), scope: ScopeUser}}
	for _, dir := range e.DataDirs {
		dirs = append(dirs, scopedDir{path: filepath.Join(dir, "applications"), scope: ScopeSystem})
	}
	return dirs
}

type scopedDir struct {
	path  string
	scope Scope
}

// desktopFile is the parsed subset of a desktop entry that ownership
// resolution cares about.
type desktopFile struct {
	path string
	main map[string]string
}

func parseDesktopFile(path string) (*desktopFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	parsed := &desktopFile{path: path, main: map[string]string{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	inMain := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inMain = line == "[Desktop Entry]"
			continue
		}
		if !inMain {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, dup := parsed.main[key]; !dup {
			parsed.main[key] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return parsed, nil
}

// execProgram returns the first Exec= argument with desktop-entry quoting
// removed, or "" when Exec is missing.
func (d *desktopFile) execProgram() string {
	return firstExecArgument(d.main["Exec"])
}

func firstExecArgument(exec string) string {
	exec = strings.TrimSpace(exec)
	if exec == "" {
		return ""
	}
	if exec[0] != '"' {
		program, _, _ := strings.Cut(exec, " ")
		return program
	}
	var b strings.Builder
	escaped := false
	for _, r := range exec[1:] {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			return b.String()
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// defaultHandler returns the desktop-file ID registered as the default for a
// x-scheme-handler MIME type in the user's mimeapps.list, or "".
func (e Env) defaultHandler(mimeType string) string {
	f, err := os.Open(filepath.Join(e.ConfigHome, "mimeapps.list"))
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	inDefaults := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inDefaults = line == "[Default Applications]"
			continue
		}
		if !inDefaults {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != mimeType {
			continue
		}
		first, _, _ := strings.Cut(strings.TrimSpace(value), ";")
		return strings.TrimSpace(first)
	}
	return ""
}
