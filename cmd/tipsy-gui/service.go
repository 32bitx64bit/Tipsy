package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/app"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/diagnostics"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
	tipsyruntime "github.com/tipsy-linux/tipsy/internal/runtime"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

// productionService is intentionally a thin adapter. It translates backend
// values into presentation-neutral GUI values and owns no package, settings,
// or compatibility policy.
type productionService struct {
	installer    setupBackend
	packageTrust func(context.Context) (setupsvc.TrustPolicy, error)
	settings     *clientsettings.Service

	launchMu       sync.Mutex
	launchBackend  authorizedLaunchBackend
	preparedLaunch authorizedLaunchSession
	launchActive   bool
}

type packageTrustReceiver interface {
	SetPackageTrust(setupsvc.TrustPolicy)
}

type setupBackend interface {
	Snapshot(context.Context) (setupsvc.InstallSnapshot, error)
	AutomaticAvailability(context.Context) setupsvc.SourceAvailability
	Install(context.Context, setupsvc.InstallRequest, setupsvc.ProgressFunc) (*setupsvc.InstallResult, error)
}

var _ guimodel.Service = (*productionService)(nil)

type authorizedLaunchSession interface {
	State() app.LaunchAuthorityState
	Launch(context.Context, tipsyruntime.LaunchOptions) error
	Close() error
}

type authorizedLaunchBackend struct {
	open func(context.Context, bool) (authorizedLaunchSession, error)
}

func newProductionService() guimodel.Service {
	installer := setupsvc.New()
	return &productionService{
		installer: installer,
		packageTrust: func(ctx context.Context) (setupsvc.TrustPolicy, error) {
			return app.PackageAuthorizationTrust(ctx, false)
		},
		settings: clientsettings.New(),
		launchBackend: authorizedLaunchBackend{open: func(ctx context.Context, approveDevelopment bool) (authorizedLaunchSession, error) {
			return app.OpenLaunchSession(ctx, approveDevelopment)
		}},
	}
}

func (s *productionService) Snapshot(ctx context.Context) (guimodel.InstallSnapshot, error) {
	if s == nil || s.installer == nil {
		return guimodel.InstallSnapshot{}, fmt.Errorf("installation verification service is unavailable")
	}
	snapshot, err := s.installer.Snapshot(ctx)
	if err != nil {
		kind := setupsvc.ErrorKindOf(err)
		if !errors.Is(err, context.Canceled) && kind != setupsvc.ErrCanceled && (snapshot.Readiness == setupsvc.ReadinessRejected || kind != "") {
			return rejectedInstallSnapshot(kind), nil
		}
		return guimodel.InstallSnapshot{}, err
	}
	return guiInstallSnapshot(snapshot), nil
}

const (
	guiNotInstalledStatus = "Not installed — no authenticated official Roblox client is active. Run Setup with an official Android x86-64 package."
	guiLaunchInputsStatus = "Authenticated launch inputs verified. Play can attempt a live launch; Home rendering, input, audio, and gameplay are separate runtime results."
)

func guiInstallSnapshot(snapshot setupsvc.InstallSnapshot) guimodel.InstallSnapshot {
	version := snapshot.VersionName
	if version == "" && snapshot.VersionCode != 0 {
		version = fmt.Sprintf("Version %d", snapshot.VersionCode)
	}
	switch {
	case snapshot.Readiness == setupsvc.ReadinessLaunchInputs && snapshot.Installed:
		if version == "" {
			version = "Authenticated client"
		}
		return guimodel.InstallSnapshot{Installed: true, Readiness: setupsvc.ReadinessLaunchInputs, Version: version, Status: guiLaunchInputsStatus}
	case snapshot.Readiness == setupsvc.ReadinessRejected:
		return rejectedInstallSnapshot("")
	case snapshot.Readiness == setupsvc.ReadinessNotInstalled && !snapshot.Installed:
		return guimodel.InstallSnapshot{Readiness: setupsvc.ReadinessNotInstalled, Version: "Not installed", Status: guiNotInstalledStatus}
	default:
		// A partial or contradictory positive verdict is never launch authority.
		return rejectedInstallSnapshot(setupsvc.ErrIntegrity)
	}
}

func rejectedInstallSnapshot(kind setupsvc.ErrorKind) guimodel.InstallSnapshot {
	return guimodel.InstallSnapshot{
		Readiness: setupsvc.ReadinessRejected,
		Version:   "Verification rejected",
		Status:    rejectedReadinessMessage(kind),
	}
}

