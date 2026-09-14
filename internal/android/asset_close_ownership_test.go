// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func TestAAssetCloseReleasesBorrowLeaseExactlyOnce(t *testing.T) {
	var releases atomic.Uint32
	var asset unsafe.Pointer
	asset = newBorrowedAsset(nil, 0, -1, func() {
		releases.Add(1)
		// The descriptor is invalid before C enters Go. This exercises a
		// re-entrant close while the outer close still owns its allocation.
		closeAssetForTest(asset)
	})
	if asset == nil {
		t.Fatal("newBorrowedAsset returned nil")
	}
	if got := assetBorrowReleaseCountForTest(); got != 1 {
		t.Fatalf("active borrow leases before close = %d, want 1", got)
	}

	closeAssetForTest(asset)
	if got := releases.Load(); got != 1 {
		t.Fatalf("release callback calls = %d, want 1", got)
	}
	if got := assetBorrowReleaseCountForTest(); got != 0 {
		t.Fatalf("active borrow leases after close = %d, want 0", got)
	}
}

func TestAAssetCloseNilDoesNotReleaseBorrowLease(t *testing.T) {
	var releases atomic.Uint32
	token := registerAssetBorrowRelease(func() { releases.Add(1) })
	if token == 0 {
		t.Fatal("registerAssetBorrowRelease returned zero")
	}
	t.Cleanup(func() { releaseAssetBorrow(token) })

	closeAssetForTest(nil)
	if got := releases.Load(); got != 0 {
		t.Fatalf("nil close release calls = %d, want 0", got)
	}
	if got := assetBorrowReleaseCountForTest(); got != 1 {
		t.Fatalf("nil close changed active borrow leases to %d, want 1", got)
	}
}

func TestAAssetCloseReleasesPinnedBorrowOnlyAfterCStopsUsingIt(t *testing.T) {
	data := []byte("pinned-borrow")
	pinner := new(runtime.Pinner)
	pinner.Pin(unsafe.SliceData(data))
	var releases atomic.Uint32
	asset := newBorrowedAsset(unsafe.Pointer(unsafe.SliceData(data)), int64(len(data)), -1, func() {
		pinner.Unpin()
		releases.Add(1)
	})
	if asset == nil {
		// newBorrowedAsset already ran the release callback on allocation
		// failure, so there is no pin left to clean up here.
		t.Fatal("newBorrowedAsset returned nil")
	}
	closeAssetForTest(asset)
	if got := releases.Load(); got != 1 {
		t.Fatalf("pinned borrow release callback calls = %d, want 1", got)
	}
	// owned=0 keeps C from freeing the Go allocation. The C close invalidates
	// its descriptor before the callback unpins this buffer.
	if got := string(data); got != "pinned-borrow" {
		t.Fatalf("borrowed Go data after close = %q", got)
	}
}

func TestLegacyAAssetConstructorDoesNotInventBorrowLease(t *testing.T) {
	baseline := assetBorrowReleaseCountForTest()
	asset := newAsset(nil, 0, 0, -1)
	if asset == nil {
		t.Fatal("newAsset returned nil")
	}
	closeAssetForTest(asset)
	if got := assetBorrowReleaseCountForTest(); got != baseline {
		t.Fatalf("legacy close changed active borrow leases to %d, want %d", got, baseline)
	}
}

func TestAssetBorrowReleaseIsIdempotent(t *testing.T) {
	var releases atomic.Uint32
	token := registerAssetBorrowRelease(func() { releases.Add(1) })
	releaseAssetBorrow(token)
	releaseAssetBorrow(token)
	if got := releases.Load(); got != 1 {
		t.Fatalf("manual lease release calls = %d, want 1", got)
	}
}

func TestAAssetCloseReleasesDistinctBorrowLeasesConcurrently(t *testing.T) {
	const assets = 32
	baseline := assetBorrowReleaseCountForTest()
	var releases atomic.Uint32
	toClose := make([]unsafe.Pointer, 0, assets)
	for range assets {
		asset := newBorrowedAsset(nil, 0, -1, func() { releases.Add(1) })
		if asset == nil {
			t.Fatal("newBorrowedAsset returned nil")
		}
		toClose = append(toClose, asset)
	}
	if got := assetBorrowReleaseCountForTest(); got != baseline+assets {
		t.Fatalf("active borrow leases before concurrent close = %d, want %d", got, baseline+assets)
	}

	var wg sync.WaitGroup
	for _, asset := range toClose {
		wg.Add(1)
		go func(asset unsafe.Pointer) {
			defer wg.Done()
			closeAssetForTest(asset)
		}(asset)
	}
	wg.Wait()
	if got := releases.Load(); got != assets {
		t.Fatalf("concurrent release callback calls = %d, want %d", got, assets)
	}
	if got := assetBorrowReleaseCountForTest(); got != baseline {
		t.Fatalf("active borrow leases after concurrent close = %d, want %d", got, baseline)
	}
}
