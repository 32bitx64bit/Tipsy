// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// mappedDirAsset is one PROT_READ file mapping used as a directory-asset blob.
// Lifetime lives here rather than in assetCache maps so a retired generation
// can drop aliases while a live AAsset still holds the pages, and so Munmap
// runs exactly once.
type mappedDirAsset struct {
	mapping []byte
	cached  bool
	pins    uint32
}

var mappedDirAssets struct {
	sync.Mutex
	byKey map[uintptr]*mappedDirAsset
}

func mappedDirAssetKey(data []byte) uintptr {
	if len(data) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(unsafe.SliceData(data)))
}

// mapDirAssetFile returns a file-backed PROT_READ view of path. Empty files
// are an empty slice with no mapping. Missing paths stay os.ErrNotExist.
// Directory assets are not copied onto the Go heap; ZIP inflate is a separate
// path and must not call this.
func mapDirAssetFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, &os.PathError{Op: "read", Path: path, Err: unix.EISDIR}
	}
	size := fi.Size()
	if size == 0 {
		return []byte{}, nil
	}
	if size < 0 || int64(int(size)) != size {
		return nil, &os.PathError{Op: "mmap", Path: path, Err: unix.EFBIG}
	}

	mapping, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, &os.PathError{Op: "mmap", Path: path, Err: err}
	}

	key := mappedDirAssetKey(mapping)
	mappedDirAssets.Lock()
	if mappedDirAssets.byKey == nil {
		mappedDirAssets.byKey = make(map[uintptr]*mappedDirAsset)
	}
	mappedDirAssets.byKey[key] = &mappedDirAsset{mapping: mapping}
	mappedDirAssets.Unlock()
	return mapping, nil
}

func mappedDirAssetLocked(key uintptr) *mappedDirAsset {
	if key == 0 || mappedDirAssets.byKey == nil {
		return nil
	}
	return mappedDirAssets.byKey[key]
}

func unmapDirAsset(m *mappedDirAsset) {
	if m == nil || len(m.mapping) == 0 {
		return
	}
	mapping := m.mapping
	m.mapping = nil
	_ = unix.Munmap(mapping)
}

func adoptMappedDirAsset(key uintptr) {
	if key == 0 {
		return
	}
	mappedDirAssets.Lock()
	if m := mappedDirAssetLocked(key); m != nil {
		m.cached = true
	}
	mappedDirAssets.Unlock()
}

func releaseMappedDirAssetFromCache(key uintptr) {
	if key == 0 {
		return
	}
	mappedDirAssets.Lock()
	m := mappedDirAssetLocked(key)
	if m == nil {
		mappedDirAssets.Unlock()
		return
	}
	m.cached = false
	if m.pins != 0 {
		mappedDirAssets.Unlock()
		return
	}
	delete(mappedDirAssets.byKey, key)
	mappedDirAssets.Unlock()
	unmapDirAsset(m)
}

func retainMappedDirAssetPin(key uintptr) bool {
	if key == 0 {
		return false
	}
	mappedDirAssets.Lock()
	m := mappedDirAssetLocked(key)
	if m == nil {
		mappedDirAssets.Unlock()
		return false
	}
	m.pins++
	mappedDirAssets.Unlock()
	return true
}

func releaseMappedDirAssetPin(key uintptr) {
	if key == 0 {
		return
	}
	mappedDirAssets.Lock()
	m := mappedDirAssetLocked(key)
	if m == nil || m.pins == 0 {
		mappedDirAssets.Unlock()
		return
	}
	m.pins--
	if m.cached || m.pins != 0 {
		mappedDirAssets.Unlock()
		return
	}
	delete(mappedDirAssets.byKey, key)
	mappedDirAssets.Unlock()
	unmapDirAsset(m)
}

func discardMappedDirAsset(data []byte) {
	key := mappedDirAssetKey(data)
	if key == 0 {
		return
	}
	mappedDirAssets.Lock()
	m := mappedDirAssetLocked(key)
	if m == nil || m.cached || m.pins != 0 {
		mappedDirAssets.Unlock()
		return
	}
	delete(mappedDirAssets.byKey, key)
	mappedDirAssets.Unlock()
	unmapDirAsset(m)
}

func dirAssetIsMapped(data []byte) bool {
	key := mappedDirAssetKey(data)
	if key == 0 {
		return false
	}
	mappedDirAssets.Lock()
	m := mappedDirAssetLocked(key)
	mappedDirAssets.Unlock()
	return m != nil
}

func mappedDirAssetCountForTest() int {
	mappedDirAssets.Lock()
	n := len(mappedDirAssets.byKey)
	mappedDirAssets.Unlock()
	return n
}

func dirAssetIsMappedForTest(data []byte) bool {
	return dirAssetIsMapped(data)
}
