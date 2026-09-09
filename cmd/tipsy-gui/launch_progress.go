package main

import (
	"context"

	qt "github.com/mappu/miqt/qt6"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type launchOrigin int

const (
	launchFromHome launchOrigin = iota
	launchFromExternal
)

type launchPreparation struct {
	authority guimodel.LaunchAuthority
	err       error
}

type launchProgressWindow struct {
	dialog                   *qt.QDialog
	title, detail, authority *qt.QLabel
	note                     *qt.QLabel
	busy                     *qt.QProgressBar
	actions                  *qt.QWidget
	active                   bool
}

const safeLaunchFailure = "Roblox could not start. Open Tipsy to check Installation and Diagnostics, then try again."
const safeRuntimeFailure = "Roblox closed unexpectedly. Open Tipsy to check Diagnostics before trying again."

func (w *mainWindow) ensureLaunchProgress() {
	if w.externalProgress != nil {
		return
	}
	p := &launchProgressWindow{dialog: qt.NewQDialog(w.win.QWidget)}
	p.dialog.SetWindowTitle("Tipsy — Launching Roblox")
	p.dialog.SetWindowIcon(w.icon)
	p.dialog.Resize(420, 280)
	p.dialog.SetMinimumSize2(360, 240)
	p.dialog.SetModal(false)
	layout := qt.NewQVBoxLayout(p.dialog.QWidget)
	layout.SetContentsMargins(28, 24, 28, 24)
	layout.SetSpacing(14)
	logo := qt.NewQLabel2()
	logo.SetPixmap(w.icon.Pixmap2(64, 64))
	logo.SetAlignment(qt.AlignCenter)
	logo.SetAccessibleName("Tipsy logo")
	layout.AddWidget(logo.QWidget)
	p.title = qt.NewQLabel3("Launching Roblox…")
	setObjectName(p.title.QObject, "progressTitle")
	p.title.SetWordWrap(true)
	p.title.SetAlignment(qt.AlignCenter)
	layout.AddWidget(p.title.QWidget)
	p.detail = qt.NewQLabel3("Checking launch authorization…")
	setObjectName(p.detail.QObject, "mutedText")
	p.detail.SetWordWrap(true)
	p.detail.SetAlignment(qt.AlignCenter)
	p.detail.SetAccessibleName("Launch progress")
	layout.AddWidget(p.detail.QWidget)
	p.busy = qt.NewQProgressBar2()
	p.busy.SetRange(0, 0)
	p.busy.SetTextVisible(false)
	p.busy.SetAccessibleName("Roblox startup in progress")
	layout.AddWidget(p.busy.QWidget)
	p.note = qt.NewQLabel3("This window closes when Roblox is ready.")
	p.note.SetWordWrap(true)
	p.note.SetAlignment(qt.AlignCenter)
	setObjectName(p.note.QObject, "mutedText")
	layout.AddWidget(p.note.QWidget)
	p.authority = qt.NewQLabel2()
	p.authority.SetWordWrap(true)
	setObjectName(p.authority.QObject, "noticeWarning")
	p.authority.Hide()
	layout.AddWidget(p.authority.QWidget)
	p.actions = qt.NewQWidget2()
	actions := qt.NewQHBoxLayout(p.actions)
	actions.SetContentsMargins(0, 4, 0, 0)
	actions.AddStretch()
	open := qt.NewQPushButton3("Open Tipsy")
	setObjectName(open.QObject, "primaryButton")
	open.OnClicked(func() { p.dialog.Hide(); w.launchOrigin = launchFromHome; w.Show(); w.selectPage(1) })
	actions.AddWidget(open.QWidget)
	close := qt.NewQPushButton3("Close")
	close.OnClicked(func() { p.dialog.Close() })
	actions.AddWidget(close.QWidget)
	p.actions.Hide()
	layout.AddWidget(p.actions)
	// Startup has no safe cancellation contract. Closing this surface must not
	// terminate an in-process client halfway through initialization.
	p.dialog.OnCloseEvent(func(super func(*qt.QCloseEvent), event *qt.QCloseEvent) {
		if p.active {
			event.Ignore()
			return
		}
		super(event)
	})
	p.dialog.OnReject(func(super func()) {
		if !p.active {
			super()
		}
	})
	w.externalProgress = p
}

func (w *mainWindow) showExternalProgress() {
	w.ensureLaunchProgress()
	p := w.externalProgress
	p.active = true
	p.dialog.SetWindowFlag2(qt.WindowCloseButtonHint, false)
	p.title.SetText("Launching Roblox…")
	p.detail.SetText("Checking launch authorization…")
	p.busy.Show()
	p.note.Show()
	p.actions.Hide()
	p.authority.Hide()
	p.dialog.SetWindowTitle("Tipsy — Launching Roblox")
	placeWidgetOnDisplay(p.dialog.QWidget, configuredDisplay(w))
	p.dialog.Show()
}

// beginLaunch owns only presentation and delegates authority/runtime work to
// the existing service and LaunchModel. The URI remains opaque and is never
// copied into text, diagnostics, tooltips or error messages.
func (w *mainWindow) beginLaunch(origin launchOrigin, request guimodel.LaunchRequest) bool {
	state := w.launch.View().State
	if w.launchInitializing || w.launchPreparing || state == guimodel.LaunchStarting || state == guimodel.LaunchRunning || !w.setup.View().Snapshot.LaunchReady() {
		return false
	}
	w.launchConsentRequested = false
	w.launchOrigin = origin
	w.launchFailure = false
	w.launchRequest = request
	w.launchPreparing = true
	if w.playState != nil {
		setObjectName(w.playState.QObject, "mutedText")
		refreshStyle(w.playState.QWidget)
	}
	if origin == launchFromExternal {
		w.showExternalProgress()
	}
	w.prepareLaunch(false)
	w.refreshLaunchState()
	return true
}

func (w *mainWindow) prepareLaunch(approved bool) {
	result := make(chan launchPreparation, 1)
	w.launchPreparation = result
	// No Qt values cross this goroutine boundary. The GUI timer consumes the
	// result and displays consent on the Qt thread if it is required.
	service := w.service
	go func() {
		authority, err := service.PrepareLaunch(context.Background(), approved)
		result <- launchPreparation{authority, err}
	}()
}

func (w *mainWindow) pollLaunchPreparation() {
	if !w.launchPreparing || w.launchPreparation == nil {
		return
	}
	select {
	case result := <-w.launchPreparation:
		w.launchPreparation = nil
		w.setLaunchAuthority(result.authority)
		if result.authority.DevelopmentConsentRequired {
			if w.launchConsentRequested {
				w.launchPreparing = false
				w.launchRequest = guimodel.LaunchRequest{}
				w.showLaunchFailure(safeLaunchFailure)
				return
			}
			w.launchConsentRequested = true
			parent := w.win.QWidget
			if w.launchOrigin == launchFromExternal {
				parent = w.externalProgress.dialog.QWidget
				w.externalProgress.detail.SetText("Your approval is required to continue.")
			}
			if !confirmDevelopmentLaunch(parent) {
				w.launchPreparing = false
				w.launchRequest = guimodel.LaunchRequest{}
				w.showLaunchFailure("Launch was not authorized. Open Tipsy when you are ready to review the development-build approval.")
				return
			}
			w.prepareLaunch(true)
			return
		}
		w.launchPreparing = false
		if result.err != nil {
			w.launchRequest = guimodel.LaunchRequest{}
			w.showLaunchFailure(safeLaunchFailure)
			return
		}
		request := w.launchRequest
		w.launchRequest = guimodel.LaunchRequest{}
		if err := w.launch.StartRequest(context.Background(), request); err != nil {
			w.showLaunchFailure(safeLaunchFailure)
			return
		}
		w.lastLaunchState = guimodel.LaunchStarting
		if w.launchOrigin == launchFromExternal {
			w.externalProgress.detail.SetText("Waiting for Roblox to open its window…")
			if result.authority.Mode == string(setupsvc.DevelopmentUnrestricted) {
				w.externalProgress.authority.SetText("DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session.")
				w.externalProgress.authority.Show()
			}
		}
	default:
	}
}

func (w *mainWindow) showLaunchFailure(message string) {
	w.launchFailure = true
	w.launcherHidden = false
	qt.QGuiApplication_SetQuitOnLastWindowClosed(true)
	if w.launchOrigin == launchFromExternal {
		w.ensureLaunchProgress()
		p := w.externalProgress
		p.active = false
		p.dialog.SetWindowFlag2(qt.WindowCloseButtonHint, true)
		p.dialog.SetWindowTitle("Tipsy — Launch could not complete")
		if w.launch.View().Started {
			p.title.SetText("Roblox closed unexpectedly")
		} else {
			p.title.SetText("Could not launch Roblox")
		}
		p.detail.SetText(message)
		p.busy.Hide()
		p.note.Hide()
		p.actions.Show()
		p.dialog.Show()
	} else {
		w.Show()
		w.playState.SetText(message)
		setObjectName(w.playState.QObject, "noticeError")
		w.playState.Show()
		refreshStyle(w.playState.QWidget)
	}
	w.status.ShowMessage2("Roblox could not be launched.", 6000)
}

// External starts paint before package verification or settings loading. Models
// are built privately by the worker and transferred to the GUI thread once,
// avoiding concurrent access to SettingsModel's presentation state.
type launchInitialization struct {
	setup                 *guimodel.SetupModel
	settings              *guimodel.SettingsModel
	setupErr, settingsErr error
}

func (w *mainWindow) startExternalInitialization(uri string) {
	if w.launchInitializing || w.launchPreparing || w.launch.View().State != guimodel.LaunchIdle {
		return
	}
	w.launchInitializing = true
	w.launchOrigin = launchFromExternal
	w.launchRequest = guimodel.LaunchRequest{URI: uri}
	w.showExternalProgress()
	w.externalProgress.detail.SetText("Checking the installed client…")
	result := make(chan launchInitialization, 1)
	w.launchInitialization = result
	service := w.service
	go func() {
		setup := guimodel.NewSetupModel(service)
		settings := guimodel.NewSettingsModel(service)
		setupErr := setup.Load(context.Background())
		settingsErr := settings.Load(context.Background())
		result <- launchInitialization{setup, settings, setupErr, settingsErr}
	}()
}

func (w *mainWindow) pollLaunchInitialization() {
	select {
	case result := <-w.launchInitialization:
		w.launchInitializing = false
		w.launchInitialization = nil
		w.setup, w.settings = result.setup, result.settings
		w.setupLoadErr, w.settingsErr = result.setupErr, result.settingsErr
		w.finishShell()
		request := w.launchRequest
		w.launchRequest = guimodel.LaunchRequest{}
		if w.FirstRun() {
			w.externalProgress.active = false
			w.externalProgress.dialog.Hide()
			w.launchOrigin = launchFromHome
			w.Show()
			w.ShowSetupWizard(true)
			return
		}
		w.beginLaunch(launchFromExternal, request)
	default:
	}
}
