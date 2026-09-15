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
	"time"
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

func assertAssetCacheAccounting(t testing.TB, snapshot assetCacheSnapshot) {
	t.Helper()
	if snapshot.NameEntries != snapshot.PositiveEntries+snapshot.NegativeEntries {
		t.Fatalf("name accounting mismatch: %+v", snapshot)
	}
	if snapshot.NegativeEntries != snapshot.NegativeCandidates+snapshot.NegativeOther {
		t.Fatalf("negative accounting mismatch: %+v", snapshot)
	}
	if snapshot.CachedBytes != snapshot.BorrowedCacheBytes+snapshot.EvictableCacheBytes {
		t.Fatalf("cached byte ownership mismatch: %+v", snapshot)
	}
	if snapshot.BorrowedCacheBytes > snapshot.CachedBytes || snapshot.EvictableCacheBytes > snapshot.CachedBytes {
		t.Fatalf("cache byte partition exceeds cached bytes: %+v", snapshot)
	}
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
	snapshot := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, snapshot)
	if snapshot.NameEntries != 2 || snapshot.PositiveEntries != 2 || snapshot.PathEntries != 1 || snapshot.ZipEntries != 0 || snapshot.BlobCount != 1 || snapshot.CachedBytes != int64(len(b)) || snapshot.BorrowedCacheBytes != 0 || snapshot.EvictableCacheBytes != int64(len(b)) {
		t.Fatalf("directory alias/blob accounting = %+v", snapshot)
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
	first := assetsForOpen()
	if snapshot := first.snapshot(); !snapshot.ArchiveOpen {
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

	second := assetsForOpen()
	if first != second {
		t.Fatal("second APK open reparsed the central directory")
	}

	_, err = openAssetBytes("missing.bin")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing apk asset: %v", err)
	}
}

func TestAssetCacheSnapshotAccountsAliasesAndBlobOwnership(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("alias-blob")
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/content/alias.txt": payload,
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })
	before := assetCacheSnapshotForTest()

	short, err := openAssetBytes("alias.txt")
	if err != nil {
		t.Fatal(err)
	}
	prefixed, err := openAssetBytes("content/alias.txt")
	if err != nil {
		t.Fatal(err)
	}
	if unsafe.SliceData(short) != unsafe.SliceData(prefixed) {
		t.Fatal("alias paths must share one blob")
	}

	snapshot := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, snapshot)
	if snapshot.NameEntries != 2 || snapshot.PositiveEntries != 2 || snapshot.PathEntries != 0 || snapshot.ZipEntries != 1 || snapshot.BlobCount != 1 {
		t.Fatalf("alias/blob entry accounting = %+v", snapshot)
	}
	if snapshot.CachedBytes != int64(len(payload)) || snapshot.BorrowedCacheBytes != 0 || snapshot.EvictableCacheBytes != int64(len(payload)) {
		t.Fatalf("unborrowed alias/blob byte accounting = %+v", snapshot)
	}
	if snapshot.ActiveHandles != before.ActiveHandles || snapshot.PinnedBytes != before.PinnedBytes || snapshot.LiveWorkingSetBytes != before.LiveWorkingSetBytes {
		t.Fatalf("cache-only aliases changed live ownership: before=%+v after=%+v", before, snapshot)
	}
	t.Logf("asset cache accounting snapshot generation=%d names=%d blobs=%d cached_bytes=%d borrowed_cache_bytes=%d evictable_cache_bytes=%d active_handles=%d pinned_bytes=%d in_flight=%d negative_candidates=%d", snapshot.Generation, snapshot.NameEntries, snapshot.BlobCount, snapshot.CachedBytes, snapshot.BorrowedCacheBytes, snapshot.EvictableCacheBytes, snapshot.ActiveHandles, snapshot.PinnedBytes, snapshot.InFlightLoads, snapshot.NegativeCandidates)

	asset := assetFromBytes(short)
	if asset == nil {
		t.Fatal("newBorrowedAsset allocation failed")
	}
	borrowed := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, borrowed)
	if borrowed.BlobCount != 1 || borrowed.CachedBytes != int64(len(payload)) || borrowed.BorrowedCacheBytes != int64(len(payload)) || borrowed.EvictableCacheBytes != 0 {
		closeAssetForTest(asset)
		t.Fatalf("borrowed alias/blob byte accounting = %+v", borrowed)
	}
	if borrowed.ActiveHandles != before.ActiveHandles+1 || borrowed.PinnedBytes != before.PinnedBytes+int64(len(payload)) || borrowed.LiveWorkingSetBytes != before.LiveWorkingSetBytes+int64(len(payload)) {
		closeAssetForTest(asset)
		t.Fatalf("borrowed live ownership = %+v, before=%+v", borrowed, before)
	}

	closeAssetForTest(asset)
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.CachedBytes != int64(len(payload)) || after.BorrowedCacheBytes != 0 || after.EvictableCacheBytes != int64(len(payload)) || after.ActiveHandles != before.ActiveHandles || after.PinnedBytes != before.PinnedBytes {
		t.Fatalf("last close must restore an evictable cached blob: %+v, before=%+v", after, before)
	}
}

