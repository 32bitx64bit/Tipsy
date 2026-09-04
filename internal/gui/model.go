// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package gui contains presentation-neutral state for the Qt desktop client.
// It deliberately describes workflows only; package installation, client
// settings and launch behavior belong to backend services.
package gui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

type Renderer string

const (
	RendererAuto   Renderer = "auto"
	RendererOpenGL Renderer = "opengl"
	RendererVulkan Renderer = "vulkan"

	MinFrameRate = 30
	MaxFrameRate = 240
)

type Settings struct {
	Renderer  Renderer
	FPSMode   FPSMode
	FrameRate int
}

// RendererOption is a backend-provided capability. The GUI keeps every known
// renderer visible, but only Available options may be selected and applied.
type RendererOption struct {
	Renderer  Renderer
	Available bool
	Reason    string
}

type FPSMode string

const (
	FPSAuto      FPSMode = "auto"
	FPSLimited   FPSMode = "limited"
	FPSUnlimited FPSMode = "unlimited"
)

func DefaultSettings() Settings {
	return Settings{Renderer: RendererAuto, FPSMode: FPSAuto}
}

func ValidateSettings(settings Settings, renderers []RendererOption) error {
	settings = normalizeSettings(settings)
	switch settings.Renderer {
	case RendererAuto, RendererOpenGL, RendererVulkan:
	default:
		return fmt.Errorf("choose Auto, OpenGL, or Vulkan")
	}
	option, ok := rendererOption(renderers, settings.Renderer)
	if !ok || !option.Available {
		reason := strings.TrimSpace(option.Reason)
		if reason == "" {
			reason = "this renderer is not available in the current build"
		}
		return fmt.Errorf("%s is unavailable: %s", rendererName(settings.Renderer), reason)
	}
	return validateFrameRate(settings)
}

func validateSettingsShape(settings Settings) error {
	settings = normalizeSettings(settings)
	switch settings.Renderer {
	case RendererAuto, RendererOpenGL, RendererVulkan:
	default:
		return fmt.Errorf("choose Auto, OpenGL, or Vulkan")
	}
	return validateFrameRate(settings)
}

func validateFrameRate(settings Settings) error {
	switch settings.FPSMode {
	case FPSAuto, FPSUnlimited:
	case FPSLimited:
		if settings.FrameRate < MinFrameRate || settings.FrameRate > MaxFrameRate {
			return fmt.Errorf("frame rate must be between %d and %d FPS", MinFrameRate, MaxFrameRate)
		}
	default:
		return errors.New("choose Automatic, Limited, or Unlimited frame rate")
	}
	return nil
}

func rendererOption(options []RendererOption, renderer Renderer) (RendererOption, bool) {
	for _, option := range options {
		if option.Renderer == renderer {
			return option, true
		}
	}
	return RendererOption{Renderer: renderer}, false
}

func rendererName(renderer Renderer) string {
	switch renderer {
	case RendererAuto:
		return "Auto"
	case RendererOpenGL:
		return "OpenGL"
	case RendererVulkan:
		return "Vulkan"
	default:
		return "Renderer"
	}
}

func normalizeRendererOptions(options []RendererOption) []RendererOption {
	result := make([]RendererOption, 0, 3)
	for _, renderer := range []Renderer{RendererAuto, RendererOpenGL, RendererVulkan} {
		option, ok := rendererOption(options, renderer)
		if !ok {
			option = RendererOption{
				Renderer: renderer,
				Reason:   "Availability was not reported by the settings backend.",
			}
		}
		result = append(result, option)
	}
	return result
}

func normalizeSettings(settings Settings) Settings {
	if settings.FPSMode != FPSLimited {
		settings.FrameRate = 0
	}
	return settings
}

type ApplyResult struct {
	RestartRequired bool
	FrameRateNote   string
}

type CheckStatus string

const (
	CheckReady   CheckStatus = "ready"
	CheckWarning CheckStatus = "warning"
	CheckBlocked CheckStatus = "blocked"
)

type DoctorCheck struct {
	Name   string
	Detail string
	Status CheckStatus
	Remedy string
}

type DoctorSummary struct {
	Ready  bool
	Checks []DoctorCheck
}

