// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

// TestOffscreenVisualProof constructs the real shell with synthetic data. It
// always verifies that Qt can lay out and grab the window headlessly; a caller
// may set TIPSY_GUI_SCREENSHOT to retain the PNG for manual inspection.
func TestOffscreenVisualProof(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))

	app := qt.NewQApplication([]string{"tipsy-gui-visual-proof"})
	qt.QApplication_SetStyleWithStyle("Fusion")
	app.SetStyleSheet(appStyleSheet)

	service := &secureVisualService{
		visualService: visualService{
			launchEntered: make(chan struct{}),
			launchReady:   make(chan struct{}),
			launchDone:    make(chan struct{}),
		},
		requireConsent: true,
		authority: guimodel.LaunchAuthority{
			Mode:    "development-unrestricted",
			Warning: "WARNING: DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session.",
		},
	}
	icon := brandIcon()
	win := newMainWindow(service, icon)
	// Appearance changes are immediate and separate from the settings draft.
	win.appearanceButtons[appearanceDark].Click()
	if loadAppearance() != appearanceDark || !win.appearanceIsDark || !win.appearanceButtons[appearanceDark].IsChecked() {
		t.Fatal("dark appearance was not persisted and reflected by the footer selector")
	}
	win.appearanceButtons[appearanceLight].Click()
	if loadAppearance() != appearanceLight || win.appearanceIsDark {
		t.Fatal("light appearance did not apply immediately")
	}
	win.appearanceButtons[appearanceSystem].Click()
	if loadAppearance() != appearanceSystem || !win.appearanceButtons[appearanceSystem].IsChecked() || win.appearanceButtons[appearanceLight].IsChecked() || win.appearanceButtons[appearanceDark].IsChecked() {
		t.Fatal("System appearance controls are inconsistent")
	}
	if mode := appearanceMode(os.Getenv("TIPSY_GUI_TEST_APPEARANCE")); validAppearance(mode) {
		win.chooseAppearance(mode)
	}
	if win.preferenceFPS.Text() != "60 FPS" || win.preferenceVSync.Text() != "Off" || win.preferenceWindow.Text() != "Windowed" {
		t.Fatal("home does not show saved launch preferences")
	}

	win.Show()
	qt.QCoreApplication_ProcessEvents()
	qt.QCoreApplication_ProcessEvents()

	pixmap := win.win.Grab()
	if pixmap.IsNull() {
		t.Fatal("offscreen Qt returned an empty window grab")
	}
	if pixmap.Width() < 640 || pixmap.Height() < 650 {
		t.Fatalf("unexpected visual proof size %dx%d", pixmap.Width(), pixmap.Height())
	}
	if os.Getenv("TIPSY_GUI_EXPECT_HIDPI") == "1" && pixmap.DevicePixelRatio() < 1.5 {
		t.Fatalf("expected HiDPI pixmap, device pixel ratio is %.2f", pixmap.DevicePixelRatio())
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" && !pixmap.Save2(path, "PNG") {
		t.Fatalf("save offscreen visual proof to %s", path)
	}

	win.setInstallText(setupsvc.ReadinessRejected, "Verification rejected", rejectedReadinessMessage(setupsvc.ErrCompatibility))
	qt.QCoreApplication_ProcessEvents()
	if win.installBadge.Text() != "REJECTED" || win.installPageBadge.Text() != "REJECTED" || win.playButton.IsEnabled() {
		t.Fatalf("rejected readiness did not disable Play on both status surfaces: home=%q install=%q enabled=%v", win.installBadge.Text(), win.installPageBadge.Text(), win.playButton.IsEnabled())
	}
	if !strings.Contains(win.installDetail.Text(), "Update Tipsy") || strings.Contains(win.installDetail.Text(), "could not be read") {
		t.Fatalf("rejected readiness was not actionable: %q", win.installDetail.Text())
	}
	if !strings.Contains(win.settingsClientStatus.Text(), "rejected") || !strings.Contains(win.settingsClientStatus.Text(), "Play stays disabled") {
		t.Fatalf("settings did not present rejected readiness: %q", win.settingsClientStatus.Text())
	}
	win.refreshSnapshot()
	qt.QCoreApplication_ProcessEvents()

	win.selectPage(2)
	qt.QCoreApplication_ProcessEvents()
	if win.settingsVSync == nil || win.settingsVSync.IsChecked() || win.settingsVSync.AccessibleName() != "VSync" {
		t.Fatalf("VSync control did not render unchecked and accessible: %#v", win.settingsVSync)
	}
	if win.settingsClientStatus == nil || !strings.Contains(win.settingsClientStatus.Text(), "authenticated launch inputs") || !strings.Contains(win.settingsClientStatus.Text(), "live Home") {
		t.Fatalf("settings client readiness was not truthful and visible: %#v", win.settingsClientStatus)
	}
	if win.settingsLowTexture == nil || win.settingsLowTexture.IsChecked() || win.settingsLowTexture.AccessibleName() != "Low texture mode" {
		t.Fatalf("Low texture mode control did not render unchecked and accessible: %#v", win.settingsLowTexture)
	}
	if win.settingsDiscordPresence == nil || win.settingsDiscordPresence.IsChecked() || win.settingsDiscordPresence.AccessibleName() != "Discord Rich Presence" {
		t.Fatalf("Discord Rich Presence control did not render unchecked and accessible: %#v", win.settingsDiscordPresence)
	}
	if win.settingsDiscordJoin == nil || win.settingsDiscordJoin.IsChecked() || win.settingsDiscordJoin.AccessibleName() != "Show Join button" || win.settingsDiscordJoin.IsEnabled() {
		t.Fatalf("Join button control did not render unchecked and disabled: %#v", win.settingsDiscordJoin)
	}
	if description := win.settingsLowTexture.AccessibleDescription(); !strings.Contains(strings.ToLower(description), "memory") || !strings.Contains(description, "VRAM") {
		t.Fatalf("Low texture mode accessibility copy is not honest: %q", description)
	}
	if win.settingsDisplay == nil || win.settingsDisplay.AccessibleName() != "Default monitor" || win.settingsDisplay.CurrentIndex() != 0 {
		t.Fatalf("default monitor control did not render on the main monitor: %#v", win.settingsDisplay)
	}
	if win.settingsStartFullscreen == nil || win.settingsStartFullscreen.IsChecked() || win.settingsStartFullscreen.AccessibleName() != "Start Roblox fullscreen" {
		t.Fatalf("start fullscreen control did not render unchecked and accessible: %#v", win.settingsStartFullscreen)
	}
	if description := win.settingsStartFullscreen.AccessibleDescription(); !strings.Contains(description, "Tipsy host startup preference") || !strings.Contains(description, "does not mirror") {
		t.Fatalf("start fullscreen accessibility copy is not honest: %q", description)
	}
	if got := win.settingsDisplay.CurrentText(); got != "Main monitor (default)" {
		t.Fatalf("default monitor text=%q", got)
	}
	if got := win.settingsVSync.Text(); got != vsyncToggleText(false) {
		t.Fatalf("unchecked VSync state text=%q, want %q", got, vsyncToggleText(false))
	}
	if got := win.settingsLowTexture.Text(); got != lowTextureToggleText(false) {
		t.Fatalf("unchecked low texture state text=%q, want %q", got, lowTextureToggleText(false))
	}
	if got := win.settingsDiscordPresence.Text(); got != discordPresenceToggleText(false) {
		t.Fatalf("unchecked Discord presence text=%q, want %q", got, discordPresenceToggleText(false))
	}
	if got := win.settingsDiscordJoin.Text(); got != discordJoinToggleText(false) {
		t.Fatalf("unchecked Discord join text=%q, want %q", got, discordJoinToggleText(false))
	}
	if got := win.settingsStartFullscreen.Text(); got != startFullscreenToggleText(false) {
		t.Fatalf("unchecked start fullscreen text=%q, want %q", got, startFullscreenToggleText(false))
	}
	if description := win.settingsFPSMode.AccessibleDescription(); !strings.Contains(description, "experimental uncapped request") || !strings.Contains(description, "no frame rate is guaranteed") {
		t.Fatalf("Unlimited accessibility copy is not honest: %q", description)
	}
	settingsPixmap := win.win.Grab()
	if settingsPixmap.IsNull() || settingsPixmap.Width() != pixmap.Width() || settingsPixmap.Height() != pixmap.Height() {
		t.Fatalf("settings page did not render at the window size: %dx%d", settingsPixmap.Width(), settingsPixmap.Height())
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		settingsPath := strings.TrimSuffix(path, ext) + "-settings" + ext
		if !settingsPixmap.Save2(settingsPath, "PNG") {
			t.Fatalf("save settings visual proof to %s", settingsPath)
		}
	}

	// Popups and dialogs must share the live application palette, not only the
	// central window stylesheet. Retain window-only synthetic visual evidence.
	win.settingsRenderer.ShowPopup()
	qt.QCoreApplication_ProcessEvents()
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		if !win.settingsRenderer.View().Window().Grab().Save2(strings.TrimSuffix(path, ext)+"-renderer-popup"+ext, "PNG") {
			t.Fatal("save renderer popup")
		}
	}
	win.settingsRenderer.HidePopup()
	dialog := qt.NewQMessageBox6(qt.QMessageBox__Information, "Settings saved", "Restart Roblox to apply the saved graphics preferences.", qt.QMessageBox__Ok, win.win.QWidget)
	dialog.Show()
	qt.QCoreApplication_ProcessEvents()
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		if !dialog.Grab().Save2(strings.TrimSuffix(path, ext)+"-dialog"+ext, "PNG") {
			t.Fatal("save themed dialog")
		}
	}
	dialog.Close()
	dialog.Delete()
	win.win.ActivateWindow()
	qt.QApplication_SetActiveWindow(win.win.QWidget)
	qt.QCoreApplication_ProcessEvents()
	win.settingsVSync.SetChecked(true)
	if win.preferenceVSync.Text() != "Off" {
		t.Fatal("unsaved draft changed the home summary")
	}
	qt.QCoreApplication_ProcessEvents()
	if !win.settingsVSync.IsChecked() || win.settingsVSync.Text() != vsyncToggleText(true) {
		t.Fatalf("checked VSync state is not unmistakable: checked=%v text=%q", win.settingsVSync.IsChecked(), win.settingsVSync.Text())
	}
	checkedSettings := win.win.Grab()
	if checkedSettings.IsNull() {
		t.Fatal("checked VSync settings page did not render")
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		checkedPath := strings.TrimSuffix(path, ext) + "-settings-vsync-on" + ext
		if !checkedSettings.Save2(checkedPath, "PNG") {
			t.Fatalf("save checked VSync visual proof to %s", checkedPath)
		}
	}
	win.settingsVSync.SetChecked(false)
	qt.QCoreApplication_ProcessEvents()
	win.settingsVSync.SetFocusWithReason(qt.TabFocusReason)
	qt.QCoreApplication_ProcessEvents()
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.UnsafePointer() != win.settingsVSync.QWidget.UnsafePointer() {
		t.Fatal("VSync checkbox did not retain keyboard-visible focus")
	}
	focusedSettings := win.win.Grab()
	if focusedSettings.IsNull() {
		t.Fatal("focused VSync settings page did not render")
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		focusedPath := strings.TrimSuffix(path, ext) + "-settings-vsync-focus" + ext
		if !focusedSettings.Save2(focusedPath, "PNG") {
			t.Fatalf("save focused VSync visual proof to %s", focusedPath)
		}
	}
	win.settingsVSync.SetEnabled(false)
	win.settingsRenderer.SetFocusWithReason(qt.TabFocusReason)
	qt.QCoreApplication_ProcessEvents()
	if win.settingsVSync.IsEnabled() {
		t.Fatal("VSync checkbox did not enter disabled presentation state")
	}
	disabledSettings := win.win.Grab()
	if disabledSettings.IsNull() {
		t.Fatal("disabled VSync settings page did not render")
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		disabledPath := strings.TrimSuffix(path, ext) + "-settings-vsync-disabled" + ext
		if !disabledSettings.Save2(disabledPath, "PNG") {
			t.Fatalf("save disabled VSync visual proof to %s", disabledPath)
		}
	}
	win.settingsVSync.SetEnabled(true)
	qt.QCoreApplication_ProcessEvents()
	win.win.Resize(780, 560)
	win.settingsFPSMode.SetCurrentIndex(2)
	qt.QCoreApplication_ProcessEvents()
	if win.settingsFPS.IsEnabled() || !strings.Contains(win.settingsHint.Text(), "experimental uncapped request") || !strings.Contains(win.settingsHint.Text(), "no frame rate is guaranteed") {
		t.Fatalf("Unlimited state copy=%q limitEnabled=%v", win.settingsHint.Text(), win.settingsFPS.IsEnabled())
	}
	compactSettings := win.win.Grab()
	if compactSettings.IsNull() || !win.settingsVSync.IsVisible() || !win.settingsLowTexture.IsVisible() || !win.settingsDiscordPresence.IsVisible() || !win.settingsDiscordJoin.IsVisible() || !win.settingsStartFullscreen.IsVisible() {
		t.Fatal("compact settings page did not keep the settings controls in the scrollable layout")
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		compactPath := strings.TrimSuffix(path, ext) + "-settings-compact" + ext
		if !compactSettings.Save2(compactPath, "PNG") {
			t.Fatalf("save compact settings visual proof to %s", compactPath)
		}
	}
	win.win.Resize(1080, 720)
	qt.QCoreApplication_ProcessEvents()

	wizard := qt.NewQWizard(win.win.QWidget)
	wizard.SetWizardStyle(qt.QWizard__ModernStyle)
	wizard.Resize(780, 560)
	wizard.SetMinimumSize2(720, 520)
	sidebar := win.buildWizardSide(true)
	wizard.SetSideWidget(sidebar.widget)
	welcomeID := wizard.AddPage(win.buildWelcomeWizardPage(true))
	source, _, _, _, _, _ := win.buildSourceWizardPage()
	sourceID := wizard.AddPage(source)
	styleWizardButtons(wizard)
	updateWizardSteps(sidebar.steps, 0)
	wizard.Show()
	qt.QCoreApplication_ProcessEvents()
	wizardPixmap := wizard.Grab()
	if wizardPixmap.IsNull() || wizard.CurrentId() != welcomeID {
		t.Fatal("offscreen wizard Welcome page did not render")
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		wizardPath := strings.TrimSuffix(path, ext) + "-wizard-welcome" + ext
		if !wizardPixmap.Save2(wizardPath, "PNG") {
			t.Fatalf("save offscreen wizard proof to %s", wizardPath)
		}
	}
	wizard.SetCurrentId(sourceID)
	updateWizardSteps(sidebar.steps, 2)
	qt.QCoreApplication_ProcessEvents()
	sourcePixmap := wizard.Grab()
	if sourcePixmap.IsNull() {
		t.Fatal("offscreen wizard Source page did not render")
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" {
		ext := filepath.Ext(path)
		sourcePath := strings.TrimSuffix(path, ext) + "-wizard-source" + ext
		if !sourcePixmap.Save2(sourcePath, "PNG") {
			t.Fatalf("save offscreen wizard source proof to %s", sourcePath)
		}
	}
	wizard.Close()
	wizard.Delete()

	win.selectPage(0)
	originalConfirm := confirmDevelopmentLaunch
	defer func() { confirmDevelopmentLaunch = originalConfirm }()
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
	win.refreshLaunchState()
	if !win.win.IsVisible() || win.launcherHidden {
		t.Fatal("launcher hid before the client acknowledged startup")
	}
	close(service.launchReady)
	waitForLaunchState(t, win.launch, guimodel.LaunchRunning)
	// Started can arrive between GUI timer ticks. Native close must not quit
	// the in-process host before refresh disables quit-on-last-window and hides.
	win.win.Close()
	if !win.win.IsVisible() {
		t.Fatal("close during acknowledged-start timer gap dismissed the host")
	}
	win.refreshLaunchState()
	if win.win.IsVisible() || !win.launcherHidden {
		t.Fatal("launcher remained visible after the client acknowledged startup")
	}
	close(service.launchDone)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := win.launch.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if view := win.launch.View(); view.State != guimodel.LaunchExited || !view.Started {
		t.Fatalf("launch view=%+v, want clean acknowledged exit", view)
	}

	win.win.Close()
	win.win.Delete()
	app.Delete()
}

