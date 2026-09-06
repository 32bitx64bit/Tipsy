// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
)

func TestBuildAndPrepareGenerationAuthenticatesEveryNativeAndOmitsOrigins(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root := t.TempDir()
	source := filepath.Join(root, "source")
	storeRoot := filepath.Join(root, "generations")
	t.Cleanup(func() { makeGenerationTreeWritable(storeRoot) })
	report := writeGenerationSource(t, source, cert)
	trust := testTrust(cert)
	authorization, err := AuthorizeReport(report, trust)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := BuildGenerationInventory(context.Background(), source, report, authorization)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := integrity.CanonicalInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "selected-secret-path") || strings.Contains(string(raw), "source") || strings.Contains(string(raw), "account") {
		t.Fatalf("canonical inventory disclosed origin/account data: %s", raw)
	}
	foundNative := false
	foundAsset := false
	for _, record := range inventory.Files {
		if record.Path == "lib/x86_64/libroblox.so" {
			foundNative = record.Origin == integrity.OriginAPK && record.Executable && record.APKEntry == "lib/x86_64/libroblox.so" && record.APKDigest == report.Packages[0].FileSHA256
		}
		if record.Path == "assets/client.json" {
			foundAsset = record.Origin == integrity.OriginAPK && record.APKEntry == record.Path && record.APKDigest == report.Packages[0].FileSHA256
		}
	}
	if !foundNative {
		t.Fatalf("native dependency lacks authenticated origin: %+v", inventory.Files)
	}
	if !foundAsset {
		t.Fatalf("asset lacks verified APK provenance: %+v", inventory.Files)
	}
	id, err := PrepareGeneration(context.Background(), source, storeRoot, report, trust)
	if err != nil {
		t.Fatal(err)
	}
	store := integrity.Store{Root: storeRoot}
	if err := store.Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	generation, err := store.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	natives, err := generation.NativeDescriptors()
	if err != nil || len(natives) != 1 || natives[0].SONAME != "libroblox.so" {
		t.Fatalf("native handoff=%+v err=%v", natives, err)
	}
}

func TestOpenAuthorizedGenerationRejectsSelfDescribingUnsignedInventory(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	root := t.TempDir()
	source := filepath.Join(root, "source")
	storeRoot := filepath.Join(root, "store")
	t.Cleanup(func() { makeGenerationTreeWritable(storeRoot) })
	report := writeGenerationSource(t, source, cert)
	id, err := PrepareGeneration(context.Background(), source, storeRoot, report, testTrust(cert))
	if err != nil {
		t.Fatal(err)
	}
	store := integrity.Store{Root: storeRoot}
	if err := store.Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if generation, err := OpenAuthorizedGeneration(context.Background(), store, testTrust(cert)); err == nil {
		generation.Close()
		t.Fatal("unsigned retained APK and self-describing inventory were accepted")
	}
}

