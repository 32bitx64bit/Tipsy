// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
	"unsafe"
)

// assertAssetStartupCacheInvariants independently scans the authoritative maps
// rather than reusing production counters or indexes. It deliberately belongs
// in tests: normal open/close maintenance must not rediscover this information
// by walking every successful cache entry or blob.
func assertAssetStartupCacheInvariants(t testing.TB, cache *assetCache) {
	t.Helper()

	cache.mu.RLock()
	cachedBytes := cache.cachedBytes
	nextExpiry := cache.nextNegativeExpiry
	indexedNames := make(map[string]struct{}, len(cache.negativeNames))
	for name := range cache.negativeNames {
		indexedNames[name] = struct{}{}
	}
	var actualBytes int64
	for _, blob := range cache.blobs {
		actualBytes += blob.bytes
	}
	actualNegatives := make(map[string]struct{})
	var earliest time.Time
	for name, entry := range cache.nameCache {
		if !errors.Is(entry.err, os.ErrNotExist) {
			continue
		}
		actualNegatives[name] = struct{}{}
		if earliest.IsZero() || entry.negativeExpiresAt.Before(earliest) {
			earliest = entry.negativeExpiresAt
		}
	}
	cache.mu.RUnlock()

	if cachedBytes != actualBytes {
		t.Fatalf("cachedBytes=%d, independently summed blob bytes=%d", cachedBytes, actualBytes)
	}
	if len(indexedNames) != len(actualNegatives) {
		t.Fatalf("negative index count=%d, ENOENT name-cache count=%d", len(indexedNames), len(actualNegatives))
	}
	for name := range actualNegatives {
		if _, indexed := indexedNames[name]; !indexed {
			t.Fatalf("ENOENT name-cache entry %q is missing from negativeNames", name)
		}
	}
	for name := range indexedNames {
		if _, negative := actualNegatives[name]; !negative {
			t.Fatalf("negativeNames has orphan %q", name)
		}
	}
	if earliest.IsZero() {
		if !nextExpiry.IsZero() {
			t.Fatalf("nextNegativeExpiry=%s with no negative names", nextExpiry)
		}
		return
	}
	if nextExpiry.IsZero() || nextExpiry.After(earliest) {
		t.Fatalf("nextNegativeExpiry=%s, earliest negative expiry=%s", nextExpiry, earliest)
	}
}

func TestAssetStartupCacheAccountingAliasesAndRemoval(t *testing.T) {
	cache := newAssetCache(700, "", "")
	data := []byte("one-blob-many-aliases")
	key := assetBlobKey(data)

	cache.mu.Lock()
	cache.setNamePositiveLocked("short", data)
	cache.setNamePositiveLocked("content/short", data)
	cache.setPathLocked("/private/path/short", data)
	cache.setZIPLocked("assets/content/short", data)
	cache.mu.Unlock()
	assertAssetStartupCacheInvariants(t, cache)
	if got := cache.cachedBytesLockedForTest(); got != int64(len(data)) {
		t.Fatalf("one backing blob cached bytes=%d, want %d", got, len(data))
	}

	cache.mu.Lock()
	cache.removeBlobAliasesLocked(key)
	cache.removeBlobAliasesLocked(key) // A repeated removal must be harmless.
	cache.mu.Unlock()
	assertAssetStartupCacheInvariants(t, cache)
	if got := cache.cachedBytesLockedForTest(); got != 0 {
		t.Fatalf("removed blob cached bytes=%d, want 0", got)
	}
}

func TestAssetStartupNegativeReplacementAndExpiration(t *testing.T) {
	now := time.Date(2026, time.September, 14, 13, 0, 0, 0, time.UTC)
	restore := setAssetCachePolicyForTest(assetCachePolicy{byteBudget: assetCacheByteBudget, negativeCap: 8, negativeTTL: time.Second})
	t.Cleanup(restore)
	cache := newAssetCache(701, "", "")

	cache.mu.Lock()
	cache.cacheNegativeLocked("replacement", os.ErrNotExist, now)
	cache.setNamePositiveLocked("replacement", []byte("now-present"))
	cache.pruneExpiredNegativeLocked(now.Add(time.Second))
	entry, found := cache.nameCache["replacement"]
	cache.mu.Unlock()
	if !found || entry.err != nil || string(entry.data) != "now-present" {
		t.Fatalf("expired maintenance removed positive replacement: %#v found=%t", entry, found)
	}
	assertAssetStartupCacheInvariants(t, cache)

	cache.mu.Lock()
	cache.cacheNegativeLocked("lookup", os.ErrNotExist, now)
	_, found = cache.cachedNameLocked("lookup", now.Add(time.Second))
	cache.mu.Unlock()
	if found {
		t.Fatal("negative lookup accepted the exact expiration boundary")
	}
	assertAssetStartupCacheInvariants(t, cache)

	cache.mu.Lock()
	cache.cacheNegativeLocked("maintenance-a", os.ErrNotExist, now)
	cache.cacheNegativeLocked("maintenance-b", os.ErrNotExist, now.Add(time.Second))
	cache.pruneExpiredNegativeLocked(now.Add(2 * time.Second))
	_, first := cache.nameCache["maintenance-a"]
	_, second := cache.nameCache["maintenance-b"]
	cache.mu.Unlock()
	if first || second {
		t.Fatalf("maintenance retained exact-boundary negatives: first=%t second=%t", first, second)
	}
	assertAssetStartupCacheInvariants(t, cache)
}

