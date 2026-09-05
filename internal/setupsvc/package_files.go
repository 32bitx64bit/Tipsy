// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const storeInstallerDetail = "this file is an app-store installer, not the official Roblox client. On Uptodown, download the XAPK (typically well over 100 MB), not the small APK named like uptodown-com.roblox.client.apk"

func isStoreInstallerPackage(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "com.uptodown" || strings.HasPrefix(n, "com.uptodown.")
}

func keepX86PackageFiles(paths []string, dir, op string) ([]string, error) {
	if op == "" {
		op = "package"
	}
	if len(paths) == 0 {
		return nil, setupError(ErrInvalidRequest, op, "choose at least one official package file", nil)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, op, "cannot create the package filter directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	if len(paths) == 1 {
		return selectX86PackageFiles(paths[0], dir, op)
	}
	return keepX86FromSplitSet(paths, op)
}

func keepX86FromSplitSet(paths []string, op string) ([]string, error) {
	var bases, x86 []string
	for _, path := range paths {
		name := strings.ToLower(filepath.Base(path))
		switch {
		case isX86SplitName(name):
			x86 = append(x86, path)
		case isSplitAPKName(name):
			continue
		default:
			bases = append(bases, path)
		}
	}
	base := pickBaseAPK(bases)
	if base == "" {
		return nil, setupError(ErrWrongPackage, op, "the selected files do not include a Roblox base APK", nil)
	}
	out := []string{base}
	if len(x86) > 0 {
		out = append(out, x86[0])
	}
	return out, nil
}

func selectX86PackageFiles(packagePath, dir, op string) ([]string, error) {
	if op == "" {
		op = "package"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, op, "cannot create the package filter directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	f, err := os.Open(packagePath)
	if err != nil {
		return nil, setupError(ErrInvalidArchive, op, "downloaded package cannot be opened", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, setupError(ErrInvalidArchive, op, "downloaded package cannot be inspected", err)
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return nil, setupError(ErrInvalidArchive, op, "downloaded package is not a ZIP/APK", err)
	}
	if zipHasRootManifest(zr) {
		if zipHasNamedNative(zr, "libuptodown-native.so") {
			return nil, setupError(ErrWrongPackage, op, storeInstallerDetail, nil)
		}
		if !zipHasX86_64Roblox(zr) {
			return nil, setupError(ErrMissingX8664, op, "the downloaded APK does not include x86-64 libroblox.so", nil)
		}
		dest := filepath.Join(dir, "base.apk")
		if filepath.Clean(packagePath) != dest {
			if err := os.Rename(packagePath, dest); err != nil {
				return nil, setupError(ErrInstall, op, "cannot keep the downloaded APK", err)
			}
		}
		return []string{dest}, nil
	}
	baseName, x86Name := pickBundleAPKs(zr)
	if baseName == "" {
		return nil, setupError(ErrWrongPackage, op, "the downloaded bundle does not contain a base APK", nil)
	}
	if x86Name == "" && !zipEntryHasX86_64Roblox(zr, baseName) {
		return nil, setupError(ErrMissingX8664, op, "the downloaded bundle does not include x86-64 libroblox.so", nil)
	}
	var out []string
	extracted, err := extractZipFile(zr, baseName, filepath.Join(dir, "base.apk"), op)
	if err != nil {
		return nil, err
	}
	out = append(out, extracted)
	if x86Name != "" {
		extracted, err = extractZipFile(zr, x86Name, filepath.Join(dir, "split_config.x86_64.apk"), op)
		if err != nil {
			return nil, err
		}
		out = append(out, extracted)
	}
	_ = os.Remove(packagePath)
	return out, nil
}

func pickBundleAPKs(zr *zip.Reader) (baseName, x86Name string) {
	var candidates []string
	for _, zf := range zr.File {
		name := filepath.ToSlash(zf.Name)
		if !safeZIPName(name) || zf.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".apk") {
			continue
		}
		base := strings.ToLower(filepath.Base(name))
		if isX86SplitName(base) {
			x86Name = name
			continue
		}
		if isSplitAPKName(base) {
			continue
		}
		candidates = append(candidates, name)
	}
	return pickBaseAPK(candidates), x86Name
}

func pickBaseAPK(names []string) string {
	if len(names) == 0 {
		return ""
	}
	var preferred, roblox, rest []string
	for _, name := range names {
		base := strings.ToLower(filepath.Base(name))
		switch base {
		case "base.apk", "com.roblox.client.apk":
			preferred = append(preferred, name)
		default:
			if strings.Contains(base, "roblox") {
				roblox = append(roblox, name)
			} else {
				rest = append(rest, name)
			}
		}
	}
	if len(preferred) > 0 {
		for _, name := range preferred {
			if strings.EqualFold(filepath.Base(name), "base.apk") {
				return name
			}
		}
		return preferred[0]
	}
	if len(roblox) > 0 {
		return roblox[0]
	}
	if len(rest) == 1 {
		return rest[0]
	}
	return ""
}

func isX86SplitName(base string) bool {
	switch strings.ToLower(base) {
	case "split_config.x86_64.apk", "config.x86_64.apk", "split_config.amd64.apk":
		return true
	default:
		return strings.HasSuffix(strings.ToLower(base), ".x86_64.apk")
	}
}

func isSplitAPKName(base string) bool {
	base = strings.ToLower(base)
	return strings.HasPrefix(base, "config.") || strings.HasPrefix(base, "split_config.") || strings.HasPrefix(base, "split_")
}

func zipHasRootManifest(zr *zip.Reader) bool {
	for _, zf := range zr.File {
		if filepath.ToSlash(zf.Name) == "AndroidManifest.xml" {
			return true
		}
	}
	return false
}

func zipHasX86_64Roblox(zr *zip.Reader) bool {
	for _, zf := range zr.File {
		if isX86_64RobloxPath(zf.Name) {
			return true
		}
	}
	return false
}

func zipEntryHasX86_64Roblox(zr *zip.Reader, name string) bool {
	for _, zf := range zr.File {
		if filepath.ToSlash(zf.Name) != name {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(rc, int64(DefaultLimits().MaxEntryBytes)))
		rc.Close()
		if err != nil {
			return false
		}
		inner, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return false
		}
		return zipHasX86_64Roblox(inner)
	}
	return false
}

