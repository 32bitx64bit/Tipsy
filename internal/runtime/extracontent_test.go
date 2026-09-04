// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRbxassetRelPath(t *testing.T) {
	got, err := rbxassetRelPath("rbxasset://models/DataModelPatch/DataModelPatch.rbxm")
	if err != nil {
		t.Fatal(err)
	}
	if got != "models/DataModelPatch/DataModelPatch.rbxm" {
		t.Fatalf("got %q", got)
	}
	got, err = rbxassetRelPath("rbxasset://content/models/DataModelPatch/DataModelPatch.rbxm")
	if err != nil {
		t.Fatal(err)
	}
	if got != "models/DataModelPatch/DataModelPatch.rbxm" {
		t.Fatalf("stripped content prefix: %q", got)
	}
	for _, bad := range []string{
		"rbxassetid://1",
		"rbxasset://../evil.rbxm",
		"rbxasset://models/../DataModelPatch.rbxm",
		"",
	} {
		if _, err := rbxassetRelPath(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestEnsureOfficialPatchesFetchesAndPlaces(t *testing.T) {
	body := []byte("<roblox!official-patch-fixture>")
	sum := md5.Sum(body)
	wantHash := hex.EncodeToString(sum[:])

	var gotID, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = r.URL.Query().Get("id")
		gotVer = r.URL.Query().Get("version")
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	oldURL, oldClient := assetDeliveryURL, assetHTTPClient
	t.Cleanup(func() {
		assetDeliveryURL = oldURL
		assetHTTPClient = oldClient
	})
	assetHTTPClient = srv.Client()
	assetDeliveryURL = func(assetID, version string) string {
		return srv.URL + "/v1/asset/?id=" + assetID + "&version=" + version
	}

	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "content", "configs", "DataModelPatchConfig")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"AssetId":"115043838414809","AssetVersion":"6833","LocalAssetURI":"rbxasset://models/DataModelPatch/DataModelPatch.rbxm","LocalAssetHash":"` + wantHash + `"}`
	if err := os.WriteFile(filepath.Join(cfgDir, "DataModelPatchConfig.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureOfficialPatches(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if gotID != "115043838414809" || gotVer != "6833" {
		t.Fatalf("query id=%s version=%s", gotID, gotVer)
	}
	for _, root := range []string{"content", "ExtraContent", "android"} {
		p := filepath.Join(dir, root, "models", "DataModelPatch", "DataModelPatch.rbxm")
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("%s bytes mismatch", p)
		}
	}
	ota := filepath.Join(dir, otaRbxmCacheFolder, "DataModelPatch_115043838414809_6833_cache")
	gotOTA, err := os.ReadFile(ota)
	if err != nil {
		t.Fatalf("RbxmFileManager OTA cache: %v", err)
	}
	if !bytes.Equal(gotOTA, body) {
		t.Fatal("OTA cache bytes mismatch")
	}

	hits := 0
	assetDeliveryURL = func(assetID, version string) string {
		hits++
		return srv.URL + "/nope"
	}
	if err := EnsureOfficialPatches(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("refetched when hash already matched (%d)", hits)
	}
}

func TestEnsureOfficialPatchesGunzipsAndSkipsHashlessConfigs(t *testing.T) {
	plain := []byte("<roblox!gzipped-fixture>")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(plain)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(gz.Bytes())
	}))
	t.Cleanup(srv.Close)
	oldURL, oldClient := assetDeliveryURL, assetHTTPClient
	t.Cleanup(func() {
		assetDeliveryURL = oldURL
		assetHTTPClient = oldClient
	})
	assetHTTPClient = srv.Client()
	assetDeliveryURL = func(assetID, version string) string { return srv.URL }

	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "content", "configs", "DataModelPatchConfig")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"AssetId":"1","AssetVersion":"2","LocalAssetURI":"rbxasset://models/DataModelPatch/DataModelPatch.rbxm","LocalAssetHash":"` + hex.EncodeToString(sum[:]) + `"}`
	if err := os.WriteFile(filepath.Join(cfgDir, "DataModelPatchConfig.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "content", "configs", "PlaybackAppPatchConfig")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "PlaybackAppPatchConfig.json"), []byte(`{"LocalAssetURI":"rbxasset://models/PlaybackAppPatch/PlaybackAppPatch.rbxm"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureOfficialPatches(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "content", "models", "DataModelPatch", "DataModelPatch.rbxm"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("gunzip mismatch %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "content", "models", "PlaybackAppPatch", "PlaybackAppPatch.rbxm")); err == nil {
		t.Fatal("URI-only config must not invent a download")
	}
}

func TestEnsureOfficialPatchesRejectsHashMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<roblox!wrong>"))
	}))
	t.Cleanup(srv.Close)
	oldURL, oldClient := assetDeliveryURL, assetHTTPClient
	t.Cleanup(func() {
		assetDeliveryURL = oldURL
		assetHTTPClient = oldClient
	})
	assetHTTPClient = srv.Client()
	assetDeliveryURL = func(assetID, version string) string { return srv.URL }

	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "content", "configs", "DataModelPatchConfig")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"AssetId":"1","AssetVersion":"1","LocalAssetURI":"rbxasset://models/DataModelPatch/DataModelPatch.rbxm","LocalAssetHash":"00000000000000000000000000000000"}`
	if err := os.WriteFile(filepath.Join(cfgDir, "DataModelPatchConfig.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	err := EnsureOfficialPatches(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "md5") {
		t.Fatalf("err=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "content", "models", "DataModelPatch", "DataModelPatch.rbxm")); statErr == nil {
		t.Fatal("must not write mismatched patch")
	}
}

func TestEnsureOfficialPatchesContextCanceled(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "content", "configs", "DataModelPatchConfig")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "DataModelPatchConfig.json"), []byte(`{"AssetId":"1","LocalAssetURI":"rbxasset://models/DataModelPatch/DataModelPatch.rbxm"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := EnsureOfficialPatches(ctx, dir); err == nil {
		t.Fatal("expected context error")
	}
}

