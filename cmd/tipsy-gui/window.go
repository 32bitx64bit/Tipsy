package main

import (
	"context"
	"fmt"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
	"github.com/tipsy-linux/tipsy/internal/version"
)

const apkFileFilter = "Android packages (*.apk *.apkm *.xapk *.apks *.zip);;APK files (*.apk);;All files (*)"

type mainWindow struct {
	win      *qt.QMainWindow
	service  guimodel.Service
	icon     *qt.QIcon
	stack    *qt.QStackedWidget
	pages    []*qt.QScrollArea
	status   *qt.QStatusBar
	nav      []*qt.QPushButton
	setup    *guimodel.SetupModel
	settings *guimodel.SettingsModel
	launch   *guimodel.LaunchModel

	setupLoadErr error
	settingsErr  error

	appearance                                       appearanceMode
	appearanceIsDark                                 bool
	systemDarkFallback                               bool
	appearanceButtons                                map[appearanceMode]*qt.QPushButton
	preferenceFPS, preferenceVSync, preferenceWindow *qt.QLabel
	homeTitle, homeClient, homeClientMark            *qt.QLabel
	playButton                                       *qt.QPushButton
	playState                                        *qt.QLabel
	playAuthority                                    *qt.QLabel
	installBadge                                     *qt.QLabel
	installVersion                                   *qt.QLabel
	installDetail                                    *qt.QLabel
	installPageBadge                                 *qt.QLabel
	installPageVer                                   *qt.QLabel
	installPageDetail                                *qt.QLabel
	settingsRenderer                                 *qt.QComboBox
	settingsFPSMode                                  *qt.QComboBox
	settingsFPS                                      *qt.QSpinBox
	settingsVSync                                    *qt.QCheckBox
	settingsLowTexture                               *qt.QCheckBox
	settingsDiscordPresence                          *qt.QCheckBox
	settingsDiscordJoin                              *qt.QCheckBox
	settingsDisplay                                  *qt.QComboBox
	settingsStartFullscreen                          *qt.QCheckBox
	settingsDisplayKeys                              []string
	settingsSyncing                                  bool
	controllerSettings                               guimodel.ControllerSettings
	controllerLoadErr                                error
	controllerSyncing                                bool
	controllerEnable                                 *qt.QCheckBox
	controllerStateNote                              *qt.QLabel
	controllerPadRows                                *qt.QVBoxLayout
	controllerPadNote                                *qt.QLabel
	controllerDeadL                                  *qt.QSlider
	controllerDeadR                                  *qt.QSlider
	controllerDeadLValue                             *qt.QLabel
	controllerDeadRValue                             *qt.QLabel
	controllerInvL                                   *qt.QCheckBox
	controllerInvR                                   *qt.QCheckBox
	controllerRumble                                 *qt.QCheckBox
	controllerHint                                   *qt.QLabel
	controllerTestButton                             *qt.QPushButton
	controllerTestState                              *qt.QLabel
	controllerBars                                   map[string]*qt.QProgressBar
	controllerButtonLamps                            map[int]*qt.QLabel
	controllerProbe                                  *controllerProbe
	controllerTimer                                  *qt.QTimer
	settingsApply                                    *qt.QPushButton
	settingsReset                                    *qt.QPushButton
	settingsHint                                     *qt.QLabel
	integrationStatus                                *qt.QLabel
	integrationDetail                                *qt.QLabel
	integrationAction                                *qt.QPushButton
	integrationView                                  integrationView
	doctorSummary                                    *qt.QLabel
	doctorDetails                                    *qt.QPlainTextEdit
	settingsProfile                                  *qt.QLabel
	settingsClientStatus                             *qt.QLabel
	pageEntry                                        []*qt.QWidget
	wizardScrolls                                    map[*qt.QWizardPage]*qt.QScrollArea

	installReadiness       setupsvc.ReadinessState
	lastLaunchState        guimodel.LaunchState
	launchTimer            *qt.QTimer
	launcherHidden         bool
	launchOrigin           launchOrigin
	externalProgress       *launchProgressWindow
	launchBusy             *qt.QProgressBar
	launchPreparing        bool
	launchPreparation      chan launchPreparation
	launchRequest          guimodel.LaunchRequest
	launchFailure          bool
	launchConsentRequested bool
	launchInitializing     bool
	launchInitialization   chan launchInitialization
	startupSettings        bool

	doctorPending bool
	doctorResult  chan doctorOutcome

	wizardDoctorPending bool
	wizardDoctorResult  chan doctorOutcome
	wizardDoctorStatus  *qt.QLabel
	wizardDoctorChecks  *qt.QVBoxLayout
}

