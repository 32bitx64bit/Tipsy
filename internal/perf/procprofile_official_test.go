// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package perf

import (
	"os"
	"testing"
)

func TestStartFromEnvIsNoopInOfficialRelease(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TIPSY_PPROF_DIR", dir)
	stop, err := StartFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	stop()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("official_release wrote process profiles: %v", entries)
	}
}