func TestEnsureOfficialPatchesSkipsPresentExtraContent(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("<roblox!should-not-fetch>"))
	}))
	t.Cleanup(srv.Close)
	oldURL, oldClient := assetDeliveryURL, assetHTTPClient
	t.Cleanup(func() {
		assetDeliveryURL = oldURL
		assetHTTPClient = oldClient
	})
	assetHTTPClient = srv.Client()
	assetDeliveryURL = func(assetID, version string) string { return srv.URL }

	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "content", "configs", "UniversalAppPatchConfig")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"AssetId":"118593852151835","AssetVersion":"11766","LocalAssetURI":"rbxasset://models/UniversalApp/UniversalApp.rbxm","LocalAssetHash":"00000000000000000000000000000000"}`
	if err := os.WriteFile(filepath.Join(cfgDir, "UniversalAppPatchConfig.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "ExtraContent", "models", "UniversalApp")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "UniversalApp.rbxm"), []byte("<roblox!bundled>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureOfficialPatches(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("fetched bundled ExtraContent (%d)", hits)
	}
	got, err := os.ReadFile(filepath.Join(dir, "content", "models", "UniversalApp", "UniversalApp.rbxm"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<roblox!bundled>" {
		t.Fatalf("copy=%q", got)
	}
	ota := filepath.Join(dir, otaRbxmCacheFolder, "DataModelPatch_118593852151835_11766_cache")
	gotOTA, err := os.ReadFile(ota)
	if err != nil {
		t.Fatalf("OTA cache from bundled ExtraContent: %v", err)
	}
	if string(gotOTA) != "<roblox!bundled>" {
		t.Fatalf("OTA cache=%q", gotOTA)
	}
}

func TestOtaRbxmCacheRootsRuntimeLayout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	assets := filepath.Join(RuntimeDir(), "assets")
	got := otaRbxmCacheRoots(assets)
	storage := AppStorage()
	wantApp := filepath.Join(storage.FilesDir, "appData", otaRbxmCacheFolder)
	wantCache := filepath.Join(storage.CacheDir, otaRbxmCacheFolder)
	if len(got) != 2 || got[0] != wantApp || got[1] != wantCache {
		t.Fatalf("roots=%q", got)
	}
}

func TestEnsureOfficialPatchesNoConfigs(t *testing.T) {
	if err := EnsureOfficialPatches(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := EnsureOfficialPatches(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestMaybeGunzipPatchPassthrough(t *testing.T) {
	in := []byte("<roblox!raw>")
	got, err := maybeGunzipPatch(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatal("passthrough")
	}
}

func TestAssetDeliveryURL(t *testing.T) {
	u := assetDeliveryURL("115043838414809", "6833")
	if !strings.HasPrefix(u, officialAssetDeliveryHost+officialAssetDeliveryPath+"?") {
		t.Fatalf("url %s", u)
	}
	if !strings.Contains(u, "id=115043838414809") || !strings.Contains(u, "version=6833") {
		t.Fatalf("query %s", u)
	}
}