func rejectedReadinessMessage(kind setupsvc.ErrorKind) string {
	switch kind {
	case setupsvc.ErrCompatibility:
		return "Rejected — the client's native interface is not compatible with this Tipsy build. Update Tipsy or reinstall a supported official client; launch remains disabled."
	case setupsvc.ErrWrongPackage, setupsvc.ErrUntrustedSigner, setupsvc.ErrInvalidSignature:
		return "Rejected — the installed package could not be authenticated as an authorized official Roblox client. Reinstall it from an authorized source; launch remains disabled."
	case setupsvc.ErrMissingX8664, setupsvc.ErrUnsupportedSplit:
		return "Rejected — the installed package is incomplete or lacks the required x86-64 client. Reinstall the complete official package set; launch remains disabled."
	case setupsvc.ErrInvalidArchive, setupsvc.ErrIntegrity, setupsvc.ErrPolicy, setupsvc.ErrDowngrade:
		return "Rejected — the active client failed integrity or authorization revalidation. Open Setup to repair it from an authorized official package; launch remains disabled."
	default:
		return "Rejected — the active client did not pass launch-input verification. Open Setup to repair it from an authorized official package; launch remains disabled."
	}
}

func (s *productionService) AutomaticAvailability(ctx context.Context) guimodel.AutomaticAvailability {
	availability := s.installer.AutomaticAvailability(ctx)
	explanation := availability.Explanation
	if explanation == "" && availability.LegalURL != "" {
		explanation = "Source policy: " + availability.LegalURL
	}
	return guimodel.AutomaticAvailability{Available: availability.Available, SourceName: availability.Name, Explanation: explanation, Reason: availability.Reason}
}

func (s *productionService) bindPackageTrust(ctx context.Context) error {
	if s == nil || s.packageTrust == nil {
		return nil
	}
	trust, err := s.packageTrust(ctx)
	if err != nil {
		return err
	}
	switch installer := s.installer.(type) {
	case *setupsvc.Service:
		installer.Trust = trust
	case packageTrustReceiver:
		installer.SetPackageTrust(trust)
	}
	return nil
}