type InstallSnapshot struct {
	Installed       bool
	Version         string
	UpdateAvailable bool
	Status          string
}

type AutomaticAvailability struct {
	Available   bool
	SourceName  string
	Explanation string
	Reason      string
}

type InstallMode string

const (
	InstallAutomatic InstallMode = "automatic"
	InstallLocal     InstallMode = "local"
)

type InstallRequest struct {
	Mode       InstallMode
	LocalPaths []string
}

func (r InstallRequest) Validate(automatic AutomaticAvailability) error {
	switch r.Mode {
	case InstallAutomatic:
		if !automatic.Available {
			if automatic.Reason != "" {
				return fmt.Errorf("automatic download is unavailable: %s", automatic.Reason)
			}
			return errors.New("automatic download is unavailable")
		}
	case InstallLocal:
		if len(r.LocalPaths) == 0 {
			return errors.New("choose an APK or app bundle")
		}
	default:
		return errors.New("choose an installation method")
	}
	return nil
}

type InstallProgress struct {
	Phase   string
	Message string
	Percent int
}

// Service is the only boundary the GUI uses. Implementations own all package
// provenance, verification, extraction, client-settings and launch logic.
type Service interface {
	Snapshot(context.Context) (InstallSnapshot, error)
	Doctor(context.Context) (DoctorSummary, error)
	AutomaticAvailability(context.Context) AutomaticAvailability
	Install(context.Context, InstallRequest, func(InstallProgress)) error
	// Launch blocks for the lifetime of the in-process Roblox client. The
	// callback is invoked exactly once after startup has completed far enough
	// for the client window to own the desktop surface. Implementations must not
	// report started merely because a worker goroutine was created.
	Launch(context.Context, func()) error
	LoadSettings(context.Context) (Settings, error)
	RendererOptions(context.Context) []RendererOption
	ApplySettings(context.Context, Settings) (ApplyResult, error)
	ResetSettings(context.Context) (Settings, error)
}

type SetupState string

const (
	SetupIdle      SetupState = "idle"
	SetupRunning   SetupState = "running"
	SetupFailed    SetupState = "failed"
	SetupCancelled SetupState = "cancelled"
	SetupComplete  SetupState = "complete"
)

type SetupView struct {
	State     SetupState
	Request   InstallRequest
	Progress  InstallProgress
	Error     string
	Snapshot  InstallSnapshot
	Automatic AutomaticAvailability
}

// SetupModel is safe to read from Qt's timer while installation runs on a Go
// worker. Progress callbacks never touch Qt objects.
type SetupModel struct {
	mu      sync.RWMutex
	service Service
	view    SetupView
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewSetupModel(service Service) *SetupModel {
	return &SetupModel{service: service, view: SetupView{
		State:    SetupIdle,
		Request:  InstallRequest{Mode: InstallAutomatic},
		Progress: InstallProgress{Percent: 0},
	}}
}

func (m *SetupModel) Load(ctx context.Context) error {
	if m.service == nil {
		return errors.New("setup service is unavailable")
	}
	snapshot, err := m.service.Snapshot(ctx)
	if err != nil {
		return err
	}
	automatic := m.service.AutomaticAvailability(ctx)
	m.mu.Lock()
	m.view.Snapshot = snapshot
	m.view.Automatic = automatic
	if !automatic.Available {
		m.view.Request.Mode = InstallLocal
	}
	m.mu.Unlock()
	return nil
}

func (m *SetupModel) SetRequest(request InstallRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.view.State == SetupRunning {
		return errors.New("installation is already running")
	}
	request.LocalPaths = slices.Clone(request.LocalPaths)
	m.view.Request = request
	m.view.Error = ""
	return nil
}

func (m *SetupModel) View() SetupView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	view := m.view
	view.Request.LocalPaths = slices.Clone(view.Request.LocalPaths)
	return view
}

