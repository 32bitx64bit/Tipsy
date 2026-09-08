// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gui

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeService struct {
	mu             sync.Mutex
	snapshot       InstallSnapshot
	doctor         DoctorSummary
	automatic      AutomaticAvailability
	settings       Settings
	renderers      []RendererOption
	installErr     error
	installStarted chan struct{}
	waitForCancel  bool
	installCalls   []InstallRequest
	applyCalls     []Settings
	noRestart      bool
	launches       int
	launchReq      LaunchRequest
	launchErr      error
	launchStarted  bool
	launchDone     chan struct{}
}

func (f *fakeService) Snapshot(context.Context) (InstallSnapshot, error) { return f.snapshot, nil }
func (f *fakeService) Doctor(context.Context) (DoctorSummary, error)     { return f.doctor, nil }
func (f *fakeService) AutomaticAvailability(context.Context) AutomaticAvailability {
	return f.automatic
}
func (f *fakeService) Install(ctx context.Context, req InstallRequest, progress func(InstallProgress)) error {
	f.mu.Lock()
	f.installCalls = append(f.installCalls, req)
	started := f.installStarted
	wait := f.waitForCancel
	err := f.installErr
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	progress(InstallProgress{Phase: "Download", Message: "Downloading", Percent: 37})
	if wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if err == nil {
		f.snapshot = InstallSnapshot{Installed: true, Version: "2.0", Status: "Ready"}
	}
	return err
}
func (f *fakeService) PrepareLaunch(context.Context, bool) (LaunchAuthority, error) {
	return LaunchAuthority{Mode: "official-verified"}, nil
}
func (f *fakeService) Launch(_ context.Context, req LaunchRequest, started func()) error {
	f.mu.Lock()
	f.launches++
	f.launchReq = req
	acknowledge, done, err := f.launchStarted, f.launchDone, f.launchErr
	f.mu.Unlock()
	if acknowledge {
		started()
		started() // The model must tolerate a defensive duplicate acknowledgement.
	}
	if done != nil {
		<-done
	}
	return err
}
func (f *fakeService) LoadSettings(context.Context) (Settings, error) { return f.settings, nil }
func (f *fakeService) RendererOptions(context.Context) []RendererOption {
	if f.renderers != nil {
		return slices.Clone(f.renderers)
	}
	return []RendererOption{
		{Renderer: RendererAuto, Available: true},
		{Renderer: RendererOpenGL, Available: true},
		{Renderer: RendererVulkan, Reason: "Requires the Vulkan bridge"},
	}
}
func (f *fakeService) ApplySettings(_ context.Context, settings Settings) (ApplyResult, error) {
	f.applyCalls = append(f.applyCalls, settings)
	f.settings = settings
	return ApplyResult{RestartRequired: !f.noRestart, FrameRateNote: "Experimental frame-rate note"}, nil
}
func (f *fakeService) ResetSettings(context.Context) (Settings, error) {
	f.settings = DefaultSettings()
	return f.settings, nil
}

func TestWizardTransitionsAndSourceValidation(t *testing.T) {
	view := SetupView{
		Automatic: AutomaticAvailability{Available: true},
		Request:   InstallRequest{Mode: InstallAutomatic},
	}
	step := WizardWelcome
	for _, want := range []WizardStep{WizardDoctor, WizardSource, WizardInstall} {
		var err error
		step, err = AdvanceWizard(step, view)
		if err != nil || step != want {
			t.Fatalf("advance: step=%v err=%v, want %v", step, err, want)
		}
	}
	if _, err := AdvanceWizard(step, view); err == nil {
		t.Fatal("installation page advanced before success")
	}
	view.State = SetupComplete
	if got, err := AdvanceWizard(step, view); err != nil || got != WizardReady {
		t.Fatalf("complete advance: step=%v err=%v", got, err)
	}

	view.Request = InstallRequest{Mode: InstallLocal}
	if _, err := AdvanceWizard(WizardSource, view); err == nil {
		t.Fatal("empty local selection accepted")
	}
}

func TestSetupSuccessProgressAndRetryableError(t *testing.T) {
	fake := &fakeService{
		automatic: AutomaticAvailability{Available: true, SourceName: "Trusted fixture"},
		settings:  DefaultSettings(),
	}
	model := NewSetupModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForModel(t, model)
	view := model.View()
	if view.State != SetupComplete || view.Progress.Percent != 100 || !view.Snapshot.Installed {
		t.Fatalf("unexpected completed view: %+v", view)
	}
	if len(fake.installCalls) != 1 || fake.installCalls[0].Mode != InstallAutomatic {
		t.Fatalf("unexpected install calls: %+v", fake.installCalls)
	}

	fake.installErr = errors.New("signature could not be verified; choose another package")
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForModel(t, model)
	view = model.View()
	if view.State != SetupFailed || view.Error == "" {
		t.Fatalf("actionable failure missing: %+v", view)
	}
}

