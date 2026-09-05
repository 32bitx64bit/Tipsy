// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectX86PackageFilesRejectsUptodownInstaller(t *testing.T) {
	t.Parallel()
	installer := zipBytes(t, map[string]string{
		"AndroidManifest.xml":              "manifest",
		"lib/x86_64/libuptodown-native.so": "native",
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "uptodown-com.roblox.client.apk")
	if err := os.WriteFile(path, installer, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := selectX86PackageFiles(path, dir, "test")
	if ErrorKindOf(err) != ErrWrongPackage || !strings.Contains(err.Error(), "installer") {
		t.Fatalf("err=%v", err)
	}
}

func TestKeepX86PackageFilesDropsExtraSplitsFromXAPK(t *testing.T) {
	t.Parallel()
	base := zipBytes(t, map[string]string{"AndroidManifest.xml": "base"})
	split := zipBytes(t, map[string]string{"AndroidManifest.xml": "split", "lib/x86_64/libroblox.so": "native"})
	bundle := zipBytes(t, map[string]string{
		"com.roblox.client.apk":  string(base),
		"config.x86_64.apk":      string(split),
		"config.arm64_v8a.apk":   string(base),
		"config.en.apk":          string(base),
		"uptodown-app-store.apk": string(base),
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "roblox.apk")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := keepX86PackageFiles([]string{path}, filepath.Join(dir, "filtered"), "test")
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "split_config.x86_64.apk" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestKeepX86FromSplitDirectoryDropsLanguageAndARM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestZIP(t, filepath.Join(dir, "base.apk"), map[string][]byte{"AndroidManifest.xml": []byte("base")})
	writeTestZIP(t, filepath.Join(dir, "split_config.x86_64.apk"), map[string][]byte{"lib/x86_64/libroblox.so": []byte("native")})
	writeTestZIP(t, filepath.Join(dir, "split_config.en.apk"), map[string][]byte{"AndroidManifest.xml": []byte("en")})
	writeTestZIP(t, filepath.Join(dir, "split_config.arm64_v8a.apk"), map[string][]byte{"lib/arm64-v8a/libroblox.so": []byte("arm")})
	paths, err := keepX86PackageFiles([]string{
		filepath.Join(dir, "base.apk"),
		filepath.Join(dir, "split_config.x86_64.apk"),
		filepath.Join(dir, "split_config.en.apk"),
		filepath.Join(dir, "split_config.arm64_v8a.apk"),
	}, filepath.Join(dir, "filtered"), "test")
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "split_config.x86_64.apk" {
		t.Fatalf("paths=%v", paths)
	}
}
