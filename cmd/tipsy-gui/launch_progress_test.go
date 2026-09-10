package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type progressService struct {
	visualService
	snapshotGate, prepareGate, launchGate, finish chan struct{}
	requests                                      chan guimodel.LaunchRequest
	prepareError, launchError                     error
	snapshots, preparations, launches             atomic.Int32
	consent                                       bool
	missing                                       bool
	failAfterStart                                bool
}

func newProgressService() *progressService {
	return &progressService{snapshotGate: make(chan struct{}), prepareGate: make(chan struct{}), launchGate: make(chan struct{}), finish: make(chan struct{}), requests: make(chan guimodel.LaunchRequest, 1)}
}
func (s *progressService) Snapshot(ctx context.Context) (guimodel.InstallSnapshot, error) {
	s.snapshots.Add(1)
	<-s.snapshotGate
	if s.missing {
		return guimodel.InstallSnapshot{Readiness: setupsvc.ReadinessNotInstalled}, nil
	}
	return s.visualService.Snapshot(ctx)
}
func (s *progressService) PrepareLaunch(context.Context, bool) (guimodel.LaunchAuthority, error) {
	s.preparations.Add(1)
	<-s.prepareGate
	if s.consent {
		return guimodel.LaunchAuthority{DevelopmentConsentRequired: true}, errors.New("opaque-auth-sentinel")
	}
	return guimodel.LaunchAuthority{Mode: "official-verified"}, s.prepareError
}
func (s *progressService) Launch(_ context.Context, request guimodel.LaunchRequest, started func()) error {
	s.launches.Add(1)
	s.requests <- request
	<-s.launchGate
	if s.launchError != nil && !s.failAfterStart {
		return s.launchError
	}
	started()
	<-s.finish
	return s.launchError
}

