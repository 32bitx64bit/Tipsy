// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/securitypolicy"
)

const CompiledMinimumRobloxVersionCode int64 = 2908

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
	Mode AuthorizationMode
	// ReleaseAuthenticated is set only after Tipsy's own immutable AppImage
	// has passed the compiled GitHub OIDC/Sigstore release verifier. It permits
	// the compiled Roblox signer floor to authorize an official session when a
	// separate TUF Roblox policy is intentionally not configured.
	ReleaseAuthenticated     bool
	PackageName              string
	AllowedCertificateSHA256 []string
	SupportedSplits          []string
	MinimumVersionCode       int64
	InstalledVersionCode     int64
	MinimumPolicySequence    uint64
	RobloxPolicy             *securitypolicy.RobloxPolicy
}

type AuthorizationMode string

const (
	OfficialVerified        AuthorizationMode = "official-verified"
	DevelopmentUnrestricted AuthorizationMode = "development-unrestricted"
)

type Authorization struct {
	SignerLineageID  string
	SignerLineage    []string
	PolicySequence   uint64
	Mode             AuthorizationMode
	PolicyAuthorized bool
}

func OfficialTrustPolicy() TrustPolicy {
	return TrustPolicy{
		Mode:        OfficialVerified,
		PackageName: "com.roblox.client",
		AllowedCertificateSHA256: []string{
			"44932ea35a17a267372d71b54d1a0cb3da0dca5113e94406ae2fe18090ba1477",
			"2bebd189e8d3106401347056c93d045b61e20e22d0c3cbed85474aeb00a3d12a",
		},
		SupportedSplits:    []string{"", "config.x86_64"},
		MinimumVersionCode: CompiledMinimumRobloxVersionCode,
	}
}

// WithAuthenticatedRobloxPolicy returns the conservative compiled trust floor
// intersected with an already authenticated and decoded P2 roblox-policy TUF
// target. The APK candidate itself is never a policy source.
func WithAuthenticatedRobloxPolicy(policy securitypolicy.RobloxPolicy, installedVersionCode int64, minimumPolicySequence uint64) TrustPolicy {
	trust := OfficialTrustPolicy()
	trust.RobloxPolicy = &policy
	trust.InstalledVersionCode = installedVersionCode
	trust.MinimumPolicySequence = minimumPolicySequence
	return trust
}

// KeylessReleaseTrustPolicy keeps the conservative compiled Roblox signer,
// split, and minimum-version floor. It is usable only after the surrounding
// app authority has cryptographically authenticated the exact Tipsy AppImage.
func KeylessReleaseTrustPolicy() TrustPolicy {
	trust := OfficialTrustPolicy()
	trust.ReleaseAuthenticated = true
	return trust
}

// DevelopmentTrustPolicy is an explicit non-official boundary for source
// builds when authenticated P2 policy is absent. It never yields official
// status and still requires APK signatures plus a full rotation lineage whose
// oldest and current certificates are in the compiled signer floor.
func DevelopmentTrustPolicy() TrustPolicy {
	trust := OfficialTrustPolicy()
	trust.Mode = DevelopmentUnrestricted
	trust.RobloxPolicy = nil
	return trust
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
	_, err := AuthorizeReport(rep, policy)
	return err
}