func TestAssetCacheSnapshotClassifiesNegativeCandidates(t *testing.T) {
	setAssetsLocked("", "")
	t.Cleanup(func() { setAssetsLocked("", "") })
	before := assetCacheSnapshotForTest()
	_, err := openAssetBytes("missing.txt")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing asset error = %v", err)
	}
	snapshot := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, snapshot)
	if snapshot.NameEntries != 1 || snapshot.PositiveEntries != 0 || snapshot.NegativeEntries != 1 || snapshot.NegativeCandidates != 1 || snapshot.NegativeOther != 0 || snapshot.BlobCount != 0 || snapshot.CachedBytes != 0 || snapshot.EvictableCacheBytes != 0 {
		t.Fatalf("negative candidate accounting = %+v", snapshot)
	}
	if snapshot.ActiveHandles != before.ActiveHandles || snapshot.PinnedBytes != before.PinnedBytes || snapshot.LiveWorkingSetBytes != before.LiveWorkingSetBytes {
		t.Fatalf("negative cache entry changed live ownership: before=%+v after=%+v", before, snapshot)
	}
}

func TestAssetCacheBudgetEvictsAliasesAtomically(t *testing.T) {
	first := []byte("first")
	second := []byte("second")
	restorePolicy := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  int64(len(second)),
		negativeCap: 2,
		negativeTTL: time.Minute,
	})
	t.Cleanup(restorePolicy)

	apk := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/content/first.txt": first,
		"assets/second.txt":        second,
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	short, err := openAssetBytes("first.txt")
	if err != nil || string(short) != string(first) {
		t.Fatalf("short alias: %q %v", short, err)
	}
	prefixed, err := openAssetBytes("content/first.txt")
	if err != nil || unsafe.SliceData(short) != unsafe.SliceData(prefixed) {
		t.Fatalf("prefixed alias: %q %v", prefixed, err)
	}
	before := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, before)
	if before.CacheByteBudget != int64(len(second)) || before.NameEntries != 2 || before.BlobCount != 1 || before.CachedBytes != int64(len(first)) || before.EvictableCacheBytes != int64(len(first)) {
		t.Fatalf("before budget trim = %+v", before)
	}
	t.Logf("asset cache policy before generation=%d blobs=%d cached_bytes=%d evictable_cache_bytes=%d byte_budget=%d negative_candidates=%d", before.Generation, before.BlobCount, before.CachedBytes, before.EvictableCacheBytes, before.CacheByteBudget, before.NegativeCandidates)

	got, err := openAssetBytes("second.txt")
	if err != nil || string(got) != string(second) {
		t.Fatalf("second asset: %q %v", got, err)
	}
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.NameEntries != 1 || after.PositiveEntries != 1 || after.BlobCount != 1 || after.ZipEntries != 1 || after.CachedBytes != int64(len(second)) || after.BorrowedCacheBytes != 0 || after.EvictableCacheBytes != int64(len(second)) || after.CachedBytes > after.CacheByteBudget {
		t.Fatalf("alias eviction must leave one budget-sized blob: %+v", after)
	}
	t.Logf("asset cache policy after generation=%d blobs=%d cached_bytes=%d evictable_cache_bytes=%d byte_budget=%d negative_candidates=%d", after.Generation, after.BlobCount, after.CachedBytes, after.EvictableCacheBytes, after.CacheByteBudget, after.NegativeCandidates)

	var firstReads int
	restoreRead := setAssetTestBeforeZipRead(func(name string) error {
		if name == "assets/content/first.txt" {
			firstReads++
		}
		return nil
	})
	t.Cleanup(restoreRead)
	reloaded, err := openAssetBytes("first.txt")
	if err != nil || string(reloaded) != string(first) || firstReads != 1 {
		t.Fatalf("all aliases must leave cache atomically and reload once: %q %v reads=%d", reloaded, err, firstReads)
	}
}