// All displayed strings and captures are synthetic; opaque URI sentinels are
// asserted by equality only and never included in test errors or screenshots.
func TestExternalLaunchProgress(t *testing.T) {
	if os.Getenv("TIPSY_GUI_PROGRESS_X11") != "1" {
		t.Setenv("QT_QPA_PLATFORM", "offscreen")
	}
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	app := qt.NewQApplication([]string{"tipsy-progress-test"})
	defer app.Delete()
	qt.QApplication_SetStyleWithStyle("Fusion")
	if os.Getenv("TIPSY_GUI_PROGRESS_X11") == "1" && qt.QGuiApplication_PlatformName() != "xcb" {
		t.Fatal("expected actual X11 widgets")
	}
	const opaqueURI = "roblox://opaque-launch-sentinel?ticket=private-test-sentinel"
	for _, scenario := range []string{"ready", "startup-failed", "runtime-failed", "authorization-failed", "consent-declined", "not-installed"} {
		func() {
			service := newProgressService()
			if scenario == "startup-failed" || scenario == "runtime-failed" {
				service.failAfterStart = scenario == "runtime-failed"
				service.launchError = errors.New(opaqueURI)
			}
			if scenario == "authorization-failed" {
				service.prepareError = errors.New(opaqueURI)
			}
			service.consent = scenario == "consent-declined"
			service.missing = scenario == "not-installed"
			win := newWindowBase(service, brandIcon())
			defer win.win.Delete()
			originalConfirm := confirmDevelopmentLaunch
			defer func() { confirmDevelopmentLaunch = originalConfirm }()
			confirmDevelopmentLaunch = func(parent *qt.QWidget) bool {
				if parent.UnsafePointer() != win.externalProgress.dialog.QWidget.UnsafePointer() {
					t.Fatal("external consent was not parented to progress")
				}
				return false
			}
			if mode := appearanceMode(os.Getenv("TIPSY_GUI_TEST_APPEARANCE")); validAppearance(mode) {
				win.chooseAppearance(mode)
			}
			win.startExternalInitialization(opaqueURI)
			p := win.externalProgress
			if p == nil || !p.dialog.IsVisible() || win.win.IsVisible() || win.playButton != nil {
				t.Fatal("progress did not show before package verification")
			}
			win.startExternalInitialization(opaqueURI)
			pulses := 0
			timer := qt.NewQTimer2(win.win.QObject)
			timer.OnTimeout(func() { pulses++ })
			timer.Start(5)
			waitGUI(t, func() bool { return pulses >= 3 && service.snapshots.Load() == 1 })
			p.dialog.Close()
			p.dialog.Reject()
			qt.QCoreApplication_ProcessEvents()
			if !p.dialog.IsVisible() || !p.active {
				t.Fatal("active verification window could be dismissed")
			}

			if scenario == "not-installed" {
				setupShown := false
				observer := qt.NewQTimer2(win.win.QObject)
				observer.OnTimeout(func() {
					if modal := qt.QApplication_ActiveModalWidget(); modal != nil {
						if p.dialog.IsVisible() || !win.win.IsVisible() {
							t.Fatal("setup appeared beside a duplicate progress window")
						}
						setupShown = true
						modal.Close()
					}
				})
				observer.Start(5)
				close(service.snapshotGate)
				waitGUI(t, func() bool { return setupShown })
				observer.Stop()
				if service.preparations.Load() != 0 || service.launches.Load() != 0 || win.launchRequest.URI != "" {
					t.Fatal("unready external launch retained URI or reached authorization")
				}
				win.win.Close()
				return
			}
			close(service.snapshotGate)
			waitGUI(t, func() bool { return service.preparations.Load() == 1 })
			if win.beginLaunch(launchFromExternal, guimodel.LaunchRequest{URI: opaqueURI}) {
				t.Fatal("duplicate authorization accepted")
			}
			p.dialog.Close()
			p.dialog.Reject()
			if !p.dialog.IsVisible() {
				t.Fatal("active authorization window could be dismissed")
			}
			close(service.prepareGate)
			if scenario == "authorization-failed" || scenario == "consent-declined" {
				waitGUI(t, func() bool { return win.launchFailure })
				if service.launches.Load() != 0 {
					t.Fatal("failed authorization reached runtime")
				}
				assertProgressFailure(t, win, opaqueURI)
				captureProgress(t, p.dialog.QWidget, scenario)
				p.dialog.Close()
				if p.dialog.IsVisible() {
					t.Fatal("failure window could not close")
				}
				return
			}
			waitGUI(t, func() bool { return service.launches.Load() == 1 })
			request := <-service.requests
			if request.URI != opaqueURI {
				t.Fatal("launch URI was changed")
			}
			if !p.dialog.IsVisible() || win.win.IsVisible() || !p.busy.IsVisible() || p.actions.IsVisible() {
				t.Fatal("external Starting visibility is wrong")
			}
			if win.beginLaunch(launchFromExternal, guimodel.LaunchRequest{URI: opaqueURI}) {
				t.Fatal("duplicate runtime launch accepted")
			}
			if win.launchRequest.URI != "" {
				t.Fatal("UI retained URI after handoff")
			}
			captureProgress(t, p.dialog.QWidget, "external-launching")
			p.dialog.Resize(360, 240)
			qt.QCoreApplication_ProcessEvents()
			for _, label := range []*qt.QLabel{p.title, p.detail, p.note} {
				if label.Height() < label.HeightForWidth(label.Width()) || label.Y()+label.Height() > p.dialog.Height() {
					t.Fatal("compact progress text is clipped")
				}
			}
			captureProgress(t, p.dialog.QWidget, "external-launching-compact")
			p.dialog.Resize(420, 280)

			if scenario == "startup-failed" {
				close(service.launchGate)
				waitGUI(t, func() bool { return win.launchFailure })
				assertProgressFailure(t, win, opaqueURI)
				captureProgress(t, p.dialog.QWidget, scenario)
				p.dialog.Close()
				return
			}
			close(service.launchGate)
			waitGUI(t, func() bool { return win.launcherHidden })
			if p.dialog.IsVisible() || win.win.IsVisible() || win.launch.View().State != guimodel.LaunchRunning {
				t.Fatal("progress did not hide on Started acknowledgment")
			}
			if service.launches.Load() != 1 || service.preparations.Load() != 1 {
				t.Fatal("external launch was not once-only")
			}
			close(service.finish)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := win.launch.Wait(ctx); err != nil {
				t.Fatal("synthetic launch did not finish")
			}
			if scenario == "runtime-failed" {
				waitGUI(t, func() bool { return win.launchFailure })
				assertProgressFailure(t, win, opaqueURI)
				if p.detail.Text() != safeRuntimeFailure {
					t.Fatal("post-start failure copy was not accurate")
				}
				p.dialog.Close()
			}

			timer.Stop()
		}()
	}
	// Clicking Play in an existing launcher must never create another window,
	// and failures must restore an actionable, URI-free home surface.
	service := newProgressService()
	close(service.snapshotGate)
	close(service.prepareGate)
	service.launchError = errors.New(opaqueURI)
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()
	win.Show()
	win.playButton.Click()
	waitGUI(t, func() bool { return service.launches.Load() == 1 })
	if win.externalProgress != nil || !win.win.IsVisible() || !win.launchBusy.IsVisible() || win.playButton.IsEnabled() || win.playButton.Text() != "Launching…" {
		t.Fatal("in-GUI launch did not stay in the existing window")
	}
	if win.launchRoblox() {
		t.Fatal("duplicate in-GUI launch accepted")
	}
	win.win.Close()
	if !win.win.IsVisible() {
		t.Fatal("in-GUI startup could be dismissed during initialization")
	}
	captureProgress(t, win.win.QWidget, "home-launching")
	win.win.Resize(640, 560)
	qt.QCoreApplication_ProcessEvents()
	captureProgress(t, win.win.QWidget, "home-launching-compact")
	close(service.launchGate)
	waitGUI(t, func() bool { return win.launchFailure })
	if !win.win.IsVisible() || win.launchBusy.IsVisible() || !win.playButton.IsEnabled() || win.playState.Text() != safeLaunchFailure {
		t.Fatal("failed in-GUI launch did not restore safe home state")
	}
	if strings.Contains(win.playState.Text(), "private-test-sentinel") {
		t.Fatal("raw launch failure escaped into home")
	}
	win.win.Close()

}

