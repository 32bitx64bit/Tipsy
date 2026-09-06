// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

const DevelopmentUnrestrictedWarning = "WARNING: DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session."

var ErrAuthorizedLaunchSessionClosed = errors.New("authorized launch session is closed")

// LaunchSessionError preserves a completed authority decision when generation
// opening fails, so frontends can still present the mandatory warning without
// rerunning release or policy verification.
type LaunchSessionError struct {
	Authority LaunchAuthorityState
	Err       error
}

func (e *LaunchSessionError) Error() string {
	if e == nil || e.Err == nil {
		return "authorized launch session failed"
	}
	return e.Err.Error()
}

func (e *LaunchSessionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// LaunchAuthorityState is the presentation-safe result of the app's single
// authority decision. Warning is non-empty for every development session.
type LaunchAuthorityState struct {
	Mode                       setupsvc.AuthorizationMode
	Warning                    string
	DevelopmentConsentRequired bool
}

// AuthorizedLaunchSession owns the verified generation for a complete runtime
// invocation. Close waits for an active Launch call before releasing it.
type AuthorizedLaunchSession struct {
	mu         sync.Mutex
	state      LaunchAuthorityState
	generation launchGeneration
	launch     func(context.Context, runtime.LaunchOptions) error
	closed     bool
}

// ResolveLaunchAuthority exposes the app's single authority decision without
// opening a generation. OpenLaunchSession always repeats this verification at
// the security-sensitive launch boundary.
func ResolveLaunchAuthority(ctx context.Context, approveDevelopment bool) (LaunchAuthorityState, error) {
	state, _, err := resolveLaunchAuthorityState(ctx, approveDevelopment, defaultAuthorityDependencies())
	return state, err
}

// OpenLaunchSession resolves the selected authority, repairs or opens its
// generation, and returns the only app-level launch capability.
func OpenLaunchSession(ctx context.Context, approveDevelopment bool) (*AuthorizedLaunchSession, error) {
	return openAuthorizedLaunchSession(ctx, approveDevelopment, defaultAuthorityDependencies(), defaultGenerationDependencies())
}

func openAuthorizedLaunchSession(ctx context.Context, approveDevelopment bool, authorityDeps authorityDependencies, generationDeps generationDependencies) (*AuthorizedLaunchSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if generationDeps.launch == nil {
		return nil, fmt.Errorf("runtime launch integration is incomplete")
	}
	state, authority, err := resolveLaunchAuthorityState(ctx, approveDevelopment, authorityDeps)
	if err != nil {
		return nil, &LaunchSessionError{Authority: state, Err: err}
	}
	generation, err := openOrRepairGeneration(ctx, authority, generationDeps)
	if err != nil {
		return nil, &LaunchSessionError{Authority: state, Err: err}
	}
	return &AuthorizedLaunchSession{
		state: state, generation: generation, launch: generationDeps.launch,
	}, nil
}

func resolveLaunchAuthorityState(ctx context.Context, approveDevelopment bool, deps authorityDependencies) (LaunchAuthorityState, authorityResolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := loadConfigWithDevelopmentConsent(approveDevelopment)
	if err != nil {
		return LaunchAuthorityState{}, authorityResolution{}, err
	}
	installedVersion := int64(0)
	if snapshot, snapshotErr := setupsvc.New().Snapshot(ctx); snapshotErr == nil {
		installedVersion = snapshot.VersionCode
	}
	authority, err := resolveAuthority(ctx, cfg, installedVersion, deps)
	if err != nil {
		return LaunchAuthorityState{DevelopmentConsentRequired: errors.Is(err, ErrDevelopmentConsentRequired)}, authorityResolution{}, err
	}
	state := LaunchAuthorityState{Mode: authority.Mode}
	if authority.Mode == setupsvc.DevelopmentUnrestricted {
		state.Warning = DevelopmentUnrestrictedWarning
	}
	return state, authority, nil
}

// State returns a copy suitable for CLI or GUI presentation.
func (s *AuthorizedLaunchSession) State() LaunchAuthorityState {
	if s == nil {
		return LaunchAuthorityState{}
	}
	return s.state
}

// Launch injects the session-owned generation and retains it until runtime
// returns. A caller-supplied AuthorizedGeneration is intentionally ignored.
func (s *AuthorizedLaunchSession) Launch(ctx context.Context, options runtime.LaunchOptions) error {
	if s == nil {
		return ErrAuthorizedLaunchSessionClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.generation == nil || s.launch == nil {
		return ErrAuthorizedLaunchSessionClosed
	}
	options.AuthorizedGeneration = s.generation
	return s.launch(ctx, options)
}

// Close releases the generation after any active launch returns. It is safe to
// call repeatedly.
func (s *AuthorizedLaunchSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	generation := s.generation
	s.generation = nil
	if generation == nil {
		return nil
	}
	return generation.Close()
}
