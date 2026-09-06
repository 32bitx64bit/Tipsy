// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
)

// GenerationStoreDir is deliberately outside the compatibility runtime path
// and the app-data tree. The Loader integration phase will consume active
// generations directly; account/auth/config data is never an input here.
func GenerationStoreDir(runtimeDir string) string { return runtimeDir + "-generations" }

// AuthenticatedExternalArtifact is supplied only by the authenticated policy
// owner for a separately downloaded official target. PolicyOrigin is the
// opaque canonical target identifier; it must not contain a URL or account
// data. An MD5 value cannot populate SHA256 and therefore cannot authorize an
// artifact.
type AuthenticatedExternalArtifact struct {
	SHA256       string
	Size         int64
	PolicyOrigin string
}

// PrepareGeneration builds a source-path-free canonical inventory, stages the
// exact extracted runtime as an immutable-by-convention generation, and
// returns its content identity without changing the active launch path.
func PrepareGeneration(ctx context.Context, sourceRuntimeDir, storeRoot string, rep *apk.Report, trust TrustPolicy) (string, error) {
	return PrepareGenerationWithExternal(ctx, sourceRuntimeDir, storeRoot, rep, trust, nil)
}

func PrepareGenerationWithExternal(ctx context.Context, sourceRuntimeDir, storeRoot string, rep *apk.Report, trust TrustPolicy, external map[string]AuthenticatedExternalArtifact) (string, error) {
	authorization, err := AuthorizeReport(rep, trust)
	if err != nil {
		return "", err
	}
	inventory, err := BuildGenerationInventoryWithExternal(ctx, sourceRuntimeDir, rep, authorization, external)
	if err != nil {
		return "", err
	}
	return (integrity.Store{Root: storeRoot}).Stage(ctx, sourceRuntimeDir, inventory)
}

func BuildGenerationInventory(ctx context.Context, runtimeDir string, rep *apk.Report, authorization Authorization) (integrity.Inventory, error) {
	return BuildGenerationInventoryWithExternal(ctx, runtimeDir, rep, authorization, nil)
}

