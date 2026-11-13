// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package setupsvc

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
)

func validReport(cert string) *apk.Report {
	pkg := apk.Package{
		Path:             "base.apk",
		PackageName:      "com.roblox.client",
		VersionName:      "2.734.917",
		VersionCode:      2908,
		ManifestOK:       true,
		LauncherActivity: "com.roblox.client.startup.ActivitySplash",
		GameActivities:   []string{"com.roblox.client.startup.MainGameActivity"},
		Architectures:    []string{"x86_64"},
		NativeLibraries: []apk.NativeLib{{
			APKPath: "base.apk", ABI: "x86_64", Name: "libroblox.so", ZIPPath: "lib/x86_64/libroblox.so", Size: 1,
			SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		}},
		Signing: apk.SigningInfo{HasV2: true, CryptographicallyValid: true, VerifiedScheme: "v2", VerifiedCertSHA256: []string{cert}, VerifiedLineageSHA256: []string{cert}},
	}
	return &apk.Report{
		Packages: []apk.Package{pkg},
		Merged: &apk.Merged{
			PackageName: "com.roblox.client", VersionName: pkg.VersionName, VersionCode: pkg.VersionCode,
			Architectures: []string{"x86_64"}, NativeLibraries: pkg.NativeLibraries,
		},
	}
}

func testTrust(cert string) TrustPolicy {
	return TrustPolicy{
		Mode:                     OfficialVerified,
		ReleaseAuthenticated:     true,
		PackageName:              "com.roblox.client",
		AllowedCertificateSHA256: []string{cert},
		SupportedSplits:          []string{"", "config.x86_64"},
	}
}

func writeTestZIP(t *testing.T, path string, files map[string][]byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfficialReleaseAcceptsOnlyAuthenticatedOfficialVerifiedPolicy(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rep := validReport(cert)

	for _, tc := range []struct {
		name   string
		policy TrustPolicy
	}{
		{
			name:   "empty policy",
			policy: TrustPolicy{PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{cert}, SupportedSplits: []string{"", "config.x86_64"}},
		},
		{
			name:   "empty mode with ReleaseAuthenticated",
			policy: TrustPolicy{ReleaseAuthenticated: true, PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{cert}, SupportedSplits: []string{"", "config.x86_64"}},
		},
		{
			name:   "unauthenticated OfficialVerified",
			policy: TrustPolicy{Mode: OfficialVerified, ReleaseAuthenticated: false, PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{cert}, SupportedSplits: []string{"", "config.x86_64"}},
		},
		{
			name:   "compiled-out development mode",
			policy: TrustPolicy{Mode: AuthorizationMode("dev" + "elopment-unrestricted"), ReleaseAuthenticated: true, PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{cert}, SupportedSplits: []string{"", "config.x86_64"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AuthorizeReport(rep, tc.policy); ErrorKindOf(err) != ErrPolicy {
				t.Fatalf("AuthorizeReport(%s) kind=%q err=%v, want %q", tc.name, ErrorKindOf(err), err, ErrPolicy)
			}
			if err := ValidateReport(rep, tc.policy); ErrorKindOf(err) != ErrPolicy {
				t.Fatalf("ValidateReport(%s) kind=%q err=%v, want %q", tc.name, ErrorKindOf(err), err, ErrPolicy)
			}
		})
	}

	authorized, err := AuthorizeReport(rep, testTrust(cert))
	if err != nil {
		t.Fatalf("authenticated OfficialVerified rejected: %v", err)
	}
	if authorized.Mode != OfficialVerified || !authorized.PolicyAuthorized || authorized.SignerLineageID != "compiled-keyless-release" {
		t.Fatalf("authorized=%+v", authorized)
	}
}

func TestOfficialReleaseStoreAndReadinessRejectNonOfficialModes(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	legacyMode := AuthorizationMode("dev" + "elopment-unrestricted")

	for _, trust := range []TrustPolicy{
		OfficialTrustPolicy(),
		KeylessReleaseTrustPolicy(),
		{Mode: legacyMode, ReleaseAuthenticated: true},
		{},
	} {
		store := StoreForTrust("/tmp/store", trust)
		if store.ActiveSlot != integrity.ActiveSlotOfficial {
			t.Fatalf("StoreForTrust(%+v) slot=%q, want %q", trust, store.ActiveSlot, integrity.ActiveSlotOfficial)
		}
	}

	root := t.TempDir()
	source := filepath.Join(root, "source")
	storeRoot := filepath.Join(root, "store")
	t.Cleanup(func() { makeGenerationTreeWritable(storeRoot) })
	report := writeGenerationSource(t, source, cert)
	id, err := PrepareGeneration(context.Background(), source, storeRoot, report, testTrust(cert))
	if err != nil {
		t.Fatal(err)
	}
	if err := (integrity.Store{Root: storeRoot, ActiveSlot: integrity.ActiveSlotOfficial}).Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}

	unauthenticated := New(testHooks())
	unauthenticated.GenerationStoreRoot = storeRoot
	unauthenticated.Trust = OfficialTrustPolicy()
	snap, err := unauthenticated.Snapshot(context.Background())
	if ErrorKindOf(err) != ErrPolicy || snap.Installed || snap.Readiness != ReadinessRejected {
		t.Fatalf("unauthenticated Snapshot=%+v kind=%q err=%v", snap, ErrorKindOf(err), err)
	}

	if _, err := InspectActiveClientImports(context.Background(), storeRoot, TrustPolicy{Mode: legacyMode}, testHooks().SupportsImport); ErrorKindOf(err) != ErrPolicy {
		t.Fatalf("InspectActiveClientImports(non-official) kind=%q err=%v", ErrorKindOf(err), err)
	}

	devRecord, err := json.Marshal(integrity.ActiveRecord{Schema: integrity.ActiveSchema, Generation: id, InventorySHA256: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeRoot, "active-development.json"), devRecord, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(storeRoot, "active.json"))
	if _, err := InspectActiveClientImports(context.Background(), storeRoot, KeylessReleaseTrustPolicy(), testHooks().SupportsImport); err == nil {
		t.Fatal("active-development.json was accepted under official_release")
	}
}
