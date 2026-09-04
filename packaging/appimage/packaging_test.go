// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package appimage_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate packaging test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestAppRunUsesOnlyItsAppDir(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "appimage", "AppRun"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"$appdir/usr/bin/tipsy-gui", "$appdir/usr/lib", "$appdir/usr/plugins"} {
		if !strings.Contains(text, required) {
			t.Errorf("AppRun is missing %q", required)
		}
	}
	for _, forbidden := range []string{"$HOME", ".tipsy-private", "libroblox.so"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("AppRun contains forbidden release coupling %q", forbidden)
		}
	}
}

func TestQtConfigurationIsRelative(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "packaging", "appimage", "qt.conf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"Prefix=..", "Plugins=plugins", "Libraries=lib"} {
		if !strings.Contains(text, required) {
			t.Errorf("qt.conf is missing %q", required)
		}
	}
	if strings.Contains(text, "/usr") || strings.Contains(text, "/home") {
		t.Fatal("qt.conf contains an absolute host path")
	}
}

func TestAppImageBuilderRejectsUnpinnedTool(t *testing.T) {
	repo := repoRoot(t)
	appdir := t.TempDir()
	tool := filepath.Join(t.TempDir(), "appimagetool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(repo, "scripts", "build-appimage.sh"),
		"--appdir", appdir,
		"--version", "test",
		"--tool", tool,
		"--tool-sha256", strings.Repeat("0", 64),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("builder accepted an incorrect tool digest: %s", output)
	}
	if !strings.Contains(string(output), "SHA-256 mismatch") {
		t.Fatalf("unexpected error: %s", output)
	}
}

func TestReleaseGuardNamesForbiddenPayloads(t *testing.T) {
	repo := repoRoot(t)
	for _, test := range []struct {
		name     string
		relative string
	}{
		{name: "Roblox library", relative: "usr/lib/libroblox.so"},
		{name: "private docs", relative: ".tipsy-private/docs/note.txt"},
		{name: "account data", relative: "usr/share/app-data/session.bin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			appdir := minimalAppDir(t)
			path := filepath.Join(appdir, filepath.FromSlash(test.relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("forbidden fixture"), 0o644); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(filepath.Join(repo, "scripts", "check-release-tree.sh"), appdir)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("guard accepted %s", test.relative)
			}
			if !strings.Contains(string(output), "forbidden") && !strings.Contains(string(output), "unexpected top-level") {
				t.Fatalf("guard did not name forbidden content: %s", output)
			}
		})
	}
}

func minimalAppDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := []string{
		"AppRun",
		".DirIcon",
		"io.github.tipsy_linux.Tipsy.desktop",
		"tipsy.png",
		"usr/bin/tipsy",
		"usr/bin/tipsy-gui",
		"usr/bin/qt.conf",
		"usr/plugins/platforms/libqoffscreen.so",
		"usr/plugins/platforms/libqxcb.so",
		"usr/share/applications/io.github.tipsy_linux.Tipsy.desktop",
		"usr/share/icons/hicolor/512x512/apps/tipsy.png",
		"usr/share/licenses/tipsy/LICENSE",
		"usr/share/licenses/tipsy/NOTICE",
		"usr/share/metainfo/io.github.tipsy_linux.Tipsy.metainfo.xml",
		"usr/share/tipsy/build-info",
		"usr/share/tipsy/manifest.sha256",
	}
	for _, relative := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if relative == "AppRun" || relative == "usr/bin/tipsy" || relative == "usr/bin/tipsy-gui" {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte("packaging fixture\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
