// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type readinessBackendFixture struct {
	snapshot     setupsvc.InstallSnapshot
	snapshotErr  error
	install      *setupsvc.InstallResult
	installErr   error
	installCalls int
	trust        setupsvc.TrustPolicy
}

func (f *readinessBackendFixture) SetPackageTrust(trust setupsvc.TrustPolicy) {
	f.trust = trust
}

func (f *readinessBackendFixture) Snapshot(context.Context) (setupsvc.InstallSnapshot, error) {
	return f.snapshot, f.snapshotErr
}

func (*readinessBackendFixture) AutomaticAvailability(context.Context) setupsvc.SourceAvailability {
	return setupsvc.SourceAvailability{}
}

func (f *readinessBackendFixture) Install(context.Context, setupsvc.InstallRequest, setupsvc.ProgressFunc) (*setupsvc.InstallResult, error) {
	f.installCalls++
	return f.install, f.installErr
}

func TestProductionSnapshotPresentsTypedReadinessWithoutClaimingPlayability(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		backend   *readinessBackendFixture
		installed bool
		readiness setupsvc.ReadinessState
		version   string
		contains  []string
	}{
		{
			name:      "not installed",
			backend:   &readinessBackendFixture{snapshot: setupsvc.InstallSnapshot{Readiness: setupsvc.ReadinessNotInstalled}},
			readiness: setupsvc.ReadinessNotInstalled,
			version:   "Not installed",
			contains: []string{
				"Not installed", "authenticated", "Run Setup",
			},
		},
		{
			name: "authenticated launch inputs",
			backend: &readinessBackendFixture{snapshot: setupsvc.InstallSnapshot{
				Installed: true, Readiness: setupsvc.ReadinessLaunchInputs, VersionName: "synthetic-version",
			}},
			installed: true,
			readiness: setupsvc.ReadinessLaunchInputs,
			version:   "synthetic-version",
			contains:  []string{"Authenticated launch inputs", "live launch", "separate runtime results"},
		},
		{
			name: "contradictory positive verdict",
			backend: &readinessBackendFixture{snapshot: setupsvc.InstallSnapshot{
				Readiness: setupsvc.ReadinessLaunchInputs,
			}},
			readiness: setupsvc.ReadinessRejected,
			version:   "Verification rejected",
			contains:  []string{"Rejected", "integrity", "launch remains disabled"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := (&productionService{installer: tt.backend}).Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.Installed != tt.installed || got.Readiness != tt.readiness || got.Version != tt.version {
				t.Fatalf("snapshot=%+v", got)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got.Status, want) {
					t.Fatalf("status %q does not contain %q", got.Status, want)
				}
			}
			if strings.Contains(strings.ToLower(got.Status), "playable") {
				t.Fatalf("static readiness claimed playability: %q", got.Status)
			}
		})
	}
}

func TestProductionSnapshotMapsTypedRevalidationErrorsToOpaqueRejectedState(t *testing.T) {
	t.Parallel()
	privateDetail := "/private/client/path contained secret-package-label"
	tests := []struct {
		kind     setupsvc.ErrorKind
		contains []string
	}{
		{kind: setupsvc.ErrCompatibility, contains: []string{"native interface", "Update Tipsy", "launch remains disabled"}},
		{kind: setupsvc.ErrIntegrity, contains: []string{"integrity", "Open Setup", "launch remains disabled"}},
		{kind: setupsvc.ErrInvalidSignature, contains: []string{"authenticated", "authorized source", "launch remains disabled"}},
		{kind: setupsvc.ErrMissingX8664, contains: []string{"x86-64", "complete official package set", "launch remains disabled"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.kind), func(t *testing.T) {
			t.Parallel()
			backend := &readinessBackendFixture{
				snapshot: setupsvc.InstallSnapshot{Readiness: setupsvc.ReadinessRejected},
				snapshotErr: &setupsvc.Error{
					Kind: tt.kind, Op: "installation status", Detail: privateDetail, Err: errors.New("synthetic cause"),
				},
			}
			got, err := (&productionService{installer: backend}).Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.Installed || got.Readiness != setupsvc.ReadinessRejected || got.Version != "Verification rejected" || !strings.HasPrefix(got.Status, "Rejected —") {
				t.Fatalf("rejected snapshot=%+v", got)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got.Status, want) {
					t.Fatalf("status %q does not contain %q", got.Status, want)
				}
			}
			if strings.Contains(got.Status, privateDetail) || strings.Contains(got.Status, "synthetic cause") || strings.Contains(got.Status, "could not be read") {
				t.Fatalf("rejected status leaked backend content or used the generic fallback: %q", got.Status)
			}
		})
	}
}

