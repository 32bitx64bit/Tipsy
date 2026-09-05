// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/runtime"
)

type Service struct {
	Source     Source
	Limits     Limits
	Trust      TrustPolicy
	RuntimeDir string

	inspect func(context.Context, []string) (*apk.Report, error)
	verify  func(context.Context, *apk.Report) error
	extract func(context.Context, []string, string) (*apk.ExtractResult, error)
	prepare func() error
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
		extract:    runtime.ExtractSetup,
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
	dir := s.runtimeDir()
	snapshot := InstallSnapshot{RuntimeDir: dir, Automatic: s.AutomaticAvailability(ctx)}
	if err := ctx.Err(); err != nil {
		return snapshot, setupError(ErrCanceled, "installation status", "operation canceled", err)
	}
	metaPath := filepath.Join(dir, "meta.json")
	st, err := os.Lstat(metaPath)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, setupError(ErrInstall, "installation status", "cannot read installed package metadata", err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || st.Size() > 4<<20 {
		return snapshot, setupError(ErrUnsafePath, "installation status", "installed package metadata is not a safe regular file", nil)
	}
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		return snapshot, setupError(ErrInstall, "installation status", "cannot read installed package metadata", err)
	}
	var meta apk.Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return snapshot, setupError(ErrInstall, "installation status", "installed package metadata is malformed", err)
	}
	lib := filepath.Join(dir, "lib", "x86_64", "libroblox.so")
	libInfo, err := os.Lstat(lib)
	if err != nil || !libInfo.Mode().IsRegular() || libInfo.Mode()&os.ModeSymlink != 0 {
		return snapshot, nil
	}
	snapshot.Installed = meta.PackageName == "com.roblox.client" && meta.VersionCode > 0
	snapshot.PackageName = meta.PackageName
	snapshot.VersionName = meta.VersionName
	snapshot.VersionCode = meta.VersionCode
	if snapshot.Installed {
		snapshot.Architectures = []string{"x86_64"}
	}
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
	parent := filepath.Dir(s.runtimeDir())
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
	if err := s.prepareFn()(); err != nil {
		return nil, setupError(ErrInstall, "preserve account data", "persistent account storage could not be prepared", err)
	}
	report(progress, PhaseCommitting, 0, 0, "Activating verified client atomically")
	if err := replaceRuntime(stagedRuntime, s.runtimeDir()); err != nil {
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

func replaceRuntime(staged, dest string) error {
	parent := filepath.Dir(dest)
	backup, err := os.MkdirTemp(parent, ".runtime-backup-*")
	if err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	hadOld := false
	if st, err := os.Lstat(dest); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return errors.New("runtime destination is not a real directory")
		}
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, dest); err != nil {
		if hadOld {
			_ = os.Rename(backup, dest)
		}
		return err
	}
	if hadOld {
		_ = os.RemoveAll(backup)
	}
	if d, err := os.Open(parent); err == nil {
		defer d.Close()
		return d.Sync()
	}
	return nil
}

func (s *Service) runtimeDir() string {
	if s != nil && s.RuntimeDir != "" {
		return s.RuntimeDir
	}
	return runtime.RuntimeDir()
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
	return runtime.ExtractSetup
}

func (s *Service) prepareFn() func() error {
	if s != nil && s.prepare != nil {
		return s.prepare
	}
	return runtime.PrepareAppStorageForSetup
}