func newWindowBase(service guimodel.Service, icon *qt.QIcon) *mainWindow {
	w := &mainWindow{
		service:  service,
		icon:     icon,
		setup:    guimodel.NewSetupModel(service),
		settings: guimodel.NewSettingsModel(service),
		launch:   guimodel.NewLaunchModel(service),
	}

	w.win = qt.NewQMainWindow2()
	w.win.SetWindowTitle("Tipsy")
	w.win.SetWindowIcon(icon)
	w.win.Resize(680, 820)
	w.win.SetMinimumSize2(640, 560)
	w.win.OnCloseEvent(func(super func(*qt.QCloseEvent), event *qt.QCloseEvent) {
		state := w.launch.View().State
		if w.launchInitializing || w.launchPreparing || state == guimodel.LaunchStarting || state == guimodel.LaunchRunning {
			event.Ignore()
			return
		}
		super(event)
	})
	w.initAppearance()

	w.status = qt.NewQStatusBar2()
	w.status.SetSizeGripEnabled(true)
	w.status.ShowMessage("Ready")
	w.win.SetStatusBar(w.status)

	w.launchTimer = qt.NewQTimer2(w.win.QObject)
	w.launchTimer.OnTimeout(w.refreshLaunchState)
	w.launchTimer.Start(125)
	return w
}

func newMainWindow(service guimodel.Service, icon *qt.QIcon) *mainWindow {
	w := newWindowBase(service, icon)
	w.setupLoadErr = w.setup.Load(context.Background())
	w.settingsErr = w.settings.Load(context.Background())
	w.controllerSettings, w.controllerLoadErr = loadControllerSettings()
	w.finishShell()
	return w
}

func (w *mainWindow) finishShell() {
	w.buildShell()
	w.syncAppearanceButtons()
	w.presentSnapshot()
}

func (w *mainWindow) Show() {
	placeWidgetOnDisplay(w.win.QWidget, configuredDisplay(w))
	w.win.Show()
}

func (w *mainWindow) FirstRun() bool {
	return w.setupLoadErr != nil || !w.setup.View().Snapshot.LaunchReady()
}

func (w *mainWindow) buildShell() {
	root := qt.NewQWidget2()
	setObjectName(root.QObject, "appRoot")
	outer := qt.NewQVBoxLayout(root)
	outer.SetContentsMargins(0, 0, 0, 0)
	outer.SetSpacing(0)
	top := qt.NewQWidget2()
	setObjectName(top.QObject, "topNavigation")
	row := qt.NewQHBoxLayout(top)
	row.SetContentsMargins(16, 0, 16, 0)
	row.SetSpacing(0)
	for i, item := range []struct{ text, tip string }{
		{"Home", "Play Roblox and see installation status"},
		{"Installation", "Install, update, or repair Roblox"},
		{"Settings", "Configure launch preferences"},
		{"Diagnostics", "System readiness, paths, and logs"},
	} {
		button := qt.NewQPushButton3(item.text)
		setObjectName(button.QObject, "navButton")
		button.SetCheckable(true)
		button.SetAutoExclusive(true)
		button.SetFocusPolicy(qt.StrongFocus)
		button.SetAccessibleName(item.text)
		button.SetAccessibleDescription(item.tip)
		button.SetToolTip(item.tip)
		index := i
		button.OnClicked(func() { w.selectPage(index) })
		w.nav = append(w.nav, button)
		row.AddWidget2(button.QWidget, 1)
	}
	w.nav[0].SetChecked(true)
	outer.AddWidget(top)
	w.stack = qt.NewQStackedWidget2()
	setObjectName(w.stack.QObject, "contentStack")
	w.stack.AddWidget(w.buildHomePage())
	w.stack.AddWidget(w.buildInstallPage())
	w.stack.AddWidget(w.buildSettingsPage())
	w.stack.AddWidget(w.buildDiagnosticsPage())
	outer.AddWidget2(w.stack.QWidget, 1)
	footer := qt.NewQWidget2()
	setObjectName(footer.QObject, "appearanceBar")
	foot := qt.NewQHBoxLayout(footer)
	foot.SetContentsMargins(26, 14, 26, 14)
	foot.SetSpacing(0)
	foot.AddWidget(qt.NewQLabel3("Appearance").QWidget)
	foot.AddStretch()
	w.appearanceButtons = map[appearanceMode]*qt.QPushButton{}
	for _, item := range []struct {
		mode  appearanceMode
		label string
	}{{appearanceSystem, "System"}, {appearanceLight, "Light"}, {appearanceDark, "Dark"}} {
		button := qt.NewQPushButton3(item.label)
		setObjectName(button.QObject, "appearanceButton")
		button.SetCheckable(true)
		button.SetAccessibleName("Appearance: " + item.label)
		mode := item.mode
		button.OnClicked(func() { w.chooseAppearance(mode) })
		w.appearanceButtons[mode] = button
		foot.AddWidget(button.QWidget)
	}
	w.appearanceButtons[appearanceSystem].SetToolTip("Follow the desktop color scheme; use the platform palette when no preference is provided")
	outer.AddWidget(footer)
	w.win.SetCentralWidget(root)
	quit := qt.NewQAction2("Quit")
	quit.SetShortcut(qt.NewQKeySequence2("Ctrl+Q"))
	quit.OnTriggered(qt.QCoreApplication_Quit)
	w.win.AddAction(quit)
}