func TestSetupCancel(t *testing.T) {
	fake := &fakeService{
		automatic:      AutomaticAvailability{Available: true},
		settings:       DefaultSettings(),
		installStarted: make(chan struct{}),
		waitForCancel:  true,
	}
	model := NewSetupModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.installStarted:
	case <-time.After(time.Second):
		t.Fatal("install did not start")
	}
	if !model.Cancel() {
		t.Fatal("cancel did not signal running installation")
	}
	waitForModel(t, model)
	if got := model.View().State; got != SetupCancelled {
		t.Fatalf("state=%q, want cancelled", got)
	}
}

func TestSettingsBindingValidationApplyAndReset(t *testing.T) {
	fake := &fakeService{settings: Settings{Renderer: RendererOpenGL, FPSMode: FPSLimited, FrameRate: 144, VSync: true, Display: DisplayPrimary, DiscordRichPresence: true}}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := model.View().Draft; got != fake.settings {
		t.Fatalf("loaded=%+v want=%+v", got, fake.settings)
	}
	view := model.Edit(Settings{Renderer: RendererOpenGL, FPSMode: FPSLimited, FrameRate: 240, VSync: true, Display: DisplayPrimary, DiscordRichPresence: true})
	if !view.Dirty || view.ValidationError != "" {
		t.Fatalf("valid edit view: %+v", view)
	}
	result, err := model.Apply(context.Background())
	if err != nil || !result.RestartRequired || result.FrameRateNote == "" || len(fake.applyCalls) != 1 {
		t.Fatalf("apply result=%+v err=%v calls=%v", result, err, fake.applyCalls)
	}
	if model.View().ApplyNote != result.FrameRateNote {
		t.Fatalf("backend frame-rate note was not retained: %+v", model.View())
	}
	view = model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSLimited, FrameRate: MaxFrameRate + 1})
	if view.ValidationError == "" {
		t.Fatal("out-of-range FPS accepted")
	}
	if _, err := model.Apply(context.Background()); err == nil {
		t.Fatal("invalid settings applied")
	}
	reset, err := model.Reset(context.Background())
	if err != nil || reset != DefaultSettings() || model.View().Dirty {
		t.Fatalf("reset=%+v err=%v view=%+v", reset, err, model.View())
	}
}

func TestLowTextureModeDefaultsOffAndResetsToHighQuality(t *testing.T) {
	defaults := DefaultSettings()
	if defaults.LowTextureMode {
		t.Fatal("LowTextureMode defaulted on")
	}
	fake := &fakeService{settings: defaults}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	view := model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSAuto, LowTextureMode: true, DiscordRichPresence: true, Display: DisplayPrimary})
	if !view.Dirty || view.ValidationError != "" || view.Draft.FPSMode != FPSAuto || view.Draft.VSync {
		t.Fatalf("independent low-texture edit view=%+v", view)
	}
	result, err := model.Apply(context.Background())
	if err != nil || !result.RestartRequired || len(fake.applyCalls) != 1 || !fake.applyCalls[0].LowTextureMode {
		t.Fatalf("LowTextureMode apply result=%+v err=%v calls=%+v", result, err, fake.applyCalls)
	}
	reset, err := model.Reset(context.Background())
	if err != nil || reset.LowTextureMode || model.View().Dirty || !model.View().RestartRequired {
		t.Fatalf("LowTextureMode reset=%+v err=%v view=%+v", reset, err, model.View())
	}
}

func TestStartFullscreenDefaultsOffPersistsAndResets(t *testing.T) {
	defaults := DefaultSettings()
	if defaults.StartFullscreen {
		t.Fatal("StartFullscreen defaulted on")
	}
	fake := &fakeService{settings: defaults, noRestart: true}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := defaults
	want.StartFullscreen = true
	view := model.Edit(want)
	if !view.Dirty || view.ValidationError != "" || !view.Draft.StartFullscreen {
		t.Fatalf("start fullscreen edit=%+v", view)
	}
	result, err := model.Apply(context.Background())
	if err != nil || result.RestartRequired || len(fake.applyCalls) != 1 || !fake.applyCalls[0].StartFullscreen {
		t.Fatalf("start fullscreen apply result=%+v err=%v calls=%+v", result, err, fake.applyCalls)
	}
	reset, err := model.Reset(context.Background())
	if err != nil || reset.StartFullscreen || model.View().Dirty {
		t.Fatalf("start fullscreen reset=%+v err=%v view=%+v", reset, err, model.View())
	}
}