func TestPlayModeUsesProgressWhenInstalled(t *testing.T) {
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))

	app := qt.NewQApplication([]string{"tipsy-gui-play-mode"})
	qt.QApplication_SetStyleWithStyle("Fusion")
	app.SetStyleSheet(appStyleSheet)

	service := visualService{
		launchEntered: make(chan struct{}),
		launchReady:   make(chan struct{}),
		launchDone:    make(chan struct{}),
	}
	win := newWindowBase(service, brandIcon())
	win.startExternalInitialization("")
	waitGUI(t, func() bool {
		return win.externalProgress != nil && win.externalProgress.dialog.IsVisible()
	})

	waitGUI(t, func() bool {
		select {
		case <-service.launchEntered:
			return true
		default:
			return false
		}
	})
	if win.externalProgress == nil || !win.externalProgress.dialog.IsVisible() {
		t.Fatal("external launch lacks progress window")
	}

	if win.win.IsVisible() {
		t.Fatal("play mode showed the settings window before launching")
	}
	close(service.launchReady)
	waitForLaunchState(t, win.launch, guimodel.LaunchRunning)
	win.refreshLaunchState()
	if win.win.IsVisible() {
		t.Fatal("play mode showed the settings window after the client started")
	}
	close(service.launchDone)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := win.launch.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	win.win.Close()
	win.win.Delete()
	app.Delete()
}

