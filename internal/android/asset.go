// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"
)

type nameEntry struct {
	data []byte
	err  error
}

type apkArchive struct {
	zr    *zip.ReadCloser
	path  string
	files map[string]*zip.File
}

var (
	assetsMu     sync.RWMutex
	assetsDir    string
	apkPath      string
	nameCache    = make(map[string]nameEntry)
	pathOK       = make(map[string][]byte)
	pathMiss     = make(map[string]struct{})
	zipBlobCache = make(map[string][]byte)
	pinned       = make(map[uintptr]*runtime.Pinner)
	apkArch      *apkArchive
	apkOpenErr   error
	apkOpenPath  string
	emptyAsset   byte
)

func setAssetsLocked(dir, apk string) {
	assetsMu.Lock()
	defer assetsMu.Unlock()
	if dir == assetsDir && apk == apkPath {
		return
	}
	dropCachesLocked()
	assetsDir = dir
	apkPath = apk
}

func dropCachesLocked() {
	for _, p := range pinned {
		p.Unpin()
	}
	clear(pinned)
	clear(nameCache)
	clear(pathOK)
	clear(pathMiss)
	clear(zipBlobCache)
	if apkArch != nil {
		_ = apkArch.zr.Close()
		apkArch = nil
	}
	apkOpenErr = nil
	apkOpenPath = ""
}

func dirCandidates(dir, name string) []string {
	rel := filepath.FromSlash(name)
	candidates := []string{filepath.Join(dir, rel)}
	// rbxasset://configs/... is under content/; AAsset may open either
	// "configs/..." or "content/configs/..." depending on AssetReader root.
	// DataModelPatch / UniversalApp rbxms also live under ExtraContent
	// and android (V2Start search order).
	if name != "" && name != "content" && !strings.HasPrefix(name, "content/") {
		candidates = append(candidates, filepath.Join(dir, "content", rel))
	}
	if name != "" && !strings.HasPrefix(name, "ExtraContent/") {
		candidates = append(candidates, filepath.Join(dir, "ExtraContent", rel))
	}
	if name != "" && !strings.HasPrefix(name, "android/") {
		candidates = append(candidates, filepath.Join(dir, "android", rel))
	}
	return candidates
}

func apkWants(name string) []string {
	wants := []string{"assets/" + name}
	if name != "" && name != "content" && !strings.HasPrefix(name, "content/") {
		wants = append(wants, "assets/content/"+name)
	}
	return wants
}

func cacheNameLocked(name string, data []byte, err error) {
	nameCache[name] = nameEntry{data: data, err: err}
}

// cacheSuccessAliasesLocked stores the requested name and the exact relative
// path that produced the blob. That alias's first candidate is this file, so
// search order stays intact when a more-specific sibling also exists.
func cacheSuccessAliasesLocked(requested, usedRelSlash string, data []byte) {
	cacheNameLocked(requested, data, nil)
	if usedRelSlash != "" && usedRelSlash != requested {
		if _, ok := nameCache[usedRelSlash]; !ok {
			cacheNameLocked(usedRelSlash, data, nil)
		}
	}
}

func (a *apkArchive) lookup(want string) *zip.File {
	if a == nil {
		return nil
	}
	if f := a.files[want]; f != nil {
		return f
	}
	if trimmed := strings.TrimPrefix(want, "/"); trimmed != want {
		if f := a.files[trimmed]; f != nil {
			return f
		}
	}
	return a.files["/"+want]
}

func ensureAPKLocked() (*apkArchive, error) {
	if apkPath == "" {
		return nil, nil
	}
	if apkArch != nil && apkArch.path == apkPath {
		return apkArch, nil
	}
	if apkArch != nil {
		_ = apkArch.zr.Close()
		apkArch = nil
	}
	if apkOpenErr != nil && apkOpenPath == apkPath {
		return nil, apkOpenErr
	}
	zr, err := zip.OpenReader(apkPath)
	if err != nil {
		apkOpenErr = err
		apkOpenPath = apkPath
		return nil, err
	}
	apkOpenErr = nil
	apkOpenPath = ""
	files := make(map[string]*zip.File, len(zr.File)*2)
	for _, f := range zr.File {
		if _, ok := files[f.Name]; !ok {
			files[f.Name] = f
		}
		if trimmed := strings.TrimPrefix(f.Name, "/"); trimmed != f.Name {
			if _, ok := files[trimmed]; !ok {
				files[trimmed] = f
			}
		}
	}
	apkArch = &apkArchive{zr: zr, path: apkPath, files: files}
	return apkArch, nil
}

