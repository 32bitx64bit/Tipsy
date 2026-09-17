// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func writeMappedAssetFile(t testing.TB, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func procSelfMapsContainsPath(t testing.TB, path string) bool {
	t.Helper()
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(maps, []byte(path)) {
		return true
	}
	real, err := filepath.EvalSymlinks(path)
	if err == nil && real != path {
		return bytes.Contains(maps, []byte(real))
	}
	return false
}

func TestMapDirAssetFileEmptyMissingAndDirectory(t *testing.T) {
	dir := t.TempDir()
	emptyPath := writeMappedAssetFile(t, dir, "empty.bin", nil)
	got, err := mapDirAssetFile(emptyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("empty mapping len=%d", len(got))
	}
	if dirAssetIsMappedForTest(got) {
		t.Fatal("empty file must not create a mapping")
	}

	_, err = mapDirAssetFile(filepath.Join(dir, "missing.bin"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing path: %v", err)
	}

	_, err = mapDirAssetFile(dir)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory mapping error = %v", err)
	}
	if !errors.Is(err, unix.EISDIR) {
		t.Fatalf("directory mapping error = %v, want EISDIR", err)
	}
}

func TestMapDirAssetFileMatchesBytesAndUnmapsOnDiscard(t *testing.T) {
	dir := t.TempDir()
	want := []byte("mmap-bytes-identical")
	path := writeMappedAssetFile(t, dir, "payload.bin", want)
	before := mappedDirAssetCountForTest()

	got, err := mapDirAssetFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("mapped bytes = %q, want %q", got, want)
	}
	if !dirAssetIsMappedForTest(got) {
		t.Fatal("mapped file was not registered")
	}
	if mappedDirAssetCountForTest() != before+1 {
		t.Fatalf("mapping count = %d, want %d", mappedDirAssetCountForTest(), before+1)
	}

	discardMappedDirAsset(got)
	if dirAssetIsMappedForTest(got) {
		t.Fatal("discard left the mapping registered")
	}
	discardMappedDirAsset(got)
	releaseMappedDirAssetFromCache(mappedDirAssetKey(got))
	if mappedDirAssetCountForTest() != before {
		t.Fatalf("double unmap changed mapping count to %d", mappedDirAssetCountForTest())
	}
}

func TestOpenAssetBytesDirMmapAliasesStayMapped(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "content")
	want := []byte(`{"mapped":true}`)
	path := writeMappedAssetFile(t, nested, "alias.json", want)
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	first, err := openAssetBytes("alias.json")
	if err != nil {
		t.Fatal(err)
	}
	second, err := openAssetBytes("content/alias.json")
	if err != nil {
		t.Fatal(err)
	}
	fromDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, want) || !bytes.Equal(second, fromDisk) {
		t.Fatalf("alias bytes first=%q second=%q disk=%q", first, second, fromDisk)
	}
	if unsafe.SliceData(first) != unsafe.SliceData(second) {
		t.Fatal("alias opens must share one mapping")
	}
	if !dirAssetIsMappedForTest(first) {
		t.Fatal("directory alias blob is not mmap-backed")
	}
	if !procSelfMapsContainsPath(t, path) {
		t.Fatal("expected the directory asset path in /proc/self/maps")
	}

	snapshot := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, snapshot)
	if snapshot.NameEntries != 2 || snapshot.PositiveEntries != 2 || snapshot.PathEntries != 1 || snapshot.BlobCount != 1 || snapshot.CachedBytes != int64(len(want)) || snapshot.BorrowedCacheBytes != 0 {
		t.Fatalf("mmap alias accounting = %+v", snapshot)
	}
}

func TestAssetCacheBudgetUnmapsInactiveDirBlobOnce(t *testing.T) {
	page := unix.Getpagesize()
	if page < 4096 {
		page = 4096
	}
	first := bytes.Repeat([]byte{'A'}, page)
	second := bytes.Repeat([]byte{'B'}, page)
	restore := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  int64(len(second)),
		negativeCap: 2,
		negativeTTL: time.Minute,
	})
	t.Cleanup(restore)

	dir := t.TempDir()
	firstPath := writeMappedAssetFile(t, dir, "first.bin", first)
	writeMappedAssetFile(t, dir, "second.bin", second)
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	gotFirst, err := openAssetBytes("first.bin")
	if err != nil || !bytes.Equal(gotFirst, first) {
		t.Fatalf("first.bin: %q %v", gotFirst, err)
	}
	if !dirAssetIsMappedForTest(gotFirst) {
		t.Fatal("first.bin was retained as a ReadFile-style heap copy")
	}
	if !procSelfMapsContainsPath(t, firstPath) {
		t.Fatal("first.bin missing from /proc/self/maps before eviction")
	}
	before := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, before)

	gotSecond, err := openAssetBytes("second.bin")
	if err != nil || !bytes.Equal(gotSecond, second) {
		t.Fatalf("second.bin: %q %v", gotSecond, err)
	}
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.BlobCount != 1 || after.CachedBytes != int64(len(second)) || after.BorrowedCacheBytes != 0 || after.EvictableCacheBytes != int64(len(second)) || after.CachedBytes > after.CacheByteBudget {
		t.Fatalf("dir mmap eviction accounting = %+v", after)
	}
	if dirAssetIsMappedForTest(gotFirst) {
		t.Fatal("evicted first.bin mapping was not Munmapped")
	}
	if !dirAssetIsMappedForTest(gotSecond) {
		t.Fatal("kept second.bin mapping was unmapped")
	}
	if procSelfMapsContainsPath(t, firstPath) {
		t.Fatal("evicted first.bin still appears in /proc/self/maps")
	}
	releaseMappedDirAssetFromCache(mappedDirAssetKey(gotFirst))
	if !dirAssetIsMappedForTest(gotSecond) {
		t.Fatal("repeated unmap of the evicted blob disturbed the live mapping")
	}

	reloaded, err := openAssetBytes("first.bin")
	if err != nil || !bytes.Equal(reloaded, first) {
		t.Fatalf("reload first.bin: %q %v", reloaded, err)
	}
	if !dirAssetIsMappedForTest(reloaded) {
		t.Fatal("reloaded first.bin is not mmap-backed")
	}
	if !procSelfMapsContainsPath(t, firstPath) {
		t.Fatal("reloaded first.bin missing from /proc/self/maps")
	}
	reloadSnap := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, reloadSnap)
	if reloadSnap.BlobCount != 1 || reloadSnap.CachedBytes != int64(len(first)) {
		t.Fatalf("reload must evict the other dir blob: %+v", reloadSnap)
	}
}