func TestOpenAuthorizedDevelopmentGenerationWhenProvided(t *testing.T) {
	raw := os.Getenv("TIPSY_TEST_OFFICIAL_PACKAGES")
	if raw == "" {
		t.Skip("set TIPSY_TEST_OFFICIAL_PACKAGES to colon-separated locally owned APK paths")
	}
	paths := strings.Split(raw, ":")
	report, err := apk.Inspect(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := apk.VerifyReportSignatures(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	trust := DevelopmentTrustPolicy()
	if _, err := AuthorizeReport(report, trust); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	storeRoot := filepath.Join(root, "store")
	t.Cleanup(func() { makeGenerationTreeWritable(storeRoot) })
	id, err := DeriveAndActivateRetainedAPKs(context.Background(), paths, storeRoot, trust)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	store := integrity.Store{Root: storeRoot}
	generation, err := OpenAuthorizedGeneration(context.Background(), store, trust)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()
	if authorization := generation.Authorization(); authorization.Mode != DevelopmentUnrestricted || authorization.PolicyAuthorized {
		t.Fatalf("development generation claimed official status: %+v", authorization)
	}
	if _, err := generation.NativeDescriptors(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err := generation.AuthorizedRuntimeFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if files.GenerationID != id || files.RootDir != filepath.Join(storeRoot, "generations", id) ||
		files.AssetsDir != filepath.Join(files.RootDir, "assets") || files.BaseAPKPath != filepath.Join(files.RootDir, "apk", "base.apk") || files.VersionName == "" {
		t.Fatalf("authorized runtime files are not bound to the active generation: %+v", files)
	}
}

func TestBuildGenerationRejectsUnauthenticatedNativeAndRuntimeAccountData(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name string
		edit func(*testing.T, string, *apk.Report)
	}{
		{"modified native", func(t *testing.T, source string, _ *apk.Report) {
			if err := os.WriteFile(filepath.Join(source, "lib", "x86_64", "libroblox.so"), []byte("modified-native"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"untracked native", func(t *testing.T, source string, _ *apk.Report) {
			if err := os.WriteFile(filepath.Join(source, "lib", "x86_64", "libhostile.so"), []byte("hostile"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"account data in generation", func(t *testing.T, source string, _ *apk.Report) {
			if err := os.MkdirAll(filepath.Join(source, "app-data"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "app-data", "session"), []byte("opaque"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"ambiguous report origin", func(_ *testing.T, _ string, report *apk.Report) {
			report.Merged.NativeLibraries[0].APKPath = "not-in-report.apk"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			report := writeGenerationSource(t, source, cert)
			tc.edit(t, source, report)
			authorization, err := AuthorizeReport(report, testTrust(cert))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := BuildGenerationInventory(context.Background(), source, report, authorization); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}

func TestGenerationAssetProvenanceRejectsDownloadedContentWithoutAuthenticatedSHA256Policy(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source := filepath.Join(t.TempDir(), "source")
	report := writeGenerationSource(t, source, cert)
	authorization, err := AuthorizeReport(report, testTrust(cert))
	if err != nil {
		t.Fatal(err)
	}
	extra := []byte("separately-downloaded-content")
	assetPath := filepath.Join(source, "assets", "client.json")
	if err := os.WriteFile(assetPath, extra, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildGenerationInventory(context.Background(), source, report, authorization); err == nil {
		t.Fatal("separately downloaded content was mislabeled as APK-derived")
	}
	if _, err := BuildGenerationInventoryWithExternal(context.Background(), source, report, authorization, map[string]AuthenticatedExternalArtifact{
		"assets/client.json": {SHA256: "0123456789abcdef0123456789abcdef", Size: int64(len(extra)), PolicyOrigin: "roblox-policy/extracontent/client"},
	}); err == nil {
		t.Fatal("an MD5-length value authorized external content")
	}
	digest := testSHA256(extra)
	inventory, err := BuildGenerationInventoryWithExternal(context.Background(), source, report, authorization, map[string]AuthenticatedExternalArtifact{
		"assets/client.json": {SHA256: digest, Size: int64(len(extra)), PolicyOrigin: "roblox-policy/extracontent/client"},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range inventory.Files {
		if record.Path == "assets/client.json" {
			found = record.Origin == integrity.OriginOfficialExternal && record.PolicyOrigin == "roblox-policy/extracontent/client" && record.APKEntry == "" && record.APKDigest == ""
		}
	}
	if !found {
		t.Fatalf("external asset provenance is not truthful: %+v", inventory.Files)
	}
	if _, _, err := integrity.CanonicalInventory(inventory); err != nil {
		t.Fatal(err)
	}
}

func writeGenerationSource(t *testing.T, root, cert string) *apk.Report {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "apk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib", "x86_64"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	libRaw := []byte("authenticated-native")
	assetRaw := []byte("authenticated-asset")
	var apkBuffer bytes.Buffer
	zipWriter := zip.NewWriter(&apkBuffer)
	for name, raw := range map[string][]byte{
		"lib/x86_64/libroblox.so": libRaw,
		"assets/client.json":      assetRaw,
	} {
		writer, err := zipWriter.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	apkRaw := apkBuffer.Bytes()
	if err := os.WriteFile(filepath.Join(root, "apk", "base.apk"), apkRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "x86_64", "libroblox.so"), libRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "client.json"), assetRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	apkDigest := testSHA256(apkRaw)
	libDigest := testSHA256(libRaw)
	pkg := apk.Package{
		Path: "selected-secret-path/base.apk", FileSHA256: apkDigest, Size: int64(len(apkRaw)), PackageName: "com.roblox.client",
		VersionName: "2.734.917", VersionCode: 2908, ManifestOK: true, Architectures: []string{"x86_64"},
		NativeLibraries: []apk.NativeLib{{APKPath: "selected-secret-path/base.apk", ZIPPath: "lib/x86_64/libroblox.so", ABI: "x86_64", Name: "libroblox.so", Size: int64(len(libRaw)), SHA256: libDigest}},
		Signing:         apk.SigningInfo{HasV2: true, CryptographicallyValid: true, VerifiedScheme: "v2", VerifiedCertSHA256: []string{cert}, VerifiedLineageSHA256: []string{cert}},
	}
	report := &apk.Report{Packages: []apk.Package{pkg}, Merged: &apk.Merged{
		PackageName: pkg.PackageName, VersionName: pkg.VersionName, VersionCode: pkg.VersionCode,
		Architectures: []string{"x86_64"}, NativeLibraries: append([]apk.NativeLib(nil), pkg.NativeLibraries...),
	}}
	meta := apk.Meta{
		PackageName: pkg.PackageName, VersionName: pkg.VersionName, VersionCode: pkg.VersionCode,
		Packages:  []apk.MetaFile{{Source: pkg.Path, Dest: "apk/base.apk", SHA256: apkDigest, Size: pkg.Size}},
		Libraries: []apk.MetaLib{{Name: "libroblox.so", ZIPPath: pkg.NativeLibraries[0].ZIPPath, Dest: "lib/x86_64/libroblox.so", SHA256: libDigest, Size: int64(len(libRaw))}},
	}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "meta.json"), metaRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return report
}

func testSHA256(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func makeGenerationTreeWritable(root string) {
	_ = filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			_ = os.Chmod(name, 0o700)
		}
		return nil
	})
}