func inflateZipLocked(f *zip.File) ([]byte, error) {
	if b, ok := zipBlobCache[f.Name]; ok {
		return b, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, err
	}
	zipBlobCache[f.Name] = b
	return b, nil
}

func readDirAssetLocked(name string) ([]byte, bool) {
	dir := assetsDir
	for _, p := range dirCandidates(dir, name) {
		if b, ok := pathOK[p]; ok {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				cacheNameLocked(name, b, nil)
			} else {
				cacheSuccessAliasesLocked(name, filepath.ToSlash(rel), b)
			}
			return b, true
		}
		if _, miss := pathMiss[p]; miss {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			pathMiss[p] = struct{}{}
			continue
		}
		pathOK[p] = b
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			cacheNameLocked(name, b, nil)
		} else {
			cacheSuccessAliasesLocked(name, filepath.ToSlash(rel), b)
		}
		return b, true
	}
	return nil, false
}

func readAPKAssetLocked(name string) ([]byte, error) {
	arch, err := ensureAPKLocked()
	if err != nil {
		return nil, err
	}
	if arch == nil {
		return nil, os.ErrNotExist
	}
	for _, want := range apkWants(name) {
		f := arch.lookup(want)
		if f == nil {
			continue
		}
		b, err := inflateZipLocked(f)
		if err != nil {
			return nil, err
		}
		alias := strings.TrimPrefix(f.Name, "/")
		alias = strings.TrimPrefix(alias, "assets/")
		cacheSuccessAliasesLocked(name, alias, b)
		return b, nil
	}
	return nil, os.ErrNotExist
}

func loadAssetLocked(name string) ([]byte, error) {
	if assetsDir != "" {
		if b, ok := readDirAssetLocked(name); ok {
			return b, nil
		}
	}
	if apkPath != "" {
		return readAPKAssetLocked(name)
	}
	return nil, os.ErrNotExist
}

func openAssetBytes(name string) ([]byte, error) {
	name = strings.TrimPrefix(name, "/")
	assetsMu.RLock()
	if e, ok := nameCache[name]; ok {
		assetsMu.RUnlock()
		return e.data, e.err
	}
	assetsMu.RUnlock()

	assetsMu.Lock()
	defer assetsMu.Unlock()
	if e, ok := nameCache[name]; ok {
		return e.data, e.err
	}
	b, err := loadAssetLocked(name)
	if _, ok := nameCache[name]; !ok {
		cacheNameLocked(name, b, err)
	}
	return b, err
}

func pinAssetBytes(b []byte) (unsafe.Pointer, int64) {
	if len(b) == 0 {
		return unsafe.Pointer(&emptyAsset), 0
	}
	data := unsafe.SliceData(b)
	key := uintptr(unsafe.Pointer(data))
	assetsMu.RLock()
	_, ok := pinned[key]
	assetsMu.RUnlock()
	if !ok {
		assetsMu.Lock()
		if _, exists := pinned[key]; !exists {
			p := new(runtime.Pinner)
			p.Pin(data)
			pinned[key] = p
		}
		assetsMu.Unlock()
	}
	return unsafe.Pointer(data), int64(len(b))
}

func assetFromBytes(b []byte) unsafe.Pointer {
	buf, n := pinAssetBytes(b)
	// owned=0: AAsset_close must not free the pinned/cached blob. Reopens
	// share this pointer; C reads stay zero-copy after the first pin.
	// Pin lifetime is Go-owned until setAssetsLocked invalidates.
	return newAsset(buf, n, 0, -1)
}