func TestAssetCacheBudgetEvictsInactiveBlobWhileUnrelatedColdLoadRuns(t *testing.T) {
	borrowedData := []byte("borrow")
	restoreHighPolicy := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  int64(len(borrowedData)),
		negativeCap: 2,
		negativeTTL: time.Minute,
	})
	t.Cleanup(restoreHighPolicy)

	apk := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/borrow.txt":  borrowedData,
		"assets/trigger.txt": []byte("t"),
		"assets/cold.txt":    []byte("c"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	data, err := openAssetBytes("borrow.txt")
	if err != nil || string(data) != string(borrowedData) {
		t.Fatalf("borrow source: %q %v", data, err)
	}
	baseline := assetCacheSnapshotForTest()
	asset := assetFromBytes(data)
	if asset == nil {
		t.Fatal("newBorrowedAsset allocation failed")
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeAssetForTest(asset)
		}
	})

	restoreHighPolicy()
	restoreLowPolicy := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  1,
		negativeCap: 2,
		negativeTTL: time.Minute,
	})
	t.Cleanup(restoreLowPolicy)

	// Finishing this load invokes the selector while borrow.txt is still pinned.
	if got, err := openAssetBytes("trigger.txt"); err != nil || string(got) != "t" {
		t.Fatalf("trigger source: %q %v", got, err)
	}
	pinned := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, pinned)
	if pinned.CachedBytes != int64(len(borrowedData)) || pinned.BorrowedCacheBytes != int64(len(borrowedData)) || pinned.EvictableCacheBytes != 0 || pinned.ActiveHandles != baseline.ActiveHandles+1 || pinned.PinnedBytes != baseline.PinnedBytes+int64(len(borrowedData)) {
		t.Fatalf("budget selector chose a pinned blob: baseline=%+v pinned=%+v", baseline, pinned)
	}
	t.Logf("asset cache borrow before-close generation=%d cached_bytes=%d borrowed_cache_bytes=%d evictable_cache_bytes=%d active_handles=%d pinned_bytes=%d in_flight=%d byte_budget=%d", pinned.Generation, pinned.CachedBytes, pinned.BorrowedCacheBytes, pinned.EvictableCacheBytes, pinned.ActiveHandles, pinned.PinnedBytes, pinned.InFlightLoads, pinned.CacheByteBudget)

	entered := make(chan struct{})
	release := make(chan struct{})
	restoreRead := setAssetTestBeforeZipRead(func(name string) error {
		if name != "assets/cold.txt" {
			return nil
		}
		close(entered)
		<-release
		return nil
	})
	t.Cleanup(restoreRead)
	coldDone := make(chan error, 1)
	go func() {
		_, err := openAssetBytes("cold.txt")
		coldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cold reader did not reach barrier")
	}

	closeAssetForTest(asset)
	closed = true
	duringCold := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, duringCold)
	if duringCold.InFlightLoads != 1 || duringCold.CachedBytes != 0 || duringCold.BorrowedCacheBytes != 0 || duringCold.EvictableCacheBytes != 0 || duringCold.ActiveHandles != baseline.ActiveHandles || duringCold.PinnedBytes != baseline.PinnedBytes {
		t.Fatalf("close during unrelated cold load must evict the now-inactive blob: baseline=%+v during=%+v", baseline, duringCold)
	}
	t.Logf("asset cache borrow after-close-during-unrelated-cold generation=%d cached_bytes=%d borrowed_cache_bytes=%d evictable_cache_bytes=%d active_handles=%d pinned_bytes=%d in_flight=%d byte_budget=%d", duringCold.Generation, duringCold.CachedBytes, duringCold.BorrowedCacheBytes, duringCold.EvictableCacheBytes, duringCold.ActiveHandles, duringCold.PinnedBytes, duringCold.InFlightLoads, duringCold.CacheByteBudget)

	close(release)
	if err := <-coldDone; err != nil {
		t.Fatalf("cold load: %v", err)
	}
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.InFlightLoads != 0 || after.CachedBytes != 1 || after.BorrowedCacheBytes != 0 || after.EvictableCacheBytes != 1 || after.CachedBytes > after.CacheByteBudget || after.ActiveHandles != baseline.ActiveHandles || after.PinnedBytes != baseline.PinnedBytes {
		t.Fatalf("cold completion must trim the now-inactive older blob: baseline=%+v after=%+v", baseline, after)
	}
	t.Logf("asset cache borrow after-cold-complete generation=%d cached_bytes=%d borrowed_cache_bytes=%d evictable_cache_bytes=%d active_handles=%d pinned_bytes=%d in_flight=%d byte_budget=%d", after.Generation, after.CachedBytes, after.BorrowedCacheBytes, after.EvictableCacheBytes, after.ActiveHandles, after.PinnedBytes, after.InFlightLoads, after.CacheByteBudget)
}

