// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"
	"github.com/tipsy-linux/tipsy/internal/app"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type guiSessionFixture struct {
	state       app.LaunchAuthorityState
	launchErr   error
	launchCalls atomic.Int32
	closeCalls  atomic.Int32
	started     chan struct{}
	release     chan struct{}
	optionsMu   sync.Mutex
	lastOptions runtime.LaunchOptions
}

func (s *guiSessionFixture) State() app.LaunchAuthorityState { return s.state }

func (s *guiSessionFixture) Launch(_ context.Context, options runtime.LaunchOptions) error {
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

func (s *guiSessionFixture) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func developmentGUIState() app.LaunchAuthorityState {
	return app.LaunchAuthorityState{
		Mode:    setupsvc.DevelopmentUnrestricted,
		Warning: app.DevelopmentUnrestrictedWarning,
	}
}

func TestProductionPrepareLaunchRequiresExplicitConsentAndPropagatesWarning(t *testing.T) {
	session := &guiSessionFixture{state: developmentGUIState()}
	var calls []bool
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(_ context.Context, approve bool) (authorizedLaunchSession, error) {
		calls = append(calls, approve)
		if !approve {
			return nil, &app.LaunchSessionError{
				Authority: app.LaunchAuthorityState{DevelopmentConsentRequired: true},
				Err:       app.ErrDevelopmentConsentRequired,
			}
		}
		return session, nil
	}}}

	state, err := service.PrepareLaunch(context.Background(), false)
	if !errors.Is(err, app.ErrDevelopmentConsentRequired) || !state.DevelopmentConsentRequired {
		t.Fatalf("missing-consent result = %+v, %v", state, err)
	}
	state, err = service.PrepareLaunch(context.Background(), true)
	if err != nil || state.Mode != string(setupsvc.DevelopmentUnrestricted) || state.Warning != app.DevelopmentUnrestrictedWarning || state.DevelopmentConsentRequired {
		t.Fatalf("development result = %+v, %v", state, err)
	}
	if len(calls) != 2 || calls[0] || !calls[1] {
		t.Fatalf("explicit-consent calls = %v", calls)
	}
}

func TestProductionLaunchRetainsGenerationUntilRuntimeReturns(t *testing.T) {
	session := &guiSessionFixture{
		state:   developmentGUIState(),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	rejected := &guiSessionFixture{state: developmentGUIState()}
	var opens atomic.Int32
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(context.Context, bool) (authorizedLaunchSession, error) {
		if opens.Add(1) > 1 {
			return rejected, nil
		}
		return session, nil
	}}}
	if _, err := service.PrepareLaunch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	acknowledged := atomic.Bool{}
	done := make(chan error, 1)
	go func() {
		done <- service.Launch(context.Background(), guimodel.LaunchRequest{URI: "roblox://experiences/start?placeId=1818"}, func() {
			acknowledged.Store(true)
		})
	}()
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("authorized runtime session was not entered")
	}
	if session.closeCalls.Load() != 0 {
		t.Fatal("authorized generation closed while runtime was active")
	}
	if _, err := service.PrepareLaunch(context.Background(), true); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent preparation error = %v", err)
	}
	if session.closeCalls.Load() != 0 || rejected.closeCalls.Load() != 1 {
		t.Fatalf("active/rejected close calls = %d/%d", session.closeCalls.Load(), rejected.closeCalls.Load())
	}
	close(session.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if session.launchCalls.Load() != 1 || session.closeCalls.Load() != 1 {
		t.Fatalf("session launch/close calls = %d/%d", session.launchCalls.Load(), session.closeCalls.Load())
	}
	if acknowledged.Load() {
		t.Fatal("test session unexpectedly acknowledged startup")
	}
	session.optionsMu.Lock()
	request := session.lastOptions.Request
	session.optionsMu.Unlock()
	if request.PlaceID != 1818 {
		t.Fatalf("website request was not forwarded through authorized session: %+v", request)
	}
}

func TestProductionLaunchFailsClosedOnTamperAndNeverFallsBack(t *testing.T) {
	tamper := errors.New("active generation changed after authorization")
	session := &guiSessionFixture{state: developmentGUIState(), launchErr: tamper}
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(context.Context, bool) (authorizedLaunchSession, error) {
		return session, nil
	}}}
	if err := service.Launch(context.Background(), guimodel.LaunchRequest{}, func() {}); err == nil || !strings.Contains(err.Error(), "authorized GUI launch session is required") {
		t.Fatalf("launch without authority = %v", err)
	}
	if _, err := service.PrepareLaunch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := service.Launch(context.Background(), guimodel.LaunchRequest{}, func() {}); !errors.Is(err, tamper) {
		t.Fatalf("tamper result = %v", err)
	}
	if session.launchCalls.Load() != 1 || session.closeCalls.Load() != 1 {
		t.Fatalf("tamper launch/close calls = %d/%d", session.launchCalls.Load(), session.closeCalls.Load())
	}
	if err := service.Launch(context.Background(), guimodel.LaunchRequest{}, func() {}); err == nil {
		t.Fatal("consumed failed authority was reused or a legacy launch fallback ran")
	}
}

