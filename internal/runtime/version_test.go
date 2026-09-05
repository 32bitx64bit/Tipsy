// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstalledVersionNameFromMeta(t *testing.T) {
	dir := t.TempDir()
	body := "{\n  \"packageName\": \"com.roblox.client\",\n  \"versionName\": \"2.736.1408\",\n  \"versionCode\": 2998\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedVersionName(dir); got != "2.736.1408" {
		t.Fatalf("version=%q", got)
	}
	if got := robloxUserAgent("2.736.1408"); got != "Roblox/2.736.1408 (Linux; Android 8.0.0; tipsy)" {
		t.Fatalf("ua=%q", got)
	}
}

func TestInstalledVersionNameMissingOrEmpty(t *testing.T) {
	if got := installedVersionName(""); got != "" {
		t.Fatalf("empty dir=%q", got)
	}
	if got := installedVersionName(t.TempDir()); got != "" {
		t.Fatalf("missing meta=%q", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := installedVersionName(dir); got != "" {
		t.Fatalf("empty versionName=%q", got)
	}
	if got := robloxUserAgent(""); got != "Roblox/0 (Linux; Android 8.0.0; tipsy)" {
		t.Fatalf("empty ua=%q", got)
	}
}