func (m *SetupModel) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.service == nil {
		m.mu.Unlock()
		return errors.New("setup service is unavailable")
	}
	if m.view.State == SetupRunning {
		m.mu.Unlock()
		return errors.New("installation is already running")
	}
	request := m.view.Request
	request.LocalPaths = slices.Clone(request.LocalPaths)
	if err := request.Validate(m.view.Automatic); err != nil {
		m.view.Error = err.Error()
		m.mu.Unlock()
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done
	m.view.State = SetupRunning
	m.view.Error = ""
	m.view.Progress = InstallProgress{Phase: "Preparing", Message: "Preparing installation…", Percent: 0}
	m.mu.Unlock()

	go func() {
		err := m.service.Install(workerCtx, request, func(progress InstallProgress) {
			progress.Percent = clampPercent(progress.Percent)
			m.mu.Lock()
			if m.view.State == SetupRunning {
				m.view.Progress = progress
			}
			m.mu.Unlock()
		})
		m.mu.Lock()
		defer m.mu.Unlock()
		m.cancel = nil
		if err == nil {
			m.view.State = SetupComplete
			m.view.Progress = InstallProgress{Phase: "Ready", Message: "Roblox is installed and ready to launch.", Percent: 100}
			if snapshot, snapshotErr := m.service.Snapshot(context.Background()); snapshotErr == nil {
				m.view.Snapshot = snapshot
			}
		} else if errors.Is(err, context.Canceled) || errors.Is(workerCtx.Err(), context.Canceled) {
			m.view.State = SetupCancelled
			m.view.Error = "Installation cancelled. No account information was changed."
			m.view.Progress.Message = "Cancelled"
		} else {
			m.view.State = SetupFailed
			m.view.Error = strings.TrimSpace(err.Error())
			if m.view.Error == "" {
				m.view.Error = "Installation failed."
			}
		}
		close(done)
	}()
	return nil
}

func (m *SetupModel) Cancel() bool {
	m.mu.RLock()
	cancel := m.cancel
	running := m.view.State == SetupRunning
	m.mu.RUnlock()
	if running && cancel != nil {
		cancel()
		return true
	}
	return false
}

