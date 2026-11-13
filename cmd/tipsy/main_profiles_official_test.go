// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package main

import (
	"os"
	"testing"
)

func TestTipsyMainIgnoresProfileDirInOfficialRelease(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TIPSY_PPROF_DIR", dir)
	t.Setenv("TIPSY_PPROF_CLI_SURFACE", "")
	if code := tipsyMain([]string{"version"}); code != 0 {
		t.Fatalf("version exit %d", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("official_release wrote process profiles: %v", entries)
	}
}
