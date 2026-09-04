// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"
)

var (
	assetsMu  sync.RWMutex
	assetsDir string
	apkPath   string
)

func setAssetsLocked(dir, apk string) {
	assetsMu.Lock()
	assetsDir = dir
	apkPath = apk
	assetsMu.Unlock()
}

func openAssetBytes(name string) ([]byte, error) {
	name = strings.TrimPrefix(name, "/")
	assetsMu.RLock()
	dir := assetsDir
	apk := apkPath
	assetsMu.RUnlock()

	if dir != "" {
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
		for _, p := range candidates {
			if b, err := os.ReadFile(p); err == nil {
				return b, nil
			}
		}
	}
	if apk != "" {
		zr, err := zip.OpenReader(apk)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		wants := []string{"assets/" + name}
		if name != "" && name != "content" && !strings.HasPrefix(name, "content/") {
			wants = append(wants, "assets/content/"+name)
		}
		for _, f := range zr.File {
			trimmed := strings.TrimPrefix(f.Name, "/")
			for _, want := range wants {
				if f.Name == want || trimmed == want {
					rc, err := f.Open()
					if err != nil {
						return nil, err
					}
					b, err := io.ReadAll(rc)
					rc.Close()
					return b, err
				}
			}
		}
	}
	return nil, os.ErrNotExist
}

func assetFromBytes(b []byte) unsafe.Pointer {
	if b == nil {
		b = []byte{}
	}
	buf := CMalloc(len(b))
	if len(b) > 0 && buf != nil {
		src := unsafe.Slice((*byte)(buf), len(b))
		copy(src, b)
	}
	return newAsset(buf, int64(len(b)), 1, -1)
}
