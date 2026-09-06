// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/releasemeta"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/securitypolicy"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func TestResolveAuthorityNeverInfersOfficialFromMissingH0(t *testing.T) {
	_, err := resolveAuthority(context.Background(), &config.Config{}, 0, defaultAuthorityDependencies())
	if !errors.Is(err, ErrDevelopmentConsentRequired) {
		t.Fatalf("missing authority error = %v", err)
	}
	cfg := &config.Config{}
	cfg.ApproveDevelopment()
	got, err := resolveAuthority(context.Background(), cfg, 0, defaultAuthorityDependencies())
	if err != nil || got.Mode != setupsvc.DevelopmentUnrestricted || got.Trust.Mode != setupsvc.DevelopmentUnrestricted || got.Trust.RobloxPolicy != nil {
		t.Fatalf("explicit development resolution = %+v, %v", got, err)
	}
}

func TestResolveAuthorityRequiresProductionBoundReleaseAndPassesPolicy(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	policy := testRobloxPolicy(now)
	cfg := testOfficialConfig()
	called := false
	deps := authorityDependencies{
		now:      func() time.Time { return now },
		readRoot: func(string) ([]byte, error) { return []byte("root"), nil },
		verifyRelease: func(options releasemeta.VerifyOptions) (releasemeta.Result, error) {
			called = len(options.InitialRoot) != 0 && options.TargetsDir == cfg.OfficialVerification.TargetsDir && options.Now.Equal(now)
			return releasemeta.Result{Verified: true, State: releasemeta.OfficialVerified, ProductionRootBound: true, ProductionEvidenceBound: true}, nil
		},
		readPolicy: func(string, releasemeta.Result, time.Time) (securitypolicy.RobloxPolicy, error) { return policy, nil },
	}
	got, err := resolveAuthority(context.Background(), cfg, 3000, deps)
	if err != nil || !called {
		t.Fatalf("official authority = %+v, %v; verifier called=%v", got, err, called)
	}
	if got.Mode != setupsvc.OfficialVerified || got.Trust.Mode != setupsvc.OfficialVerified || got.Trust.RobloxPolicy == nil ||
		got.Trust.RobloxPolicy.Validity.Sequence != policy.Validity.Sequence || got.Trust.InstalledVersionCode != 3000 || got.Trust.MinimumPolicySequence != policy.Validity.Sequence {
		t.Fatalf("official trust did not consume authenticated policy: %+v", got.Trust)
	}

	deps.verifyRelease = func(releasemeta.VerifyOptions) (releasemeta.Result, error) {
		return releasemeta.Result{Verified: true, State: releasemeta.DevelopmentUnrestricted}, nil
	}
	if _, err := resolveAuthority(context.Background(), cfg, 0, deps); err == nil {
		t.Fatal("development release result granted official package authority")
	}
}

func TestReadAuthenticatedRobloxPolicyPinsTUFDigest(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	policy := testRobloxPolicy(now)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	hexDigest := hex.EncodeToString(digest[:])
	target := "policies/roblox/" + hexDigest + ".json"
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	result := releasemeta.Result{Evidence: []releasemeta.EvidenceStatus{{Kind: "roblox_policy", Target: target, SHA256: hexDigest}}}
	got, err := readAuthenticatedRobloxPolicy(root, result, now)
	if err != nil || got.Validity.Sequence != policy.Validity.Sequence {
		t.Fatalf("policy = %+v, %v", got, err)
	}
	result.Evidence[0].SHA256 = strings.Repeat("0", 64)
	if _, err := readAuthenticatedRobloxPolicy(root, result, now); err == nil {
		t.Fatal("policy whose digest differs from TUF evidence was accepted")
	}
	result.Evidence[0].SHA256 = hexDigest
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", path); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthenticatedRobloxPolicy(root, result, now); err == nil {
		t.Fatal("symlink policy target was accepted")
	}
}

type fakeLaunchGeneration struct {
	authorization setupsvc.Authorization
	closed        *int
}

func (g *fakeLaunchGeneration) NativeDescriptorSet(context.Context) (*integrity.NativeDescriptorSet, error) {
	return nil, nil
}

func (g *fakeLaunchGeneration) AuthorizedRuntimeFiles(context.Context) (runtime.AuthorizedRuntimeFiles, error) {
	return runtime.AuthorizedRuntimeFiles{}, nil
}

func (g *fakeLaunchGeneration) Authorization() setupsvc.Authorization { return g.authorization }

func (g *fakeLaunchGeneration) Close() error {
	if g.closed != nil {
		*g.closed++
	}
	return nil
}