type visualService struct {
	launchEntered chan struct{}
	launchReady   chan struct{}
	launchDone    chan struct{}
}

type secureVisualService struct {
	visualService
	mu             sync.Mutex
	requireConsent bool
	authority      guimodel.LaunchAuthority
	approvals      []bool
}

func (s *secureVisualService) PrepareLaunch(_ context.Context, approveDevelopment bool) (guimodel.LaunchAuthority, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals = append(s.approvals, approveDevelopment)
	if s.requireConsent && !approveDevelopment {
		return guimodel.LaunchAuthority{DevelopmentConsentRequired: true}, errors.New("development consent required")
	}
	return s.authority, nil
}

func (s *secureVisualService) approvalCalls() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.approvals...)
}

func (visualService) Snapshot(context.Context) (guimodel.InstallSnapshot, error) {
	return guimodel.InstallSnapshot{
		Installed: true,
		Readiness: setupsvc.ReadinessLaunchInputs,
		Version:   "2.734.917 · synthetic",
		Status:    guiLaunchInputsStatus,
	}, nil
}

func (visualService) Doctor(context.Context) (guimodel.DoctorSummary, error) {
	return guimodel.DoctorSummary{Ready: true, Checks: []guimodel.DoctorCheck{
		{Name: "Architecture", Detail: "x86_64", Status: guimodel.CheckReady},
		{Name: "X11 display", Detail: "synthetic test display", Status: guimodel.CheckReady},
		{Name: "OpenGL / EGL", Detail: "synthetic test driver", Status: guimodel.CheckReady},
	}}, nil
}

