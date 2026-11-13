// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package desktop

import (
	"testing"
)

func TestEnvFromOSRejectsRelativeXDGBaseDirs(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("HOME", "/home/x")
	t.Setenv("XDG_DATA_HOME", "relative/data")
	t.Setenv("XDG_CONFIG_HOME", "../cfg")
	env := EnvFromOS()
	if env.DataHome != "/home/x/.local/share" {
		t.Fatalf("DataHome = %q, want /home/x/.local/share", env.DataHome)
	}
	if env.ConfigHome != "/home/x/.config" {
		t.Fatalf("ConfigHome = %q, want /home/x/.config", env.ConfigHome)
	}
	if dir := env.ApplicationsDir(); dir != "/home/x/.local/share/applications" {
		t.Fatalf("ApplicationsDir = %q, want a directory under $HOME", dir)
	}
}

func TestEnvFromOSKeepsAbsoluteXDGBaseDirs(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("HOME", "/home/x")
	t.Setenv("XDG_DATA_HOME", "/data/home")
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	env := EnvFromOS()
	if env.DataHome != "/data/home" || env.ConfigHome != "/cfg" {
		t.Fatalf("env = %+v", env)
	}
}

func TestEnvFromOSDefaultXDGDirsAreUnderHome(t *testing.T) {
	t.Setenv("FLATPAK_ID", "")
	t.Setenv("HOME", "/home/x")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	env := EnvFromOS()
	if env.DataHome != "/home/x/.local/share" || env.ConfigHome != "/home/x/.config" {
		t.Fatalf("env = %+v", env)
	}
}