func TestDevelopmentLaunchDerivesFromExistingRetainedAPKAndPersistsConsent(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	legacyAPKDir := filepath.Join(runtimeDir(), "apk")
	if err := os.MkdirAll(legacyAPKDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyAPK := filepath.Join(legacyAPKDir, "base.apk")
	if err := os.WriteFile(legacyAPK, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	openCalls, deriveCalls, launchCalls, closeCalls := 0, 0, 0, 0
	generation := &fakeLaunchGeneration{authorization: setupsvc.Authorization{Mode: setupsvc.DevelopmentUnrestricted}, closed: &closeCalls}
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			openCalls++
			if openCalls == 1 {
				return nil, os.ErrNotExist
			}
			return generation, nil
		},
		derive: func(_ context.Context, paths []string, store string, trust setupsvc.TrustPolicy) (string, error) {
			deriveCalls++
			if len(paths) != 1 || paths[0] != legacyAPK || store != generationStoreRoot() || trust.Mode != setupsvc.DevelopmentUnrestricted {
				t.Fatalf("derive inputs = %v, %q, %+v", paths, store, trust)
			}
			return strings.Repeat("a", 64), nil
		},
		launch: func(_ context.Context, options runtime.LaunchOptions) error {
			launchCalls++
			if options.AuthorizedGeneration != generation || !options.Probe {
				t.Fatalf("launch options = %+v", options)
			}
			return nil
		},
	}
	var stdout, stderr strings.Builder
	code := cmdLaunchWithDependencies(context.Background(), []string{"--development", "--probe"}, &stdout, &stderr, defaultAuthorityDependencies(), deps)
	if code != 0 || deriveCalls != 1 || launchCalls != 1 || closeCalls != 1 || !strings.Contains(stderr.String(), "DevelopmentUnrestricted") {
		t.Fatalf("code=%d derive=%d launch=%d close=%d stderr=%q", code, deriveCalls, launchCalls, closeCalls, stderr.String())
	}
	cfg, err := config.Load()
	if err != nil || !cfg.DevelopmentApproved() {
		t.Fatalf("development consent = %+v, %v", cfg, err)
	}
}

func TestActiveGenerationFailureRepairsWithoutTouchingAccountData(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	storeRoot := generationStoreRoot()
	retainedDir := filepath.Join(storeRoot, "apks", "sha256")
	if err := os.MkdirAll(retainedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(retainedDir, strings.Repeat("a", 64)+".apk")
	if err := os.WriteFile(retained, []byte("signed-fixture"), 0o400); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(config.Paths().DataDir, "app-data", "com.roblox.client", "files", "opaque")
	if err := os.MkdirAll(filepath.Dir(account), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(account, []byte("account-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	openCalls := 0
	generation := &fakeLaunchGeneration{authorization: setupsvc.Authorization{Mode: setupsvc.DevelopmentUnrestricted}}
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			openCalls++
			if openCalls == 1 {
				return nil, errors.New("active generation digest mismatch")
			}
			return generation, nil
		},
		derive: func(_ context.Context, paths []string, _ string, _ setupsvc.TrustPolicy) (string, error) {
			if len(paths) != 1 || paths[0] != retained {
				t.Fatalf("repair paths = %v", paths)
			}
			return strings.Repeat("b", 64), nil
		},
	}
	authority := authorityResolution{Mode: setupsvc.DevelopmentUnrestricted, Trust: setupsvc.DevelopmentTrustPolicy()}
	got, err := openOrRepairGeneration(context.Background(), authority, deps)
	if err != nil || got != generation {
		t.Fatalf("repaired generation = %#v, %v", got, err)
	}
	defer got.Close()
	raw, err := os.ReadFile(account)
	if err != nil || string(raw) != "account-sentinel" {
		t.Fatalf("account data changed: %q, %v", raw, err)
	}
}

func TestNilGenerationResultFailsClosedAndCanRepair(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	retainedDir := filepath.Join(generationStoreRoot(), "apks", "sha256")
	if err := os.MkdirAll(retainedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(retainedDir, strings.Repeat("a", 64)+".apk")
	if err := os.WriteFile(retained, []byte("signed-fixture"), 0o400); err != nil {
		t.Fatal(err)
	}

	openCalls := 0
	want := &fakeLaunchGeneration{authorization: setupsvc.Authorization{Mode: setupsvc.DevelopmentUnrestricted}}
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			openCalls++
			if openCalls == 1 {
				return nil, nil
			}
			return want, nil
		},
		derive: func(_ context.Context, paths []string, _ string, _ setupsvc.TrustPolicy) (string, error) {
			if len(paths) != 1 || paths[0] != retained {
				t.Fatalf("repair paths = %v", paths)
			}
			return strings.Repeat("b", 64), nil
		},
	}
	authority := authorityResolution{Mode: setupsvc.DevelopmentUnrestricted, Trust: setupsvc.DevelopmentTrustPolicy()}
	got, err := openOrRepairGeneration(context.Background(), authority, deps)
	if err != nil || got != want {
		t.Fatalf("repaired nil generation = %#v, %v", got, err)
	}
	defer got.Close()
}

func testOfficialConfig() *config.Config {
	return &config.Config{OfficialVerification: &config.OfficialVerification{
		InitialRootPath: "/release/root.json", MetadataDir: "/release/metadata", TargetsDir: "/release/targets",
		ArtifactPath: "/release/Tipsy.AppImage", TargetPath: "stable/linux/x86_64/artifact.AppImage", Channel: "stable", StateDir: "/release/state",
	}}
}

func testRobloxPolicy(now time.Time) securitypolicy.RobloxPolicy {
	return securitypolicy.RobloxPolicy{
		Schema: "tipsy.roblox-policy.v1",
		Validity: securitypolicy.Validity{
			Sequence: 7, NotBefore: now.Add(-time.Hour).Format(time.RFC3339), Expires: now.Add(time.Hour).Format(time.RFC3339),
		},
		PackageName: "com.roblox.client", Platform: "android", Architecture: "x86_64",
		MinVersionCode: 2908, MaxVersionCode: 4000, AllowedSplits: []string{"base", "config.x86_64"},
		SignerLineages: []securitypolicy.SignerLineage{{
			ID: "roblox-main", SHA256: []string{strings.Repeat("a", 64)}, MinVersionCode: 2908, MaxVersionCode: 4000,
		}},
	}
}
