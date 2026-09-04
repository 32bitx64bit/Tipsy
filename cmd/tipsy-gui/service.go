package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/diagnostics"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	tipsyruntime "github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

// productionService is intentionally a thin adapter. It translates backend
// values into presentation-neutral GUI values and owns no package, settings,
// or compatibility policy.
type productionService struct {
	installer *setupsvc.Service
	settings  *clientsettings.Service
}

var _ guimodel.Service = (*productionService)(nil)

func newProductionService() guimodel.Service {
	return &productionService{installer: setupsvc.New(), settings: clientsettings.New()}
}

func (s *productionService) Snapshot(ctx context.Context) (guimodel.InstallSnapshot, error) {
	snapshot, err := s.installer.Snapshot(ctx)
	if err != nil {
		return guimodel.InstallSnapshot{}, err
	}
	version := snapshot.VersionName
	if version == "" && snapshot.VersionCode != 0 {
		version = fmt.Sprintf("Version %d", snapshot.VersionCode)
	}
	statusText := "No official Roblox client is installed."
	if snapshot.Installed {
		statusText = "Verified official Android x86-64 client ready."
	}
	return guimodel.InstallSnapshot{Installed: snapshot.Installed, Version: version, Status: statusText}, nil
}

func (s *productionService) AutomaticAvailability(ctx context.Context) guimodel.AutomaticAvailability {
	availability := s.installer.AutomaticAvailability(ctx)
	explanation := ""
	if availability.LegalURL != "" {
		explanation = "Source policy: " + availability.LegalURL
	}
	return guimodel.AutomaticAvailability{Available: availability.Available, SourceName: availability.Name, Explanation: explanation, Reason: availability.Reason}
}

