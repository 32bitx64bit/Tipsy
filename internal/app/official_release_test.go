// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type officialTestGeneration struct {
	authorization setupsvc.Authorization
	closed        atomic.Int32
}

func (g *officialTestGeneration) NativeDescriptorSet(context.Context) (*integrity.NativeDescriptorSet, error) {
	return nil, nil
}

func (g *officialTestGeneration) AuthorizedRuntimeFiles(context.Context) (runtime.AuthorizedRuntimeFiles, error) {
	return runtime.AuthorizedRuntimeFiles{}, nil
}

func (g *officialTestGeneration) Authorization() setupsvc.Authorization { return g.authorization }

func (g *officialTestGeneration) Close() error {
	g.closed.Add(1)
	return nil
}

func TestOfficialReleaseRejectsDevelopmentFlagsInLaunchAndSetup(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache"))

	devFlag := "--" + "development"

	code, _, errOut := runArgs(t, "launch", devFlag, "--probe")
	if code != 2 {
		t.Fatalf("launch %s exit=%d, want 2; stderr=%s", devFlag, code, errOut)
	}
	if !strings.Contains(errOut, "unexpected argument") {
		t.Fatalf("launch stderr=%s", errOut)
	}

	apkPath := filepath.Join(t.TempDir(), "base.apk")
	writeCLITestAPK(t, apkPath, map[string][]byte{
		"AndroidManifest.xml":     []byte("manifest"),
		"lib/x86_64/libroblox.so": []byte("cli-setup-lib"),
	})
	code, _, errOut = runArgs(t, "setup", devFlag, apkPath)
	if code != 2 {
		t.Fatalf("setup %s exit=%d, want 2; stderr=%s", devFlag, code, errOut)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Fatalf("setup stderr=%s", errOut)
	}

	cfg, err := config.Load()
	if err != nil || cfg.DevelopmentApproved() {
		t.Fatalf("rejected flag persisted consent: cfg=%+v err=%v", cfg, err)
	}
}

func TestOfficialReleaseFailsClosedEvenWithPersistedDevelopmentConsent(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))

	if err := config.Update(func(c *config.Config) error {
		c.ApproveDevelopment()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil || !cfg.DevelopmentApproved() {
		t.Fatalf("setup persisted consent fixture failed: %+v, %v", cfg, err)
	}

	unauthDeps := authorityDependencies{
		identifyOfficialRelease: func(context.Context) error { return errOfficialReleaseUnavailable },
	}
	if _, err := resolveAuthority(context.Background(), cfg, unauthDeps); !errors.Is(err, ErrOfficialReleaseRequired) {
		t.Fatalf("resolveAuthority with persisted consent = %v, want ErrOfficialReleaseRequired", err)
	}
	if trust, err := packageAuthorizationTrust(context.Background(), false, unauthDeps); !errors.Is(err, ErrOfficialReleaseRequired) || trust.Mode != "" {
		t.Fatalf("packageAuthorizationTrust fallback = %+v, %v", trust, err)
	}

	opened := false
	genDeps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			opened = true
			return nil, nil
		},
		derive: func(context.Context, []string, string, setupsvc.TrustPolicy) (string, error) { return "", nil },
		launch: func(context.Context, runtime.LaunchOptions) error { return nil },
	}
	_, err = openAuthorizedLaunchSession(context.Background(), false, unauthDeps, genDeps)
	var sessionErr *LaunchSessionError
	if !errors.Is(err, ErrOfficialReleaseRequired) || !errors.As(err, &sessionErr) || sessionErr.Authority.DevelopmentConsentRequired || opened {
		t.Fatalf("openAuthorizedLaunchSession unauthenticated = %v (sessionErr=%+v opened=%v)", err, sessionErr, opened)
	}

	code, _, errOut := runArgs(t, "launch", "--probe")
	if code != 1 || strings.Contains(errOut, "--"+"development") {
		t.Fatalf("launch --probe exit=%d stderr=%q", code, errOut)
	}
}

func TestOfficialReleaseRejectsExplicitDevelopmentApprovalAndAcceptsAuthenticatedOfficial(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))

	officialDeps := authorityDependencies{
		identifyOfficialRelease: func(context.Context) error { return nil },
	}
	if _, err := packageAuthorizationTrust(context.Background(), true, officialDeps); !errors.Is(err, errDevelopmentDisabled) {
		t.Fatalf("packageAuthorizationTrust(approveDevelopment=true) = %v, want errDevelopmentDisabled", err)
	}
	trust, err := packageAuthorizationTrust(context.Background(), false, officialDeps)
	if err != nil || trust.Mode != setupsvc.OfficialVerified || !trust.ReleaseAuthenticated {
		t.Fatalf("packageAuthorizationTrust(official) = %+v, %v", trust, err)
	}

	legacyAPKDir := filepath.Join(runtimeDir(), "apk")
	if err := os.MkdirAll(legacyAPKDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyAPKDir, "base.apk"), []byte("legacy-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	unauthorizedGen := &officialTestGeneration{
		authorization: setupsvc.Authorization{Mode: setupsvc.OfficialVerified, PolicyAuthorized: false},
	}
	derived := false
	rejectDeps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			return unauthorizedGen, nil
		},
		derive: func(context.Context, []string, string, setupsvc.TrustPolicy) (string, error) {
			derived = true
			return "", nil
		},
		launch: func(context.Context, runtime.LaunchOptions) error { return nil },
	}
	if _, err := openAuthorizedLaunchSession(context.Background(), false, officialDeps, rejectDeps); err == nil || derived || unauthorizedGen.closed.Load() != 1 {
		t.Fatalf("unauthenticated policy generation: err=%v derived=%v closed=%d", err, derived, unauthorizedGen.closed.Load())
	}

	validGen := &officialTestGeneration{
		authorization: setupsvc.Authorization{Mode: setupsvc.OfficialVerified, PolicyAuthorized: true},
	}
	launched := false
	okDeps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			return validGen, nil
		},
		derive: func(context.Context, []string, string, setupsvc.TrustPolicy) (string, error) { return "", nil },
		launch: func(_ context.Context, opts runtime.LaunchOptions) error {
			launched = opts.AuthorizedGeneration == validGen
			return nil
		},
	}
	if _, err := openAuthorizedLaunchSession(context.Background(), true, officialDeps, okDeps); !errors.Is(err, errDevelopmentDisabled) {
		t.Fatalf("openAuthorizedLaunchSession(approveDevelopment=true) = %v", err)
	}
	session, err := openAuthorizedLaunchSession(context.Background(), false, officialDeps, okDeps)
	if err != nil {
		t.Fatal(err)
	}
	if state := session.State(); state.Mode != setupsvc.OfficialVerified || state.Warning != "" || state.DevelopmentConsentRequired {
		t.Fatalf("official session state = %+v", state)
	}
	if err := session.Launch(context.Background(), runtime.LaunchOptions{}); err != nil || !launched {
		t.Fatalf("session.Launch = %v, launched=%v", err, launched)
	}
	if err := session.Close(); err != nil || validGen.closed.Load() != 1 {
		t.Fatalf("session.Close = %v, closed=%d", err, validGen.closed.Load())
	}
}