func TestAssetCachePolicyProtectsOnlyPublishingBlob(t *testing.T) {
	restore := setAssetCachePolicyForTest(assetCachePolicy{byteBudget: 0, negativeCap: 2, negativeTTL: time.Minute})
	t.Cleanup(restore)

	cache := newAssetCache(999, "", "")
	load := &assetLoad{done: make(chan struct{})}
	data := []byte("publishing")

	cache.mu.Lock()
	cache.protectLoadBlobLocked(load, data)
	cache.setNamePositiveLocked("publishing", data)
	cache.enforceAssetCachePolicyLocked(assetCacheNow())
	cache.mu.Unlock()
	protected := cache.snapshot()

	cache.mu.Lock()
	cache.releaseLoadBlobLocked(load)
	cache.enforceAssetCachePolicyLocked(assetCacheNow())
	cache.mu.Unlock()
	released := cache.snapshot()

	if protected.CachedBytes != int64(len(data)) || protected.BlobCount != 1 || protected.PositiveEntries != 1 {
		t.Fatalf("in-flight publication was evicted: %+v", protected)
	}
	if released.CachedBytes != 0 || released.BlobCount != 0 || released.PositiveEntries != 0 {
		t.Fatalf("completed publication remained protected: %+v", released)
	}
}

func TestAssetNegativeCachePolicyCapsExpiresAndInvalidatesSource(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	restoreClock := setAssetCacheNowForTest(func() time.Time { return now })
	t.Cleanup(restoreClock)
	restorePolicy := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  assetCacheByteBudget,
		negativeCap: 2,
		negativeTTL: 10 * time.Second,
	})
	t.Cleanup(restorePolicy)

	missing := t.TempDir()
	setAssetsLocked(missing, "")
	t.Cleanup(func() { setAssetsLocked("", "") })
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := openAssetBytes(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("negative %q: %v", name, err)
		}
	}
	capped := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, capped)
	if capped.NegativeEntries != 2 || capped.NegativeCandidates != 2 || capped.NegativeOther != 0 || capped.NameEntries != 2 || capped.CachedBytes != 0 || capped.NegativeCacheLimit != 2 {
		t.Fatalf("bounded ENOENT cache = %+v", capped)
	}
	t.Logf("asset negative policy before generation=%d names=%d negative_candidates=%d negative_limit=%d cached_bytes=%d", capped.Generation, capped.NameEntries, capped.NegativeCandidates, capped.NegativeCacheLimit, capped.CachedBytes)

	replacement := t.TempDir()
	if err := os.WriteFile(filepath.Join(replacement, "c.txt"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(replacement, "")
	if got, err := openAssetBytes("c.txt"); err != nil || string(got) != "replacement" {
		t.Fatalf("source replacement must invalidate ENOENT entry: %q %v", got, err)
	}

	if _, err := openAssetBytes("expires.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expiry setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replacement, "expires.txt"), []byte("expired-negative"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openAssetBytes("expires.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpired ENOENT cache unexpectedly reloaded: %v", err)
	}
	now = now.Add(11 * time.Second)
	if got, err := openAssetBytes("expires.txt"); err != nil || string(got) != "expired-negative" {
		t.Fatalf("expired ENOENT entry must reload: %q %v", got, err)
	}
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.NegativeEntries != 0 || after.PositiveEntries != 2 || after.CachedBytes != int64(len("replacement")+len("expired-negative")) || after.CachedBytes > after.CacheByteBudget {
		t.Fatalf("expiry/replacement accounting = %+v", after)
	}
	t.Logf("asset negative policy after generation=%d names=%d negative_candidates=%d negative_limit=%d cached_bytes=%d", after.Generation, after.NameEntries, after.NegativeCandidates, after.NegativeCacheLimit, after.CachedBytes)
}

func TestOpenAssetBytesDoesNotCacheNonENOENTDirectoryFailure(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	if err := os.Mkdir(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	if _, err := openAssetBytes("bad.txt"); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory read error must remain non-ENOENT: %v", err)
	}
	if snapshot := assetCacheSnapshotForTest(); snapshot.NameEntries != 0 || snapshot.NegativeEntries != 0 || snapshot.NegativeOther != 0 || snapshot.CachedBytes != 0 {
		t.Fatalf("non-ENOENT directory failure entered cache: %+v", snapshot)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("recovered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := openAssetBytes("bad.txt"); err != nil || string(got) != "recovered" {
		t.Fatalf("uncached directory failure must recover: %q %v", got, err)
	}
}

func TestAssetCacheBudgetConcurrentReaders(t *testing.T) {
	restorePolicy := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  6,
		negativeCap: 2,
		negativeTTL: time.Minute,
	})
	t.Cleanup(restorePolicy)
	apk := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/content/alias.txt": []byte("alias"),
		"assets/other.txt":         []byte("second"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 20 {
				for _, want := range []struct {
					name string
					data string
				}{
					{name: "alias.txt", data: "alias"},
					{name: "content/alias.txt", data: "alias"},
					{name: "other.txt", data: "second"},
				} {
					got, err := openAssetBytes(want.name)
					if err != nil || string(got) != want.data {
						t.Errorf("concurrent %q: %q %v", want.name, got, err)
						return
					}
				}
			}
		}()
	}
	readers.Wait()
	snapshot := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, snapshot)
	if snapshot.InFlightLoads != 0 || snapshot.BorrowedCacheBytes != 0 || snapshot.CachedBytes > snapshot.CacheByteBudget || snapshot.EvictableCacheBytes != snapshot.CachedBytes {
		t.Fatalf("concurrent budget accounting = %+v", snapshot)
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
	t.Cleanup(func() { closeAssetForTest(p) })
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
	t.Cleanup(func() { closeAssetForTest(p2) })
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
	assetPinsMu.RLock()
	_, ok := assetPins[key]
	assetPinsMu.RUnlock()
	if !ok {
		t.Fatal("expected Go pinner for nonempty blob")
	}

	// owned=0: C never frees the Go allocation. Its real close callback now
	// releases this pin only after invalidating the descriptor.
	if string(b) != "pin-me" {
		t.Fatal("Go blob must remain readable while pinned")
	}
	assetPinsMu.RLock()
	_, still := assetPins[key]
	assetPinsMu.RUnlock()
	if !still {
		t.Fatal("pin lifetime must outlive every native AAsset borrow")
	}

	setAssetsLocked("", "")
	assetPinsMu.RLock()
	_, still = assetPins[key]
	assetPinsMu.RUnlock()
	if !still {
		t.Fatal("source change must not unpin a possibly borrowed AAsset")
	}
	if string(b) != "pin-me" {
		t.Fatal("local Go slice must remain readable after source retirement")
	}

	closeAssetForTest(p)
	assetPinsMu.RLock()
	_, still = assetPins[key]
	assetPinsMu.RUnlock()
	if still {
		t.Fatal("last real AAsset_close must unpin and delete the zero-handle blob")
	}
}