func (s *productionService) Install(ctx context.Context, request guimodel.InstallRequest, progress func(guimodel.InstallProgress)) error {
	backendRequest := setupsvc.InstallRequest{Mode: setupsvc.InstallMode(request.Mode), LocalPaths: request.LocalPaths}
	_, err := s.installer.Install(ctx, backendRequest, func(update setupsvc.InstallProgress) {
		percent := phasePercent(update)
		progress(guimodel.InstallProgress{Phase: phaseTitle(update.Phase), Message: update.Message, Percent: percent})
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || setupsvc.ErrorKindOf(err) == setupsvc.ErrCanceled {
			return context.Canceled
		}
		return friendlySetupError(err)
	}
	return nil
}

func (s *productionService) Launch(ctx context.Context, started func()) error {
	return tipsyruntime.Launch(ctx, tipsyruntime.LaunchOptions{Started: started})
}

func (s *productionService) LoadSettings(ctx context.Context) (guimodel.Settings, error) {
	settings, err := s.settings.Load(ctx)
	if err != nil {
		return guimodel.Settings{}, err
	}
	return guiSettings(settings), nil
}

func (s *productionService) RendererOptions(context.Context) []guimodel.RendererOption {
	options := clientsettings.RendererOptions()
	result := make([]guimodel.RendererOption, 0, len(options))
	for _, option := range options {
		result = append(result, guimodel.RendererOption{
			Renderer:  guimodel.Renderer(option.Renderer),
			Available: option.Available,
			Reason:    option.Reason,
		})
	}
	return result
}

func (s *productionService) ApplySettings(ctx context.Context, settings guimodel.Settings) (guimodel.ApplyResult, error) {
	result, err := s.settings.Apply(ctx, backendSettings(settings))
	return guimodel.ApplyResult{RestartRequired: result.RestartRequired, FrameRateNote: result.FrameRateNote}, err
}

func (s *productionService) ResetSettings(ctx context.Context) (guimodel.Settings, error) {
	settings, err := s.settings.Reset(ctx)
	return guiSettings(settings), err
}

func (s *productionService) Doctor(ctx context.Context) (guimodel.DoctorSummary, error) {
	report := diagnostics.Doctor(ctx)
	if report == nil {
		return guimodel.DoctorSummary{}, fmt.Errorf("diagnostics returned no report")
	}
	checks := []guimodel.DoctorCheck{
		{
			Name:   "Architecture",
			Detail: report.System.Architecture,
			Status: status(report.System.Architecture == "x86_64" || report.System.Architecture == "amd64", guimodel.CheckBlocked),
			Remedy: "Tipsy requires an x86-64 Linux host.",
		},
		{
			Name:   "X11 display",
			Detail: displayDetail(report.Display.Session, report.Display.DISPLAY),
			Status: status(report.Display.DISPLAY != "" && report.Display.DISPLAY != "unset", guimodel.CheckBlocked),
			Remedy: "Start Tipsy inside an X11 desktop session and ensure DISPLAY is set.",
		},
		{
			Name:   "OpenGL / EGL",
			Detail: "EGL " + report.GPU.EGL + " · OpenGL ES " + report.GPU.GLES,
			Status: status(!strings.EqualFold(report.GPU.EGL, "not found"), guimodel.CheckBlocked),
			Remedy: "Install working Mesa or vendor EGL/OpenGL drivers.",
		},
		{
			Name:   "Roblox client",
			Detail: report.Roblox.RuntimeFiles,
			Status: status(report.Roblox.DataDirPresent, guimodel.CheckWarning),
			Remedy: "Use the setup assistant to choose an official APK or bundle.",
		},
	}
	ready := true
	for _, check := range checks {
		if check.Status == guimodel.CheckBlocked {
			ready = false
		}
	}
	return guimodel.DoctorSummary{Ready: ready, Checks: checks}, nil
}

func guiSettings(settings clientsettings.Settings) guimodel.Settings {
	result := guimodel.Settings{Renderer: guimodel.Renderer(settings.Renderer), FPSMode: guimodel.FPSMode(settings.FrameRate.Mode), FrameRate: settings.FrameRate.Limit}
	if result.Renderer == "" {
		result.Renderer = guimodel.RendererAuto
	}
	if result.FPSMode == "" {
		result.FPSMode = guimodel.FPSAuto
	}
	return result
}

func backendSettings(settings guimodel.Settings) clientsettings.Settings {
	limit := 0
	if settings.FPSMode == guimodel.FPSLimited {
		limit = settings.FrameRate
	}
	return clientsettings.Settings{
		Renderer: clientsettings.Renderer(settings.Renderer),
		FrameRate: clientsettings.FrameRate{
			Mode:  clientsettings.FrameRateMode(settings.FPSMode),
			Limit: limit,
		},
	}
}

func status(ok bool, failure guimodel.CheckStatus) guimodel.CheckStatus {
	if ok {
		return guimodel.CheckReady
	}
	return failure
}

func displayDetail(session, display string) string {
	if session == "" {
		session = "unknown session"
	}
	if display == "" {
		display = "DISPLAY unset"
	}
	return session + " · " + display
}

func phaseTitle(phase setupsvc.ProgressPhase) string {
	switch phase {
	case setupsvc.PhaseAcquiring:
		return "Downloading package"
	case setupsvc.PhaseValidating:
		return "Verifying package"
	case setupsvc.PhaseExtracting:
		return "Extracting client"
	case setupsvc.PhaseCommitting:
		return "Finishing installation"
	case setupsvc.PhaseComplete:
		return "Ready"
	default:
		return "Preparing"
	}
}

func phasePercent(progress setupsvc.InstallProgress) int {
	if progress.Total > 0 {
		value := int(progress.Completed * 100 / progress.Total)
		if value < 0 {
			return 0
		}
		if value > 100 {
			return 100
		}
		return value
	}
	switch progress.Phase {
	case setupsvc.PhaseAcquiring:
		return 20
	case setupsvc.PhaseValidating:
		return 48
	case setupsvc.PhaseExtracting:
		return 68
	case setupsvc.PhaseCommitting:
		return 92
	case setupsvc.PhaseComplete:
		return 100
	default:
		return 5
	}
}

func friendlySetupError(err error) error {
	switch setupsvc.ErrorKindOf(err) {
	case setupsvc.ErrWrongPackage:
		return fmt.Errorf("this is not an official Roblox client package: %w", err)
	case setupsvc.ErrMissingX8664:
		return fmt.Errorf("this package does not contain the required x86-64 client: %w", err)
	case setupsvc.ErrUntrustedSigner, setupsvc.ErrInvalidSignature:
		return fmt.Errorf("the package signature could not be verified; choose a package from your authorized source: %w", err)
	case setupsvc.ErrUnsupportedSplit:
		return fmt.Errorf("the split set is incomplete or unsupported; select the base APK and every required split: %w", err)
	case setupsvc.ErrInvalidArchive, setupsvc.ErrIntegrity:
		return fmt.Errorf("the package is damaged or failed integrity checks: %w", err)
	case setupsvc.ErrSourceUnavailable, setupsvc.ErrSourceTrust:
		return fmt.Errorf("automatic download is unavailable; choose an APK or bundle: %w", err)
	default:
		return err
	}
}
