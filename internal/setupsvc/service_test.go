// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/securitypolicy"
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

func TestValidateDevelopmentPackageWhenProvided(t *testing.T) {
	raw := os.Getenv("TIPSY_TEST_OFFICIAL_PACKAGES")
	if raw == "" {
		t.Skip("set TIPSY_TEST_OFFICIAL_PACKAGES to colon-separated locally owned APK paths")
	}
	paths := strings.Split(raw, ":")
	staged, err := copyAndValidateLocal(context.Background(), paths, filepath.Join(t.TempDir(), "staged"), DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := apk.Inspect(context.Background(), staged)
	if err != nil {
		t.Fatal(err)
	}
	if err := apk.VerifyReportSignatures(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	authorization, err := AuthorizeReport(rep, DevelopmentTrustPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Mode != DevelopmentUnrestricted || authorization.PolicyAuthorized {
		t.Fatalf("development authorization claimed official policy: %+v", authorization)
	}
	t.Logf("validated official package %s (%d), files=%d", rep.Merged.VersionName, rep.Merged.VersionCode, len(rep.Packages))
}

func testTrust(cert string) TrustPolicy {
	return TrustPolicy{Mode: DevelopmentUnrestricted, PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{cert}, SupportedSplits: []string{"", "config.x86_64"}}
}

func TestValidateReportRejectsPackageABIAndSignature(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := ValidateReport(validReport(cert), testTrust(cert)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*apk.Report)
		kind ErrorKind
	}{
		{"package", func(r *apk.Report) { r.Merged.PackageName = "example.invalid" }, ErrWrongPackage},
		{"abi", func(r *apk.Report) { r.Merged.Architectures = nil; r.Merged.NativeLibraries = nil }, ErrMissingX8664},
		{"merged version", func(r *apk.Report) { r.Merged.VersionCode++ }, ErrInvalidArchive},
		{"manifest startup", func(r *apk.Report) { r.Packages[0].GameActivities = []string{"com.roblox.client.OtherGameActivity"} }, ErrInvalidArchive},
		{"root payload path", func(r *apk.Report) { r.Merged.NativeLibraries[0].ZIPPath = "lib/x86_64/not-libroblox.so" }, ErrMissingX8664},
		{"root payload source", func(r *apk.Report) { r.Merged.NativeLibraries[0].APKPath = "not-in-report.apk" }, ErrMissingX8664},
		{"root payload size", func(r *apk.Report) { r.Merged.NativeLibraries[0].Size = 0 }, ErrMissingX8664},
		{"root payload digest", func(r *apk.Report) { r.Merged.NativeLibraries[0].SHA256 = "abcd" }, ErrMissingX8664},
		{"signature", func(r *apk.Report) { r.Packages[0].Signing.CryptographicallyValid = false }, ErrInvalidSignature},
		{"signer", func(r *apk.Report) { r.Packages[0].Signing.VerifiedCertSHA256 = []string{"bbbb"} }, ErrUntrustedSigner},
		{"split", func(r *apk.Report) { r.Packages[0].SplitName = "config.arm64_v8a" }, ErrUnsupportedSplit},
		{"store", func(r *apk.Report) {
			r.Merged.PackageName = "com.uptodown"
			r.Packages[0].PackageName = "com.uptodown"
		}, ErrWrongPackage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validReport(cert)
			tc.edit(r)
			if err := ValidateReport(r, testTrust(cert)); ErrorKindOf(err) != tc.kind {
				t.Fatalf("kind=%q err=%v want=%q", ErrorKindOf(err), err, tc.kind)
			}
		})
	}
}

func TestValidateReportRejectsUptodownInstaller(t *testing.T) {
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := validReport(cert)
	r.Merged.PackageName = "com.uptodown"
	r.Packages[0].PackageName = "com.uptodown"
	err := ValidateReport(r, testTrust(cert))
	if ErrorKindOf(err) != ErrWrongPackage || !strings.Contains(err.Error(), "installer") {
		t.Fatalf("err=%v", err)
	}
}

func TestAuthenticatedPolicyAuthorizesRotationWithoutSelfAuthorizingCandidate(t *testing.T) {
	const (
		compiled = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		rotated  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	policy := securitypolicy.RobloxPolicy{
		Schema: "tipsy.roblox-policy.v1",
		Validity: securitypolicy.Validity{
			Sequence: 7, NotBefore: time.Unix(1, 0).UTC().Format(time.RFC3339), Expires: time.Unix(1<<30, 0).UTC().Format(time.RFC3339),
		},
		PackageName: "com.roblox.client", Platform: "android", Architecture: "x86_64",
		MinVersionCode: 2908, MaxVersionCode: 4000, AllowedSplits: []string{"base", "config.x86_64"},
		SignerLineages: []securitypolicy.SignerLineage{{ID: "official-rotation", SHA256: []string{compiled, rotated}, MinVersionCode: 2908, MaxVersionCode: 4000}},
	}
	rep := validReport(rotated)
	rep.Packages[0].Signing.HasV2 = false
	rep.Packages[0].Signing.HasV3 = true
	rep.Packages[0].Signing.VerifiedScheme = "v3"
	rep.Packages[0].Signing.VerifiedLineageSHA256 = []string{compiled, rotated}
	trust := TrustPolicy{
		PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{compiled}, SupportedSplits: []string{"", "config.x86_64"},
		MinimumVersionCode: CompiledMinimumRobloxVersionCode, RobloxPolicy: &policy, MinimumPolicySequence: 7,
	}
	authorization, err := AuthorizeReport(rep, trust)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.SignerLineageID != "official-rotation" || authorization.PolicySequence != 7 || strings.Join(authorization.SignerLineage, ",") != compiled+","+rotated {
		t.Fatalf("authorization=%+v", authorization)
	}

	t.Run("candidate cannot teach compiled root", func(t *testing.T) {
		self := validReport(rotated)
		self.Packages[0].Signing.HasV2 = false
		self.Packages[0].Signing.HasV3 = true
		self.Packages[0].Signing.VerifiedScheme = "v3"
		self.Packages[0].Signing.VerifiedLineageSHA256 = []string{rotated}
		if _, err := AuthorizeReport(self, trust); ErrorKindOf(err) != ErrUntrustedSigner {
			t.Fatalf("self-authorized signer err=%v", err)
		}
	})
	t.Run("policy omits authenticated predecessor", func(t *testing.T) {
		missing := policy
		missing.SignerLineages = []securitypolicy.SignerLineage{{ID: "incomplete", SHA256: []string{rotated}, MinVersionCode: 2908, MaxVersionCode: 4000}}
		badTrust := trust
		badTrust.RobloxPolicy = &missing
		if _, err := AuthorizeReport(rep, badTrust); ErrorKindOf(err) != ErrUntrustedSigner {
			t.Fatalf("incomplete lineage err=%v", err)
		}
	})
	t.Run("stale policy", func(t *testing.T) {
		stale := trust
		stale.MinimumPolicySequence = 8
		if _, err := AuthorizeReport(rep, stale); ErrorKindOf(err) != ErrPolicy {
			t.Fatalf("stale policy err=%v", err)
		}
	})
	t.Run("installed downgrade", func(t *testing.T) {
		downgrade := trust
		downgrade.InstalledVersionCode = rep.Merged.VersionCode + 1
		if _, err := AuthorizeReport(rep, downgrade); ErrorKindOf(err) != ErrDowngrade {
			t.Fatalf("downgrade err=%v", err)
		}
	})
	t.Run("duplicate split", func(t *testing.T) {
		duplicated := *rep
		duplicated.Packages = append([]apk.Package(nil), rep.Packages...)
		split := duplicated.Packages[0]
		split.SplitName, split.IsSplit = "config.x86_64", true
		duplicated.Packages = append(duplicated.Packages, split, split)
		if _, err := AuthorizeReport(&duplicated, trust); ErrorKindOf(err) != ErrUnsupportedSplit {
			t.Fatalf("duplicate split err=%v", err)
		}
	})
}

func TestAuthorizationModesKeepDevelopmentDistinctAndCandidateCannotSelfAuthorize(t *testing.T) {
	const (
		compiled = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		rotated  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		unknown  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)
	report := validReport(rotated)
	report.Packages[0].Signing.HasV2 = false
	report.Packages[0].Signing.HasV3_1 = true
	report.Packages[0].Signing.VerifiedScheme = "v3.1"
	report.Packages[0].Signing.VerifiedLineageSHA256 = []string{compiled, rotated}

	official := TrustPolicy{Mode: OfficialVerified, PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{compiled, rotated}, SupportedSplits: []string{"", "config.x86_64"}}
	if _, err := AuthorizeReport(report, official); ErrorKindOf(err) != ErrPolicy {
		t.Fatalf("official mode without authenticated policy err=%v", err)
	}
	keyless := official
	keyless.ReleaseAuthenticated = true
	keylessAuthorization, err := AuthorizeReport(report, keyless)
	if err != nil || keylessAuthorization.Mode != OfficialVerified || !keylessAuthorization.PolicyAuthorized || keylessAuthorization.PolicySequence != 0 || keylessAuthorization.SignerLineageID != "compiled-keyless-release" {
		t.Fatalf("keyless official authorization=%+v err=%v", keylessAuthorization, err)
	}
	development := official
	development.Mode = DevelopmentUnrestricted
	authorization, err := AuthorizeReport(report, development)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.Mode != DevelopmentUnrestricted || authorization.PolicyAuthorized || authorization.PolicySequence != 0 || authorization.SignerLineageID != "compiled-development" {
		t.Fatalf("development authorization=%+v", authorization)
	}

	self := validReport(unknown)
	self.Packages[0].Signing.HasV2 = false
	self.Packages[0].Signing.HasV3_1 = true
	self.Packages[0].Signing.VerifiedScheme = "v3.1"
	self.Packages[0].Signing.VerifiedLineageSHA256 = []string{compiled, unknown}
	if _, err := AuthorizeReport(self, development); ErrorKindOf(err) != ErrUntrustedSigner {
		t.Fatalf("candidate taught development mode a new terminus: %v", err)
	}
}

func TestCopyLocalRejectsSymlinkLimitAndCancellation(t *testing.T) {
	root := t.TempDir()
	apkPath := writeTestZIP(t, filepath.Join(root, "base.apk"), map[string][]byte{"x": []byte("ok")})
	link := filepath.Join(root, "link.apk")
	if err := os.Symlink(apkPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAndValidateLocal(context.Background(), []string{link}, filepath.Join(root, "out1"), DefaultLimits(), nil); ErrorKindOf(err) != ErrUnsafePath {
		t.Fatalf("symlink err=%v", err)
	}
	limits := DefaultLimits()
	limits.MaxFileBytes = 1
	if _, err := copyAndValidateLocal(context.Background(), []string{apkPath}, filepath.Join(root, "out2"), limits, nil); ErrorKindOf(err) != ErrSizeLimit {
		t.Fatalf("limit err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyAndValidateLocal(ctx, []string{apkPath}, filepath.Join(root, "out3"), DefaultLimits(), nil); ErrorKindOf(err) != ErrCanceled {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestCopyLocalExpandsRealSplitDirectoryAndRejectsNestedSymlink(t *testing.T) {
	root := t.TempDir()
	splits := filepath.Join(root, "splits")
	if err := os.MkdirAll(filepath.Join(splits, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestZIP(t, filepath.Join(splits, "base.apk"), map[string][]byte{"x": []byte("base")})
	writeTestZIP(t, filepath.Join(splits, "nested", "split_config.x86_64.apk"), map[string][]byte{"x": []byte("split")})
	paths, err := copyAndValidateLocal(context.Background(), []string{splits}, filepath.Join(root, "staged"), DefaultLimits(), nil)
	if err != nil || len(paths) != 2 {
		t.Fatalf("expanded paths=%v err=%v", paths, err)
	}

	unsafeDir := filepath.Join(root, "unsafe")
	if err := os.MkdirAll(unsafeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(splits, "base.apk"), filepath.Join(unsafeDir, "linked.apk")); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAndValidateLocal(context.Background(), []string{unsafeDir}, filepath.Join(root, "rejected"), DefaultLimits(), nil); ErrorKindOf(err) != ErrUnsafePath {
		t.Fatalf("nested symlink err=%v", err)
	}
}

func TestZIPStructureRejectsTraversalAndDuplicates(t *testing.T) {
	root := t.TempDir()
	bad := writeTestZIP(t, filepath.Join(root, "bad.apk"), map[string][]byte{"../escape": []byte("x")})
	if err := validateZIPStructure(bad, DefaultLimits()); ErrorKindOf(err) != ErrInvalidArchive {
		t.Fatalf("traversal err=%v", err)
	}
	dup := filepath.Join(root, "duplicate.apk")
	f, err := os.Create(dup)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for i := 0; i < 2; i++ {
		w, _ := zw.Create("same")
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	_ = f.Close()
	if err := validateZIPStructure(dup, DefaultLimits()); ErrorKindOf(err) != ErrInvalidArchive {
		t.Fatalf("duplicate err=%v", err)
	}
}

func TestDefaultAutomaticSourceIsHonestlyUnavailable(t *testing.T) {
	s := NewWithSource(UnavailableSource{Reason: "No lawful source configured."})
	a := s.AutomaticAvailability(context.Background())
	if a.Available || !strings.Contains(a.Reason, "lawful") {
		t.Fatalf("availability=%+v", a)
	}
	if _, err := s.Install(context.Background(), InstallRequest{Mode: InstallAutomatic}, nil); ErrorKindOf(err) != ErrSourceUnavailable {
		t.Fatalf("automatic err=%v", err)
	}
}

func TestInstallStagesThenAtomicallyActivatesGeneration(t *testing.T) {
	root := t.TempDir()
	input := writeTestZIP(t, filepath.Join(root, "base.apk"), map[string][]byte{
		"AndroidManifest.xml":     []byte("manifest"),
		"lib/x86_64/libroblox.so": []byte("native"),
	})
	runtimeDir := filepath.Join(root, "data", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "old-only"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(root, "data", "app-data", "com.roblox.client", "files", "session")
	if err := os.MkdirAll(filepath.Dir(account), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(account, []byte("opaque-account-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nativeBody := []byte("verified")
	s := New()
	s.RuntimeDir = runtimeDir
	s.GenerationStoreRoot = filepath.Join(root, "data", "runtime-generations")
	t.Cleanup(func() { makeGenerationTreeWritable(s.GenerationStoreRoot) })
	s.Trust = testTrust(cert)
	s.inspect = func(_ context.Context, selected []string) (*apk.Report, error) {
		report := validReport(cert)
		digest, size, err := hashGenerationFile(context.Background(), selected[0])
		if err != nil {
			return nil, err
		}
		report.Packages[0].Path, report.Packages[0].FileSHA256, report.Packages[0].Size = selected[0], digest, size
		report.Packages[0].NativeLibraries[0].APKPath = selected[0]
		report.Packages[0].NativeLibraries[0].SHA256 = testSHA256(nativeBody)
		report.Packages[0].NativeLibraries[0].Size = int64(len(nativeBody))
		report.Merged.NativeLibraries = append([]apk.NativeLib(nil), report.Packages[0].NativeLibraries...)
		return report, nil
	}
	s.verify = func(context.Context, *apk.Report) error { return nil }
	s.prepare = func() error { return nil }
	compatibilityChecked := false
	s.authorizeStaged = func(context.Context, integrity.Store, string, TrustPolicy) error {
		if !compatibilityChecked {
			return errors.New("staged authorization ran before compatibility validation")
		}
		return nil
	}
	s.compatibility = func(context.Context, string) error {
		compatibilityChecked = true
		return nil
	}
	s.snapshot = func(_ context.Context, storeRoot string, _ TrustPolicy) (InstallSnapshot, error) {
		return InstallSnapshot{
			Installed: true, Readiness: ReadinessLaunchInputs,
			RuntimeDir:  filepath.Join(storeRoot, "generations", "synthetic"),
			PackageName: "com.roblox.client", VersionName: "2.734.917", VersionCode: 2908,
			Architectures: []string{"x86_64"},
		}, nil
	}
	s.extract = func(_ context.Context, selected []string, dest string) (*apk.ExtractResult, error) {
		if err := os.MkdirAll(filepath.Join(dest, "apk"), 0o700); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(dest, "lib", "x86_64"), 0o700); err != nil {
			return nil, err
		}
		rawAPK, err := os.ReadFile(selected[0])
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, "apk", "base.apk"), rawAPK, 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, "lib", "x86_64", "libroblox.so"), nativeBody, 0o600); err != nil {
			return nil, err
		}
		apkDigest := testSHA256(rawAPK)
		meta := apk.Meta{PackageName: "com.roblox.client", VersionName: "2.734.917", VersionCode: 2908,
			Packages: []apk.MetaFile{{Dest: "apk/base.apk", SHA256: apkDigest, Size: int64(len(rawAPK))}}}
		raw, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dest, "meta.json"), raw, 0o600); err != nil {
			return nil, err
		}
		return &apk.ExtractResult{DestDir: dest}, nil
	}
	var phases []ProgressPhase
	result, err := s.Install(context.Background(), InstallRequest{Mode: InstallLocal, LocalPaths: []string{input}}, func(p InstallProgress) {
		if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
			phases = append(phases, p.Phase)
		}
	})
	if err != nil || result == nil || !result.Snapshot.Installed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if raw, err := os.ReadFile(filepath.Join(runtimeDir, "old-only")); err != nil || string(raw) != "old" {
		t.Fatalf("non-authoritative legacy runtime changed: %q err=%v", raw, err)
	}
	if got, _ := os.ReadFile(account); string(got) != "opaque-account-data" {
		t.Fatalf("account data changed: %q", got)
	}
	if len(phases) < 5 || phases[len(phases)-1] != PhaseComplete {
		t.Fatalf("progress phases=%v", phases)
	}
	prior, err := StoreForTrust(s.GenerationStoreRoot, s.Trust).Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	priorID := prior.ID
	prior.Close()

	// Cancellation after a different generation has been staged and checked
	// must leave the trust-matched active record and account data bound to
	// the prior generation.
	nativeBody = []byte("verified-second")
	interrupted, cancel := context.WithCancel(context.Background())
	s.authorizeStaged = func(context.Context, integrity.Store, string, TrustPolicy) error {
		cancel()
		return nil
	}
	if _, err := s.Install(interrupted, InstallRequest{Mode: InstallLocal, LocalPaths: []string{input}}, nil); ErrorKindOf(err) != ErrCanceled {
		t.Fatalf("interrupted activation err=%v", err)
	}
	active, err := StoreForTrust(s.GenerationStoreRoot, s.Trust).Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if active.ID != priorID {
		t.Fatalf("interrupted setup activated %q, want prior %q", active.ID, priorID)
	}
	if got, _ := os.ReadFile(account); string(got) != "opaque-account-data" {
		t.Fatalf("account data changed after interrupted activation: %q", got)
	}
}

func TestInstallFailureAndCancellationLeaveRuntimeUntouched(t *testing.T) {
	root := t.TempDir()
	input := writeTestZIP(t, filepath.Join(root, "base.apk"), map[string][]byte{
		"AndroidManifest.xml":     []byte("manifest"),
		"lib/x86_64/libroblox.so": []byte("native"),
	})
	runtimeDir := filepath.Join(root, "data", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(runtimeDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, testErr := range []error{errors.New("extract failed"), context.Canceled} {
		s := New()
		s.RuntimeDir = runtimeDir
		s.Trust = testTrust(cert)
		s.inspect = func(context.Context, []string) (*apk.Report, error) { return validReport(cert), nil }
		s.verify = func(context.Context, *apk.Report) error { return nil }
		s.prepare = func() error { return nil }
		s.extract = func(context.Context, []string, string) (*apk.ExtractResult, error) { return nil, testErr }
		if _, err := s.Install(context.Background(), InstallRequest{Mode: InstallLocal, LocalPaths: []string{input}}, nil); err == nil {
			t.Fatalf("expected error for %v", testErr)
		}
		if got, _ := os.ReadFile(sentinel); string(got) != "old" {
			t.Fatalf("runtime changed after %v: %q", testErr, got)
		}
		stages, _ := filepath.Glob(filepath.Join(filepath.Dir(runtimeDir), ".setup-stage-*"))
		if len(stages) != 0 {
			t.Fatalf("staging remains after %v: %v", testErr, stages)
		}
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
