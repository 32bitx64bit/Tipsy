// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/runtime"
)

type Service struct {
	Source              Source
	Limits              Limits
	Trust               TrustPolicy
	RuntimeDir          string
	GenerationStoreRoot string

	inspect         func(context.Context, []string) (*apk.Report, error)
	verify          func(context.Context, *apk.Report) error
	extract         func(context.Context, []string, string) (*apk.ExtractResult, error)
	prepare         func() error
	authorizeStaged func(context.Context, integrity.Store, string, TrustPolicy) error
	compatibility   func(context.Context, string) error
	snapshot        func(context.Context, string, TrustPolicy) (InstallSnapshot, error)
}

func New() *Service {
	return NewWithSource(&APKPureSource{})
}

func NewWithSource(source Source) *Service {
	return &Service{
		Source:     source,
		Limits:     DefaultLimits(),
		Trust:      OfficialTrustPolicy(),
		RuntimeDir: runtime.RuntimeDir(),
		inspect:    apk.Inspect,
		verify:     apk.VerifyReportSignatures,
		extract:    apk.Extract,
		prepare:    runtime.PrepareAppStorageForSetup,
	}
}

func (s *Service) AutomaticAvailability(ctx context.Context) SourceAvailability {
	if s == nil || s.Source == nil {
		return SourceAvailability{Available: false, Reason: "No trusted automatic package source is configured."}
	}
	return s.Source.Availability(ctx)
}

func (s *Service) Snapshot(ctx context.Context) (InstallSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	storeRoot := s.generationStoreRoot()
	snapshot := InstallSnapshot{RuntimeDir: storeRoot, Readiness: ReadinessNotInstalled, Automatic: s.AutomaticAvailability(ctx)}
	if err := ctx.Err(); err != nil {
		return snapshot, setupError(ErrCanceled, "installation status", "operation canceled", err)
	}
	verified, err := s.snapshotFn()(ctx, storeRoot, s.trust())
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		kind := ErrorKindOf(err)
		if kind == "" {
			kind = ErrIntegrity
		}
		if kind != ErrCanceled {
			snapshot.Readiness = ReadinessRejected
			logging.Logger(logging.CatAPK).Error("installed Roblox client readiness verification failed",
				"kind", string(kind), "readiness", string(snapshot.Readiness))
		}
		return snapshot, setupError(kind, "installation status", snapshotFailureDetail(kind), err)
	}
	if !verified.Installed || verified.Readiness != ReadinessLaunchInputs {
		snapshot.Readiness = ReadinessRejected
		logging.Logger(logging.CatAPK).Error("installed Roblox client readiness verification returned an incomplete verdict",
			"kind", string(ErrIntegrity), "readiness", string(snapshot.Readiness))
		return snapshot, setupError(ErrIntegrity, "installation status", "active client verification returned an incomplete verdict", nil)
	}
	verified.Automatic = snapshot.Automatic
	snapshot = verified
	return snapshot, nil
}

func (s *Service) Install(ctx context.Context, req InstallRequest, progress ProgressFunc) (*InstallResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := checkContext(ctx, "setup"); err != nil {
		return nil, err
	}
	if !validMode(req.Mode) {
		return nil, setupError(ErrInvalidRequest, "setup", "installation mode must be local or automatic", nil)
	}
	if req.Mode == InstallAutomatic && len(req.LocalPaths) != 0 {
		return nil, setupError(ErrInvalidRequest, "setup", "automatic setup does not accept local paths", nil)
	}
	if req.Mode == InstallLocal && len(req.LocalPaths) == 0 {
		return nil, setupError(ErrInvalidRequest, "setup", "choose at least one official package file", nil)
	}
	if req.Mode == InstallAutomatic && (s.Source == nil || !s.AutomaticAvailability(ctx).Available) {
		return nil, setupError(ErrSourceUnavailable, "automatic setup", s.AutomaticAvailability(ctx).Reason, nil)
	}
	report(progress, PhasePreparing, 0, 0, "Preparing private setup workspace")
	parent := filepath.Dir(s.generationStoreRoot())
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, setupError(ErrInstall, "setup", "cannot create the Tipsy data directory", err)
	}
	stage, err := os.MkdirTemp(parent, ".setup-stage-*")
	if err != nil {
		return nil, setupError(ErrInstall, "setup", "cannot create private setup workspace", err)
	}
	_ = os.Chmod(stage, 0o700)
	defer os.RemoveAll(stage)

	limits := s.limits()
	var selected []string
	if req.Mode == InstallAutomatic {
		acquired, err := s.Source.Acquire(ctx, filepath.Join(stage, "acquired"), progress)
		if err != nil {
			return nil, err
		}
		selected, err = copyAndValidateLocal(ctx, acquired, filepath.Join(stage, "frozen"), limits, progress)
		if err != nil {
			return nil, err
		}
	} else {
		selected, err = copyAndValidateLocal(ctx, req.LocalPaths, filepath.Join(stage, "frozen"), limits, progress)
		if err != nil {
			return nil, err
		}
	}

	report(progress, PhaseValidating, 0, 0, "Keeping official x86-64 Roblox package files")
	selected, err = keepX86PackageFiles(selected, filepath.Join(stage, "filtered"), "validate package")
	if err != nil {
		return nil, err
	}

	report(progress, PhaseValidating, 0, 0, "Verifying Roblox package, architecture, splits, and signature")
	rep, err := s.inspectFn()(ctx, selected)
	if err != nil {
		return nil, setupError(ErrInvalidArchive, "validate package", "package inspection failed", err)
	}
	if err := s.verifyFn()(ctx, rep); err != nil {
		return nil, setupError(ErrInvalidSignature, "validate package", "APK cryptographic signature verification failed", err)
	}
	if err := ValidateReport(rep, s.trust()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, setupError(ErrCanceled, "setup", "operation canceled", err)
	}

	stagedRuntime := filepath.Join(stage, "runtime")
	report(progress, PhaseExtracting, 0, 0, "Extracting verified x86_64 client files")
	if _, err := s.extractFn()(ctx, selected, stagedRuntime); err != nil {
		return nil, setupError(ErrInstall, "extract package", "verified package extraction failed", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, setupError(ErrCanceled, "setup", "operation canceled", err)
	}
	report(progress, PhaseExtracting, 0, 0, "Checking loader compatibility without pinning a Roblox version")
	if err := s.compatibilityFn()(ctx, stagedRuntime); err != nil {
		kind := ErrorKindOf(err)
		if kind == "" {
			kind = ErrCompatibility
		}
		if kind != ErrCanceled {
			logging.Logger(logging.CatAPK).Error("Roblox client loader compatibility validation failed",
				"kind", string(kind))
		}
		if ErrorKindOf(err) != "" {
			return nil, err
		}
		return nil, setupError(ErrCompatibility, "check client compatibility", "the verified client does not satisfy the loader's named startup contract", err)
	}
	if err := s.prepareFn()(); err != nil {
		return nil, setupError(ErrInstall, "preserve account data", "persistent account storage could not be prepared", err)
	}
	store := integrity.Store{Root: s.generationStoreRoot()}
	id, err := PrepareGeneration(ctx, stagedRuntime, store.Root, rep, s.trust())
	if err != nil {
		return nil, setupError(ErrIntegrity, "stage generation", "extracted client provenance could not be authenticated", err)
	}
	if err := s.authorizeStagedFn()(ctx, store, id, s.trust()); err != nil {
		return nil, setupError(ErrIntegrity, "stage generation", "staged generation failed retained APK authorization", err)
	}
	report(progress, PhaseCommitting, 0, 0, "Activating verified client atomically")
	if err := store.Activate(ctx, id); err != nil {
		return nil, setupError(ErrInstall, "activate package", "could not activate the verified client", err)
	}
	// Activation is the commit point. A cancellation racing after it must not
	// report failure for an installation that is already active.
	snapshot, err := s.Snapshot(context.Background())
	if err != nil {
		return nil, err
	}
	report(progress, PhaseComplete, 1, 1, "Setup complete")
	return &InstallResult{Snapshot: snapshot}, nil
}

