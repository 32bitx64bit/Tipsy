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

// assetBorrowRelease is a per-AAsset ownership lease. The opaque token crosses
// C, not a Go data pointer: a future filesystem owner may capture its own pin
// accounting in release without exposing asset data, paths, byte counts, or
// handles to the C ABI.
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
		// Zero deliberately means no Go lease in the C ABI.
		if assetBorrowReleases.next == 0 {
			continue
		}
		if _, exists := assetBorrowReleases.release[assetBorrowReleases.next]; !exists {
			assetBorrowReleases.release[assetBorrowReleases.next] = release
			return assetBorrowReleases.next
		}
	}
}

// releaseAssetBorrow is idempotent. It removes the lease before running the
// owner callback, so a callback may cause unrelated asset operations without
// retaining the registry lock or allowing duplicate pin release.
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
// one valid C AAsset_close consumes it. Filesystem should use this only after
// it has pinned a blob and made its close callback able to release that pin.
func newBorrowedAsset(buffer unsafe.Pointer, length int64, fd int, release func()) unsafe.Pointer {
	token := registerAssetBorrowRelease(release)
	asset := unsafe.Pointer(C.tipsy_AAsset_from_buffer_with_release(
		buffer, C.int64_t(length), 0, C.int(fd), C.uintptr_t(token)))
	if asset == nil {
		releaseAssetBorrow(token)
	}
	return asset
}

// closeAssetForTest drives the real C AAsset_close path because Go test files
// cannot import C directly. It is intentionally package-private and has no
// production callers.
func closeAssetForTest(asset unsafe.Pointer) {
	C.tipsy_AAsset_close((*C.AAsset)(asset))
}

func assetBorrowReleaseCountForTest() int {
	assetBorrowReleases.Lock()
	defer assetBorrowReleases.Unlock()
	return len(assetBorrowReleases.release)
}
