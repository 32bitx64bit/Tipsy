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
)

// TestX11Acceptance is an opt-in, window-only acceptance pass. It must be run
// on a real X11 display with a synthetic XDG root; it never invokes the Roblox
// runtime or a package provider.
func TestX11Acceptance(t *testing.T) {
	if os.Getenv("TIPSY_GUI_X11_ACCEPTANCE") != "1" {
		t.Skip("set TIPSY_GUI_X11_ACCEPTANCE=1 on an isolated X11 display")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Fatal("DISPLAY must name the acceptance X11 display")
	}

	root := t.TempDir()
	for _, item := range []struct{ key, dir string }{
		{"XDG_CONFIG_HOME", "config"},
		{"XDG_DATA_HOME", "data"},
		{"XDG_CACHE_HOME", "cache"},
		{"XDG_STATE_HOME", "state"},
	} {
		t.Setenv(item.key, filepath.Join(root, item.dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))

	app := qt.NewQApplication([]string{"tipsy-gui-x11-acceptance"})
	defer app.Delete()
	qt.QApplication_SetStyleWithStyle("Fusion")
	app.SetStyleSheet(appStyleSheet)
	if platform := qt.QGuiApplication_PlatformName(); platform != "xcb" {
		t.Fatalf("Qt platform=%q, want real X11 xcb", platform)
	}

	service := newAcceptanceService()
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()
	win.Show()
	pumpEvents()
	if os.Getenv("TIPSY_GUI_ACCEPTANCE_HIDPI") == "1" {
		pixmap := win.win.Grab()
		if ratio := pixmap.DevicePixelRatio(); ratio < 1.25 {
			t.Fatalf("HiDPI case has device pixel ratio %.2f, want at least 1.25", ratio)
		}
	}
	if !win.FirstRun() {
		t.Fatal("synthetic empty installation did not require first-run setup")
	}

	testWizardSurface(t, win, service, root)
	testMainSurface(t, win, service)

	win.win.Close()
	pumpEvents()
	if _, err := os.Stat(filepath.Join(root, "install-residue")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel/retry left synthetic installation residue: %v", err)
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if !strings.HasPrefix(os.Getenv(key), root+string(os.PathSeparator)) {
			t.Fatalf("%s escaped synthetic root: %s", key, os.Getenv(key))
		}
	}
}

func testWizardSurface(t *testing.T, win *mainWindow, service *acceptanceService, root string) {
	t.Helper()
	wizard := qt.NewQWizard(win.win.QWidget)
	defer wizard.Delete()
	wizard.SetWindowTitle("Set up Tipsy")
	wizard.SetWizardStyle(qt.QWizard__ModernStyle)
	wizard.SetTitleFormat(qt.RichText)
	wizard.SetSubTitleFormat(qt.RichText)
	wizard.Resize(900, 610)
	wizard.SetMinimumSize2(720, 520)
	sidebar := win.buildWizardSide(true)
	wizard.SetSideWidget(sidebar.widget)
	welcomeID := wizard.AddPage(win.buildWelcomeWizardPage(true))
	doctorID := wizard.AddPage(win.buildDoctorWizardPage())
	source, automatic, local, localPaths, choose, selected := win.buildSourceWizardPage()
	sourceID := wizard.AddPage(source)
	install, progress, phase, message, errorLabel, cancelInstall, retry := win.buildInstallWizardPage()
	installID := wizard.AddPage(install)
	readyID := wizard.AddPage(win.buildReadyWizardPage(wizard))
	styleWizardButtons(wizard)
	updateWizardSteps(sidebar.steps, 0)
	wizard.OnCurrentIdChanged(func(id int) {
		for index, pageID := range []int{welcomeID, doctorID, sourceID, installID, readyID} {
			if id == pageID {
				updateWizardSteps(sidebar.steps, index)
				return
			}
		}
	})
	wizard.Show()
	pumpEvents()
	if wizard.CurrentId() != welcomeID || !wizard.IsVisible() {
		t.Fatal("first-run wizard did not open on Welcome")
	}
	captureAtSizes(t, wizard.QWidget, "wizard-welcome")
	wizard.SetCurrentId(doctorID)
	pumpEvents()
	captureAtSizes(t, wizard.QWidget, "wizard-doctor")
	wizard.SetCurrentId(sourceID)
	pumpEvents()
	captureAtSizes(t, wizard.QWidget, "wizard-source-unavailable")
	if automatic.IsEnabled() || !local.IsChecked() || !choose.IsEnabled() {
		t.Fatalf("source controls automaticEnabled=%v localChecked=%v chooseEnabled=%v", automatic.IsEnabled(), local.IsChecked(), choose.IsEnabled())
	}
	if text := automaticExplanation(win.setup.View().Automatic); !strings.Contains(text, "Unavailable:") || !strings.Contains(text, "lawful") {
		t.Fatalf("automatic source reason is not actionable: %q", text)
	}
	local.SetFocus()
	pumpEvents()
	if got := qt.QApplication_FocusWidget().AccessibleName(); got != local.AccessibleName() {
		t.Fatalf("local source did not accept keyboard focus: %q", got)
	}
	sendTab(t)
	if got := qt.QApplication_FocusWidget().AccessibleName(); got != choose.AccessibleName() {
		t.Fatalf("Tab order after local source=%q, want %q", got, choose.AccessibleName())
	}

	wizard.SetCurrentId(installID)
	win.resetWizardPage(install)
	phase.SetText("Verifying package")
	message.SetText("Checking package identity, signature, x86-64 architecture, and extraction safety. This synthetic fixture never reads account data.")
	progress.SetValue(45)
	errorLabel.Hide()
	cancelInstall.Show()
	retry.Hide()
	pumpEvents()
	captureAtSizes(t, wizard.QWidget, "wizard-install-progress")

	phase.SetText("Installation stopped")
	message.SetText("The selected package could not be installed.")
	errorLabel.SetText("The package signature could not be verified. Choose another official package and try again; the existing installation and account data were not changed.")
	setObjectName(errorLabel.QObject, "noticeError")
	refreshStyle(errorLabel.QWidget)
	errorLabel.Show()
	cancelInstall.Hide()
	retry.Show()
	pumpEvents()
	win.resetWizardPage(install)
	pumpEvents()
	captureAtSizes(t, wizard.QWidget, "wizard-install-error")

	phase.SetText("Ready")
	message.SetText("Roblox is installed and ready to launch.")
	progress.SetValue(100)
	errorLabel.SetText("Package identity verified. Installation is ready.")
	setObjectName(errorLabel.QObject, "noticeSuccess")
	refreshStyle(errorLabel.QWidget)
	errorLabel.Show()
	retry.Hide()
	pumpEvents()
	win.resetWizardPage(install)
	pumpEvents()
	captureAtSizes(t, wizard.QWidget, "wizard-install-ready")

	wizard.SetCurrentId(readyID)
	win.resetWizardPage(wizard.Page(readyID))
	pumpEvents()
	captureAtSizes(t, wizard.QWidget, "wizard-ready")
	wizard.SetCurrentId(sourceID)
	pumpEvents()

	missing := filepath.Join(root, "missing-official-client.apk")
	*localPaths = []string{missing}
	selected.SetText("Selected: " + filepath.Base(missing))
	selected.Show()
	if err := win.setup.SetRequest(guimodel.InstallRequest{Mode: guimodel.InstallLocal, LocalPaths: *localPaths}); err != nil {
		t.Fatal(err)
	}
	service.setInstallBehavior(installFails)
	if err := win.setup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSetup(t, win.setup)
	if view := win.setup.View(); view.State != guimodel.SetupFailed || !strings.Contains(view.Error, "does not exist") {
		t.Fatalf("safe invalid path did not produce actionable retry state: %+v", view)
	}

	service.setInstallBehavior(installWaitsForCancel)
	if err := win.setup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.waitForInstallStart(t)
	if !win.setup.Cancel() {
		t.Fatal("cancel did not reach running synthetic install")
	}
	waitForSetup(t, win.setup)
	if view := win.setup.View(); view.State != guimodel.SetupCancelled || !strings.Contains(view.Error, "No account information was changed") {
		t.Fatalf("cancel state is not safe/actionable: %+v", view)
	}

	service.setInstallBehavior(installSucceeds)
	if err := win.setup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForSetup(t, win.setup)
	if view := win.setup.View(); view.State != guimodel.SetupComplete || !view.Snapshot.Installed {
		t.Fatalf("retry did not complete: %+v", view)
	}
	wizard.Close()
	pumpEvents()
}

func testMainSurface(t *testing.T, win *mainWindow, service *acceptanceService) {
	t.Helper()
	win.refreshSnapshot()
	pumpEvents()
	for page, name := range []string{"Home", "Installation", "Settings", "Diagnostics"} {
		win.selectPage(page)
		pumpEvents()
		if win.stack.CurrentIndex() != page || !win.nav[page].IsChecked() {
			t.Fatalf("%s navigation did not select page %d", name, page)
		}
		if focused := qt.QApplication_FocusWidget(); focused == nil || focused.UnsafePointer() != win.pageEntry[page].UnsafePointer() {
			t.Fatalf("%s page switch focus=%v, want first useful control", name, focused)
		}
		captureMainPageAtSizes(t, win, page, strings.ToLower(name)+"-idle")
	}
	if !strings.Contains(win.doctorDetails.ToPlainText(), "X11 display") || !strings.Contains(win.doctorDetails.ToPlainText(), "intentionally long synthetic diagnostic") {
		t.Fatal("Diagnostics did not render doctor details")
	}
	if win.installPageBadge.Text() != "READY" || !strings.Contains(win.installPageVer.Text(), "2.734.917") || !strings.Contains(win.installPageDetail.Text(), "Verified synthetic") {
		t.Fatalf("Installation page status did not refresh: badge=%q version=%q detail=%q", win.installPageBadge.Text(), win.installPageVer.Text(), win.installPageDetail.Text())
	}

	win.win.QWidget.Raise()
	win.win.QWidget.ActivateWindow()
	qt.QApplication_SetActiveWindow(win.win.QWidget)
	win.nav[0].SetFocusWithReason(qt.TabFocusReason)
	pumpEvents()
	if got := qt.QApplication_FocusWidget().AccessibleName(); got != "Home" {
		t.Fatalf("main keyboard focus did not start on Home: %q", got)
	}
	if got := win.nav[0].NextInFocusChain().AccessibleName(); got != "Installation" {
		t.Fatalf("declared focus chain after Home=%q, want Installation", got)
	}
	sendTab(t)
	focused := qt.QApplication_FocusWidget()
	if focused == nil || !focused.IsVisible() || !focused.IsEnabled() || focused.FocusPolicy() == qt.NoFocus {
		t.Fatalf("main Tab traversal reached unusable widget class=%q object=%q", focused.MetaObject().ClassName(), focused.ObjectName())
	}
	win.nav[2].Click()
	pumpEvents()
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Rendering backend" {
		t.Fatalf("Settings sidebar activation focus=%q, want Rendering backend", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Frame-rate mode" {
		t.Fatalf("Settings page Tab focus=%q, want Frame-rate mode", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "VSync" {
		t.Fatalf("Settings page Tab focus=%q, want VSync", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Low texture mode" {
		t.Fatalf("Settings page Tab focus=%q, want Low texture mode", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Discord Rich Presence" {
		t.Fatalf("Settings page Tab focus=%q, want Discord Rich Presence", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Show Join button" {
		t.Fatalf("Settings page Tab focus=%q, want Show Join button", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Default monitor" {
		t.Fatalf("Settings page Tab focus=%q, want Default monitor", focused.AccessibleName())
	}
	sendTab(t)
	if focused := qt.QApplication_FocusWidget(); focused == nil || focused.AccessibleName() != "Start Roblox fullscreen" {
		t.Fatalf("Settings page Tab focus=%q, want Start Roblox fullscreen", focused.AccessibleName())
	}

	win.selectPage(2)
	if win.settingsApply.IsEnabled() || win.settingsRenderer.CurrentText() != "Auto (recommended)" || win.settingsVSync.IsChecked() || win.settingsLowTexture.IsChecked() || !win.settingsDiscordPresence.IsChecked() || win.settingsDiscordJoin.IsChecked() || win.settingsDisplay.CurrentText() != "Main monitor (default)" || win.settingsStartFullscreen.IsChecked() {
		t.Fatal("settings did not start clean on Auto with VSync off, high textures, main monitor, windowed launch, and Apply disabled")
	}
	model := win.settingsRenderer.Model()
	for row, wantEnabled := range []bool{true, true, false} {
		index := model.Index(row, 0, qt.NewQModelIndex())
		enabled := model.Flags(index)&qt.ItemIsEnabled != 0
		if enabled != wantEnabled {
			t.Fatalf("renderer row %d enabled=%v want %v", row, enabled, wantEnabled)
		}
	}
	win.settingsRenderer.SetCurrentIndex(1)
	win.settingsFPSMode.SetCurrentIndex(1)
	win.settingsFPS.SetValue(240)
	win.settingsVSync.SetChecked(true)
	win.settingsStartFullscreen.SetChecked(true)
	win.settingsEdited()
	if !win.settingsFPS.IsEnabled() || !win.settingsApply.IsEnabled() || !strings.Contains(win.settingsHint.Text(), "restart") {
		t.Fatal("valid OpenGL/240 FPS edit did not enable Apply with restart notice")
	}
	win.settingsApply.Click()
	pumpEvents()
	if win.settingsApply.IsEnabled() || !strings.Contains(win.settingsHint.Text(), "saved by synthetic backend") || !strings.Contains(win.settingsHint.Text(), "Restart") {
		t.Fatalf("Apply state/note incorrect: %q", win.settingsHint.Text())
	}
	if settings, err := service.LoadSettings(context.Background()); err != nil || !settings.VSync || !settings.StartFullscreen || settings.FPSMode != guimodel.FPSLimited {
		t.Fatalf("VSync/fullscreen did not round-trip independently through Apply: settings=%+v err=%v", settings, err)
	}
	if got := win.settingsProfile.Text(); got != "OpenGL · 240 FPS · VSync on · High textures · Discord · Main monitor · Fullscreen on launch" {
		t.Fatalf("Home profile did not refresh after Apply: %q", got)
	}
	captureMainPageAtSizes(t, win, 2, "settings-limited")
	win.settingsReset.Click()
	pumpEvents()
	if win.settingsVSync.IsChecked() {
		t.Fatal("Reset defaults left VSync enabled")
	}
	if win.settingsLowTexture.IsChecked() {
		t.Fatal("Reset defaults left low texture mode enabled")
	}
	if win.settingsStartFullscreen.IsChecked() {
		t.Fatal("Reset defaults left Roblox fullscreen start enabled")
	}
	if got := win.settingsProfile.Text(); got != "Auto renderer · Automatic FPS · VSync off · High textures · Discord · Main monitor · Windowed on launch" {
		t.Fatalf("Home profile did not refresh after Reset: %q", got)
	}
	win.settingsFPSMode.SetCurrentIndex(2)
	win.settingsEdited()
	if win.settingsFPS.IsEnabled() || !win.settingsApply.IsEnabled() || !strings.Contains(win.settingsHint.Text(), "experimental uncapped request") || !strings.Contains(win.settingsHint.Text(), "no frame rate is guaranteed") {
		t.Fatal("Unlimited mode did not expose honest experimental request copy and semantic disabled limit")
	}
	if got := fpsDisplay(win.settings.View().Draft); got != "Uncapped request (experimental)" {
		t.Fatalf("Unlimited summary=%q", got)
	}
	win.settingsRenderer.SetCurrentIndex(2)
	win.settingsEdited()
	if win.settingsApply.IsEnabled() || !strings.Contains(win.settingsHint.Text(), "not available") {
		t.Fatalf("unavailable Vulkan could be applied or lacked reason: %q", win.settingsHint.Text())
	}
	captureMainPageAtSizes(t, win, 2, "settings-unlimited-vulkan-disabled")

	win.selectPage(0)
	win.win.Resize(1080, 720)
	pumpEvents()
	if !win.playButton.IsEnabled() {
		t.Fatal("Play stayed disabled after synthetic verified install")
	}
	service.prepareLaunch()
	win.playButton.Click()
	service.waitForLaunchStart(t)
	win.refreshLaunchState()
	if win.playButton.IsEnabled() || win.playButton.Text() != "Launching…" || !strings.Contains(win.playState.Text(), "X11 window") {
		t.Fatal("Play did not enter asynchronous launching state")
	}
	captureMainPageAtSizes(t, win, 0, "home-launching")
	service.acknowledgeLaunch()
	waitForLaunchState(t, win.launch, guimodel.LaunchRunning)
	win.refreshLaunchState()
	if win.win.IsVisible() || !win.launcherHidden {
		t.Fatal("launcher stayed visible after the synthetic client acknowledged startup")
	}
	service.finishLaunch()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := win.launch.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	win.refreshLaunchState()
	if win.launch.View().State != guimodel.LaunchExited || service.launchCalls() != 1 {
		t.Fatal("synthetic Play did not record a clean client exit exactly once")
	}
}

func waitForLaunchState(t *testing.T, model *guimodel.LaunchModel, want guimodel.LaunchState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if model.View().State == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("launch state=%s, want %s", model.View().State, want)
}

type acceptanceSize struct {
	width  int
	height int
	name   string
}

var acceptanceSizes = []acceptanceSize{
	{width: 780, height: 560, name: "780x560"},
	{width: 1080, height: 720, name: "1080x720"},
	{width: 1440, height: 900, name: "1440x900"},
}

func acceptanceSizesForRun() []acceptanceSize {
	if os.Getenv("TIPSY_GUI_ACCEPTANCE_HIDPI") == "1" {
		return []acceptanceSize{{width: 1080, height: 720, name: "hidpi"}}
	}
	return acceptanceSizes
}

func captureAtSizes(t *testing.T, widget *qt.QWidget, prefix string) {
	t.Helper()
	for _, size := range acceptanceSizesForRun() {
		widget.Resize(size.width, size.height)
		pumpEvents()
		if widget.Width() != size.width || widget.Height() != size.height {
			t.Fatalf("%s resize=%dx%d, want %dx%d", prefix, widget.Width(), widget.Height(), size.width, size.height)
		}
		saveAcceptanceGrab(t, widget, prefix+"-"+size.name+".png")
	}
}

func captureMainPageAtSizes(t *testing.T, win *mainWindow, page int, prefix string) {
	t.Helper()
	win.selectPage(page)
	for _, size := range acceptanceSizesForRun() {
		win.win.Resize(size.width, size.height)
		win.pages[page].VerticalScrollBar().SetValue(0)
		pumpEvents()
		if os.Getenv("TIPSY_GUI_ACCEPTANCE_HIDPI") == "1" {
			// The X11 work area may cap the main window below the requested
			// logical height once decorations are scaled. It must still retain
			// a useful viewport larger than Tipsy's normal minimum.
			if win.win.Width() < 900 || win.win.Height() < 620 {
				t.Fatalf("%s HiDPI work-area resize collapsed to %dx%d", prefix, win.win.Width(), win.win.Height())
			}
		} else if win.win.Width() != size.width || win.win.Height() != size.height {
			t.Fatalf("%s resize=%dx%d, want %dx%d", prefix, win.win.Width(), win.win.Height(), size.width, size.height)
		}
		saveAcceptanceGrab(t, win.win.QWidget, prefix+"-"+size.name+".png")
		assertMainPageLayout(t, win, page, prefix+"-"+size.name)
	}
}

func assertMainPageLayout(t *testing.T, win *mainWindow, page int, state string) {
	t.Helper()
	scroll := win.pages[page]
	if max := scroll.HorizontalScrollBar().Maximum(); max != 0 {
		t.Fatalf("%s has horizontal overflow %d (content=%d minHint=%d sizeHint=%d viewport=%d)", state, max, scroll.Widget().Width(), scroll.Widget().MinimumSizeHint().Width(), scroll.Widget().SizeHint().Width(), scroll.Viewport().Width())
	}
	content, viewport := scroll.Widget(), scroll.Viewport()
	if content.Width() > 1160 || content.Width() > viewport.Width() {
		t.Fatalf("%s content width=%d viewport=%d max=1160", state, content.Width(), viewport.Width())
	}
	entry := win.pageEntry[page]
	origin := entry.MapTo2(viewport, qt.NewQPoint2(0, 0))
	if origin.X() < 0 || origin.Y() < 0 || origin.X()+entry.Width() > viewport.Width() || origin.Y()+entry.Height() > viewport.Height() {
		t.Fatalf("%s first action is clipped: origin=%d,%d size=%dx%d viewport=%dx%d", state, origin.X(), origin.Y(), entry.Width(), entry.Height(), viewport.Width(), viewport.Height())
	}
}

func pumpEvents() {
	for range 4 {
		qt.QCoreApplication_ProcessEvents()
	}
}

func sendTab(t *testing.T) {
	t.Helper()
	target := qt.QApplication_FocusWidget()
	if target == nil {
		t.Fatal("Tab test has no focused widget")
	}
	press := qt.NewQKeyEvent3(qt.QEvent__KeyPress, int(qt.Key_Tab), qt.NoModifier, "\t")
	qt.QCoreApplication_SendEvent(target.QObject, press.QEvent)
	press.Delete()
	pumpEvents()
}

func waitForSetup(t *testing.T, model *guimodel.SetupModel) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := model.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func saveAcceptanceGrab(t *testing.T, widget *qt.QWidget, name string) {
	t.Helper()
	dir := os.Getenv("TIPSY_GUI_ACCEPTANCE_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pixmap := widget.Grab()
	if pixmap.IsNull() {
		t.Fatalf("window-only X11 grab %s was empty", name)
	}
	if !pixmap.Save2(filepath.Join(dir, name), "PNG") {
		t.Fatalf("save window-only X11 grab %s", name)
	}
}

type installBehavior int

const (
	installFails installBehavior = iota
	installWaitsForCancel
	installSucceeds
)

type acceptanceService struct {
	mu             sync.Mutex
	snapshot       guimodel.InstallSnapshot
	settings       guimodel.Settings
	behavior       installBehavior
	installStarted chan struct{}
	launchStarted  chan struct{}
	launchReady    chan struct{}
	launchDone     chan struct{}
	launchCount    int
}

func newAcceptanceService() *acceptanceService {
	return &acceptanceService{
		snapshot: guimodel.InstallSnapshot{Status: "No official Roblox client is installed."},
		settings: guimodel.DefaultSettings(),
		behavior: installFails,
	}
}

func (s *acceptanceService) Snapshot(context.Context) (guimodel.InstallSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *acceptanceService) Doctor(context.Context) (guimodel.DoctorSummary, error) {
	longDetail := "This intentionally long synthetic diagnostic verifies that detailed display, driver, and package messages wrap without clipping while remaining completely isolated from real account data."
	return guimodel.DoctorSummary{Ready: false, Checks: []guimodel.DoctorCheck{
		{Name: "Architecture", Detail: "x86_64", Status: guimodel.CheckReady},
		{Name: "X11 display", Detail: "xcb synthetic acceptance — " + longDetail, Status: guimodel.CheckReady},
		{Name: "OpenGL / EGL", Detail: "Synthetic test driver needs review.", Status: guimodel.CheckWarning, Remedy: longDetail},
		{Name: "Optional package source", Detail: "Automatic acquisition is unavailable.", Status: guimodel.CheckBlocked, Remedy: "Choose a local official package; this is a deliberately long but safe synthetic remedy used only for layout verification."},
	}}, nil
}

func (s *acceptanceService) AutomaticAvailability(context.Context) guimodel.AutomaticAvailability {
	return guimodel.AutomaticAvailability{Reason: "No lawful, verifiable automatic provider is configured."}
}

func (s *acceptanceService) Install(ctx context.Context, request guimodel.InstallRequest, progress func(guimodel.InstallProgress)) error {
	s.mu.Lock()
	behavior := s.behavior
	started := s.installStarted
	s.mu.Unlock()
	if started != nil {
		close(started)
	}
	progress(guimodel.InstallProgress{Phase: "Verifying package", Message: "Synthetic acceptance only", Percent: 45})
	switch behavior {
	case installWaitsForCancel:
		<-ctx.Done()
		return ctx.Err()
	case installSucceeds:
		s.mu.Lock()
		s.snapshot = guimodel.InstallSnapshot{Installed: true, Version: "2.734.917 · synthetic", Status: "Verified synthetic client ready."}
		s.mu.Unlock()
		return nil
	default:
		return errors.New("selected package does not exist: " + strings.Join(request.LocalPaths, ", "))
	}
}

func (s *acceptanceService) PrepareLaunch(context.Context, bool) (guimodel.LaunchAuthority, error) {
	return guimodel.LaunchAuthority{Mode: "official-verified"}, nil
}

func (s *acceptanceService) Launch(_ context.Context, _ guimodel.LaunchRequest, acknowledge func()) error {
	s.mu.Lock()
	s.launchCount++
	started, ready, done := s.launchStarted, s.launchReady, s.launchDone
	s.mu.Unlock()
	close(started)
	<-ready
	acknowledge()
	<-done
	return nil
}

func (s *acceptanceService) LoadSettings(context.Context) (guimodel.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, nil
}

func (s *acceptanceService) RendererOptions(context.Context) []guimodel.RendererOption {
	return []guimodel.RendererOption{
		{Renderer: guimodel.RendererAuto, Available: true},
		{Renderer: guimodel.RendererOpenGL, Available: true},
		{Renderer: guimodel.RendererVulkan, Reason: "Requires the Vulkan bridge and a working host driver."},
	}
}

func (s *acceptanceService) ApplySettings(_ context.Context, settings guimodel.Settings) (guimodel.ApplyResult, error) {
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return guimodel.ApplyResult{RestartRequired: true, FrameRateNote: "Frame rate saved by synthetic backend."}, nil
}

func (s *acceptanceService) ResetSettings(context.Context) (guimodel.Settings, error) {
	s.mu.Lock()
	s.settings = guimodel.DefaultSettings()
	s.mu.Unlock()
	return guimodel.DefaultSettings(), nil
}

func (s *acceptanceService) setInstallBehavior(behavior installBehavior) {
	s.mu.Lock()
	s.behavior = behavior
	s.installStarted = make(chan struct{})
	s.mu.Unlock()
}

func (s *acceptanceService) waitForInstallStart(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	started := s.installStarted
	s.mu.Unlock()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("synthetic install did not start")
	}
}

func (s *acceptanceService) waitForLaunchStart(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	started := s.launchStarted
	s.mu.Unlock()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("synthetic launch did not start")
	}
}

func (s *acceptanceService) prepareLaunch() {
	s.mu.Lock()
	s.launchStarted = make(chan struct{})
	s.launchReady = make(chan struct{})
	s.launchDone = make(chan struct{})
	s.mu.Unlock()
}

func (s *acceptanceService) acknowledgeLaunch() {
	s.mu.Lock()
	ready := s.launchReady
	s.mu.Unlock()
	close(ready)
}

func (s *acceptanceService) finishLaunch() {
	s.mu.Lock()
	done := s.launchDone
	s.mu.Unlock()
	close(done)
}

func (s *acceptanceService) launchCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchCount
}