func TestAssetFromBytesDuplicateHandlesShareOnePinUntilLastClose(t *testing.T) {
	data := []byte("shared-borrow")
	before := assetCacheSnapshotForTest()

	first := assetFromBytes(data)
	second := assetFromBytes(data)
	if first == nil || second == nil {
		if first != nil {
			closeAssetForTest(first)
		}
		if second != nil {
			closeAssetForTest(second)
		}
		t.Fatal("newBorrowedAsset allocation failed")
	}
	firstBuffer, firstLength, firstOwned := assetPinView(first)
	secondBuffer, secondLength, secondOwned := assetPinView(second)
	if firstBuffer != secondBuffer || firstLength != secondLength || firstOwned != 0 || secondOwned != 0 {
		t.Fatalf("duplicate borrowed assets must share an owned=0 buffer: first=%p/%d/%d second=%p/%d/%d", firstBuffer, firstLength, firstOwned, secondBuffer, secondLength, secondOwned)
	}

	shared := assetCacheSnapshotForTest()
	if shared.ActiveHandles != before.ActiveHandles+2 || shared.PinnedBlobs != before.PinnedBlobs+1 || shared.PinnedBytes != before.PinnedBytes+int64(len(data)) {
		t.Fatalf("duplicate borrow snapshot = %+v, before=%+v", shared, before)
	}

	closeAssetForTest(first)
	afterFirst := assetCacheSnapshotForTest()
	if afterFirst.ActiveHandles != before.ActiveHandles+1 || afterFirst.PinnedBlobs != shared.PinnedBlobs || afterFirst.PinnedBytes != shared.PinnedBytes {
		t.Fatalf("first close released a shared pin too early: %+v", afterFirst)
	}
	closeAssetForTest(second)
	afterLast := assetCacheSnapshotForTest()
	if afterLast.ActiveHandles != before.ActiveHandles || afterLast.PinnedBlobs != before.PinnedBlobs || afterLast.PinnedBytes != before.PinnedBytes {
		t.Fatalf("last close did not restore pin ownership: %+v, before=%+v", afterLast, before)
	}
}

