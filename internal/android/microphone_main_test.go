// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"fmt"
	"os"
	"testing"
)

// The OpenSL recorder now reads the persisted microphone switch from
// config.Paths().ConfigFile, so no test in this package may see the
// developer's real ~/.config/tipsy/config.json. Point the whole package at an
// empty XDG tree; individual tests override it with their own temp dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tipsy-android-xdg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tipsy android tests: temp XDG dir:", err)
		os.Exit(2)
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		fmt.Fprintln(os.Stderr, "tipsy android tests: set XDG_CONFIG_HOME:", err)
		os.Exit(2)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