func (m *SetupModel) Wait(ctx context.Context) error {
	m.mu.RLock()
	done := m.done
	m.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

type WizardStep int

const (
	WizardWelcome WizardStep = iota
	WizardDoctor
	WizardSource
	WizardInstall
	WizardReady
)

func AdvanceWizard(step WizardStep, setup SetupView) (WizardStep, error) {
	switch step {
	case WizardWelcome:
		return WizardDoctor, nil
	case WizardDoctor:
		return WizardSource, nil
	case WizardSource:
		if err := setup.Request.Validate(setup.Automatic); err != nil {
			return step, err
		}
		return WizardInstall, nil
	case WizardInstall:
		if setup.State != SetupComplete {
			return step, errors.New("finish installation before continuing")
		}
		return WizardReady, nil
	default:
		return step, errors.New("setup is already complete")
	}
}

type SettingsView struct {
	Saved           Settings
	Draft           Settings
	RendererOptions []RendererOption
	Dirty           bool
	ValidationError string
	RestartRequired bool
	ApplyNote       string
}

type SettingsModel struct {
	service Service
	view    SettingsView
}

func NewSettingsModel(service Service) *SettingsModel {
	defaults := DefaultSettings()
	options := normalizeRendererOptions(nil)
	return &SettingsModel{service: service, view: SettingsView{Saved: defaults, Draft: defaults, RendererOptions: options}}
}

func (m *SettingsModel) Load(ctx context.Context) error {
	if m.service == nil {
		return errors.New("settings service is unavailable")
	}
	settings, err := m.service.LoadSettings(ctx)
	if err != nil {
		return err
	}
	settings = normalizeSettings(settings)
	if err := validateSettingsShape(settings); err != nil {
		return fmt.Errorf("stored settings: %w", err)
	}
	options := normalizeRendererOptions(m.service.RendererOptions(ctx))
	m.view = SettingsView{Saved: settings, Draft: settings, RendererOptions: options}
	if err := ValidateSettings(settings, options); err != nil {
		m.view.ValidationError = err.Error()
	}
	return nil
}

func (m *SettingsModel) Edit(settings Settings) SettingsView {
	settings = normalizeSettings(settings)
	m.view.Draft = settings
	m.view.Dirty = settings != m.view.Saved
	m.view.ApplyNote = ""
	if err := ValidateSettings(settings, m.view.RendererOptions); err != nil {
		m.view.ValidationError = err.Error()
	} else {
		m.view.ValidationError = ""
	}
	return m.view
}

func (m *SettingsModel) Apply(ctx context.Context) (ApplyResult, error) {
	if m.service == nil {
		return ApplyResult{}, errors.New("settings service is unavailable")
	}
	m.view.Draft = normalizeSettings(m.view.Draft)
	if err := ValidateSettings(m.view.Draft, m.view.RendererOptions); err != nil {
		m.view.ValidationError = err.Error()
		return ApplyResult{}, err
	}
	result, err := m.service.ApplySettings(ctx, m.view.Draft)
	if err != nil {
		return ApplyResult{}, err
	}
	m.view.Saved = m.view.Draft
	m.view.Dirty = false
	m.view.RestartRequired = result.RestartRequired
	m.view.ApplyNote = strings.TrimSpace(result.FrameRateNote)
	return result, nil
}

func (m *SettingsModel) Reset(ctx context.Context) (Settings, error) {
	if m.service == nil {
		return Settings{}, errors.New("settings service is unavailable")
	}
	settings, err := m.service.ResetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	settings = normalizeSettings(settings)
	if err := validateSettingsShape(settings); err != nil {
		return Settings{}, fmt.Errorf("reset settings: %w", err)
	}
	restartRequired := settings != m.view.Saved
	options := slices.Clone(m.view.RendererOptions)
	m.view = SettingsView{Saved: settings, Draft: settings, RendererOptions: options, RestartRequired: restartRequired}
	if err := ValidateSettings(settings, options); err != nil {
		m.view.ValidationError = err.Error()
	}
	return settings, nil
}

func (m *SettingsModel) View() SettingsView {
	view := m.view
	view.RendererOptions = slices.Clone(m.view.RendererOptions)
	return view
}

type LaunchState string

const (
	LaunchIdle     LaunchState = "idle"
	LaunchStarting LaunchState = "starting"
	LaunchRunning  LaunchState = "running"
	LaunchExited   LaunchState = "exited"
	LaunchFailed   LaunchState = "failed"
)

type LaunchView struct {
	State   LaunchState
	Error   string
	Started bool
}

type LaunchModel struct {
	mu      sync.RWMutex
	service Service
	view    LaunchView
	done    chan struct{}
}

func NewLaunchModel(service Service) *LaunchModel {
	return &LaunchModel{service: service, view: LaunchView{State: LaunchIdle}}
}

func (m *LaunchModel) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.service == nil {
		m.mu.Unlock()
		return errors.New("launch service is unavailable")
	}
	if m.view.State == LaunchStarting || m.view.State == LaunchRunning {
		m.mu.Unlock()
		return errors.New("Roblox is already running")
	}
	done := make(chan struct{})
	m.done = done
	m.view = LaunchView{State: LaunchStarting}
	m.mu.Unlock()
	go func() {
		started := func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.done != done || m.view.State != LaunchStarting {
				return
			}
			m.view = LaunchView{State: LaunchRunning, Started: true}
		}
		err := m.service.Launch(ctx, started)
		m.mu.Lock()
		wasStarted := m.view.Started
		if err != nil && !errors.Is(err, context.Canceled) {
			m.view = LaunchView{State: LaunchFailed, Error: strings.TrimSpace(err.Error()), Started: wasStarted}
		} else if errors.Is(err, context.Canceled) && wasStarted {
			m.view = LaunchView{State: LaunchExited, Started: true}
		} else if errors.Is(err, context.Canceled) {
			m.view = LaunchView{State: LaunchIdle}
		} else if wasStarted {
			m.view = LaunchView{State: LaunchExited, Started: true}
		} else {
			m.view = LaunchView{State: LaunchFailed, Error: "Roblox exited before startup completed"}
		}
		close(done)
		m.mu.Unlock()
	}()
	return nil
}

func (m *LaunchModel) View() LaunchView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.view
}

func (m *LaunchModel) Wait(ctx context.Context) error {
	m.mu.RLock()
	done := m.done
	m.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