func AuthorizeReport(rep *apk.Report, policy TrustPolicy) (Authorization, error) {
	mode := policy.Mode
	if mode == "" {
		mode = OfficialVerified
	}
	if mode != OfficialVerified && mode != DevelopmentUnrestricted {
		return Authorization{}, setupError(ErrPolicy, "validate package", "package authorization mode is invalid", nil)
	}
	if mode == OfficialVerified && policy.RobloxPolicy == nil && !policy.ReleaseAuthenticated {
		return Authorization{}, setupError(ErrPolicy, "validate package", "official verification requires an authenticated release or Roblox policy", nil)
	}
	if mode == DevelopmentUnrestricted && policy.RobloxPolicy != nil {
		return Authorization{}, setupError(ErrPolicy, "validate package", "development authorization cannot claim authenticated policy status", nil)
	}
	if rep == nil || rep.Merged == nil || len(rep.Packages) == 0 {
		return Authorization{}, setupError(ErrInvalidArchive, "validate package", "package metadata is incomplete", nil)
	}
	if rep.Merged.PackageName != policy.PackageName {
		if isStoreInstallerPackage(rep.Merged.PackageName) {
			return Authorization{}, setupError(ErrWrongPackage, "validate package", storeInstallerDetail, nil)
		}
		return Authorization{}, setupError(ErrWrongPackage, "validate package", "the selected package is not the official Roblox client", nil)
	}
	if policy.MinimumVersionCode > 0 && rep.Merged.VersionCode < policy.MinimumVersionCode {
		return Authorization{}, setupError(ErrDowngrade, "validate package", "the selected package is below the compiled minimum version", nil)
	}
	if policy.InstalledVersionCode > 0 && rep.Merged.VersionCode < policy.InstalledVersionCode {
		return Authorization{}, setupError(ErrDowngrade, "validate package", "the selected package would downgrade the active runtime", nil)
	}
	if signed := policy.RobloxPolicy; signed != nil {
		if !validConsumedRobloxPolicy(signed, policy.MinimumPolicySequence) || signed.PackageName != policy.PackageName || signed.Platform != "android" || signed.Architecture != "x86_64" ||
			rep.Merged.VersionCode <= 0 || uint64(rep.Merged.VersionCode) < signed.MinVersionCode || uint64(rep.Merged.VersionCode) > signed.MaxVersionCode {
			return Authorization{}, setupError(ErrPolicy, "validate package", "the authenticated Roblox policy does not authorize this package version", nil)
		}
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
	baseManifestReady := false
	baseVersionName := ""
	version := int64(0)
	seenSplits := make(map[string]struct{}, len(rep.Packages))
	var signerSet string
	var authorized Authorization
	for _, p := range rep.Packages {
		if !p.ManifestOK || p.PackageName != policy.PackageName {
			if isStoreInstallerPackage(p.PackageName) {
				return Authorization{}, setupError(ErrWrongPackage, "validate package", storeInstallerDetail, nil)
			}
			return Authorization{}, setupError(ErrWrongPackage, "validate package", "every APK must declare package com.roblox.client", nil)
		}
		if p.Debuggable {
			return Authorization{}, setupError(ErrWrongPackage, "validate package", "debuggable Roblox packages are not accepted", nil)
		}
		if p.SplitName == "" && !p.IsSplit {
			baseCount++
			baseManifestReady = p.LauncherActivity != "" && slices.Contains(p.GameActivities, "com.roblox.client.startup.MainGameActivity")
			baseVersionName = p.VersionName
		}
		splitIdentity := p.SplitName
		if splitIdentity == "" {
			splitIdentity = "base"
		}
		if _, duplicate := seenSplits[splitIdentity]; duplicate {
			return Authorization{}, setupError(ErrUnsupportedSplit, "validate package", "the selected bundle repeats an APK split", nil)
		}
		seenSplits[splitIdentity] = struct{}{}
		if !allowedSplits[p.SplitName] {
			return Authorization{}, setupError(ErrUnsupportedSplit, "validate package", "the selected bundle contains an unsupported split", nil)
		}
		if signed := policy.RobloxPolicy; signed != nil && !policyAllowsSplit(signed, p.SplitName) {
			return Authorization{}, setupError(ErrUnsupportedSplit, "validate package", "the authenticated Roblox policy does not authorize this split", nil)
		}
		if version == 0 {
			version = p.VersionCode
		}
		if p.VersionCode <= 0 || p.VersionCode != version {
			return Authorization{}, setupError(ErrInvalidArchive, "validate package", "all APK splits must have the same positive version code", nil)
		}
		if p.Signing.ParseError != "" || !verifiedSchemePresent(p.Signing) || !p.Signing.CryptographicallyValid || len(p.Signing.VerifiedCertSHA256) != 1 {
			return Authorization{}, setupError(ErrInvalidSignature, "validate package", "an APK signature could not be verified", nil)
		}
		verified := append([]string(nil), p.Signing.VerifiedCertSHA256...)
		sort.Strings(verified)
		lineage := append([]string(nil), p.Signing.VerifiedLineageSHA256...)
		if len(lineage) == 0 {
			lineage = append(lineage, verified...)
		}
		for i := range lineage {
			lineage[i] = strings.ToLower(lineage[i])
		}
		if !validUniqueDigests(lineage) || lineage[len(lineage)-1] != strings.ToLower(verified[0]) || !allowedCerts[lineage[0]] {
			return Authorization{}, setupError(ErrUntrustedSigner, "validate package", "the APK signer is not rooted in the compiled Roblox identity", nil)
		}
		lineageID, policySequence, ok := "compiled-development", uint64(0), false
		policyAuthorized := false
		if mode == DevelopmentUnrestricted {
			ok = allowedCerts[lineage[len(lineage)-1]]
		} else if policy.RobloxPolicy == nil && policy.ReleaseAuthenticated {
			lineageID, policySequence, ok = "compiled-keyless-release", 0, allowedCerts[lineage[len(lineage)-1]]
			policyAuthorized = ok
		} else {
			lineageID, policySequence, ok = authorizeSignerLineage(policy.RobloxPolicy, uint64(p.VersionCode), lineage)
			policyAuthorized = ok
		}
		if !ok {
			if mode == DevelopmentUnrestricted {
				return Authorization{}, setupError(ErrUntrustedSigner, "validate package", "the development signer lineage does not terminate in the compiled Roblox identity", nil)
			}
			return Authorization{}, setupError(ErrUntrustedSigner, "validate package", "the authenticated Roblox policy does not authorize the verified signer lineage", nil)
		}
		joined := strings.Join(verified, ",")
		if signerSet == "" {
			signerSet = joined
		} else if signerSet != joined {
			return Authorization{}, setupError(ErrUntrustedSigner, "validate package", "APK splits do not share one signing identity", nil)
		}
		if len(authorized.SignerLineage) == 0 {
			authorized = Authorization{SignerLineageID: lineageID, SignerLineage: lineage, PolicySequence: policySequence, Mode: mode, PolicyAuthorized: policyAuthorized}
		} else if authorized.SignerLineageID != lineageID || strings.Join(authorized.SignerLineage, ",") != strings.Join(lineage, ",") {
			return Authorization{}, setupError(ErrUntrustedSigner, "validate package", "APK splits do not share one verified signer lineage", nil)
		}
	}
	if baseCount != 1 || rep.Merged.VersionName == "" || rep.Merged.VersionCode <= 0 ||
		version != rep.Merged.VersionCode || baseVersionName != rep.Merged.VersionName {
		return Authorization{}, setupError(ErrInvalidArchive, "validate package", "the package must contain one versioned base APK", nil)
	}
	if !baseManifestReady {
		return Authorization{}, setupError(ErrInvalidArchive, "validate package", "the base APK manifest has no launcher and GameActivity contract", nil)
	}
	foundABI := false
	for _, abi := range rep.Merged.Architectures {
		foundABI = foundABI || abi == "x86_64"
	}
	packagePaths := make(map[string]struct{}, len(rep.Packages))
	for _, pkg := range rep.Packages {
		packagePaths[pkg.Path] = struct{}{}
	}
	rootCount := 0
	for _, lib := range rep.Merged.NativeLibraries {
		if lib.ABI != "x86_64" || lib.Name != "libroblox.so" {
			continue
		}
		rootCount++
		_, knownPackage := packagePaths[lib.APKPath]
		if lib.ZIPPath != "lib/x86_64/libroblox.so" || lib.Size <= 0 || !validUniqueDigests([]string{lib.SHA256}) || !knownPackage {
			return Authorization{}, setupError(ErrMissingX8664, "validate package", "the x86-64 libroblox payload metadata is incomplete or inconsistent", nil)
		}
	}
	if !foundABI || rootCount != 1 {
		return Authorization{}, setupError(ErrMissingX8664, "validate package", "the selected package does not include x86_64 libroblox.so", nil)
	}
	return authorized, nil
}

func verifiedSchemePresent(signing apk.SigningInfo) bool {
	switch signing.VerifiedScheme {
	case "v2":
		return signing.HasV2
	case "v3":
		return signing.HasV3
	case "v3.1":
		return signing.HasV3_1
	default:
		return false
	}
}

func validUniqueDigests(digests []string) bool {
	if len(digests) == 0 || len(digests) > 32 {
		return false
	}
	seen := make(map[string]struct{}, len(digests))
	for _, digest := range digests {
		if len(digest) != 64 || strings.ToLower(digest) != digest {
			return false
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return false
		}
		if _, duplicate := seen[digest]; duplicate {
			return false
		}
		seen[digest] = struct{}{}
	}
	return true
}

func validConsumedRobloxPolicy(policy *securitypolicy.RobloxPolicy, minimumSequence uint64) bool {
	if policy == nil || policy.Schema != "tipsy.roblox-policy.v1" || policy.Validity.Sequence == 0 || policy.Validity.Sequence < minimumSequence ||
		policy.MinVersionCode == 0 || policy.MaxVersionCode < policy.MinVersionCode || len(policy.AllowedSplits) == 0 || len(policy.AllowedSplits) > 32 ||
		len(policy.SignerLineages) == 0 || len(policy.SignerLineages) > 16 {
		return false
	}
	seenSplits := make(map[string]struct{}, len(policy.AllowedSplits))
	for _, split := range policy.AllowedSplits {
		if split == "" {
			return false
		}
		if _, duplicate := seenSplits[split]; duplicate {
			return false
		}
		seenSplits[split] = struct{}{}
	}
	seenLineages := make(map[string]struct{}, len(policy.SignerLineages))
	for _, lineage := range policy.SignerLineages {
		if lineage.ID == "" || lineage.MinVersionCode == 0 || lineage.MaxVersionCode < lineage.MinVersionCode || !validUniqueDigests(lineage.SHA256) {
			return false
		}
		if _, duplicate := seenLineages[lineage.ID]; duplicate {
			return false
		}
		seenLineages[lineage.ID] = struct{}{}
	}
	return true
}

func policyAllowsSplit(policy *securitypolicy.RobloxPolicy, split string) bool {
	if split == "" {
		split = "base"
	}
	for _, allowed := range policy.AllowedSplits {
		if allowed == split {
			return true
		}
	}
	return false
}

func authorizeSignerLineage(policy *securitypolicy.RobloxPolicy, version uint64, verified []string) (string, uint64, bool) {
	if policy == nil {
		return "compiled", 0, len(verified) == 1
	}
	for _, lineage := range policy.SignerLineages {
		if version < lineage.MinVersionCode || version > lineage.MaxVersionCode {
			continue
		}
		allowed := make(map[string]struct{}, len(lineage.SHA256))
		for _, digest := range lineage.SHA256 {
			allowed[strings.ToLower(digest)] = struct{}{}
		}
		all := true
		for _, digest := range verified {
			if _, ok := allowed[digest]; !ok {
				all = false
				break
			}
		}
		if all {
			return lineage.ID, policy.Validity.Sequence, true
		}
	}
	return "", 0, false
}
