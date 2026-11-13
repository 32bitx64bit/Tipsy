// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/app"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func newVisualProofService() *secureVisualService {
	return &secureVisualService{
		visualService: visualService{
			launchEntered: make(chan struct{}),
			launchReady:   make(chan struct{}),
			launchDone:    make(chan struct{}),
		},
		requireConsent: true,
		authority: guimodel.LaunchAuthority{
			Mode: string(setupsvc.OfficialVerified),
		},
	}
}

func exerciseVisualProofLaunch(t *testing.T, win *mainWindow, service *secureVisualService) {
	t.Helper()
	if !win.beginLaunch(launchFromHome, guimodel.LaunchRequest{}) {
		t.Fatal("launch preparation did not start")
	}
	waitGUI(t, func() bool { return !win.launchPreparing })
	if !win.launchFailure {
		t.Fatal("unauthenticated launch did not fail closed under official_release")
	}
	if got := win.playAuthority.Text(); !strings.Contains(got, "OfficialVerified") {
		t.Fatalf("rejected authority warning was not visible: %q", got)
	}
	select {
	case <-service.launchEntered:
		t.Fatal("unauthenticated launch reached runtime under official_release")
	default:
	}
	if approvals := service.approvalCalls(); len(approvals) != 1 || approvals[0] {
		t.Fatalf("official_release retried with development approval: %v", approvals)
	}

	service.mu.Lock()
	service.requireConsent = false
	service.mu.Unlock()
	win.playButton.Click()
	waitGUI(t, func() bool {
		select {
		case <-service.launchEntered:
			return true
		default:
			return false
		}
	})
	if win.externalProgress != nil || !win.launchBusy.IsVisible() {
		t.Fatal("in-GUI Play did not keep its busy state in the main window")
	}
	if approvals := service.approvalCalls(); len(approvals) != 2 || approvals[0] || approvals[1] {
		t.Fatalf("official_release approval calls = %v", approvals)
	}
}

func stubExternalConsentDecline(*testing.T, *mainWindow) {}

type officialGUISessionFixture struct {
	state       app.LaunchAuthorityState
	launchErr   error
	launchCalls atomic.Int32
	closeCalls  atomic.Int32
	started     chan struct{}
	release     chan struct{}
	optionsMu   sync.Mutex
	lastOptions runtime.LaunchOptions
}

func (s *officialGUISessionFixture) State() app.LaunchAuthorityState { return s.state }

func (s *officialGUISessionFixture) Launch(_ context.Context, options runtime.LaunchOptions) error {
	s.launchCalls.Add(1)
	s.optionsMu.Lock()
	s.lastOptions = options
	s.optionsMu.Unlock()
	if s.started != nil {
		close(s.started)
	}
	if s.release != nil {
		<-s.release
	}
	return s.launchErr
}

func (s *officialGUISessionFixture) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func TestOfficialReleaseGUIPrepareLaunchRejectsDevelopmentAndUnverifiedModes(t *testing.T) {
	t.Parallel()
	openCalled := false
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(context.Context, bool) (authorizedLaunchSession, error) {
		openCalled = true
		return nil, nil
	}}}
	if _, err := service.PrepareLaunch(context.Background(), true); err == nil || openCalled {
		t.Fatalf("PrepareLaunch(approveDevelopment=true) = %v, openCalled=%v", err, openCalled)
	}

	for _, tc := range []struct {
		name  string
		state app.LaunchAuthorityState
	}{
		{
			name:  "consent required",
			state: app.LaunchAuthorityState{DevelopmentConsentRequired: true},
		},
		{
			name:  "non-official mode",
			state: app.LaunchAuthorityState{Mode: setupsvc.AuthorizationMode("development-" + "unrestricted")},
		},
		{
			name:  "official with unexpected warning",
			state: app.LaunchAuthorityState{Mode: setupsvc.OfficialVerified, Warning: "unexpected"},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			session := &officialGUISessionFixture{state: tc.state}
			svc := &productionService{launchBackend: authorizedLaunchBackend{open: func(context.Context, bool) (authorizedLaunchSession, error) {
				return session, nil
			}}}
			if _, err := svc.PrepareLaunch(context.Background(), false); err == nil {
				t.Fatalf("PrepareLaunch accepted invalid state %+v", tc.state)
			}
			if session.closeCalls.Load() != 1 {
				t.Fatalf("rejected session close calls = %d", session.closeCalls.Load())
			}
		})
	}
}

func TestOfficialReleaseGUILaunchRetainsVerifiedGenerationUntilRuntimeReturns(t *testing.T) {
	t.Parallel()
	session := &officialGUISessionFixture{
		state:   app.LaunchAuthorityState{Mode: setupsvc.OfficialVerified},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(_ context.Context, approve bool) (authorizedLaunchSession, error) {
		if approve {
			t.Fatal("unexpected approveDevelopment=true")
		}
		return session, nil
	}}}
	state, err := service.PrepareLaunch(context.Background(), false)
	if err != nil || state.Mode != string(setupsvc.OfficialVerified) || state.Warning != "" || state.DevelopmentConsentRequired {
		t.Fatalf("PrepareLaunch(official) = %+v, %v", state, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- service.Launch(context.Background(), guimodel.LaunchRequest{URI: "roblox://experiences/start?placeId=1818"}, func() {})
	}()
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("official runtime session was not entered")
	}
	if session.closeCalls.Load() != 0 {
		t.Fatal("session closed while active")
	}
	close(session.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if session.launchCalls.Load() != 1 || session.closeCalls.Load() != 1 {
		t.Fatalf("launch/close = %d/%d", session.launchCalls.Load(), session.closeCalls.Load())
	}
}

func TestOfficialReleaseSnapshotAndInstallFailClosedWithoutReleaseTrust(t *testing.T) {
	t.Parallel()
	backend := &readinessBackendFixture{
		snapshot: setupsvc.InstallSnapshot{Installed: true, Readiness: setupsvc.ReadinessLaunchInputs},
		install:  &setupsvc.InstallResult{Snapshot: setupsvc.InstallSnapshot{Installed: true, Readiness: setupsvc.ReadinessLaunchInputs}},
	}
	trustErr := app.ErrOfficialReleaseRequired
	service := &productionService{
		installer: backend,
		packageTrust: func(context.Context) (setupsvc.TrustPolicy, error) {
			return setupsvc.TrustPolicy{}, trustErr
		},
	}
	if _, err := service.Snapshot(context.Background()); !errors.Is(err, trustErr) {
		t.Fatalf("Snapshot without release trust = %v, want %v", err, trustErr)
	}
	if err := service.Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {}); !errors.Is(err, trustErr) || backend.installCalls != 0 {
		t.Fatalf("Install without release trust = %v, calls=%d", err, backend.installCalls)
	}

	officialService := &productionService{
		installer: backend,
		packageTrust: func(context.Context) (setupsvc.TrustPolicy, error) {
			return setupsvc.KeylessReleaseTrustPolicy(), nil
		},
	}
	if err := officialService.Install(context.Background(), guimodel.InstallRequest{}, func(guimodel.InstallProgress) {}); err != nil {
		t.Fatalf("Install with official trust = %v", err)
	}
	if backend.installCalls != 1 || backend.trust.Mode != setupsvc.OfficialVerified || !backend.trust.ReleaseAuthenticated {
		t.Fatalf("bound official trust = %+v (calls=%d)", backend.trust, backend.installCalls)
	}
}