func (w *mainWindow) registerPage(page *qt.QWidget, scroll *qt.QScrollArea) *qt.QWidget {
	w.pages = append(w.pages, scroll)
	return page
}

func (w *mainWindow) selectPage(index int) {
	if index < 0 || index >= w.stack.Count() {
		return
	}
	w.stack.SetCurrentIndex(index)
	if index < len(w.nav) {
		w.nav[index].SetChecked(true)
	}
	if index == 3 {
		w.runDoctor()
	}
	if index == 2 && w.integrationStatus != nil {
		// Another install may have adopted or released the launcher since
		// this window opened; re-read the desktop before showing the row.
		w.refreshIntegration()
	}
	if index == 2 && w.controllerPadRows != nil {
		// Fresh pad enumeration each time Settings opens; observation
		// only, no pad is opened for input.
		w.refreshControllerPads()
	}
	if index != 2 && w.controllerProbe != nil {
		// Never hold a pad handle outside the Controller card.
		w.stopControllerTest("Test is stopped.")
	}
	if index < len(w.pageEntry) && w.pageEntry[index] != nil {
		w.pageEntry[index].SetFocusWithReason(qt.OtherFocusReason)
	}
}

func (w *mainWindow) refreshSnapshot() {
	w.setupLoadErr = w.setup.Load(context.Background())
	w.presentSnapshot()
}

func (w *mainWindow) presentSnapshot() {
	if w.setupLoadErr != nil {
		w.setInstallText("", "Unavailable", "Installation verification is unavailable. Open Setup and check the privacy-safe logs before trying again.")
		return
	}

	w.setupLoadErr = nil
	snapshot := w.setup.View().Snapshot
	versionText := snapshot.Version
	detail := snapshot.Status
	readiness := snapshot.Readiness
	switch readiness {
	case setupsvc.ReadinessLaunchInputs:
		if versionText == "" {
			versionText = "Authenticated client"
		}
		if detail == "" {
			detail = guiLaunchInputsStatus
		}
	case setupsvc.ReadinessRejected:
		if versionText == "" {
			versionText = "Verification rejected"
		}
		if detail == "" {
			detail = rejectedReadinessMessage(setupsvc.ErrIntegrity)
		}
	case setupsvc.ReadinessNotInstalled:
		if versionText == "" {
			versionText = "Not installed"
		}
		if detail == "" {
			detail = guiNotInstalledStatus
		}
	default:
		versionText = "Unavailable"
		detail = "Installation verification is unavailable. Open Setup and check the privacy-safe logs before trying again."
	}
	w.setInstallText(readiness, versionText, detail)
}

type installUIPresentation struct {
	badge         string
	badgeStyle    string
	playTooltip   string
	idleText      string
	settingsText  string
	settingsStyle string
}

