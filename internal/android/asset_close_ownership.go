// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

// assetBorrowRelease is a per-AAsset ownership lease. The opaque token
// crosses C instead of asset data, paths, byte counts, or handles.
var assetBorrowReleases struct {
	sync.Mutex
	next    uintptr
	release map[uintptr]func()
}

func registerAssetBorrowRelease(release func()) uintptr {
	if release == nil {
		return 0
	}
	assetBorrowReleases.Lock()
	defer assetBorrowReleases.Unlock()
	if assetBorrowReleases.release == nil {
		assetBorrowReleases.release = make(map[uintptr]func())
	}
	for {
		assetBorrowReleases.next++
		// Zero means no Go lease in the C ABI.
		if assetBorrowReleases.next == 0 {
			continue
		}
		if _, exists := assetBorrowReleases.release[assetBorrowReleases.next]; !exists {
			assetBorrowReleases.release[assetBorrowReleases.next] = release
			return assetBorrowReleases.next
		}
	}
}

// releaseAssetBorrow is idempotent: it removes the lease before running the
// callback, so a callback may cause unrelated asset operations without
// holding the registry lock.
func releaseAssetBorrow(token uintptr) {
	if token == 0 {
		return
	}
	assetBorrowReleases.Lock()
	release := assetBorrowReleases.release[token]
	delete(assetBorrowReleases.release, token)
	assetBorrowReleases.Unlock()
	if release != nil {
		release()
	}
}

// newBorrowedAsset constructs an owned=0 AAsset with an opaque Go release
// lease. If C allocation fails, release runs immediately; otherwise exactly
// one AAsset_close consumes it. Callers pin the blob before constructing.
func newBorrowedAsset(buffer unsafe.Pointer, length int64, fd int, release func()) unsafe.Pointer {
	token := registerAssetBorrowRelease(release)
	asset := unsafe.Pointer(C.tipsy_AAsset_from_buffer_with_release(
		buffer, C.int64_t(length), 0, C.int(fd), C.uintptr_t(token)))
	if asset == nil {
		releaseAssetBorrow(token)
	}
	return asset
}

// closeAssetForTest drives the real C AAsset_close path; Go test files
// cannot import C directly.
func closeAssetForTest(asset unsafe.Pointer) {
	C.tipsy_AAsset_close((*C.AAsset)(asset))
}

func assetBorrowReleaseCountForTest() int {
	assetBorrowReleases.Lock()
	defer assetBorrowReleases.Unlock()
	return len(assetBorrowReleases.release)
}