func TestDiscordPresenceDefaultsOnAndDoesNotRestart(t *testing.T) {
	defaults := DefaultSettings()
	if !defaults.DiscordRichPresence || defaults.DiscordJoinButton {
		t.Fatalf("discord defaults=%+v", defaults)
	}
	fake := &fakeService{settings: defaults, noRestart: true}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSAuto, Display: DisplayPrimary, DiscordRichPresence: true, DiscordJoinButton: true})
	if !view.Dirty || !DiscordOnlyChange(view.Draft, view.Saved) {
		t.Fatalf("join-only edit view=%+v", view)
	}
	result, err := model.Apply(context.Background())
	if err != nil || result.RestartRequired || len(fake.applyCalls) != 1 || !fake.applyCalls[0].DiscordJoinButton {
		t.Fatalf("discord apply result=%+v err=%v calls=%+v", result, err, fake.applyCalls)
	}
}

func TestVSyncIsIndependentAndDefaultsOff(t *testing.T) {
	defaults := DefaultSettings()
	if defaults.VSync {
		t.Fatal("VSync defaulted on")
	}
	if defaults.LowTextureMode {
		t.Fatal("LowTextureMode defaulted on")
	}
	if defaults.Display != DisplayPrimary {
		t.Fatalf("display defaulted to %q, want primary", defaults.Display)
	}
	fake := &fakeService{settings: defaults}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	view := model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSAuto, VSync: true, DiscordRichPresence: true, Display: DisplayPrimary})
	if !view.Dirty || view.ValidationError != "" || view.Draft.FPSMode != FPSAuto {
		t.Fatalf("independent VSync edit view=%+v", view)
	}
	result, err := model.Apply(context.Background())
	if err != nil || !result.RestartRequired || len(fake.applyCalls) != 1 || !fake.applyCalls[0].VSync {
		t.Fatalf("VSync apply result=%+v err=%v calls=%+v", result, err, fake.applyCalls)
	}
	reset, err := model.Reset(context.Background())
	if err != nil || reset.VSync || model.View().Dirty || !model.View().RestartRequired {
		t.Fatalf("VSync reset=%+v err=%v view=%+v", reset, err, model.View())
	}
}

func TestDisplayPlacementDefaultsPrimaryAndEditsIndependently(t *testing.T) {
	fake := &fakeService{settings: DefaultSettings()}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := model.View().Draft.Display; got != DisplayPrimary {
		t.Fatalf("loaded display=%q", got)
	}
	view := model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSAuto, Display: DisplayPointer, DiscordRichPresence: true})
	if !view.Dirty || view.ValidationError != "" || view.Draft.Display != DisplayPointer || view.Draft.VSync {
		t.Fatalf("pointer edit view=%+v", view)
	}
	if _, err := model.Apply(context.Background()); err != nil || fake.applyCalls[0].Display != DisplayPointer {
		t.Fatalf("pointer apply err=%v calls=%+v", err, fake.applyCalls)
	}
	view = model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSAuto, Display: "HDMI-0", DiscordRichPresence: true})
	if view.Draft.Display != "HDMI-0" || view.ValidationError != "" {
		t.Fatalf("named output edit view=%+v", view)
	}
	view = model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSAuto, Display: "bad/name", DiscordRichPresence: true})
	if view.ValidationError == "" {
		t.Fatal("path-like monitor name accepted")
	}
}

func TestFrameRateModesAreSemantic(t *testing.T) {
	for _, settings := range []Settings{
		{Renderer: RendererAuto, FPSMode: FPSAuto},
		{Renderer: RendererOpenGL, FPSMode: FPSLimited, FrameRate: 60},
	} {
		if err := ValidateSettings(settings, (&fakeService{}).RendererOptions(context.Background())); err != nil {
			t.Fatalf("ValidateSettings(%+v): %v", settings, err)
		}
	}
	options := (&fakeService{}).RendererOptions(context.Background())
	if err := ValidateSettings(Settings{Renderer: RendererAuto, FPSMode: FPSLimited, FrameRate: MaxFrameRate + 1}, options); err == nil {
		t.Fatal("out-of-range numeric limit accepted")
	}
	model := NewSettingsModel(&fakeService{settings: DefaultSettings()})
	model.Edit(Settings{Renderer: RendererAuto, FPSMode: FPSUnlimited, FrameRate: 144})
	if got := model.View().Draft.FrameRate; got != 0 {
		t.Fatalf("unlimited mode leaked numeric surrogate %d", got)
	}
}

