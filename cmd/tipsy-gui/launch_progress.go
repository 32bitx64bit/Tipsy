package main

import (
	"context"

	qt "github.com/mappu/miqt/qt6"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

type launchOrigin int

const (
	launchFromHome launchOrigin = iota
	launchFromExternal
)

// ownerNotifyingService preserves the service boundary used by the GUI models.
type ownerNotifyingService struct {
	guimodel.Service
}

func (s ownerNotifyingService) Launch(ctx context.Context, request guimodel.LaunchRequest, started func()) error {
	return s.Service.Launch(ctx, request, started)
}

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
	cancel                   *qt.QPushButton
	active                   bool
	// onCancel aborts the launch or startup verification behind this dialog.
	// It reports whether cancellation was accepted; an active client that
	// already reached LaunchRunning is never torn down from this surface.
	onCancel func() bool
}

// requestCancel asks the window to abort the in-flight launch and dismisses
// the dialog. It returns false when cancellation is unavailable so close and
// reject handlers can leave the dialog in place instead of losing the only
// visible window.
func (p *launchProgressWindow) requestCancel() bool {
	if !p.active || p.onCancel == nil || !p.onCancel() {
		return false
	}
	p.active = false
	p.cancel.Hide()
	p.dialog.Hide()
	return true
}

const safeLaunchFailure = "Roblox could not start. Open Tipsy to check Installation and Diagnostics, then try again."
const safeRuntimeFailure = "Roblox closed unexpectedly. Open Tipsy to check Diagnostics before trying again."

func (w *mainWindow) ensureLaunchProgress() {
	if w.externalProgress != nil {
		return
	}
	p := &launchProgressWindow{dialog: qt.NewQDialog(w.win.QWidget)}
	p.dialog.SetWindowTitle(guiTitle("Tipsy — Launching Roblox"))
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
	p.cancel = qt.NewQPushButton3("Cancel")
	p.cancel.SetAccessibleName("Cancel Roblox launch")
	p.cancel.SetAccessibleDescription("Stop waiting for startup and return to Tipsy")
	p.cancel.SetToolTip("Cancel startup and return to Tipsy")
	p.cancel.OnClicked(func() { p.requestCancel() })
	p.cancel.Hide()
	busyRow := qt.NewQWidget2()
	busyLayout := qt.NewQHBoxLayout(busyRow)
	busyLayout.SetContentsMargins(0, 0, 0, 0)
	busyLayout.AddWidget2(p.busy.QWidget, 1)
	busyLayout.AddWidget(p.cancel.QWidget)
	layout.AddWidget(busyRow)
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
	// Closing the progress window or pressing Esc cancels the launch. The
	// window manager's close button stays available while startup runs, so a
	// stalled client can never leave an unclosable surface behind.
	p.onCancel = w.cancelActiveLaunch
	p.dialog.OnCloseEvent(func(super func(*qt.QCloseEvent), event *qt.QCloseEvent) {
		if p.active && !p.requestCancel() {
			event.Ignore()
			return
		}
		super(event)
	})
	p.dialog.OnReject(func(super func()) {
		if p.active && !p.requestCancel() {
			return
		}
		super()
	})
	w.externalProgress = p
}