func TestAcquireAssetBorrowReleaseModelsConstructorAllocationFailure(t *testing.T) {
	data := []byte("allocation-failure-borrow")
	before := assetCacheSnapshotForTest()
	_, _, release := acquireAssetBorrow(data)
	acquired := assetCacheSnapshotForTest()
	if acquired.ActiveHandles != before.ActiveHandles+1 || acquired.PinnedBlobs != before.PinnedBlobs+1 || acquired.PinnedBytes != before.PinnedBytes+int64(len(data)) {
		t.Fatalf("acquired borrow snapshot = %+v, before=%+v", acquired, before)
	}

	// newBorrowedAsset calls this exact closure synchronously if C's AAsset
	// allocation fails. Calling it here validates the cache side of that
	// allocation-failure contract without changing the ABI specialist's C seam.
	release()
	release()
	after := assetCacheSnapshotForTest()
	if after.ActiveHandles != before.ActiveHandles || after.PinnedBlobs != before.PinnedBlobs || after.PinnedBytes != before.PinnedBytes {
		t.Fatalf("allocation-failure release did not restore pin ownership: %+v, before=%+v", after, before)
	}
}

func TestAssetFromBytesRetainsBorrowAcrossSourceRetirement(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/borrow.txt": []byte("borrowed-through-retirement"),
	})
	replacement := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/replacement.txt": []byte("replacement"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	data, err := openAssetBytes("borrow.txt")
	if err != nil {
		t.Fatal(err)
	}
	before := assetCacheSnapshotForTest()
	asset := assetFromBytes(data)
	if asset == nil {
		t.Fatal("newBorrowedAsset allocation failed")
	}
	borrowed := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, borrowed)
	if borrowed.Generation != before.Generation || borrowed.BlobCount != 1 || borrowed.CachedBytes != int64(len(data)) || borrowed.BorrowedCacheBytes != int64(len(data)) || borrowed.EvictableCacheBytes != 0 || borrowed.ActiveHandles != before.ActiveHandles+1 || borrowed.PinnedBytes != before.PinnedBytes+int64(len(data)) {
		closeAssetForTest(asset)
		t.Fatalf("borrow acquisition snapshot = %+v, before=%+v", borrowed, before)
	}
	old := assetsForOpen()

	setAssetsLocked("", replacement)
	retired := old.snapshot()
	assertAssetCacheAccounting(t, retired)
	if !retired.Retired || retired.ArchiveOpen || retired.Generation != borrowed.Generation || retired.BlobCount != 0 || retired.CachedBytes != 0 || retired.BorrowedCacheBytes != 0 || retired.EvictableCacheBytes != 0 || retired.ActiveHandles != borrowed.ActiveHandles || retired.PinnedBytes != borrowed.PinnedBytes || retired.LiveWorkingSetBytes != borrowed.LiveWorkingSetBytes {
		closeAssetForTest(asset)
		t.Fatalf("retired source lost a live native borrow: %+v", retired)
	}
	if replacementSnapshot := assetCacheSnapshotForTest(); replacementSnapshot.Generation <= retired.Generation {
		closeAssetForTest(asset)
		t.Fatalf("replacement generation did not advance: retired=%+v replacement=%+v", retired, replacementSnapshot)
	}
	if got := string(data); got != "borrowed-through-retirement" {
		closeAssetForTest(asset)
		t.Fatalf("borrowed data after source retirement = %q", got)
	}

	closeAssetForTest(asset)
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.ActiveHandles != before.ActiveHandles || after.PinnedBlobs != before.PinnedBlobs || after.PinnedBytes != before.PinnedBytes {
		t.Fatalf("close after retirement did not release the borrow: %+v, before=%+v", after, before)
	}
}

