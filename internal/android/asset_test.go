// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAssetBytesContentPrefix(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "content", "configs", "DataModelPatchConfig")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"ok":true}`)
	if err := os.WriteFile(filepath.Join(nested, "DataModelPatchConfig.json"), want, 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("configs/DataModelPatchConfig/DataModelPatchConfig.json")
	if err != nil {
		t.Fatalf("rbxasset-relative: %v", err)
	}
	if string(b) != string(want) {
		t.Fatalf("rbxasset-relative=%q", b)
	}
	b, err = openAssetBytes("content/configs/DataModelPatchConfig/DataModelPatchConfig.json")
	if err != nil {
		t.Fatalf("content-prefixed: %v", err)
	}
	if string(b) != string(want) {
		t.Fatalf("content-prefixed=%q", b)
	}
}

func TestOpenAssetBytesExtraContentAndAndroid(t *testing.T) {
	dir := t.TempDir()
	extra := filepath.Join(dir, "ExtraContent", "models", "DataModelPatch")
	andro := filepath.Join(dir, "android", "models", "UniversalApp")
	if err := os.MkdirAll(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(andro, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extra, "DataModelPatch.rbxm"), []byte("<roblox!dmp>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(andro, "UniversalApp.rbxm"), []byte("<roblox!ua>"), 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("models/DataModelPatch/DataModelPatch.rbxm")
	if err != nil {
		t.Fatalf("ExtraContent: %v", err)
	}
	if string(b) != "<roblox!dmp>" {
		t.Fatalf("ExtraContent=%q", b)
	}
	b, err = openAssetBytes("models/UniversalApp/UniversalApp.rbxm")
	if err != nil {
		t.Fatalf("android: %v", err)
	}
	if string(b) != "<roblox!ua>" {
		t.Fatalf("android=%q", b)
	}
}
