// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/runtime"
)

type verifiedGenerationAPK struct {
	packageInfo apk.Package
	file        *integrity.PinnedFile
	entries     *apk.ZIPEntryVerifier
}

// AuthorizedGeneration is the launch-facing result of verifying the retained
// APK signatures, current policy, canonical inventory, and every guest DSO.
// Its generation is intentionally private so Loader cannot bypass this gate.
type AuthorizedGeneration struct {
	generation    *integrity.Generation
	authorization Authorization
	apks          map[string]verifiedGenerationAPK
	external      map[string]AuthenticatedExternalArtifact
	storeRoot     string
}

// OpenAuthorizedGeneration opens the atomically active generation and proves
// it still derives from retained, currently authorized APK descriptors.
func OpenAuthorizedGeneration(ctx context.Context, store integrity.Store, trust TrustPolicy) (*AuthorizedGeneration, error) {
	return OpenAuthorizedGenerationWithExternal(ctx, store, trust, nil)
}

// OpenAuthorizedGenerationWithExternal additionally authenticates separately
// downloaded targets against policy metadata supplied by the policy owner.
// The default official opener supplies none and therefore fails closed when a
// generation contains external content.
func OpenAuthorizedGenerationWithExternal(ctx context.Context, store integrity.Store, trust TrustPolicy, external map[string]AuthenticatedExternalArtifact) (*AuthorizedGeneration, error) {
	storeRoot, err := filepath.Abs(store.Root)
	if err != nil || store.Root == "" {
		return nil, fmt.Errorf("setup: generation store is unavailable")
	}
	store = StoreForTrust(store.Root, trust)
	generation, err := store.Active(ctx)
	if err != nil {
		return nil, err
	}
	authorized, err := authorizeGeneration(ctx, generation, trust, external)
	if err != nil {
		_ = generation.Close()
		return nil, err
	}
	authorized.storeRoot = storeRoot
	return authorized, nil
}