func (w *mainWindow) showExternalProgress() {
	w.ensureLaunchProgress()
	p := w.externalProgress
	p.active = true
	p.dialog.SetWindowFlag2(qt.WindowCloseButtonHint, true)
	p.title.SetText("Launching Roblox…")
	p.detail.SetText("Checking launch authorization…")
	p.note.SetText("This window closes when Roblox is ready.")
	p.busy.Show()
	p.note.Show()
	p.actions.Hide()
	p.cancel.Show()
	p.authority.Hide()
	p.dialog.SetWindowTitle(guiTitle("Tipsy — Launching Roblox"))
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
	w.beginLaunchContext()
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
	ctx := w.launchContext
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		defer w.queueOwnerState()
		authority, err := service.PrepareLaunch(ctx, approved)
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
			w.handleDevelopmentConsentRequired()
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
		ctx := w.launchContext
		if ctx == nil {
			ctx = context.Background()
		}
		if err := w.launch.StartRequest(ctx, request); err != nil {
			w.showLaunchFailure(safeLaunchFailure)
			return
		}
		w.lastLaunchState = guimodel.LaunchStarting
		if w.launchOrigin == launchFromExternal {
			w.externalProgress.detail.SetText("Waiting for Roblox to open its window…")
			w.updateExternalProgressAuthority(result.authority)
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
		p.dialog.SetWindowTitle(guiTitle("Tipsy — Launch could not complete"))
		if w.launch.View().Started {
			p.title.SetText("Roblox closed unexpectedly")
		} else {
			p.title.SetText("Could not launch Roblox")
		}
		p.detail.SetText(message)
		p.busy.Hide()
		p.note.Hide()
		p.actions.Show()
		p.cancel.Hide()
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

// External starts paint before package verification or settings loading.
// Models are built on the worker and transferred to the GUI thread once, so
// SettingsModel's presentation state is never touched concurrently.
type launchInitialization struct {
	setup                 *guimodel.SetupModel
	settings              *guimodel.SettingsModel
	setupErr, settingsErr error
}

func loadWindowModels(ctx context.Context, service guimodel.Service) launchInitialization {
	setup := guimodel.NewSetupModel(service)
	settings := guimodel.NewSettingsModel(service)
	setupErr := setup.Load(ctx)
	settingsErr := settings.Load(ctx)
	return launchInitialization{setup: setup, settings: settings, setupErr: setupErr, settingsErr: settingsErr}
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
	ctx := w.beginLaunchContext()
	result := make(chan launchInitialization, 1)
	w.launchInitialization = result
	service := w.service
	go func() {
		defer w.queueOwnerState()
		result <- loadWindowModels(ctx, service)
	}()
}

func (w *mainWindow) startSettingsInitialization() {
	if w.launchInitializing || w.launchPreparing || w.launch.View().State != guimodel.LaunchIdle {
		return
	}
	w.launchInitializing = true
	w.startupSettings = true
	w.showStartupProgress()
	ctx := w.beginLaunchContext()
	result := make(chan launchInitialization, 1)
	w.launchInitialization = result
	service := w.service
	go func() {
		defer w.queueOwnerState()
		result <- loadWindowModels(ctx, service)
	}()
}

func (w *mainWindow) showStartupProgress() {
	w.ensureLaunchProgress()
	p := w.externalProgress
	p.active = true
	p.dialog.SetWindowFlag2(qt.WindowCloseButtonHint, true)
	p.title.SetText("Starting Tipsy…")
	p.detail.SetText("Checking the installed client…")
	p.note.SetText("This window closes when Tipsy is ready.")
	p.note.Show()
	p.busy.Show()
	p.actions.Hide()
	p.cancel.Show()
	p.authority.Hide()
	p.dialog.SetWindowTitle(guiTitle("Tipsy — Checking the installed client"))
	placeWidgetOnDisplay(p.dialog.QWidget, configuredDisplay(w))
	p.dialog.Show()
}

func (w *mainWindow) pollLaunchInitialization() {
	select {
	case result := <-w.launchInitialization:
		w.launchInitializing = false
		w.launchInitialization = nil
		w.releaseLaunchContext()
		w.setup, w.settings = result.setup, result.settings
		w.setupLoadErr, w.settingsErr = result.setupErr, result.settingsErr
		w.finishShell()
		if w.startupSettings {
			w.startupSettings = false
			w.externalProgress.active = false
			w.externalProgress.dialog.Hide()
			w.Show()
			if w.FirstRun() {
				w.ShowSetupWizard(true)
			}
			return
		}
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