func TestProductionSnapshotStillPropagatesCancellation(t *testing.T) {
	t.Parallel()
	backend := &readinessBackendFixture{
		snapshot:    setupsvc.InstallSnapshot{Readiness: setupsvc.ReadinessRejected},
		snapshotErr: &setupsvc.Error{Kind: setupsvc.ErrCanceled, Err: context.Canceled},
	}
	if _, err := (&productionService{installer: backend}).Snapshot(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestProductionInstallCompatibilityFailureIsActionableAndOpaque(t *testing.T) {
	t.Parallel()
	privateDetail := "/private/client/path missing synthetic_native_symbol"
	backend := &readinessBackendFixture{installErr: &setupsvc.Error{
		Kind: setupsvc.ErrCompatibility, Detail: privateDetail, Err: errors.New("synthetic cause"),
	}}
	err := (&productionService{installer: backend}).Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {})
	if err == nil || !strings.Contains(err.Error(), "required native Android interface") || !strings.Contains(err.Error(), "update Tipsy") {
		t.Fatalf("compatibility error=%v", err)
	}
	if strings.Contains(err.Error(), privateDetail) || strings.Contains(err.Error(), "synthetic_native_symbol") {
		t.Fatalf("compatibility error leaked backend content: %q", err)
	}
}

func TestProductionInstallRequiresAuthenticatedLaunchInputs(t *testing.T) {
	t.Parallel()
	positive := setupsvc.InstallSnapshot{Installed: true, Readiness: setupsvc.ReadinessLaunchInputs}
	for name, result := range map[string]*setupsvc.InstallResult{
		"nil result":         nil,
		"incomplete verdict": {Snapshot: setupsvc.InstallSnapshot{Installed: true}},
		"rejected verdict":   {Snapshot: setupsvc.InstallSnapshot{Readiness: setupsvc.ReadinessRejected}},
	} {
		name, result := name, result
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			backend := &readinessBackendFixture{install: result}
			if err := (&productionService{installer: backend}).Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {}); err == nil {
				t.Fatal("incomplete install result was accepted")
			}
		})
	}
	backend := &readinessBackendFixture{install: &setupsvc.InstallResult{Snapshot: positive}}
	if err := (&productionService{installer: backend}).Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {}); err != nil {
		t.Fatalf("positive install result=%v", err)
	}
}

func TestProductionInstallBindsDevelopmentPackageTrustBeforeBackendInstall(t *testing.T) {
	t.Parallel()
	positive := setupsvc.InstallSnapshot{Installed: true, Readiness: setupsvc.ReadinessLaunchInputs}
	backend := &readinessBackendFixture{install: &setupsvc.InstallResult{Snapshot: positive}}
	service := &productionService{
		installer: backend,
		packageTrust: func(context.Context) (setupsvc.TrustPolicy, error) {
			return setupsvc.DevelopmentTrustPolicy(), nil
		},
	}
	if err := service.Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {}); err != nil {
		t.Fatalf("development install=%v", err)
	}
	if backend.installCalls != 1 {
		t.Fatalf("install calls=%d", backend.installCalls)
	}
	if backend.trust.Mode != setupsvc.DevelopmentUnrestricted || backend.trust.ReleaseAuthenticated || backend.trust.RobloxPolicy != nil {
		t.Fatalf("bound trust=%+v", backend.trust)
	}
}

func TestProductionInstallAppliesDevelopmentTrustToLiveSetupService(t *testing.T) {
	t.Parallel()
	inner := setupsvc.New()
	if inner.Trust.Mode != setupsvc.OfficialVerified || inner.Trust.ReleaseAuthenticated || inner.Trust.RobloxPolicy != nil {
		t.Fatalf("default live trust=%+v", inner.Trust)
	}
	service := &productionService{
		installer: inner,
		packageTrust: func(context.Context) (setupsvc.TrustPolicy, error) {
			return setupsvc.DevelopmentTrustPolicy(), nil
		},
	}
	err := service.Install(context.Background(), guimodel.InstallRequest{Mode: guimodel.InstallLocal}, func(guimodel.InstallProgress) {})
	if err == nil {
		t.Fatal("empty local install unexpectedly succeeded")
	}
	if inner.Trust.Mode != setupsvc.DevelopmentUnrestricted || inner.Trust.ReleaseAuthenticated || inner.Trust.RobloxPolicy != nil {
		t.Fatalf("live setup trust=%+v", inner.Trust)
	}
}

func TestProductionInstallDoesNotInstallWhenOfficialReleaseVerificationFails(t *testing.T) {
	t.Parallel()
	positive := setupsvc.InstallSnapshot{Installed: true, Readiness: setupsvc.ReadinessLaunchInputs}
	backend := &readinessBackendFixture{install: &setupsvc.InstallResult{Snapshot: positive}}
	broken := errors.New("official AppImage identity: running executable is outside the AppImage payload")
	service := &productionService{
		installer: backend,
		packageTrust: func(context.Context) (setupsvc.TrustPolicy, error) {
			return setupsvc.TrustPolicy{}, broken
		},
	}
	if err := service.Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {}); !errors.Is(err, broken) {
		t.Fatalf("broken official install=%v", err)
	}
	if backend.installCalls != 0 {
		t.Fatal("package install ran without package authority")
	}
	if backend.trust.Mode != "" {
		t.Fatalf("trust was bound after authority failure: %+v", backend.trust)
	}
}

func TestInstallPresentationKeepsThreeReadinessStatesDistinct(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state        setupsvc.ReadinessState
		badge        string
		style        string
		playDisabled bool
	}{
		{state: setupsvc.ReadinessNotInstalled, badge: "NOT INSTALLED", style: "statusWarning", playDisabled: true},
		{state: setupsvc.ReadinessRejected, badge: "REJECTED", style: "statusRejected", playDisabled: true},
		{state: setupsvc.ReadinessLaunchInputs, badge: "LAUNCH READY", style: "statusReady"},
	}
	for _, tt := range tests {
		presentation := installPresentation(tt.state)
		if presentation.badge != tt.badge || presentation.badgeStyle != tt.style {
			t.Fatalf("presentation for %q=%+v", tt.state, presentation)
		}
		if tt.playDisabled && !strings.Contains(strings.ToLower(presentation.idleText), "install") && !strings.Contains(strings.ToLower(presentation.idleText), "disabled") {
			t.Fatalf("disabled state lacks actionable idle copy: %+v", presentation)
		}
		if strings.Contains(strings.ToLower(presentation.idleText+presentation.settingsText), "playable") {
			t.Fatalf("static presentation claimed playability: %+v", presentation)
		}
	}
}