func TestUnavailableRendererIsVisibleButRejected(t *testing.T) {
	fake := &fakeService{settings: DefaultSettings()}
	model := NewSettingsModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	options := model.View().RendererOptions
	if len(options) != 3 || options[2].Renderer != RendererVulkan || options[2].Available || options[2].Reason == "" {
		t.Fatalf("Vulkan capability was not preserved for the UI: %+v", options)
	}
	view := model.Edit(Settings{Renderer: RendererVulkan, FPSMode: FPSAuto})
	if view.ValidationError == "" {
		t.Fatal("unavailable Vulkan renderer was accepted")
	}
	if _, err := model.Apply(context.Background()); err == nil || len(fake.applyCalls) != 0 {
		t.Fatalf("unavailable renderer reached backend: err=%v calls=%v", err, fake.applyCalls)
	}
}

func TestUnavailableAutomaticSelectsLocal(t *testing.T) {
	fake := &fakeService{
		automatic: AutomaticAvailability{Reason: "provider not configured"},
		settings:  DefaultSettings(),
	}
	model := NewSetupModel(fake)
	if err := model.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := model.View().Request.Mode; got != InstallLocal {
		t.Fatalf("mode=%q, want local", got)
	}
	if err := model.Start(context.Background()); err == nil {
		t.Fatal("empty local package accepted")
	}
}

func TestLaunchModelAcknowledgesStartThenRecordsCleanExit(t *testing.T) {
	fake := &fakeService{settings: DefaultSettings(), launchStarted: true}
	model := NewLaunchModel(fake)
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.launches != 1 || model.View().State != LaunchExited || !model.View().Started {
		t.Fatalf("launches=%d view=%+v", fake.launches, model.View())
	}
}

func TestLaunchModelForwardsWebsiteURI(t *testing.T) {
	fake := &fakeService{settings: DefaultSettings(), launchStarted: true}
	model := NewLaunchModel(fake)
	if err := model.StartRequest(context.Background(), LaunchRequest{URI: "roblox://experiences/start?placeId=1818"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.launchReq.URI != "roblox://experiences/start?placeId=1818" {
		t.Fatalf("launch URI=%q", fake.launchReq.URI)
	}
}

func TestLaunchModelPublishesRunningOnlyAfterAcknowledgement(t *testing.T) {
	done := make(chan struct{})
	fake := &fakeService{settings: DefaultSettings(), launchStarted: true, launchDone: done}
	model := NewLaunchModel(fake)
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for model.View().State != LaunchRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if view := model.View(); view.State != LaunchRunning || !view.Started {
		t.Fatalf("view=%+v, want acknowledged running client", view)
	}
	if err := model.Start(context.Background()); err == nil {
		t.Fatal("second launch accepted while acknowledged client is running")
	}
	close(done)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if view := model.View(); view.State != LaunchExited || !view.Started {
		t.Fatalf("view=%+v, want clean acknowledged exit", view)
	}
}

func TestLaunchModelRejectsCleanReturnWithoutStartAcknowledgement(t *testing.T) {
	fake := &fakeService{settings: DefaultSettings()}
	model := NewLaunchModel(fake)
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if view.State != LaunchFailed || view.Started || view.Error == "" {
		t.Fatalf("view=%+v, want pre-start failure", view)
	}
}

func TestLaunchModelPreservesStartedOnLaterFailure(t *testing.T) {
	fake := &fakeService{settings: DefaultSettings(), launchStarted: true, launchErr: errors.New("client failed")}
	model := NewLaunchModel(fake)
	if err := model.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	view := model.View()
	if view.State != LaunchFailed || !view.Started || view.Error != "client failed" {
		t.Fatalf("view=%+v", view)
	}
}

func TestLaunchModelCancellationRespectsStartBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		started bool
		want    LaunchState
	}{
		{name: "before acknowledgement", want: LaunchIdle},
		{name: "after acknowledgement", started: true, want: LaunchExited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeService{settings: DefaultSettings(), launchStarted: tc.started, launchErr: context.Canceled}
			model := NewLaunchModel(fake)
			if err := model.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := model.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if view := model.View(); view.State != tc.want || view.Started != tc.started {
				t.Fatalf("view=%+v, want state=%s started=%t", view, tc.want, tc.started)
			}
		})
	}
}

func waitForModel(t *testing.T, model *SetupModel) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