func (s *productionService) Install(ctx context.Context, request guimodel.InstallRequest, progress func(guimodel.InstallProgress)) error {
	if err := s.bindPackageTrust(ctx); err != nil {
		if errors.Is(err, context.Canceled) || setupsvc.ErrorKindOf(err) == setupsvc.ErrCanceled {
			return context.Canceled
		}
		return err
	}
	backendRequest := setupsvc.InstallRequest{Mode: setupsvc.InstallMode(request.Mode), LocalPaths: request.LocalPaths}
	result, err := s.installer.Install(ctx, backendRequest, func(update setupsvc.InstallProgress) {
		percent := phasePercent(update)
		progress(guimodel.InstallProgress{Phase: phaseTitle(update.Phase), Message: update.Message, Percent: percent})
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || setupsvc.ErrorKindOf(err) == setupsvc.ErrCanceled {
			return context.Canceled
		}
		return friendlySetupError(err)
	}
	if result == nil || !result.Snapshot.Installed || result.Snapshot.Readiness != setupsvc.ReadinessLaunchInputs {
		return errors.New("installation finished without authenticated launch inputs; open Setup to retry with an authorized official package")
	}
	return nil
}

// PrepareLaunch resolves authority through internal/app and retains exactly one
// authenticated generation for the next Launch call. It never infers official
// authority from missing release metadata.
func (s *productionService) PrepareLaunch(ctx context.Context, approveDevelopment bool) (guimodel.LaunchAuthority, error) {
	if s == nil || s.launchBackend.open == nil {
		return guimodel.LaunchAuthority{}, fmt.Errorf("authorized GUI launch integration is unavailable")
	}
	session, err := s.launchBackend.open(ctx, approveDevelopment)
	if err != nil {
		state := app.LaunchAuthorityState{}
		var sessionErr *app.LaunchSessionError
		if errors.As(err, &sessionErr) {
			state = sessionErr.Authority
		}
		return guiLaunchAuthority(state), err
	}
	if session == nil {
		return guimodel.LaunchAuthority{}, fmt.Errorf("authorized GUI launch session is nil")
	}
	state := guiLaunchAuthority(session.State())
	if state.DevelopmentConsentRequired {
		_ = session.Close()
		return state, fmt.Errorf("authorized GUI launch session returned incomplete authority")
	}
	switch state.Mode {
	case string(setupsvc.DevelopmentUnrestricted):
		if state.Warning != app.DevelopmentUnrestrictedWarning {
			_ = session.Close()
			return state, fmt.Errorf("development GUI launch omitted its required warning")
		}
	case string(setupsvc.OfficialVerified):
		if state.Warning != "" {
			_ = session.Close()
			return state, fmt.Errorf("official GUI launch returned an unexpected warning")
		}
	default:
		_ = session.Close()
		return state, fmt.Errorf("authorized GUI launch session returned an unknown authority mode")
	}

	s.launchMu.Lock()
	if s.launchActive {
		s.launchMu.Unlock()
		_ = session.Close()
		return state, fmt.Errorf("Roblox is already running")
	}
	previous := s.preparedLaunch
	s.preparedLaunch = session
	s.launchMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return state, nil
}

func guiLaunchAuthority(state app.LaunchAuthorityState) guimodel.LaunchAuthority {
	return guimodel.LaunchAuthority{
		Mode:                       string(state.Mode),
		Warning:                    state.Warning,
		DevelopmentConsentRequired: state.DevelopmentConsentRequired,
	}
}

func (s *productionService) Launch(ctx context.Context, req guimodel.LaunchRequest, started func()) (result error) {
	launchReq, err := rbxuri.Parse(req.URI)
	if err != nil {
		s.launchMu.Lock()
		prepared := s.preparedLaunch
		s.preparedLaunch = nil
		s.launchMu.Unlock()
		if prepared != nil {
			return errors.Join(err, prepared.Close())
		}
		return err
	}
	s.launchMu.Lock()
	session := s.preparedLaunch
	if session == nil || s.launchActive {
		s.launchMu.Unlock()
		return fmt.Errorf("authorized GUI launch session is required")
	}
	s.preparedLaunch = nil
	s.launchActive = true
	s.launchMu.Unlock()

	defer func() {
		result = errors.Join(result, session.Close())
		s.launchMu.Lock()
		s.launchActive = false
		s.launchMu.Unlock()
	}()
	return session.Launch(ctx, tipsyruntime.LaunchOptions{Started: started, Request: launchReq})
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
	result := guimodel.Settings{Renderer: guimodel.Renderer(settings.Renderer), FPSMode: guimodel.FPSMode(settings.FrameRate.Mode), FrameRate: settings.FrameRate.Limit, VSync: settings.VSync, LowTextureMode: settings.LowTextureMode, Display: guimodel.NormalizeDisplay(settings.Display), StartFullscreen: settings.StartFullscreen, DiscordRichPresence: settings.DiscordRichPresence, DiscordJoinButton: settings.DiscordJoinButton}
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
		Renderer:            clientsettings.Renderer(settings.Renderer),
		VSync:               settings.VSync,
		LowTextureMode:      settings.LowTextureMode,
		Display:             clientsettings.NormalizeDisplay(settings.Display),
		StartFullscreen:     settings.StartFullscreen,
		DiscordRichPresence: settings.DiscordRichPresence,
		DiscordJoinButton:   settings.DiscordJoinButton,
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
		return "Launch inputs authenticated"
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
		var typed *setupsvc.Error
		if errors.As(err, &typed) && strings.Contains(strings.ToLower(typed.Detail), "installer") {
			return fmt.Errorf("%s", typed.Detail)
		}
		return fmt.Errorf("this is not an official Roblox client package: %w", err)
	case setupsvc.ErrMissingX8664:
		return fmt.Errorf("this package does not contain the required x86-64 client: %w", err)
	case setupsvc.ErrUntrustedSigner, setupsvc.ErrInvalidSignature:
		return fmt.Errorf("the package signature could not be verified; choose a package from your authorized source: %w", err)
	case setupsvc.ErrUnsupportedSplit:
		return fmt.Errorf("the split set is incomplete or unsupported; select the base APK and every required split: %w", err)
	case setupsvc.ErrInvalidArchive, setupsvc.ErrIntegrity:
		return fmt.Errorf("the package is damaged or failed integrity checks: %w", err)
	case setupsvc.ErrCompatibility:
		return errors.New("this client changed a required native Android interface and is not compatible with this Tipsy build; update Tipsy or choose a supported official client package")
	case setupsvc.ErrSourceUnavailable, setupsvc.ErrSourceTrust:
		return fmt.Errorf("automatic download is unavailable; choose an APK or bundle: %w", err)
	default:
		return err
	}
}