func TestAssetDirMmapBorrowSurvivesBudgetAndUnmapsOnClose(t *testing.T) {
	payload := bytes.Repeat([]byte{'M'}, 32*1024)
	restoreHigh := setAssetCachePolicyForTest(assetCachePolicy{
		byteBudget:  int64(len(payload)),
		negativeCap: 2,
		negativeTTL: time.Minute,
	})
	t.Cleanup(restoreHigh)

	dir := t.TempDir()
	path := writeMappedAssetFile(t, dir, "borrow.bin", payload)
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	data, err := openAssetBytes("borrow.bin")
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("borrow.bin: %q %v", data, err)
	}
	before := assetCacheSnapshotForTest()
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

	borrowed := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, borrowed)
	if borrowed.BorrowedCacheBytes != int64(len(payload)) || borrowed.EvictableCacheBytes != 0 || borrowed.ActiveHandles != before.ActiveHandles+1 {
		closeAssetForTest(asset)
		closed = true
		t.Fatalf("mmap borrow accounting = %+v, before=%+v", borrowed, before)
	}
	if borrowed.PinnedBytes != before.PinnedBytes || borrowed.PinnedBlobs != before.PinnedBlobs {
		closeAssetForTest(asset)
		closed = true
		t.Fatalf("mmap borrow used a Go pinner: %+v, before=%+v", borrowed, before)
	}
	if !dirAssetIsMappedForTest(data) || !procSelfMapsContainsPath(t, path) {
		closeAssetForTest(asset)
		closed = true
		t.Fatal("borrowed directory mapping was unmapped")
	}

	restoreHigh()
	restoreLow := setAssetCachePolicyForTest(assetCachePolicy{byteBudget: 0, negativeCap: 2, negativeTTL: time.Minute})
	t.Cleanup(restoreLow)
	if got, err := openAssetBytes("trigger.bin"); !errors.Is(err, os.ErrNotExist) {
		closeAssetForTest(asset)
		closed = true
		t.Fatalf("trigger: %q %v", got, err)
	}
	pressed := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, pressed)
	if pressed.BorrowedCacheBytes != int64(len(payload)) || pressed.CachedBytes != int64(len(payload)) || !dirAssetIsMappedForTest(data) {
		closeAssetForTest(asset)
		closed = true
		t.Fatalf("budget selector unmapped a live AAsset: %+v", pressed)
	}
	if got := string(data[:1]); got != "M" {
		closeAssetForTest(asset)
		closed = true
		t.Fatalf("borrowed mmap unreadable under budget pressure: %q", got)
	}

	closeAssetForTest(asset)
	closed = true
	after := assetCacheSnapshotForTest()
	assertAssetCacheAccounting(t, after)
	if after.CachedBytes != 0 || after.BlobCount != 0 || after.ActiveHandles != before.ActiveHandles || after.BorrowedCacheBytes != 0 {
		t.Fatalf("last close did not unmap/evict: %+v", after)
	}
	if dirAssetIsMappedForTest(data) {
		t.Fatal("last AAsset close left the directory mapping registered")
	}
	if procSelfMapsContainsPath(t, path) {
		t.Fatal("closed mapping still appears in /proc/self/maps")
	}
}

func TestAssetDirMmapRetainsBorrowAcrossSourceRetirement(t *testing.T) {
	payload := []byte("mapped-through-retirement")
	dir := t.TempDir()
	path := writeMappedAssetFile(t, dir, "keep.bin", payload)
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })

	data, err := openAssetBytes("keep.bin")
	if err != nil {
		t.Fatal(err)
	}
	before := assetCacheSnapshotForTest()
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

	old := assetsForOpen()
	setAssetsLocked(t.TempDir(), "")
	retired := old.snapshot()
	assertAssetCacheAccounting(t, retired)
	if !retired.Retired || retired.BlobCount != 0 || retired.CachedBytes != 0 || retired.BorrowedCacheBytes != 0 {
		t.Fatalf("retired mmap cache accounting = %+v", retired)
	}
	if retired.ActiveHandles != before.ActiveHandles+1 {
		t.Fatalf("retirement dropped the native borrow: %+v", retired)
	}
	if !dirAssetIsMappedForTest(data) || !procSelfMapsContainsPath(t, path) {
		t.Fatal("retirement unmapped a live AAsset")
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("borrowed mmap after retirement = %q", data)
	}

	closeAssetForTest(asset)
	closed = true
	if dirAssetIsMappedForTest(data) {
		t.Fatal("close after retirement left the directory mapping registered")
	}
	if procSelfMapsContainsPath(t, path) {
		t.Fatal("mapping after retirement close still appears in /proc/self/maps")
	}
	after := assetCacheSnapshotForTest()
	if after.ActiveHandles != before.ActiveHandles || after.PinnedBytes != before.PinnedBytes {
		t.Fatalf("close after mmap retirement did not restore handles: %+v, before=%+v", after, before)
	}
}