func TestAssetStartupNegativeCapacityLeavesSuccessfulNames(t *testing.T) {
	now := time.Date(2026, time.September, 14, 13, 1, 0, 0, time.UTC)
	restore := setAssetCachePolicyForTest(assetCachePolicy{byteBudget: assetCacheByteBudget, negativeCap: 2, negativeTTL: time.Minute})
	t.Cleanup(restore)
	cache := newAssetCache(702, "", "")

	cache.mu.Lock()
	for i := 0; i != 300; i++ {
		cache.setNamePositiveLocked(fmt.Sprintf("success-%03d", i), []byte{byte(i), byte(i >> 8), 1})
	}
	for _, name := range []string{"negative-a", "negative-b", "negative-c", "negative-d"} {
		cache.cacheNegativeLocked(name, os.ErrNotExist, now)
	}
	cache.enforceAssetCachePolicyLocked(now)
	remainingNegatives := len(cache.negativeNames)
	remainingSuccess := 0
	for i := 0; i != 300; i++ {
		if entry, found := cache.nameCache[fmt.Sprintf("success-%03d", i)]; found && entry.err == nil {
			remainingSuccess++
		}
	}
	cache.mu.Unlock()
	if remainingNegatives != 2 || remainingSuccess != 300 {
		t.Fatalf("negative capacity changed unrelated successes: negatives=%d successes=%d", remainingNegatives, remainingSuccess)
	}
	assertAssetStartupCacheInvariants(t, cache)
}

func TestAssetStartupRetirementClearsMaintenanceOnlyAfterActiveLoad(t *testing.T) {
	now := time.Date(2026, time.September, 14, 13, 2, 0, 0, time.UTC)
	cache := newAssetCache(703, "", "")

	cache.mu.Lock()
	cache.cacheNegativeLocked("retire", os.ErrNotExist, now)
	cache.setNamePositiveLocked("positive", []byte("retired-data"))
	cache.retired = true
	cache.activeLoads = 1
	cache.dropRetiredCachesLocked()
	deferred := len(cache.negativeNames) == 1 && cache.cachedBytes != 0
	cache.activeLoads = 0
	cache.dropRetiredCachesLocked()
	cleared := len(cache.negativeNames) == 0 && cache.nextNegativeExpiry.IsZero() && cache.cachedBytes == 0
	cache.mu.Unlock()
	if !deferred || !cleared {
		t.Fatalf("retirement maintenance cleanup lifecycle deferred=%t cleared=%t", deferred, cleared)
	}
	assertAssetStartupCacheInvariants(t, cache)
}

func TestAssetStartupNativeCloseUnderBudgetPressure(t *testing.T) {
	data := []byte("native-close-cache-pressure")
	restoreHigh := setAssetCachePolicyForTest(assetCachePolicy{byteBudget: int64(len(data)), negativeCap: 8, negativeTTL: time.Minute})
	t.Cleanup(restoreHigh)

	dir := t.TempDir()
	if err := os.WriteFile(dir+"/asset.bin", data, 0o600); err != nil {
		t.Fatal(err)
	}
	setAssetsLocked(dir, "")
	t.Cleanup(func() { setAssetsLocked("", "") })
	loaded, err := openAssetBytes("asset.bin")
	if err != nil {
		t.Fatal(err)
	}
	cache := assetsForOpen()

	assets := make([]unsafe.Pointer, 0, 16)
	defer func() {
		for _, asset := range assets {
			if asset != nil {
				closeAssetForTest(asset)
			}
		}
	}()
	for range cap(assets) {
		asset := assetFromBytes(loaded)
		if asset == nil {
			t.Fatal("newBorrowedAsset allocation failed")
		}
		assets = append(assets, asset)
	}
	assertAssetStartupCacheInvariants(t, cache)

	restoreLow := setAssetCachePolicyForTest(assetCachePolicy{byteBudget: 0, negativeCap: 8, negativeTTL: time.Minute})
	t.Cleanup(restoreLow)
	for i, asset := range assets {
		closeAssetForTest(asset)
		assets[i] = nil
	}
	assertAssetStartupCacheInvariants(t, cache)
	if snapshot := cache.snapshot(); snapshot.CachedBytes != 0 || snapshot.BlobCount != 0 || snapshot.ActiveHandles != 0 || snapshot.PinnedBytes != 0 {
		t.Fatalf("last real C close did not release/evict under pressure: %+v", snapshot)
	}
}

// cachedBytesLockedForTest keeps assertions under the cache lock without
// putting a production full-cache diagnostic scan back on the open/close path.
func (c *assetCache) cachedBytesLockedForTest() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedBytesLocked()
}

func BenchmarkAssetStartupOpenClose(b *testing.B) {
	for _, names := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("names=%d", names), func(b *testing.B) {
			cache := newAssetCache(uint64(10_000+names), "", "")
			previous := currentAssets.Load()
			currentAssets.Store(cache)
			b.Cleanup(func() { currentAssets.Store(previous) })

			assetNames := make([]string, names)
			cache.mu.Lock()
			for i := range assetNames {
				assetNames[i] = fmt.Sprintf("warmed-%05d", i)
				data := make([]byte, 16)
				data[0] = byte(i)
				data[1] = byte(i >> 8)
				cache.setNamePositiveLocked(assetNames[i], data)
			}
			cache.mu.Unlock()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				data, err := openAssetBytes(assetNames[i%len(assetNames)])
				if err != nil {
					b.Fatal(err)
				}
				asset := assetFromBytes(data)
				if asset == nil {
					b.Fatal("newBorrowedAsset allocation failed")
				}
				closeAssetForTest(asset)
			}
		})
	}
}
