// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

func TestAuthorizedLaunchSessionPersistsExplicitStateAndOwnsLifetime(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	retainedDir := filepath.Join(generationStoreRoot(), "apks", "sha256")
	if err := os.MkdirAll(retainedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(retainedDir, strings.Repeat("a", 64)+".apk")
	if err := os.WriteFile(retained, []byte("signed-fixture"), 0o400); err != nil {
		t.Fatal(err)
	}

	var closeCalls atomic.Int32
	generation := &sessionTestGeneration{
		authorization: setupsvc.Authorization{Mode: setupsvc.DevelopmentUnrestricted},
		closeCalls:    &closeCalls,
	}
	openCalls := 0
	launchEntered := make(chan struct{})
	releaseLaunch := make(chan struct{})
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			openCalls++
			if openCalls == 1 {
				return nil, os.ErrNotExist
			}
			return generation, nil
		},
		derive: func(_ context.Context, paths []string, _ string, trust setupsvc.TrustPolicy) (string, error) {
			if len(paths) != 1 || paths[0] != retained || trust.Mode != setupsvc.DevelopmentUnrestricted {
				t.Fatalf("derive inputs = %v, %+v", paths, trust)
			}
			return strings.Repeat("b", 64), nil
		},
		launch: func(_ context.Context, options runtime.LaunchOptions) error {
			if options.AuthorizedGeneration != generation {
				return fmt.Errorf("launch generation = %#v", options.AuthorizedGeneration)
			}
			close(launchEntered)
			<-releaseLaunch
			if closeCalls.Load() != 0 {
				return fmt.Errorf("generation closed while runtime was active")
			}
			return nil
		},
	}
	session, err := openAuthorizedLaunchSession(context.Background(), true, defaultAuthorityDependencies(), deps)
	if err != nil {
		t.Fatal(err)
	}
	state := session.State()
	if state.Mode != setupsvc.DevelopmentUnrestricted || state.Warning != DevelopmentUnrestrictedWarning {
		t.Fatalf("state = %+v", state)
	}

	launchDone := make(chan error, 1)
	foreign := &sessionTestGeneration{}
	go func() {
		launchDone <- session.Launch(context.Background(), runtime.LaunchOptions{AuthorizedGeneration: foreign})
	}()
	<-launchEntered
	closeDone := make(chan error, 1)
	go func() { closeDone <- session.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned during active launch: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseLaunch)
	if err := <-launchDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("generation close calls = %d", closeCalls.Load())
	}
	if err := session.Close(); err != nil || closeCalls.Load() != 1 {
		t.Fatalf("idempotent close = %v, calls=%d", err, closeCalls.Load())
	}
	if err := session.Launch(context.Background(), runtime.LaunchOptions{}); !errors.Is(err, ErrAuthorizedLaunchSessionClosed) {
		t.Fatalf("launch after close = %v", err)
	}
}

func TestAuthorizedLaunchSessionMissingConsentFailsBeforeGeneration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	opened := false
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			opened = true
			return nil, nil
		},
		derive: func(context.Context, []string, string, setupsvc.TrustPolicy) (string, error) {
			return "", nil
		},
		launch: func(context.Context, runtime.LaunchOptions) error { return nil },
	}
	_, err := openAuthorizedLaunchSession(context.Background(), false, defaultAuthorityDependencies(), deps)
	var sessionErr *LaunchSessionError
	if !errors.Is(err, ErrDevelopmentConsentRequired) || !errors.As(err, &sessionErr) || !sessionErr.Authority.DevelopmentConsentRequired || opened {
		t.Fatalf("missing consent = %v, generation opened=%v", err, opened)
	}
	state, err := ResolveLaunchAuthority(context.Background(), false)
	if !errors.Is(err, ErrDevelopmentConsentRequired) || !state.DevelopmentConsentRequired || state.Mode != "" || state.Warning != "" {
		t.Fatalf("missing-consent state = %+v, %v", state, err)
	}
}

func TestResolveLaunchAuthorityPersistsExplicitDevelopmentState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	state, err := ResolveLaunchAuthority(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != setupsvc.DevelopmentUnrestricted || state.Warning != DevelopmentUnrestrictedWarning || state.DevelopmentConsentRequired {
		t.Fatalf("development state = %+v", state)
	}
	state, err = ResolveLaunchAuthority(context.Background(), false)
	if err != nil || state.Mode != setupsvc.DevelopmentUnrestricted || state.Warning == "" {
		t.Fatalf("persisted development state = %+v, %v", state, err)
	}
}

func TestResolveLaunchAuthorityReturnsOfficialStateForIdentifiedAppImage(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	deps := authorityDependencies{
		identifyOfficialRelease: func(context.Context) error { return nil },
	}
	state, authority, err := resolveLaunchAuthorityState(context.Background(), false, deps)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != setupsvc.OfficialVerified || state.Warning != "" || state.DevelopmentConsentRequired || authority.Mode != setupsvc.OfficialVerified {
		t.Fatalf("official state = %+v, authority=%+v", state, authority)
	}
}

func TestAuthorizedLaunchSessionClosesRejectedGeneration(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	var closeCalls atomic.Int32
	wrong := &sessionTestGeneration{
		authorization: setupsvc.Authorization{Mode: setupsvc.OfficialVerified, PolicyAuthorized: true},
		closeCalls:    &closeCalls,
	}
	deps := generationDependencies{
		open: func(context.Context, integrity.Store, setupsvc.TrustPolicy) (launchGeneration, error) {
			return wrong, nil
		},
		derive: func(context.Context, []string, string, setupsvc.TrustPolicy) (string, error) {
			return "", nil
		},
		launch: func(context.Context, runtime.LaunchOptions) error { return nil },
	}
	_, err := openAuthorizedLaunchSession(context.Background(), true, defaultAuthorityDependencies(), deps)
	var sessionErr *LaunchSessionError
	if err == nil || !errors.As(err, &sessionErr) || sessionErr.Authority.Mode != setupsvc.DevelopmentUnrestricted || sessionErr.Authority.Warning == "" || closeCalls.Load() != 1 {
		t.Fatalf("rejected generation = %v, close calls=%d", err, closeCalls.Load())
	}
}

type sessionTestGeneration struct {
	authorization setupsvc.Authorization
	closeCalls    *atomic.Int32
}

func (g *sessionTestGeneration) NativeDescriptorSet(context.Context) (*integrity.NativeDescriptorSet, error) {
	return nil, nil
}

func (g *sessionTestGeneration) AuthorizedRuntimeFiles(context.Context) (runtime.AuthorizedRuntimeFiles, error) {
	return runtime.AuthorizedRuntimeFiles{}, nil
}

func (g *sessionTestGeneration) Authorization() setupsvc.Authorization { return g.authorization }

func (g *sessionTestGeneration) Close() error {
	if g.closeCalls != nil {
		g.closeCalls.Add(1)
	}
	return nil
}
