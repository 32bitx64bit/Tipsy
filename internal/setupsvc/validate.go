// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
)

type Limits struct {
	MaxFiles              int
	MaxFileBytes          int64
	MaxTotalInputBytes    int64
	MaxZIPEntries         int
	MaxEntryBytes         uint64
	MaxTotalExpandedBytes uint64
	MaxNestedAPKs         int
}

func DefaultLimits() Limits {
	return Limits{
		MaxFiles:              32,
		MaxFileBytes:          2 << 30,
		MaxTotalInputBytes:    4 << 30,
		MaxZIPEntries:         200000,
		MaxEntryBytes:         1 << 30,
		MaxTotalExpandedBytes: 8 << 30,
		MaxNestedAPKs:         32,
	}
}

type TrustPolicy struct {
	PackageName              string
	AllowedCertificateSHA256 []string
	SupportedSplits          []string
}

func OfficialTrustPolicy() TrustPolicy {
	return TrustPolicy{
		PackageName: "com.roblox.client",
		AllowedCertificateSHA256: []string{
			"44932ea35a17a267372d71b54d1a0cb3da0dca5113e94406ae2fe18090ba1477",
			"2bebd189e8d3106401347056c93d045b61e20e22d0c3cbed85474aeb00a3d12a",
		},
		SupportedSplits: []string{"", "config.x86_64"},
	}
}

func supportedExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".apk", ".apkm", ".xapk", ".apks", ".zip":
		return true
	default:
		return false
	}
}

func copyAndValidateLocal(ctx context.Context, paths []string, dir string, limits Limits, progress ProgressFunc) ([]string, error) {
	if len(paths) == 0 || len(paths) > limits.MaxFiles {
		return nil, setupError(ErrInvalidRequest, "local package", fmt.Sprintf("choose between 1 and %d package files", limits.MaxFiles), nil)
	}
	paths, err := expandLocalPaths(ctx, paths, limits)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, "local package", "cannot create private staging directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	type input struct {
		path string
		name string
		size int64
	}
	inputs := make([]input, 0, len(paths))
	used := map[string]bool{}
	var total int64
	for _, path := range paths {
		if err := checkContext(ctx, "local package"); err != nil {
			return nil, err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return nil, setupError(ErrUnsafePath, "local package", "a selected package cannot be read", err)
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return nil, setupError(ErrUnsafePath, "local package", "selected packages must be regular files, not links or directories", nil)
		}
		if st.Size() <= 0 || st.Size() > limits.MaxFileBytes || total > limits.MaxTotalInputBytes-st.Size() {
			return nil, setupError(ErrSizeLimit, "local package", "selected package data exceeds the safe setup limit", nil)
		}
		name := filepath.Base(path)
		if name == "" || name == "." || !supportedExtension(name) {
			return nil, setupError(ErrInvalidRequest, "local package", "select APK, APKM, XAPK, APKS, or ZIP package files", nil)
		}
		if used[name] {
			return nil, setupError(ErrInvalidRequest, "local package", "selected package filenames must be unique", nil)
		}
		used[name] = true
		total += st.Size()
		inputs = append(inputs, input{path: path, name: name, size: st.Size()})
	}
	var done int64
	var out []string
	for _, in := range inputs {
		src, err := os.Open(in.path)
		if err != nil {
			return nil, setupError(ErrUnsafePath, "local package", "cannot open selected package", err)
		}
		opened, err := src.Stat()
		if err != nil || !opened.Mode().IsRegular() || opened.Size() != in.size {
			src.Close()
			return nil, setupError(ErrUnsafePath, "local package", "selected package changed while setup was reading it", err)
		}
		dest := filepath.Join(dir, in.name)
		df, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			src.Close()
			return nil, setupError(ErrInstall, "local package", "cannot stage selected package", err)
		}
		reader := &progressReader{ctx: ctx, r: io.LimitReader(src, in.size+1), onRead: func(n int64) {
			done += n
			report(progress, PhaseAcquiring, done, total, "Copying selected package data")
		}}
		n, copyErr := io.Copy(df, reader)
		syncErr := df.Sync()
		closeErr := df.Close()
		src.Close()
		if copyErr != nil || syncErr != nil || closeErr != nil || n != in.size {
			_ = os.Remove(dest)
			if copyErr == nil {
				copyErr = syncErr
			}
			if copyErr == nil {
				copyErr = closeErr
			}
			return nil, setupError(ErrInstall, "local package", "selected package copy did not complete", copyErr)
		}
		if err := validateZIPStructure(dest, limits); err != nil {
			return nil, err
		}
		out = append(out, dest)
	}
	return out, nil
}

