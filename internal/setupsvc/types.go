// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package setupsvc provides the cancellable, UI-independent client installer.
package setupsvc

import (
	"context"
	"errors"
	"fmt"
)

type InstallMode string

const (
	InstallLocal     InstallMode = "local"
	InstallAutomatic InstallMode = "automatic"
)

type ProgressPhase string

const (
	PhasePreparing  ProgressPhase = "preparing"
	PhaseAcquiring  ProgressPhase = "acquiring"
	PhaseValidating ProgressPhase = "validating"
	PhaseExtracting ProgressPhase = "extracting"
	PhaseCommitting ProgressPhase = "committing"
	PhaseComplete   ProgressPhase = "complete"
)

type InstallRequest struct {
	Mode       InstallMode
	LocalPaths []string
}

type InstallProgress struct {
	Phase     ProgressPhase
	Completed int64
	Total     int64
	Message   string
}

type ProgressFunc func(InstallProgress)

type SourceAvailability struct {
	Available   bool
	Name        string
	Reason      string
	LegalURL    string
	Explanation string
}

// ReadinessState is deliberately narrower than runtime playability. Static
// package and generation validation can prove that the authenticated launch
// inputs are present and structurally compatible; only a live launch can
// prove rendering, networking, input, audio, or successful gameplay.
type ReadinessState string

const (
	ReadinessNotInstalled ReadinessState = "not-installed"
	ReadinessRejected     ReadinessState = "rejected"
	ReadinessLaunchInputs ReadinessState = "authenticated-launch-inputs"
)

type InstallSnapshot struct {
	Installed     bool
	Readiness     ReadinessState
	RuntimeDir    string
	PackageName   string
	VersionName   string
	VersionCode   int64
	Architectures []string
	Automatic     SourceAvailability
}

type InstallResult struct {
	Snapshot InstallSnapshot
}

type ErrorKind string

const (
	ErrInvalidRequest    ErrorKind = "invalid_request"
	ErrUnsafePath        ErrorKind = "unsafe_path"
	ErrSizeLimit         ErrorKind = "size_limit"
	ErrInvalidArchive    ErrorKind = "invalid_archive"
	ErrWrongPackage      ErrorKind = "wrong_package"
	ErrUnsupportedSplit  ErrorKind = "unsupported_split"
	ErrMissingX8664      ErrorKind = "missing_x86_64"
	ErrUntrustedSigner   ErrorKind = "untrusted_signer"
	ErrInvalidSignature  ErrorKind = "invalid_signature"
	ErrCompatibility     ErrorKind = "compatibility"
	ErrDowngrade         ErrorKind = "downgrade"
	ErrPolicy            ErrorKind = "policy"
	ErrSourceUnavailable ErrorKind = "source_unavailable"
	ErrSourceTrust       ErrorKind = "source_trust"
	ErrNetwork           ErrorKind = "network"
	ErrIntegrity         ErrorKind = "integrity"
	ErrInstall           ErrorKind = "install"
	ErrCanceled          ErrorKind = "canceled"
)

type Error struct {
	Kind   ErrorKind
	Op     string
	Detail string
	Err    error
}

func (e *Error) Error() string {
	if e == nil {
		return "setup error"
	}
	msg := string(e.Kind)
	if e.Op != "" {
		msg = e.Op + ": " + msg
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func setupError(kind ErrorKind, op, detail string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: ErrCanceled, Op: op, Detail: "operation canceled", Err: err}
	}
	return &Error{Kind: kind, Op: op, Detail: detail, Err: err}
}

func ErrorKindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

func report(progress ProgressFunc, phase ProgressPhase, completed, total int64, message string) {
	if progress != nil {
		progress(InstallProgress{Phase: phase, Completed: completed, Total: total, Message: message})
	}
}

func validMode(mode InstallMode) bool { return mode == InstallLocal || mode == InstallAutomatic }

func checkContext(ctx context.Context, op string) error {
	if ctx == nil {
		return fmt.Errorf("%s: nil context", op)
	}
	if err := ctx.Err(); err != nil {
		return setupError(ErrCanceled, op, "operation canceled", err)
	}
	return nil
}
