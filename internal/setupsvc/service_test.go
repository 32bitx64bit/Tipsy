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
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/apk"
)

func validReport(cert string) *apk.Report {
	pkg := apk.Package{
		Path:            "base.apk",
		PackageName:     "com.roblox.client",
		VersionName:     "2.734.917",
		VersionCode:     2908,
		ManifestOK:      true,
		Architectures:   []string{"x86_64"},
		NativeLibraries: []apk.NativeLib{{ABI: "x86_64", Name: "libroblox.so", ZIPPath: "lib/x86_64/libroblox.so"}},
		Signing:         apk.SigningInfo{HasV2: true, CryptographicallyValid: true, VerifiedCertSHA256: []string{cert}},
	}
	return &apk.Report{
		Packages: []apk.Package{pkg},
		Merged: &apk.Merged{
			PackageName: "com.roblox.client", VersionName: pkg.VersionName, VersionCode: pkg.VersionCode,
			Architectures: []string{"x86_64"}, NativeLibraries: pkg.NativeLibraries,
		},
	}
}

func TestValidateOfficialPackageWhenProvided(t *testing.T) {
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
	if err := ValidateReport(rep, OfficialTrustPolicy()); err != nil {
		t.Fatal(err)
	}
	t.Logf("validated official package %s (%d), files=%d", rep.Merged.VersionName, rep.Merged.VersionCode, len(rep.Packages))
}

func testTrust(cert string) TrustPolicy {
	return TrustPolicy{PackageName: "com.roblox.client", AllowedCertificateSHA256: []string{cert}, SupportedSplits: []string{"", "config.x86_64"}}
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
		{"signature", func(r *apk.Report) { r.Packages[0].Signing.CryptographicallyValid = false }, ErrInvalidSignature},
		{"signer", func(r *apk.Report) { r.Packages[0].Signing.VerifiedCertSHA256 = []string{"bbbb"} }, ErrUntrustedSigner},
		{"split", func(r *apk.Report) { r.Packages[0].SplitName = "config.arm64_v8a" }, ErrUnsupportedSplit},
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

func TestADBSourceImportsOnlyBaseAndX8664Split(t *testing.T) {
	source := &ADBSource{ADBPath: "adb-test"}
	source.run = func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case joined == "get-state":
			return []byte("device\n"), nil
		case joined == "shell pm path com.roblox.client":
			return []byte("package:/data/app/roblox/base.apk\npackage:/data/app/roblox/split_config.en.apk\npackage:/data/app/roblox/split_config.x86_64.apk\n"), nil
		case len(args) == 3 && args[0] == "pull":
			if err := os.WriteFile(args[2], []byte("apk"), 0o600); err != nil {
				return nil, err
			}
			return nil, nil
		default:
			return nil, errors.New("unexpected adb invocation")
		}
	}
	if !source.Availability(context.Background()).Available {
		t.Fatal("fake authorized device unavailable")
	}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 2 || filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "split_config.x86_64.apk" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}

func TestADBSourceRejectsUntrustedRemotePath(t *testing.T) {
	source := &ADBSource{ADBPath: "adb-test"}
	source.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "get-state" {
			return []byte("device\n"), nil
		}
		return []byte("package:/sdcard/base.apk\n"), nil
	}
	_, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if ErrorKindOf(err) != ErrSourceTrust {
		t.Fatalf("unsafe ADB path err=%v", err)
	}
}

func TestHTTPSBundleSourceDownloadHashAndRedirectTrust(t *testing.T) {
	body := []byte("synthetic-apk-body")
	hash := sha256.Sum256(body)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})}
	source := &HTTPSBundleSource{
		Name: "test", LegalURL: "https://trusted.example/legal", Client: client, AllowedHosts: []string{"trusted.example"},
		Artifacts: []RemoteArtifact{{Name: "base.apk", URL: "https://trusted.example/base.apk", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(body))}},
	}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	got, _ := os.ReadFile(paths[0])
	if !bytes.Equal(got, body) {
		t.Fatalf("download=%q", got)
	}

	source.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Location", "https://evil.invalid/package.apk")
		return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	source.Artifacts[0].URL = "https://trusted.example/base.apk?synthetic-secret"
	_, err = source.Acquire(context.Background(), t.TempDir(), nil)
	if ErrorKindOf(err) != ErrNetwork || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatalf("redirect err=%v", err)
	}
}

func TestHTTPSBundleSourceCancellationCleansPartialFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &cancelingBody{cancel: cancel, data: []byte("0123456789")}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 10, Body: body, Header: make(http.Header)}, nil
	})}
	source := &HTTPSBundleSource{
		Name: "test", LegalURL: "https://trusted.example/legal", Client: client, AllowedHosts: []string{"trusted.example"},
		Artifacts: []RemoteArtifact{{Name: "base.apk", URL: "https://trusted.example/base.apk", SHA256: strings.Repeat("0", 64), Size: 10}},
	}
	dir := t.TempDir()
	_, err := source.Acquire(ctx, dir, nil)
	if ErrorKindOf(err) != ErrCanceled {
		t.Fatalf("cancel err=%v kind=%q", err, ErrorKindOf(err))
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("partial files remain: %v", entries)
	}
}

func TestInstallStagesThenAtomicallyReplacesRuntime(t *testing.T) {
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
	s := New()
	s.RuntimeDir = runtimeDir
	s.Trust = testTrust(cert)
	s.inspect = func(context.Context, []string) (*apk.Report, error) { return validReport(cert), nil }
	s.verify = func(context.Context, *apk.Report) error { return nil }
	s.prepare = func() error { return nil }
	s.extract = func(_ context.Context, _ []string, dest string) (*apk.ExtractResult, error) {
		if err := os.MkdirAll(filepath.Join(dest, "lib", "x86_64"), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, "lib", "x86_64", "libroblox.so"), []byte("verified"), 0o600); err != nil {
			return nil, err
		}
		meta := apk.Meta{PackageName: "com.roblox.client", VersionName: "2.734.917", VersionCode: 2908}
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
	if _, err := os.Stat(filepath.Join(runtimeDir, "old-only")); !os.IsNotExist(err) {
		t.Fatalf("old runtime survived replacement: %v", err)
	}
	if got, _ := os.ReadFile(account); string(got) != "opaque-account-data" {
		t.Fatalf("account data changed: %q", got)
	}
	if len(phases) < 5 || phases[len(phases)-1] != PhaseComplete {
		t.Fatalf("progress phases=%v", phases)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type cancelingBody struct {
	cancel context.CancelFunc
	data   []byte
	sent   bool
}

func (b *cancelingBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.EOF
	}
	b.sent = true
	n := copy(p, b.data[:5])
	b.cancel()
	return n, nil
}

func (*cancelingBody) Close() error { return nil }