func TestSettingsStartupVerifiesBeforeShell(t *testing.T) {
	if os.Getenv("TIPSY_GUI_PROGRESS_X11") != "1" {
		t.Setenv("QT_QPA_PLATFORM", "offscreen")
	}
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	app := qt.NewQApplication([]string{"tipsy-settings-startup-test"})
	defer app.Delete()
	qt.QApplication_SetStyleWithStyle("Fusion")

	service := newProgressService()
	win := newWindowBase(service, brandIcon())
	defer win.win.Delete()
	win.startSettingsInitialization()
	p := win.externalProgress
	if p == nil || !p.dialog.IsVisible() || win.playButton != nil || win.win.IsVisible() {
		t.Fatal("settings startup did not paint the verifying surface first")
	}
	if !strings.Contains(p.detail.Text(), "Checking") {
		t.Fatalf("verifying copy = %q", p.detail.Text())
	}
	pulses := 0
	timer := qt.NewQTimer2(win.win.QObject)
	timer.OnTimeout(func() { pulses++ })
	timer.Start(5)
	waitGUI(t, func() bool { return pulses >= 3 && service.snapshots.Load() == 1 })
	if win.playButton != nil {
		t.Fatal("shell was built before verification completed")
	}
	close(service.snapshotGate)
	waitGUI(t, func() bool {
		return win.playButton != nil && win.win.IsVisible() && !p.dialog.IsVisible()
	})
	if win.FirstRun() {
		t.Fatal("synthetic launch-ready client was treated as first run")
	}
	if win.preferenceFPS.Text() != "60 FPS" {
		t.Fatalf("settings were not applied to the shell: %q", win.preferenceFPS.Text())
	}
	timer.Stop()
	win.win.Close()
}

func assertProgressFailure(t *testing.T, win *mainWindow, sentinel string) {
	t.Helper()
	p := win.externalProgress
	if !p.dialog.IsVisible() || win.win.IsVisible() || p.active || p.busy.IsVisible() || !p.actions.IsVisible() {
		t.Fatal("failure surface is not actionable")
	}
	for _, text := range []string{p.title.Text(), p.detail.Text(), p.authority.Text(), win.playState.Text(), win.playAuthority.Text()} {
		if strings.Contains(text, sentinel) || strings.Contains(text, "private-test-sentinel") || strings.Contains(text, "opaque-auth-sentinel") {
			t.Fatal("opaque launch data escaped into UI")
		}
	}
}
func captureProgress(t *testing.T, widget *qt.QWidget, name string) {
	t.Helper()
	dir := os.Getenv("TIPSY_GUI_PROGRESS_SCREENSHOT_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	qt.QCoreApplication_ProcessEvents()
	pixmap := widget.Grab()
	if pixmap.IsNull() {
		t.Fatal("empty progress capture")
	}
	if os.Getenv("TIPSY_GUI_PROGRESS_HIDPI") == "1" && pixmap.DevicePixelRatio() < 1.5 {
		t.Fatal("progress HiDPI scaling missing")
	}
	if !pixmap.Save2(filepath.Join(dir, name+".png"), "PNG") {
		t.Fatal("save progress capture")
	}
}
