// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
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

	service := visualService{
		launchEntered: make(chan struct{}),
		launchReady:   make(chan struct{}),
		launchDone:    make(chan struct{}),
	}
	icon := brandIcon()
	win := newMainWindow(service, icon)
	win.Show()
	qt.QCoreApplication_ProcessEvents()
	qt.QCoreApplication_ProcessEvents()

	pixmap := win.win.Grab()
	if pixmap.IsNull() {
		t.Fatal("offscreen Qt returned an empty window grab")
	}
	if pixmap.Width() < 1000 || pixmap.Height() < 650 {
		t.Fatalf("unexpected visual proof size %dx%d", pixmap.Width(), pixmap.Height())
	}
	if os.Getenv("TIPSY_GUI_EXPECT_HIDPI") == "1" && pixmap.DevicePixelRatio() < 1.5 {
		t.Fatalf("expected HiDPI pixmap, device pixel ratio is %.2f", pixmap.DevicePixelRatio())
	}
	if path := os.Getenv("TIPSY_GUI_SCREENSHOT"); path != "" && !pixmap.Save2(path, "PNG") {
		t.Fatalf("save offscreen visual proof to %s", path)
	}

	win.selectPage(2)
	qt.QCoreApplication_ProcessEvents()
	if win.settingsVSync == nil || win.settingsVSync.IsChecked() || win.settingsVSync.AccessibleName() != "VSync" {
		t.Fatalf("VSync control did not render unchecked and accessible: %#v", win.settingsVSync)
	}
	if win.settingsDisplay == nil || win.settingsDisplay.AccessibleName() != "Default monitor" || win.settingsDisplay.CurrentIndex() != 0 {
		t.Fatalf("default monitor control did not render on the main monitor: %#v", win.settingsDisplay)
	}
	if got := win.settingsDisplay.CurrentText(); got != "Main monitor (default)" {
		t.Fatalf("default monitor text=%q", got)
	}
	if got := win.settingsVSync.Text(); got != vsyncToggleText(false) {
		t.Fatalf("unchecked VSync state text=%q, want %q", got, vsyncToggleText(false))
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
	win.settingsVSync.SetChecked(true)
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
	if compactSettings.IsNull() || !win.settingsVSync.IsVisible() {
		t.Fatal("compact settings page did not keep the VSync control in the scrollable layout")
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
	win.playButton.Click()
	select {
	case <-service.launchEntered:
	case <-time.After(time.Second):
		t.Fatal("offscreen launch backend was not entered")
	}
	win.refreshLaunchState()
	if !win.win.IsVisible() || win.launcherHidden {
		t.Fatal("launcher hid before the client acknowledged startup")
	}
	close(service.launchReady)
	waitForLaunchState(t, win.launch, guimodel.LaunchRunning)
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

func TestPlayModeSkipsLauncherWhenInstalled(t *testing.T) {
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
	win := newMainWindow(service, brandIcon())
	win.startInMode(guiModePlay, "")
	qt.QCoreApplication_ProcessEvents()

	select {
	case <-service.launchEntered:
	case <-time.After(time.Second):
		t.Fatal("play mode did not start the client")
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

func (visualService) Snapshot(context.Context) (guimodel.InstallSnapshot, error) {
	return guimodel.InstallSnapshot{
		Installed: true,
		Version:   "2.734.917 · synthetic",
		Status:    "Verified official Android x86-64 client ready.",
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
	return guimodel.Settings{Renderer: guimodel.RendererAuto, FPSMode: guimodel.FPSLimited, FrameRate: 60}, nil
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