func authorizeGeneration(ctx context.Context, generation *integrity.Generation, trust TrustPolicy, external map[string]AuthenticatedExternalArtifact) (*AuthorizedGeneration, error) {
	if generation == nil {
		return nil, fmt.Errorf("setup: generation is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	packages := make([]apk.Package, 0)
	apks := make(map[string]verifiedGenerationAPK)
	for _, record := range generation.Inventory.Files {
		if record.Origin != integrity.OriginAPK || record.APKEntry != "@apk" {
			continue
		}
		if record.Executable || record.SHA256 != record.APKDigest || !strings.HasPrefix(record.Path, "apk/") {
			return nil, fmt.Errorf("setup: retained APK inventory record is inconsistent")
		}
		pinned := generation.Files[record.Path]
		if pinned == nil || pinned.File == nil {
			return nil, fmt.Errorf("setup: retained APK descriptor is unavailable")
		}
		pkg, err := apk.InspectVerifiedReaderAt(ctx, pinned.File, record.Size, record.Path)
		if err != nil {
			return nil, fmt.Errorf("setup: retained APK verification failed: %w", err)
		}
		if pkg.FileSHA256 != record.SHA256 {
			return nil, fmt.Errorf("setup: retained APK digest differs from inventory")
		}
		if _, duplicate := apks[record.APKDigest]; duplicate {
			return nil, fmt.Errorf("setup: retained APK digest is duplicated")
		}
		if err := pinned.Recheck(ctx); err != nil {
			return nil, fmt.Errorf("setup: retained APK changed during verification: %w", err)
		}
		entries, err := apk.NewZIPEntryVerifier(pinned.File, record.Size)
		if err != nil {
			return nil, fmt.Errorf("setup: retained APK asset index failed: %w", err)
		}
		apks[record.APKDigest] = verifiedGenerationAPK{packageInfo: pkg, file: pinned, entries: entries}
		packages = append(packages, pkg)
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("setup: generation has no retained APK")
	}
	report := apk.ReportFromPackages(packages)
	authorization, err := AuthorizeReport(report, trust)
	if err != nil {
		return nil, err
	}
	inventory := generation.Inventory
	if report.Merged == nil || inventory.PackageName != report.Merged.PackageName || inventory.VersionName != report.Merged.VersionName || inventory.VersionCode != report.Merged.VersionCode ||
		inventory.PolicySequence != authorization.PolicySequence || inventory.AuthorizationMode != string(authorization.Mode) || inventory.PolicyAuthorized != authorization.PolicyAuthorized || inventory.SignerLineageID != authorization.SignerLineageID || !slices.Equal(inventory.SignerLineage, authorization.SignerLineage) {
		return nil, fmt.Errorf("setup: generation authority differs from authenticated APK policy")
	}
	wantSplits := make([]string, 0, len(report.Packages))
	for _, pkg := range report.Packages {
		split := pkg.SplitName
		if split == "" {
			split = "base"
		}
		wantSplits = append(wantSplits, split)
	}
	sort.Strings(wantSplits)
	if !slices.Equal(inventory.Splits, wantSplits) {
		return nil, fmt.Errorf("setup: generation split inventory differs from retained APKs")
	}

	expectedNatives := make(map[string]struct{})
	usedExternal := make(map[string]struct{}, len(external))
	apkDigestByPath := make(map[string]string, len(report.Packages))
	for _, pkg := range report.Packages {
		apkDigestByPath[pkg.Path] = pkg.FileSHA256
	}
	for _, lib := range report.Merged.NativeLibraries {
		if lib.ABI != "x86_64" {
			continue
		}
		originDigest := apkDigestByPath[lib.APKPath]
		if originDigest == "" {
			return nil, fmt.Errorf("setup: merged native dependency has no retained APK origin")
		}
		expectedNatives[originDigest+"\x00"+lib.ZIPPath+"\x00"+lib.SHA256] = struct{}{}
	}
	for _, record := range inventory.Files {
		switch {
		case record.Origin == integrity.OriginAPK && record.APKEntry == "@apk":
			continue
		case record.Origin == integrity.OriginDerived:
			if record.Path != "meta.json" || record.APKEntry != "@derived/meta" || record.APKDigest != "" || record.Executable {
				return nil, fmt.Errorf("setup: generation has unsafe derived metadata")
			}
		case record.Executable:
			if record.Origin != integrity.OriginAPK || !strings.HasPrefix(record.Path, "lib/x86_64/") {
				return nil, fmt.Errorf("setup: executable generation file is outside the native directory")
			}
			key := record.APKDigest + "\x00" + record.APKEntry + "\x00" + record.SHA256
			if _, ok := expectedNatives[key]; !ok {
				return nil, fmt.Errorf("setup: native dependency does not derive from a retained APK")
			}
			delete(expectedNatives, key)
		case record.Origin == integrity.OriginAPK && strings.HasPrefix(record.Path, "assets/"):
			base, ok := apks[record.APKDigest]
			if !ok || base.packageInfo.SplitName != "" || record.APKEntry != record.Path {
				return nil, fmt.Errorf("setup: asset does not derive from the retained base APK")
			}
		case record.Origin == integrity.OriginOfficialExternal && strings.HasPrefix(record.Path, "assets/"):
			authenticated, ok := external[record.Path]
			if !ok || authenticated.Size != record.Size || strings.ToLower(authenticated.SHA256) != record.SHA256 || authenticated.PolicyOrigin != record.PolicyOrigin {
				return nil, fmt.Errorf("setup: external asset lacks current authenticated official policy")
			}
			usedExternal[record.Path] = struct{}{}
		default:
			return nil, fmt.Errorf("setup: generation contains an unclassified file")
		}
	}
	if len(expectedNatives) != 0 {
		return nil, fmt.Errorf("setup: generation omits an authenticated native dependency")
	}
	if len(usedExternal) != len(external) {
		return nil, fmt.Errorf("setup: authenticated external policy does not exactly match the active generation")
	}
	externalCopy := make(map[string]AuthenticatedExternalArtifact, len(external))
	for name, artifact := range external {
		externalCopy[name] = artifact
	}
	return &AuthorizedGeneration{generation: generation, authorization: authorization, apks: apks, external: externalCopy}, nil
}

func (g *AuthorizedGeneration) ID() string {
	if g == nil || g.generation == nil {
		return ""
	}
	return g.generation.ID
}

func (g *AuthorizedGeneration) Authorization() Authorization {
	if g == nil {
		return Authorization{}
	}
	return Authorization{SignerLineageID: g.authorization.SignerLineageID, SignerLineage: append([]string(nil), g.authorization.SignerLineage...), PolicySequence: g.authorization.PolicySequence, Mode: g.authorization.Mode, PolicyAuthorized: g.authorization.PolicyAuthorized}
}

// NativeDescriptorSet rechecks the retained APK and every native file, then
// returns them bound to the authenticated generation and inventory digest.
func (g *AuthorizedGeneration) NativeDescriptorSet(ctx context.Context) (*integrity.NativeDescriptorSet, error) {
	if g == nil || g.generation == nil {
		return nil, fmt.Errorf("setup: authorized generation is closed")
	}
	for _, retained := range g.apks {
		if err := retained.file.Recheck(ctx); err != nil {
			return nil, err
		}
	}
	set, err := g.generation.NativeDescriptorSet()
	if err != nil {
		return nil, err
	}
	natives, err := set.Descriptors()
	if err != nil {
		return nil, err
	}
	for _, native := range natives {
		if err := native.File.Recheck(ctx); err != nil {
			return nil, err
		}
	}
	if set.GenerationID() != g.generation.ID || set.InventorySHA256() != g.generation.InventorySHA256 {
		return nil, fmt.Errorf("setup: native descriptor set is bound to a different generation")
	}
	return set, nil
}

// NativeDescriptors temporarily satisfies the Phase 3B runtime interface.
// The higher-level integration must switch to NativeDescriptorSet so identity
// cannot be discarded before Loader validation.
func (g *AuthorizedGeneration) NativeDescriptors(ctx context.Context) ([]integrity.NativeDescriptor, error) {
	set, err := g.NativeDescriptorSet(ctx)
	if err != nil {
		return nil, err
	}
	return set.Descriptors()
}

// AuthorizedRuntimeFiles verifies every APK-derived asset against the retained
// signed base APK and returns the canonical same-generation paths needed by
// the Android asset bridge. The caller must retain g until launch completes.
func (g *AuthorizedGeneration) AuthorizedRuntimeFiles(ctx context.Context) (runtime.AuthorizedRuntimeFiles, error) {
	if g == nil || g.generation == nil || g.storeRoot == "" {
		return runtime.AuthorizedRuntimeFiles{}, fmt.Errorf("setup: authorized generation is closed")
	}
	basePath := ""
	for _, record := range g.generation.Inventory.Files {
		switch {
		case record.Origin == integrity.OriginAPK && record.APKEntry == "@apk":
			pinned := g.generation.Files[record.Path]
			if pinned == nil || pinned.File == nil {
				return runtime.AuthorizedRuntimeFiles{}, fmt.Errorf("setup: retained APK descriptor is unavailable")
			}
			if err := pinned.Recheck(ctx); err != nil {
				return runtime.AuthorizedRuntimeFiles{}, err
			}
			if record.Path == "apk/base.apk" {
				basePath = record.Path
			}
		case strings.HasPrefix(record.Path, "assets/"):
			if err := g.verifyFileOrigin(ctx, record.Path, false); err != nil {
				return runtime.AuthorizedRuntimeFiles{}, err
			}
		}
	}
	// The batch above proves each entry from the already-open signed APK. Hash
	// the APK descriptor once after the complete batch instead of once per
	// asset; the latter turns launch into O(asset count * APK size).
	for _, retained := range g.apks {
		if err := retained.file.Recheck(ctx); err != nil {
			return runtime.AuthorizedRuntimeFiles{}, err
		}
	}
	if basePath == "" {
		return runtime.AuthorizedRuntimeFiles{}, fmt.Errorf("setup: authenticated base APK is unavailable")
	}
	root := filepath.Join(g.storeRoot, "generations", g.generation.ID)
	return runtime.AuthorizedRuntimeFiles{
		GenerationID: g.generation.ID,
		RootDir:      root,
		AssetsDir:    filepath.Join(root, "assets"),
		BaseAPKPath:  filepath.Join(root, filepath.FromSlash(basePath)),
		VersionName:  g.generation.Inventory.VersionName,
	}, nil
}

// VerifyFileOrigin lazily verifies an asset against the retained base APK.
func (g *AuthorizedGeneration) VerifyFileOrigin(ctx context.Context, name string) error {
	return g.verifyFileOrigin(ctx, name, true)
}

func (g *AuthorizedGeneration) verifyFileOrigin(ctx context.Context, name string, recheckAPK bool) error {
	if g == nil || g.generation == nil {
		return fmt.Errorf("setup: authorized generation is closed")
	}
	pinned := g.generation.Files[name]
	if pinned == nil || pinned.Record.Executable || !strings.HasPrefix(name, "assets/") {
		return fmt.Errorf("setup: file is not a lazily verified generation asset")
	}
	if pinned.Record.Origin == integrity.OriginOfficialExternal {
		authenticated, ok := g.external[name]
		if !ok || authenticated.Size != pinned.Record.Size || strings.ToLower(authenticated.SHA256) != pinned.Record.SHA256 || authenticated.PolicyOrigin != pinned.Record.PolicyOrigin {
			return fmt.Errorf("setup: external asset lacks authenticated official policy")
		}
		return pinned.Recheck(ctx)
	}
	if pinned.Record.Origin != integrity.OriginAPK || pinned.Record.APKEntry != name {
		return fmt.Errorf("setup: file is not an APK-derived generation asset")
	}
	retained, ok := g.apks[pinned.Record.APKDigest]
	if !ok || retained.packageInfo.SplitName != "" || retained.entries == nil {
		return fmt.Errorf("setup: asset has no authenticated base APK")
	}
	if err := retained.entries.Verify(ctx, pinned.Record.APKEntry, pinned.Record.Size, pinned.Record.SHA256); err != nil {
		return err
	}
	if recheckAPK {
		if err := retained.file.Recheck(ctx); err != nil {
			return err
		}
	}
	return pinned.Recheck(ctx)
}

func (g *AuthorizedGeneration) Close() error {
	if g == nil || g.generation == nil {
		return nil
	}
	err := g.generation.Close()
	g.generation = nil
	g.apks = nil
	g.external = nil
	g.storeRoot = ""
	return err
}