func installPresentation(readiness setupsvc.ReadinessState) installUIPresentation {
	switch readiness {
	case setupsvc.ReadinessLaunchInputs:
		return installUIPresentation{
			badge:         "LAUNCH READY",
			badgeStyle:    "statusReady",
			playTooltip:   "Attempt a live launch using the authenticated official Roblox client",
			idleText:      "Authenticated launch inputs are ready; live Home rendering is checked only after launch",
			settingsText:  "Client status: authenticated launch inputs. Settings can be saved; live Home and gameplay remain separate runtime results.",
			settingsStyle: "noticeSuccess",
		}
	case setupsvc.ReadinessRejected:
		return installUIPresentation{
			badge:         "REJECTED",
			badgeStyle:    "statusRejected",
			playTooltip:   "Repair the rejected client verification in Setup before launching",
			idleText:      "Launch is disabled because the active client did not pass verification",
			settingsText:  "Client status: rejected. Settings can be saved, but Play stays disabled until Setup repairs and revalidates the client.",
			settingsStyle: "noticeError",
		}
	case setupsvc.ReadinessNotInstalled:
		return installUIPresentation{
			badge:         "NOT INSTALLED",
			badgeStyle:    "statusWarning",
			playTooltip:   "Install and authenticate an official Roblox client before launching",
			idleText:      "Install an official Android x86-64 client before launching",
			settingsText:  "Client status: not installed. Settings can be saved now, then applied when an authenticated client is installed.",
			settingsStyle: "noticeWarning",
		}
	default:
		return installUIPresentation{
			badge:         "UNAVAILABLE",
			badgeStyle:    "statusNeutral",
			playTooltip:   "Resolve installation verification before launching",
			idleText:      "Launch is disabled until installation verification is available",
			settingsText:  "Client status is unavailable. Settings can be saved, but Play stays disabled until verification succeeds.",
			settingsStyle: "noticeWarning",
		}
	}
}

func (w *mainWindow) setInstallText(readiness setupsvc.ReadinessState, versionText, detail string) {
	w.installReadiness = readiness
	if w.homeTitle != nil {
		title, client := "Ready when you are.", "Client installed"
		switch readiness {
		case setupsvc.ReadinessNotInstalled:
			title, client = "Let’s get you set up.", "Client not installed"
		case setupsvc.ReadinessRejected:
			title, client = "Your client needs attention.", "Client verification rejected"
		case setupsvc.ReadinessLaunchInputs:
		default:
			title, client = "Let’s check your client.", "Client status unavailable"
		}
		w.homeTitle.SetText(title)
		w.homeClient.SetText(client)
		w.homeClientMark.SetVisible(readiness == setupsvc.ReadinessLaunchInputs)
	}
	presentation := installPresentation(readiness)
	for _, badge := range []*qt.QLabel{w.installBadge, w.installPageBadge} {
		if badge == nil {
			continue
		}
		badge.SetText(presentation.badge)
		setObjectName(badge.QObject, presentation.badgeStyle)
		refreshStyle(badge.QWidget)
	}
	for _, label := range []*qt.QLabel{w.installVersion, w.installPageVer} {
		if label != nil {
			label.SetText(versionText)
		}
	}
	for _, label := range []*qt.QLabel{w.installDetail, w.installPageDetail} {
		if label != nil {
			label.SetText(detail)
		}
	}
	if w.playButton != nil {
		w.playButton.SetEnabled(readiness == setupsvc.ReadinessLaunchInputs)
		w.playButton.SetToolTip(presentation.playTooltip)
	}
	if w.playState != nil {
		state := w.launch.View().State
		if state != guimodel.LaunchStarting && state != guimodel.LaunchRunning {
			w.playState.SetText(presentation.idleText)
			w.playState.SetVisible(readiness != setupsvc.ReadinessLaunchInputs)
		}
	}
	if w.settingsClientStatus != nil {
		w.settingsClientStatus.SetText(presentation.settingsText)
		setObjectName(w.settingsClientStatus.QObject, presentation.settingsStyle)
		refreshStyle(w.settingsClientStatus.QWidget)
	}
}

func (w *mainWindow) launchRoblox() bool {
	return w.beginLaunch(launchFromHome, guimodel.LaunchRequest{})
}

var confirmDevelopmentLaunch = func(parent *qt.QWidget) bool {
	buttons := qt.QMessageBox__StandardButton(int(qt.QMessageBox__Yes) | int(qt.QMessageBox__No))
	dialog := qt.NewQMessageBox6(
		qt.QMessageBox__Warning,
		"Authorize development launch",
		"This local build cannot be authenticated as an official Tipsy release. Continue in DevelopmentUnrestricted mode?\n\nThis records your explicit choice in Tipsy's owner-private configuration. The official Roblox APK and active generation will still be verified, but this Tipsy session must not be represented as OfficialVerified.",
		buttons,
		parent,
	)
	dialog.SetDefaultButtonWithButton(qt.QMessageBox__No)
	dialog.SetEscapeButtonWithButton(qt.QMessageBox__No)
	result := dialog.Exec()
	dialog.Delete()
	return result == int(qt.QMessageBox__Yes)
}

