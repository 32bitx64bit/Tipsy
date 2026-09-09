// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"unsafe"
)

func writeTestAPK(t testing.TB, dir string, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(dir, "base.apk")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// cAsset matches android_bridge.h AAsset (LP64). Tests inspect owned/buffer
// without cgo; this package rejects cgo in _test.go.
type cAsset struct {
	magic  uint64
	buffer unsafe.Pointer
	length int64
	pos    int64
	fd     int32
	owned  int32
}

func assetPinView(p unsafe.Pointer) (buf unsafe.Pointer, length int64, owned int) {
	if p == nil {
		return nil, 0, 0
	}
	a := (*cAsset)(p)
	return a.buffer, a.length, int(a.owned)
}

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

func TestOpenAssetBytesSearchOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("direct"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "content", "hello.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/hello.txt": []byte("zip"),
	})
	setAssetsLocked(dir, apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "direct" {
		t.Fatalf("dir-before-alias-and-apk=%q", b)
	}
	b, err = openAssetBytes("content/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "content" {
		t.Fatalf("content-prefixed must not reuse short-name blob=%q", b)
	}
}

func TestOpenAssetBytesCachesAndInvalidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "once.txt")
	if err := os.WriteFile(path, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	b1, err := openAssetBytes("once.txt")
	if err != nil {
		t.Fatal(err)
	}
	b2, err := openAssetBytes("once.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != "v1" || string(b2) != "v1" {
		t.Fatalf("cached reads %q %q", b1, b2)
	}
	if unsafe.SliceData(b1) != unsafe.SliceData(b2) {
		t.Fatal("repeat open must reuse the inflated backing slice")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	b3, err := openAssetBytes("once.txt")
	if err != nil {
		t.Fatalf("deleted file should still hit name cache: %v", err)
	}
	if string(b3) != "v1" {
		t.Fatalf("stale cache=%q", b3)
	}

	other := t.TempDir()
	setAssetsLocked(other, "")
	_, err = openAssetBytes("once.txt")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dir change must drop cache: %v", err)
	}

	apk1 := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/hello.txt": []byte("apk-a"),
	})
	apk2 := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/hello.txt": []byte("apk-b"),
	})
	setAssetsLocked("", apk1)
	got, err := openAssetBytes("hello.txt")
	if err != nil || string(got) != "apk-a" {
		t.Fatalf("apk1: %q %v", got, err)
	}
	setAssetsLocked("", apk2)
	got, err = openAssetBytes("hello.txt")
	if err != nil || string(got) != "apk-b" {
		t.Fatalf("apk path change must drop zip cache: %q %v", got, err)
	}
}

func TestOpenAssetBytesDirPathCacheAcrossAliases(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "content")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nested, "only.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("only.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again, err := openAssetBytes("content/only.json")
	if err != nil {
		t.Fatalf("alias must reuse path cache: %v", err)
	}
	if unsafe.SliceData(b) != unsafe.SliceData(again) {
		t.Fatal("content/ alias should share the same blob as the short name")
	}
}

func TestOpenAssetBytesAPKIndexAndContentPrefix(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/hello.txt":                           []byte("from-zip"),
		"assets/content/configs/DataModelPatch.json": []byte(`{"zip":true}`),
		"/assets/slash.bin":                          []byte("slash"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "from-zip" {
		t.Fatalf("apk hello=%q", b)
	}
	assetsMu.RLock()
	first := apkArch
	assetsMu.RUnlock()
	if first == nil || first.lookup("assets/hello.txt") == nil {
		t.Fatal("expected kept zip index")
	}

	cfg, err := openAssetBytes("configs/DataModelPatch.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg) != `{"zip":true}` {
		t.Fatalf("apk content alias=%q", cfg)
	}
	prefixed, err := openAssetBytes("content/configs/DataModelPatch.json")
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.SliceData(cfg) != unsafe.SliceData(prefixed) {
		t.Fatal("zip content alias must reuse the inflated entry")
	}
	slash, err := openAssetBytes("slash.bin")
	if err != nil {
		t.Fatal(err)
	}
	if string(slash) != "slash" {
		t.Fatalf("leading-slash zip name=%q", slash)
	}

	assetsMu.RLock()
	second := apkArch
	assetsMu.RUnlock()
	if first != second {
		t.Fatal("second APK open reparsed the central directory")
	}

	_, err = openAssetBytes("missing.bin")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing apk asset: %v", err)
	}
}

func TestOpenAssetBytesEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("empty")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("empty=%q", b)
	}
	_, err = openAssetBytes("nope")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	p := assetFromBytes(b)
	if p == nil {
		t.Fatal("empty AAsset")
	}
	buf, length, owned := assetPinView(p)
	if buf != unsafe.Pointer(&emptyAsset) {
		t.Fatal("empty asset must use the static byte, not CMalloc")
	}
	if owned != 0 || length != 0 {
		t.Fatalf("empty owned=%d len=%d", owned, length)
	}

	p2 := assetFromBytes(nil)
	if p2 == nil {
		t.Fatal("nil AAsset")
	}
	buf, _, owned = assetPinView(p2)
	if buf != unsafe.Pointer(&emptyAsset) {
		t.Fatal("nil asset must use the static byte")
	}
	if owned != 0 {
		t.Fatalf("nil owned=%d", owned)
	}
}

func TestOpenAssetBytesPinLifetime(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/pin.txt": []byte("pin-me"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	b, err := openAssetBytes("pin.txt")
	if err != nil {
		t.Fatal(err)
	}
	p := assetFromBytes(b)
	if p == nil {
		t.Fatal("AAsset")
	}
	buf, length, owned := assetPinView(p)
	data := unsafe.SliceData(b)
	if buf != unsafe.Pointer(data) {
		t.Fatal("C buffer must be the pinned Go blob, not a CMalloc copy")
	}
	if owned != 0 {
		t.Fatalf("owned=%d; C must not free a Go-owned pin", owned)
	}
	if length != int64(len(b)) {
		t.Fatalf("length=%d want %d", length, len(b))
	}

	key := uintptr(unsafe.Pointer(data))
	assetsMu.RLock()
	_, ok := pinned[key]
	assetsMu.RUnlock()
	if !ok {
		t.Fatal("expected Go pinner for nonempty blob")
	}

	// owned=0: ndk.c AAsset_close skips free. Pin lifetime stays Go-owned
	// until setAssetsLocked; this test does not call close (no cgo in tests).
	if string(b) != "pin-me" {
		t.Fatal("Go blob must remain readable while pinned")
	}
	assetsMu.RLock()
	_, still := pinned[key]
	assetsMu.RUnlock()
	if !still {
		t.Fatal("pin lifetime is Go-owned until setAssetsLocked, not per-open")
	}

	setAssetsLocked("", "")
	assetsMu.RLock()
	n := len(pinned)
	assetsMu.RUnlock()
	if n != 0 {
		t.Fatalf("dir/apk change must unpin; pinned=%d", n)
	}
	if string(b) != "pin-me" {
		t.Fatal("local Go slice must remain readable after unpin")
	}
}

func TestOpenAssetBytesConcurrent(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/hello.txt":              []byte("hi"),
		"assets/content/configs/x.json": []byte(`{"n":1}`),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				b, err := openAssetBytes("hello.txt")
				if err != nil || string(b) != "hi" {
					t.Errorf("hello: %q %v", b, err)
					return
				}
				b, err = openAssetBytes("configs/x.json")
				if err != nil || string(b) != `{"n":1}` {
					t.Errorf("cfg: %q %v", b, err)
					return
				}
				_, err = openAssetBytes("missing.bin")
				if !errors.Is(err, os.ErrNotExist) {
					t.Errorf("missing: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func BenchmarkOpenAssetBytes(b *testing.B) {
	dir := b.TempDir()
	apk := writeTestAPK(b, dir, map[string][]byte{
		"assets/hello.txt": []byte("benchmark-payload"),
	})

	b.Run("first", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			setAssetsLocked("", "")
			setAssetsLocked("", apk)
			b.StartTimer()
			got, err := openAssetBytes("hello.txt")
			if err != nil {
				b.Fatal(err)
			}
			if string(got) != "benchmark-payload" {
				b.Fatalf("got %q", got)
			}
		}
		b.StopTimer()
		setAssetsLocked("", "")
	})

	b.Run("reopen", func(b *testing.B) {
		setAssetsLocked("", apk)
		b.Cleanup(func() { setAssetsLocked("", "") })
		if got, err := openAssetBytes("hello.txt"); err != nil || string(got) != "benchmark-payload" {
			b.Fatalf("warmup: %q %v", got, err)
		}

		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got, err := openAssetBytes("hello.txt")
			if err != nil {
				b.Fatal(err)
			}
			if len(got) == 0 {
				b.Fatal("empty")
			}
		}
	})
}
