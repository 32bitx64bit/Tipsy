// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func TestResolveAuthorityNeverInfersOfficialWithoutAppImage(t *testing.T) {
	_, err := resolveAuthority(context.Background(), &config.Config{}, defaultAuthorityDependencies())
	if !errors.Is(err, ErrDevelopmentConsentRequired) {
		t.Fatalf("missing authority error = %v", err)
	}
	cfg := &config.Config{}
	cfg.ApproveDevelopment()
	got, err := resolveAuthority(context.Background(), cfg, defaultAuthorityDependencies())
	if err != nil || got.Mode != setupsvc.DevelopmentUnrestricted || got.Trust.Mode != setupsvc.DevelopmentUnrestricted || got.Trust.RobloxPolicy != nil {
		t.Fatalf("explicit development resolution = %+v, %v", got, err)
	}
}

func TestResolveAuthorityPrefersOfficialAppImageOverDevelopmentConsent(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApproveDevelopment()
	deps := defaultAuthorityDependencies()
	deps.identifyOfficialRelease = func(context.Context) error { return nil }
	got, err := resolveAuthority(context.Background(), cfg, deps)
	if err != nil || got.Mode != setupsvc.OfficialVerified || got.Trust.Mode != setupsvc.OfficialVerified || !got.Trust.ReleaseAuthenticated || got.Trust.RobloxPolicy != nil {
		t.Fatalf("authority=%+v err=%v", got, err)
	}
}

func TestResolveAuthorityRejectsBrokenAppImageInsteadOfDevelopmentFallback(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApproveDevelopment()
	broken := errors.New("running executable is outside the AppImage payload")
	deps := defaultAuthorityDependencies()
	deps.identifyOfficialRelease = func(context.Context) error { return broken }
	_, err := resolveAuthority(context.Background(), cfg, deps)
	if !errors.Is(err, broken) || errors.Is(err, ErrDevelopmentConsentRequired) {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveAuthorityAllowsDevelopmentOnlyOutsideAppImage(t *testing.T) {
	cfg := &config.Config{}
	cfg.ApproveDevelopment()
	deps := defaultAuthorityDependencies()
	deps.identifyOfficialRelease = func(context.Context) error { return errOfficialReleaseUnavailable }
	got, err := resolveAuthority(context.Background(), cfg, deps)
	if err != nil || got.Mode != setupsvc.DevelopmentUnrestricted || got.Trust.ReleaseAuthenticated {
		t.Fatalf("authority=%+v err=%v", got, err)
	}
}

func TestPackageAuthorizationTrustUsesDevelopmentWhenOfficialReleaseIsUnavailable(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	deps := defaultAuthorityDependencies()
	deps.identifyOfficialRelease = func(context.Context) error { return errOfficialReleaseUnavailable }

	trust, err := packageAuthorizationTrust(context.Background(), false, deps)
	if err != nil || trust.Mode != setupsvc.DevelopmentUnrestricted || trust.ReleaseAuthenticated || trust.RobloxPolicy != nil || trust.PackageName != "com.roblox.client" {
		t.Fatalf("unavailable official authority without consent = %+v, %v", trust, err)
	}

	trust, err = packageAuthorizationTrust(context.Background(), true, deps)
	if err != nil || trust.Mode != setupsvc.DevelopmentUnrestricted || trust.ReleaseAuthenticated {
		t.Fatalf("unavailable official authority with consent = %+v, %v", trust, err)
	}
}

func TestPackageAuthorizationTrustKeepsOfficialAppImageOfficial(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	deps := defaultAuthorityDependencies()
	deps.identifyOfficialRelease = func(context.Context) error { return nil }
	trust, err := packageAuthorizationTrust(context.Background(), true, deps)
	if err != nil || trust.Mode != setupsvc.OfficialVerified || !trust.ReleaseAuthenticated || trust.RobloxPolicy != nil {
		t.Fatalf("verified AppImage package trust = %+v, %v", trust, err)
	}
}

func TestPackageAuthorizationTrustRejectsBrokenAppImageInsteadOfDevelopmentFallback(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	broken := errors.New("running executable is outside the AppImage payload")
	deps := defaultAuthorityDependencies()
	deps.identifyOfficialRelease = func(context.Context) error { return broken }
	trust, err := packageAuthorizationTrust(context.Background(), true, deps)
	if !errors.Is(err, broken) || trust.Mode == setupsvc.DevelopmentUnrestricted {
		t.Fatalf("broken official package trust = %+v, %v", trust, err)
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

func TestRepairPicksNewestOfMultipleRetainedAPKs(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	retainedDir := filepath.Join(generationStoreRoot(), "apks", "sha256")
	if err := os.MkdirAll(retainedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(retainedDir, strings.Repeat("a", 64)+".apk")
	newer := filepath.Join(retainedDir, strings.Repeat("b", 64)+".apk")
	if err := os.WriteFile(older, []byte("older-apk"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("newer-apk"), 0o400); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	newTime := time.Now().Add(-time.Minute)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	openCalls := 0
	want := &fakeLaunchGeneration{authorization: setupsvc.Authorization{Mode: setupsvc.OfficialVerified, PolicyAuthorized: true}}
	wrong := &fakeLaunchGeneration{authorization: setupsvc.Authorization{Mode: setupsvc.DevelopmentUnrestricted}}
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			openCalls++
			if openCalls == 1 {
				return wrong, nil
			}
			return want, nil
		},
		derive: func(_ context.Context, paths []string, _ string, trust setupsvc.TrustPolicy) (string, error) {
			if len(paths) != 1 || paths[0] != newer {
				t.Fatalf("repair paths = %v, want only newest %q", paths, newer)
			}
			if trust.Mode != setupsvc.OfficialVerified {
				t.Fatalf("repair trust mode = %q", trust.Mode)
			}
			return strings.Repeat("c", 64), nil
		},
	}
	authority := authorityResolution{Mode: setupsvc.OfficialVerified, Trust: setupsvc.KeylessReleaseTrustPolicy()}
	got, err := openOrRepairGeneration(context.Background(), authority, deps)
	if err != nil || got != want {
		t.Fatalf("official repair = %#v, %v", got, err)
	}
	defer got.Close()
}