func (w *mainWindow) setLaunchAuthority(authority guimodel.LaunchAuthority) {
	if w.playAuthority == nil {
		return
	}
	switch {
	case authority.DevelopmentConsentRequired:
		w.playAuthority.SetText("Approval required — local builds cannot launch as OfficialVerified.")
		setObjectName(w.playAuthority.QObject, "noticeWarning")
	case authority.Mode == string(setupsvc.DevelopmentUnrestricted):
		warning := authority.Warning
		if warning == "" {
			warning = "DevelopmentUnrestricted mode is active; this is not an OfficialVerified Tipsy session."
		}
		w.playAuthority.SetText(warning)
		setObjectName(w.playAuthority.QObject, "noticeWarning")
	case authority.Mode == string(setupsvc.OfficialVerified):
		w.playAuthority.SetText("OfficialVerified — release authority and the active Roblox generation are authenticated.")
		setObjectName(w.playAuthority.QObject, "noticeSuccess")
	default:
		w.playAuthority.SetText("Launch authority will be verified before Roblox starts.")
		setObjectName(w.playAuthority.QObject, "noticeInfo")
	}
	w.playAuthority.SetVisible(authority.DevelopmentConsentRequired || authority.Mode == string(setupsvc.DevelopmentUnrestricted))
	refreshStyle(w.playAuthority.QWidget)
}

func (w *mainWindow) refreshLaunchState() {
	w.pollDoctor()
	if w.launchInitializing {
		w.pollLaunchInitialization()
		return
	}
	if w.playButton == nil {
		return
	}
	w.pollLaunchPreparation()
	view := w.launch.View()
	if view.State == guimodel.LaunchRunning && !w.launcherHidden {
		qt.QGuiApplication_SetQuitOnLastWindowClosed(false)
		w.win.Hide()
		if w.externalProgress != nil {
			w.externalProgress.active = false
			w.externalProgress.dialog.Hide()
		}
		w.launcherHidden = true
	}
	busy := w.launchPreparing || view.State == guimodel.LaunchStarting
	running := busy || view.State == guimodel.LaunchRunning
	w.playButton.SetEnabled(!running && w.setup.View().Snapshot.LaunchReady())
	w.launchBusy.SetVisible(busy && w.launchOrigin == launchFromHome)
	if busy {
		w.playButton.SetText("Launching…")
		if w.launchPreparing {
			w.playState.SetText("Checking launch authorization…")
		} else {
			w.playState.SetText("Starting the official client in its own X11 window")
		}
		setObjectName(w.playState.QObject, "mutedText")
		w.playState.Show()
	} else if view.State == guimodel.LaunchRunning {
		w.playButton.SetText("Roblox is running")
	} else {
		w.playButton.SetText("▶   Play Roblox")
		if !w.launchFailure {
			w.playState.SetText(installPresentation(w.installReadiness).idleText)
		}
	}
	if !w.launchPreparing && !w.launchFailure && view.State == guimodel.LaunchFailed && w.lastLaunchState != guimodel.LaunchFailed {
		message := safeLaunchFailure
		if view.Started {
			message = safeRuntimeFailure
		}
		w.showLaunchFailure(message)
	}
	if view.State == guimodel.LaunchExited && view.Started {
		qt.QCoreApplication_Quit()
	}
	w.lastLaunchState = view.State
}

func (w *mainWindow) refreshSettingsProfile() {
	if w.settingsProfile == nil {
		return
	}
	profile := w.settings.View().Saved
	w.settingsProfile.SetText(settingsProfileText(profile))
	w.refreshLaunchPreferences()
}

func showAboutMessage(parent *qt.QWidget) {
	qt.QMessageBox_About(parent, "About Tipsy", fmt.Sprintf(
		"<h2>Tipsy %s</h2><p>A Linux desktop runtime for the official Roblox Android x86-64 client.</p>"+
			"<p>Tipsy is licensed GPL-3.0-or-later. Roblox is not included and remains copyright Roblox Corporation.</p>"+
			"<p>The desktop application is built with Qt 6 and MIQT. X11 is the primary display target.</p>",
		version.String(),
	))
}