// BuildGenerationInventoryWithExternal classifies every file by truthful
// provenance. APK assets are verified against the retained signed base APK;
// non-APK assets require an exact authenticated SHA-256, size, and policy
// target supplied out-of-band by the policy owner.
func BuildGenerationInventoryWithExternal(ctx context.Context, runtimeDir string, rep *apk.Report, authorization Authorization, external map[string]AuthenticatedExternalArtifact) (integrity.Inventory, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if rep == nil || rep.Merged == nil || len(rep.Packages) == 0 || len(authorization.SignerLineage) == 0 {
		return integrity.Inventory{}, fmt.Errorf("setup: cannot inventory an unauthorized package")
	}
	rawMeta, err := os.ReadFile(filepath.Join(runtimeDir, "meta.json"))
	if err != nil {
		return integrity.Inventory{}, fmt.Errorf("setup: read extracted metadata: %w", err)
	}
	var meta apk.Meta
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		return integrity.Inventory{}, fmt.Errorf("setup: decode extracted metadata: %w", err)
	}
	if meta.PackageName != rep.Merged.PackageName || meta.VersionCode != rep.Merged.VersionCode || meta.VersionName != rep.Merged.VersionName {
		return integrity.Inventory{}, fmt.Errorf("setup: extracted package identity differs from verified report")
	}

	apkDigestByDest := make(map[string]string, len(meta.Packages))
	baseDigest := ""
	baseAPKPath := ""
	baseAPKSize := int64(0)
	matchedPackages := make(map[int]struct{}, len(meta.Packages))
	for _, file := range meta.Packages {
		rel := filepath.ToSlash(file.Dest)
		if _, duplicate := apkDigestByDest[rel]; duplicate || !strings.HasPrefix(rel, "apk/") {
			return integrity.Inventory{}, fmt.Errorf("setup: extracted APK metadata has an unsafe or duplicate destination")
		}
		digest := strings.ToLower(file.SHA256)
		matched := -1
		for i, pkg := range rep.Packages {
			if pkg.FileSHA256 == digest && pkg.Size == file.Size && pkg.SplitName == file.SplitName {
				if _, used := matchedPackages[i]; !used {
					matched = i
					break
				}
			}
		}
		if matched < 0 {
			return integrity.Inventory{}, fmt.Errorf("setup: extracted APK metadata differs from the verified report")
		}
		matchedPackages[matched] = struct{}{}
		apkDigestByDest[rel] = digest
		if file.SplitName == "" {
			baseDigest = digest
			baseAPKPath = filepath.Join(runtimeDir, filepath.FromSlash(rel))
			baseAPKSize = file.Size
		}
	}
	if baseDigest == "" || len(matchedPackages) != len(rep.Packages) {
		return integrity.Inventory{}, fmt.Errorf("setup: extracted base APK is missing")
	}
	baseAPK, err := os.Open(baseAPKPath)
	if err != nil {
		return integrity.Inventory{}, fmt.Errorf("setup: open retained base APK: %w", err)
	}
	defer baseAPK.Close()
	baseEntries, err := apk.NewZIPEntryVerifier(baseAPK, baseAPKSize)
	if err != nil {
		return integrity.Inventory{}, fmt.Errorf("setup: index retained base APK: %w", err)
	}
	libOrigin := make(map[string]struct{ entry, apkDigest, fileDigest string })
	for _, lib := range rep.Merged.NativeLibraries {
		if lib.ABI != "x86_64" {
			continue
		}
		originDigest := ""
		for _, pkg := range rep.Packages {
			if pkg.Path == lib.APKPath {
				originDigest = strings.ToLower(pkg.FileSHA256)
				break
			}
		}
		if originDigest == "" {
			return integrity.Inventory{}, fmt.Errorf("setup: native dependency source is absent from the verified package set")
		}
		rel := filepath.ToSlash(filepath.Join("lib", "x86_64", lib.Name))
		origin := struct{ entry, apkDigest, fileDigest string }{
			entry: lib.ZIPPath, apkDigest: originDigest, fileDigest: strings.ToLower(lib.SHA256),
		}
		if previous, duplicate := libOrigin[rel]; duplicate && previous != origin {
			return integrity.Inventory{}, fmt.Errorf("setup: native dependency has ambiguous APK origins")
		}
		libOrigin[rel] = origin
	}

	var files []integrity.FileRecord
	usedExternal := make(map[string]struct{}, len(external))
	err = filepath.WalkDir(runtimeDir, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == runtimeDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("setup: generation source contains a symbolic link")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeType != 0 {
			return fmt.Errorf("setup: generation source contains a special file")
		}
		rel, err := filepath.Rel(runtimeDir, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		digest, size, err := hashGenerationFile(ctx, name)
		if err != nil {
			return err
		}
		record := integrity.FileRecord{Path: rel, Size: size, SHA256: digest}
		switch {
		case rel == "meta.json":
			record.Origin, record.APKEntry = integrity.OriginDerived, "@derived/meta"
		case strings.HasPrefix(rel, "apk/"):
			origin, ok := apkDigestByDest[rel]
			if !ok || origin != digest {
				return fmt.Errorf("setup: retained APK digest differs from verified report")
			}
			record.Origin, record.APKEntry, record.APKDigest = integrity.OriginAPK, "@apk", origin
		case strings.HasPrefix(rel, "lib/x86_64/"):
			origin, ok := libOrigin[rel]
			if !ok || origin.apkDigest == "" || origin.fileDigest != digest {
				return fmt.Errorf("setup: native dependency has no authenticated APK origin")
			}
			record.Origin, record.APKEntry, record.APKDigest, record.Executable = integrity.OriginAPK, origin.entry, origin.apkDigest, true
		case strings.HasPrefix(rel, "assets/"):
			if err := baseEntries.Verify(ctx, rel, size, digest); err == nil {
				record.Origin, record.APKEntry, record.APKDigest = integrity.OriginAPK, rel, baseDigest
				break
			}
			authenticated, ok := external[rel]
			if !ok || authenticated.Size != size || strings.ToLower(authenticated.SHA256) != digest || authenticated.PolicyOrigin == "" {
				return fmt.Errorf("setup: asset %q is not APK-derived and has no authenticated official SHA-256 policy origin", rel)
			}
			record.Origin, record.PolicyOrigin = integrity.OriginOfficialExternal, authenticated.PolicyOrigin
			usedExternal[rel] = struct{}{}
		default:
			return fmt.Errorf("setup: unexpected generation file %q", rel)
		}
		files = append(files, record)
		return nil
	})
	if err != nil {
		return integrity.Inventory{}, err
	}
	if len(usedExternal) != len(external) {
		return integrity.Inventory{}, fmt.Errorf("setup: authenticated external policy names a file absent from the generation")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	splits := []string{"base"}
	for _, pkg := range rep.Packages {
		if pkg.SplitName != "" {
			splits = append(splits, pkg.SplitName)
		}
	}
	sort.Strings(splits)
	return integrity.Inventory{
		Schema: integrity.InventorySchema, PackageName: rep.Merged.PackageName,
		VersionName: rep.Merged.VersionName, VersionCode: rep.Merged.VersionCode,
		PolicySequence: authorization.PolicySequence, AuthorizationMode: string(authorization.Mode), PolicyAuthorized: authorization.PolicyAuthorized, SignerLineageID: authorization.SignerLineageID,
		SignerLineage: append([]string(nil), authorization.SignerLineage...), Splits: splits, Files: files,
	}, nil
}

func hashGenerationFile(ctx context.Context, name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			size += int64(n)
			_, _ = hash.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