// expandLocalPaths preserves the CLI's directory-of-splits workflow without
// weakening the service boundary: directory trees are never followed through
// symlinks, every encountered leaf must be regular, and only APK leaves are
// selected from a directory.
func expandLocalPaths(ctx context.Context, paths []string, limits Limits) ([]string, error) {
	var expanded []string
	for _, path := range paths {
		if err := checkContext(ctx, "local package"); err != nil {
			return nil, err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return nil, setupError(ErrUnsafePath, "local package", "a selected package path cannot be read", err)
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, setupError(ErrUnsafePath, "local package", "selected package paths must not be symbolic links", nil)
		}
		if st.Mode().IsRegular() {
			expanded = append(expanded, path)
			continue
		}
		if !st.IsDir() {
			return nil, setupError(ErrUnsafePath, "local package", "selected package paths must be regular files or real directories", nil)
		}
		found := 0
		err = filepath.WalkDir(path, func(child string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return setupError(ErrUnsafePath, "local package", "a package directory cannot be read safely", walkErr)
			}
			if err := checkContext(ctx, "local package"); err != nil {
				return err
			}
			if child == path {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return setupError(ErrUnsafePath, "local package", "a package directory entry cannot be inspected safely", err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return setupError(ErrUnsafePath, "local package", "package directories must not contain symbolic links", nil)
			}
			if entry.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return setupError(ErrUnsafePath, "local package", "package directories must contain only regular files", nil)
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".apk") {
				expanded = append(expanded, child)
				found++
				if len(expanded) > limits.MaxFiles {
					return setupError(ErrSizeLimit, "local package", "package directory contains too many APK files", nil)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if found == 0 {
			return nil, setupError(ErrInvalidRequest, "local package", "selected package directory contains no APK files", nil)
		}
	}
	if len(expanded) == 0 || len(expanded) > limits.MaxFiles {
		return nil, setupError(ErrSizeLimit, "local package", "selected package set contains too many files", nil)
	}
	return expanded, nil
}

func validateZIPStructure(path string, limits Limits) error {
	f, err := os.Open(path)
	if err != nil {
		return setupError(ErrInvalidArchive, "package archive", "cannot open package ZIP", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return setupError(ErrInvalidArchive, "package archive", "cannot stat package ZIP", err)
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return setupError(ErrInvalidArchive, "package archive", "selected package is not a valid ZIP/APK", err)
	}
	if len(zr.File) == 0 || len(zr.File) > limits.MaxZIPEntries {
		return setupError(ErrSizeLimit, "package archive", "package has an unsafe number of ZIP entries", nil)
	}
	seen := make(map[string]bool, len(zr.File))
	hasRootManifest := false
	for _, zf := range zr.File {
		if filepath.ToSlash(zf.Name) == "AndroidManifest.xml" {
			hasRootManifest = true
			break
		}
	}
	var expanded uint64
	nested := 0
	for _, zf := range zr.File {
		name := filepath.ToSlash(zf.Name)
		if !safeZIPName(name) || seen[name] || zf.Flags&1 != 0 {
			return setupError(ErrInvalidArchive, "package archive", "package contains an unsafe, duplicate, or encrypted ZIP entry", nil)
		}
		seen[name] = true
		if zf.UncompressedSize64 > limits.MaxEntryBytes || expanded > limits.MaxTotalExpandedBytes-zf.UncompressedSize64 {
			return setupError(ErrSizeLimit, "package archive", "expanded package data exceeds the safe setup limit", nil)
		}
		expanded += zf.UncompressedSize64
		if strings.HasSuffix(strings.ToLower(name), ".apk") && !hasRootManifest {
			nested++
		}
		if zf.UncompressedSize64 > 16<<20 && zf.CompressedSize64 > 0 && zf.UncompressedSize64/zf.CompressedSize64 > 1000 {
			return setupError(ErrSizeLimit, "package archive", "package contains an implausibly compressed ZIP entry", nil)
		}
	}
	if nested > limits.MaxNestedAPKs {
		return setupError(ErrSizeLimit, "package archive", "package bundle contains too many APKs", nil)
	}
	return nil
}

func safeZIPName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func ValidateReport(rep *apk.Report, policy TrustPolicy) error {
	if rep == nil || rep.Merged == nil || len(rep.Packages) == 0 {
		return setupError(ErrInvalidArchive, "validate package", "package metadata is incomplete", nil)
	}
	if rep.Merged.PackageName != policy.PackageName {
		if isStoreInstallerPackage(rep.Merged.PackageName) {
			return setupError(ErrWrongPackage, "validate package", storeInstallerDetail, nil)
		}
		return setupError(ErrWrongPackage, "validate package", "the selected package is not the official Roblox client", nil)
	}
	allowedSplits := make(map[string]bool, len(policy.SupportedSplits))
	for _, split := range policy.SupportedSplits {
		allowedSplits[split] = true
	}
	allowedCerts := make(map[string]bool, len(policy.AllowedCertificateSHA256))
	for _, cert := range policy.AllowedCertificateSHA256 {
		allowedCerts[strings.ToLower(cert)] = true
	}
	baseCount := 0
	version := int64(0)
	var signerSet string
	for _, p := range rep.Packages {
		if !p.ManifestOK || p.PackageName != policy.PackageName {
			if isStoreInstallerPackage(p.PackageName) {
				return setupError(ErrWrongPackage, "validate package", storeInstallerDetail, nil)
			}
			return setupError(ErrWrongPackage, "validate package", "every APK must declare package com.roblox.client", nil)
		}
		if p.Debuggable {
			return setupError(ErrWrongPackage, "validate package", "debuggable Roblox packages are not accepted", nil)
		}
		if p.SplitName == "" && !p.IsSplit {
			baseCount++
		}
		if !allowedSplits[p.SplitName] {
			return setupError(ErrUnsupportedSplit, "validate package", "the selected bundle contains an unsupported split", nil)
		}
		if version == 0 {
			version = p.VersionCode
		}
		if p.VersionCode <= 0 || p.VersionCode != version {
			return setupError(ErrInvalidArchive, "validate package", "all APK splits must have the same positive version code", nil)
		}
		if p.Signing.ParseError != "" || !p.Signing.HasV2 || !p.Signing.CryptographicallyValid || len(p.Signing.VerifiedCertSHA256) == 0 {
			return setupError(ErrInvalidSignature, "validate package", "an APK signature could not be verified", nil)
		}
		verified := append([]string(nil), p.Signing.VerifiedCertSHA256...)
		sort.Strings(verified)
		for _, cert := range verified {
			if !allowedCerts[strings.ToLower(cert)] {
				return setupError(ErrUntrustedSigner, "validate package", "an APK is not signed by the pinned Roblox identity", nil)
			}
		}
		joined := strings.Join(verified, ",")
		if signerSet == "" {
			signerSet = joined
		} else if signerSet != joined {
			return setupError(ErrUntrustedSigner, "validate package", "APK splits do not share one signing identity", nil)
		}
	}
	if baseCount != 1 || rep.Merged.VersionName == "" || rep.Merged.VersionCode <= 0 {
		return setupError(ErrInvalidArchive, "validate package", "the package must contain one versioned base APK", nil)
	}
	foundABI, foundRoblox := false, false
	for _, abi := range rep.Merged.Architectures {
		foundABI = foundABI || abi == "x86_64"
	}
	for _, lib := range rep.Merged.NativeLibraries {
		foundRoblox = foundRoblox || lib.ABI == "x86_64" && lib.Name == "libroblox.so"
	}
	if !foundABI || !foundRoblox {
		return setupError(ErrMissingX8664, "validate package", "the selected package does not include x86_64 libroblox.so", nil)
	}
	return nil
}
