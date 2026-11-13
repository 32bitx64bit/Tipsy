// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTipsyMainFlushesProfilesBeforeExit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TIPSY_PPROF_DIR", dir)
	t.Setenv("TIPSY_PPROF_CLI_SURFACE", "")
	if code := tipsyMain([]string{"version"}); code != 0 {
		t.Fatalf("version exit %d", code)
	}
	for _, name := range []string{"cpu.pprof", "heap.pprof", "alloc.pprof", "goroutine.pprof", "threadcreate.pprof"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}