func isX86_64RobloxPath(name string) bool {
	name = filepath.ToSlash(name)
	return name == "lib/x86_64/libroblox.so" || strings.HasSuffix(name, "/lib/x86_64/libroblox.so")
}

func zipHasNamedNative(zr *zip.Reader, name string) bool {
	suffix := "/" + name
	for _, zf := range zr.File {
		n := filepath.ToSlash(zf.Name)
		if n == name || strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

func extractZipFile(zr *zip.Reader, name, dest, op string) (string, error) {
	if op == "" {
		op = "package"
	}
	for _, zf := range zr.File {
		if filepath.ToSlash(zf.Name) != name {
			continue
		}
		if zf.UncompressedSize64 == 0 || zf.UncompressedSize64 > DefaultLimits().MaxEntryBytes {
			return "", setupError(ErrSizeLimit, op, "a nested APK exceeds the safe size limit", nil)
		}
		rc, err := zf.Open()
		if err != nil {
			return "", setupError(ErrInvalidArchive, op, "cannot read a nested APK", err)
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			rc.Close()
			return "", setupError(ErrInstall, op, "cannot stage a nested APK", err)
		}
		n, copyErr := io.Copy(f, io.LimitReader(rc, int64(zf.UncompressedSize64)+1))
		syncErr := f.Sync()
		closeErr := f.Close()
		rc.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || uint64(n) != zf.UncompressedSize64 {
			_ = os.Remove(dest)
			if copyErr == nil {
				copyErr = syncErr
			}
			if copyErr == nil {
				copyErr = closeErr
			}
			return "", setupError(ErrInvalidArchive, op, "nested APK extraction did not complete", copyErr)
		}
		return dest, nil
	}
	return "", setupError(ErrInvalidArchive, op, "nested APK is missing from the bundle", nil)
}