func TestProductionPrepareLaunchRejectsDevelopmentWithoutWarning(t *testing.T) {
	session := &guiSessionFixture{state: app.LaunchAuthorityState{Mode: setupsvc.DevelopmentUnrestricted}}
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(context.Context, bool) (authorizedLaunchSession, error) {
		return session, nil
	}}}
	if _, err := service.PrepareLaunch(context.Background(), true); err == nil {
		t.Fatal("warning-free DevelopmentUnrestricted authority was accepted")
	}
	if session.closeCalls.Load() != 1 {
		t.Fatalf("rejected session close calls = %d", session.closeCalls.Load())
	}
}

func TestProductionInvalidURIClosesPreparedAuthority(t *testing.T) {
	session := &guiSessionFixture{state: developmentGUIState()}
	service := &productionService{launchBackend: authorizedLaunchBackend{open: func(context.Context, bool) (authorizedLaunchSession, error) {
		return session, nil
	}}}
	if _, err := service.PrepareLaunch(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := service.Launch(context.Background(), guimodel.LaunchRequest{URI: "https://example.invalid/not-roblox"}, func() {}); err == nil {
		t.Fatal("invalid external URI was accepted")
	}
	if session.launchCalls.Load() != 0 || session.closeCalls.Load() != 1 {
		t.Fatalf("invalid URI launch/close calls = %d/%d", session.launchCalls.Load(), session.closeCalls.Load())
	}
}

func newVisualProofService() *secureVisualService {
	return &secureVisualService{
		visualService: visualService{
			launchEntered: make(chan struct{}),
			launchReady:   make(chan struct{}),
			launchDone:    make(chan struct{}),
		},
		requireConsent: true,
		authority: guimodel.LaunchAuthority{
			Mode:    string(setupsvc.DevelopmentUnrestricted),
			Warning: app.DevelopmentUnrestrictedWarning,
		},
	}
}

func exerciseVisualProofLaunch(t *testing.T, win *mainWindow, service *secureVisualService) {
	t.Helper()
	originalConfirm := confirmDevelopmentLaunch
	t.Cleanup(func() { confirmDevelopmentLaunch = originalConfirm })
	confirmDevelopmentLaunch = func(*qt.QWidget) bool { return false }
	if !win.beginLaunch(launchFromHome, guimodel.LaunchRequest{}) {
		t.Fatal("launch preparation did not start")
	}
	waitGUI(t, func() bool { return !win.launchPreparing })
	if !win.launchFailure {
		t.Fatal("declined development launch did not show failure")
	}

	if got := win.playAuthority.Text(); !strings.Contains(got, "Approval required") || !strings.Contains(got, "OfficialVerified") {
		t.Fatalf("missing-consent warning was not visible: %q", got)
	}
	select {
	case <-service.launchEntered:
		t.Fatal("declined development consent reached the launch backend")
	default:
	}
	confirmDevelopmentLaunch = func(*qt.QWidget) bool { return true }
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

	if got := win.playAuthority.Text(); got != service.authority.Warning {
		t.Fatalf("development warning was not propagated exactly: %q", got)
	}
	if approvals := service.approvalCalls(); len(approvals) != 3 || approvals[0] || approvals[1] || !approvals[2] {
		t.Fatalf("development approval flow = %v", approvals)
	}
}

func stubExternalConsentDecline(t *testing.T, win *mainWindow) {
	t.Helper()
	originalConfirm := confirmDevelopmentLaunch
	t.Cleanup(func() { confirmDevelopmentLaunch = originalConfirm })
	confirmDevelopmentLaunch = func(parent *qt.QWidget) bool {
		if parent.UnsafePointer() != win.externalProgress.dialog.QWidget.UnsafePointer() {
			t.Fatal("external consent was not parented to progress")
		}
		return false
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
	if backend.trust.Mode != setupsvc.DevelopmentUnrestricted || backend.trust.ReleaseAuthenticated {
		t.Fatalf("bound trust=%+v", backend.trust)
	}
}

func TestProductionInstallAppliesDevelopmentTrustToLiveSetupService(t *testing.T) {
	t.Parallel()
	inner := setupsvc.New(app.SetupHooks())
	if inner.Trust.Mode != setupsvc.OfficialVerified || inner.Trust.ReleaseAuthenticated {
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
	if inner.Trust.Mode != setupsvc.DevelopmentUnrestricted || inner.Trust.ReleaseAuthenticated {
		t.Fatalf("live setup trust=%+v", inner.Trust)
	}
}