func (visualService) AutomaticAvailability(context.Context) guimodel.AutomaticAvailability {
	return guimodel.AutomaticAvailability{
		Reason: "No lawful, verifiable automatic provider is configured.",
	}
}

func (visualService) Install(context.Context, guimodel.InstallRequest, func(guimodel.InstallProgress)) error {
	return nil
}

func (visualService) PrepareLaunch(context.Context, bool) (guimodel.LaunchAuthority, error) {
	return guimodel.LaunchAuthority{Mode: "official-verified"}, nil
}

func (s visualService) Launch(_ context.Context, _ guimodel.LaunchRequest, started func()) error {
	if s.launchEntered != nil {
		close(s.launchEntered)
	}
	if s.launchReady != nil {
		<-s.launchReady
	}
	started()
	if s.launchDone != nil {
		<-s.launchDone
	}
	return nil
}

func (visualService) LoadSettings(context.Context) (guimodel.Settings, error) {
	settings := guimodel.DefaultSettings()
	settings.FPSMode = guimodel.FPSLimited
	settings.FrameRate = 60
	return settings, nil
}

func (visualService) RendererOptions(context.Context) []guimodel.RendererOption {
	return []guimodel.RendererOption{
		{Renderer: guimodel.RendererAuto, Available: true, Reason: "Uses the supported client path."},
		{Renderer: guimodel.RendererOpenGL, Available: true, Reason: "X11 EGL/OpenGL ES path is ready."},
		{Renderer: guimodel.RendererVulkan, Reason: "Requires the Vulkan bridge and a working host driver."},
	}
}

func (visualService) ApplySettings(context.Context, guimodel.Settings) (guimodel.ApplyResult, error) {
	return guimodel.ApplyResult{RestartRequired: true}, nil
}

func (visualService) ResetSettings(context.Context) (guimodel.Settings, error) {
	return guimodel.DefaultSettings(), nil
}

func (visualService) ControllerPads(context.Context) (guimodel.ControllerState, error) {
	return guimodel.ControllerState{
		Enabled:      true,
		PathSelector: "direct",
		Note:         "synthetic visual-test state: no gamepad",
	}, nil
}

func waitGUI(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		qt.QCoreApplication_ProcessEvents()
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for synthetic GUI state")
}