func TestAssetFromBytesConcurrentDistinctClosesReleaseAllHandles(t *testing.T) {
	const handles = 32
	data := []byte("concurrent-borrow")
	before := assetCacheSnapshotForTest()
	assets := make([]unsafe.Pointer, 0, handles)
	for range handles {
		asset := assetFromBytes(data)
		if asset == nil {
			for _, allocated := range assets {
				closeAssetForTest(allocated)
			}
			t.Fatal("newBorrowedAsset allocation failed")
		}
		assets = append(assets, asset)
	}
	borrowed := assetCacheSnapshotForTest()
	if borrowed.ActiveHandles != before.ActiveHandles+handles || borrowed.PinnedBlobs != before.PinnedBlobs+1 || borrowed.PinnedBytes != before.PinnedBytes+int64(len(data)) {
		for _, asset := range assets {
			closeAssetForTest(asset)
		}
		t.Fatalf("concurrent borrow snapshot = %+v, before=%+v", borrowed, before)
	}

	var wg sync.WaitGroup
	for _, asset := range assets {
		wg.Add(1)
		go func(asset unsafe.Pointer) {
			defer wg.Done()
			closeAssetForTest(asset)
		}(asset)
	}
	wg.Wait()
	after := assetCacheSnapshotForTest()
	if after.ActiveHandles != before.ActiveHandles || after.PinnedBlobs != before.PinnedBlobs || after.PinnedBytes != before.PinnedBytes {
		t.Fatalf("concurrent closes did not release all handles: %+v, before=%+v", after, before)
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

func TestOpenAssetBytesColdLoadDoesNotBlockUnrelatedHotHit(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/hot.txt":  []byte("hot"),
		"assets/cold.txt": []byte("cold"),
	})
	replacement := writeTestAPK(t, t.TempDir(), map[string][]byte{
		"assets/replacement.txt": []byte("replacement"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })
	if got, err := openAssetBytes("hot.txt"); err != nil || string(got) != "hot" {
		t.Fatalf("warm hot asset: %q %v", got, err)
	}
	old := assetsForOpen()

	entered := make(chan struct{})
	release := make(chan struct{})
	restore := setAssetTestBeforeZipRead(func(name string) error {
		if name != "assets/cold.txt" {
			return nil
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return nil
	})
	t.Cleanup(restore)

	coldDone := make(chan error, 1)
	go func() {
		_, err := openAssetBytes("cold.txt")
		coldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cold ZIP read did not reach controlled barrier")
	}
	if snapshot := old.snapshot(); snapshot.InFlightLoads != 1 || snapshot.Inflating != 1 || !snapshot.ArchiveOpen {
		t.Fatalf("cold barrier snapshot = %+v, want one active inflation and archive", snapshot)
	}

	hotDone := make(chan error, 1)
	started := time.Now()
	go func() {
		got, err := openAssetBytes("hot.txt")
		if err == nil && string(got) != "hot" {
			err = errors.New("hot cache returned wrong contents")
		}
		hotDone <- err
	}()
	select {
	case err := <-hotDone:
		if err != nil {
			t.Fatalf("cached hot hit: %v", err)
		}
		t.Logf("candidate unrelated cached hot hit completed in %s while cold ZIP read remained blocked", time.Since(started).Round(time.Microsecond))
	case <-time.After(time.Second):
		t.Fatal("cached hot hit waited behind the cold ZIP read")
	}

	switched := time.Now()
	setAssetsLocked("", replacement)
	if elapsed := time.Since(switched); elapsed > time.Second {
		t.Fatalf("source change waited for cold load: %s", elapsed)
	}
	if snapshot := old.snapshot(); !snapshot.Retired || snapshot.InFlightLoads != 1 || !snapshot.ArchiveOpen || snapshot.ArchiveCloseCount != 0 {
		t.Fatalf("retired in-flight archive snapshot = %+v", snapshot)
	}
	close(release)
	if err := <-coldDone; err != nil {
		t.Fatalf("cold load: %v", err)
	}
	if snapshot := old.snapshot(); snapshot.ArchiveOpen || snapshot.ArchiveCloseCount != 1 || snapshot.InFlightLoads != 0 {
		t.Fatalf("completed retired archive snapshot = %+v", snapshot)
	}
}

func TestOpenAssetBytesDeduplicatesErrorAndUnblocksWaiters(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/fail.txt": []byte("never-read"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	entered := make(chan struct{})
	release := make(chan struct{})
	boom := errors.New("controlled asset reader cancellation")
	restore := setAssetTestBeforeZipRead(func(name string) error {
		if name != "assets/fail.txt" {
			return nil
		}
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
		return boom
	})
	t.Cleanup(restore)

	const callers = 8
	results := make(chan error, callers)
	go func() {
		_, err := openAssetBytes("fail.txt")
		results <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("controlled failing read did not reach barrier")
	}
	for i := 1; i < callers; i++ {
		go func() {
			_, err := openAssetBytes("fail.txt")
			results <- err
		}()
	}
	if snapshot := assetCacheSnapshotForTest(); snapshot.InFlightLoads != 1 || snapshot.NegativeEntries != 0 || snapshot.BlobCount != 0 {
		t.Fatalf("same-key error must have one in-flight loader, snapshot=%+v", snapshot)
	}
	select {
	case err := <-results:
		t.Fatalf("waiter escaped before controlled cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for i := 0; i < callers; i++ {
		if err := <-results; !errors.Is(err, boom) {
			t.Fatalf("waiter %d error = %v, want controlled cancellation", i, err)
		}
	}
	if snapshot := assetCacheSnapshotForTest(); snapshot.InFlightLoads != 0 || snapshot.NegativeEntries != 0 || snapshot.NegativeOther != 0 || snapshot.BlobCount != 0 || snapshot.CachedBytes != 0 {
		t.Fatalf("non-ENOENT reader failure must not enter any cache, snapshot=%+v", snapshot)
	}
	restore()
	got, err := openAssetBytes("fail.txt")
	if err != nil || string(got) != "never-read" {
		t.Fatalf("uncached failure must retry the source: %q %v", got, err)
	}
}

func TestOpenAssetBytesBoundsConcurrentInflation(t *testing.T) {
	dir := t.TempDir()
	apk := writeTestAPK(t, dir, map[string][]byte{
		"assets/cold-a.txt": []byte("a"),
		"assets/cold-b.txt": []byte("b"),
		"assets/cold-c.txt": []byte("c"),
	})
	setAssetsLocked("", apk)
	t.Cleanup(func() { setAssetsLocked("", "") })

	entered := make(chan string, 3)
	releaseOne := make(chan struct{})
	restore := setAssetTestBeforeZipRead(func(name string) error {
		entered <- name
		<-releaseOne
		return nil
	})
	t.Cleanup(restore)

	results := make(chan error, 3)
	for _, name := range []string{"cold-a.txt", "cold-b.txt", "cold-c.txt"} {
		go func(name string) {
			_, err := openAssetBytes(name)
			results <- err
		}(name)
	}
	for i := 0; i < maxConcurrentAssetInflations; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("expected bounded inflations to reach controlled barrier")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := assetCacheSnapshotForTest()
		if snapshot.Inflating == maxConcurrentAssetInflations && snapshot.InFlightLoads == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("inflation-bound snapshot = %+v", snapshot)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-entered:
		t.Fatal("third inflation bypassed configured bound")
	case <-time.After(20 * time.Millisecond):
	}
	releaseOne <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("queued inflation did not proceed after a permit release")
	}
	close(releaseOne)
	for i := 0; i < 3; i++ {
		if err := <-results; err != nil {
			t.Fatalf("inflation %d: %v", i, err)
		}
	}
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