func (s *Service) runtimeDir() string {
	if s != nil && s.RuntimeDir != "" {
		return s.RuntimeDir
	}
	return runtime.RuntimeDir()
}

func (s *Service) generationStoreRoot() string {
	if s != nil && s.GenerationStoreRoot != "" {
		return s.GenerationStoreRoot
	}
	return GenerationStoreDir(s.runtimeDir())
}

func (s *Service) limits() Limits {
	if s != nil && s.Limits.MaxFiles > 0 {
		return s.Limits
	}
	return DefaultLimits()
}

func (s *Service) trust() TrustPolicy {
	if s != nil && s.Trust.PackageName != "" {
		return s.Trust
	}
	return OfficialTrustPolicy()
}

func (s *Service) inspectFn() func(context.Context, []string) (*apk.Report, error) {
	if s != nil && s.inspect != nil {
		return s.inspect
	}
	return apk.Inspect
}

func (s *Service) verifyFn() func(context.Context, *apk.Report) error {
	if s != nil && s.verify != nil {
		return s.verify
	}
	return apk.VerifyReportSignatures
}

func (s *Service) extractFn() func(context.Context, []string, string) (*apk.ExtractResult, error) {
	if s != nil && s.extract != nil {
		return s.extract
	}
	return apk.Extract
}

func (s *Service) prepareFn() func() error {
	if s != nil && s.prepare != nil {
		return s.prepare
	}
	return runtime.PrepareAppStorageForSetup
}

func (s *Service) authorizeStagedFn() func(context.Context, integrity.Store, string, TrustPolicy) error {
	if s != nil && s.authorizeStaged != nil {
		return s.authorizeStaged
	}
	return func(ctx context.Context, store integrity.Store, id string, trust TrustPolicy) error {
		generation, err := integrity.OpenGeneration(ctx, store.Root, id)
		if err != nil {
			return err
		}
		authorized, err := authorizeGeneration(ctx, generation, trust, nil)
		if err != nil {
			_ = generation.Close()
			return err
		}
		if authorized.ID() != id {
			_ = authorized.Close()
			return fmt.Errorf("setup: staged authorization changed generation identity")
		}
		return authorized.Close()
	}
}

func (s *Service) compatibilityFn() func(context.Context, string) error {
	if s != nil && s.compatibility != nil {
		return s.compatibility
	}
	return ValidateExtractedClientCompatibility
}

func (s *Service) snapshotFn() func(context.Context, string, TrustPolicy) (InstallSnapshot, error) {
	if s != nil && s.snapshot != nil {
		return s.snapshot
	}
	return verifyInstalledClientSnapshot
}

func snapshotFailureDetail(kind ErrorKind) string {
	switch kind {
	case ErrWrongPackage:
		return "the active package is not the official Roblox client"
	case ErrInvalidSignature, ErrUntrustedSigner:
		return "the retained Roblox package signing identity could not be authenticated"
	case ErrMissingX8664:
		return "the active package does not contain a verified x86-64 libroblox payload"
	case ErrCompatibility:
		return "the active client does not satisfy the loader's named startup contract"
	case ErrCanceled:
		return "operation canceled"
	default:
		return "the active client failed authenticated package or generation integrity verification"
	}
}
