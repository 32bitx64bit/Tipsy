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

	playButton              *qt.QPushButton
	playState               *qt.QLabel
	playAuthority           *qt.QLabel
	installBadge            *qt.QLabel
	installVersion          *qt.QLabel
	installDetail           *qt.QLabel
	installPageBadge        *qt.QLabel
	installPageVer          *qt.QLabel
	installPageDetail       *qt.QLabel
	settingsRenderer        *qt.QComboBox
	settingsFPSMode         *qt.QComboBox
	settingsFPS             *qt.QSpinBox
	settingsVSync           *qt.QCheckBox
	settingsLowTexture      *qt.QCheckBox
	settingsDiscordPresence *qt.QCheckBox
	settingsDiscordJoin     *qt.QCheckBox
	settingsDisplay         *qt.QComboBox
	settingsStartFullscreen *qt.QCheckBox
	settingsDisplayKeys     []string
	settingsSyncing         bool
	settingsApply           *qt.QPushButton
	settingsReset           *qt.QPushButton
	settingsHint            *qt.QLabel
	doctorSummary           *qt.QLabel
	doctorDetails           *qt.QPlainTextEdit
	settingsProfile         *qt.QLabel
	settingsClientStatus    *qt.QLabel
	pageEntry               []*qt.QWidget
	wizardScrolls           map[*qt.QWizardPage]*qt.QScrollArea

	installReadiness setupsvc.ReadinessState
	lastLaunchState  guimodel.LaunchState
	launchTimer      *qt.QTimer
	launcherHidden   bool
}

func newMainWindow(service guimodel.Service, icon *qt.QIcon) *mainWindow {
	w := &mainWindow{
		service:  service,
		icon:     icon,
		setup:    guimodel.NewSetupModel(service),
		settings: guimodel.NewSettingsModel(service),
		launch:   guimodel.NewLaunchModel(service),
	}
	w.setupLoadErr = w.setup.Load(context.Background())
	w.settingsErr = w.settings.Load(context.Background())

	w.win = qt.NewQMainWindow2()
	w.win.SetWindowTitle("Tipsy - Settings")
	w.win.SetWindowIcon(icon)
	w.win.Resize(1080, 720)
	w.win.SetMinimumSize2(780, 560)
	w.addMenuBar()
	w.buildShell()

	w.status = qt.NewQStatusBar2()
	w.status.SetSizeGripEnabled(true)
	w.status.ShowMessage("Ready")
	w.win.SetStatusBar(w.status)

	w.launchTimer = qt.NewQTimer2(w.win.QObject)
	w.launchTimer.OnTimeout(w.refreshLaunchState)
	w.launchTimer.Start(125)
	w.refreshSnapshot()
	return w
}

func (w *mainWindow) Show() {
	placeWidgetOnDisplay(w.win.QWidget, configuredDisplay(w))
	w.win.Show()
}

func (w *mainWindow) startInMode(mode, uri string) {
	if (mode == guiModePlay || uri != "") && !w.FirstRun() {
		qt.QGuiApplication_SetQuitOnLastWindowClosed(false)
		started, err := w.startAuthorizedLaunch(context.Background(), guimodel.LaunchRequest{URI: uri})
		if err != nil {
			qt.QGuiApplication_SetQuitOnLastWindowClosed(true)
			w.Show()
			qt.QMessageBox_Warning(w.win.QWidget, "Could not launch Roblox", err.Error())
			return
		}
		if !started {
			qt.QGuiApplication_SetQuitOnLastWindowClosed(true)
			w.Show()
			return
		}
		w.lastLaunchState = guimodel.LaunchStarting
		w.refreshLaunchState()
		return
	}
	w.Show()
	if w.FirstRun() {
		w.ShowSetupWizard(true)
	}
}

func (w *mainWindow) FirstRun() bool {
	return w.setupLoadErr != nil || !w.setup.View().Snapshot.LaunchReady()
}

func (w *mainWindow) buildShell() {
	root := qt.NewQWidget2()
	setObjectName(root.QObject, "appRoot")
	outer := qt.NewQHBoxLayout(root)
	outer.SetContentsMargins(0, 0, 0, 0)
	outer.SetSpacing(0)

	sidebar := qt.NewQWidget2()
	setObjectName(sidebar.QObject, "sidebar")
	sidebar.SetFixedWidth(224)
	side := qt.NewQVBoxLayout(sidebar)
	side.SetContentsMargins(22, 26, 22, 22)
	side.SetSpacing(8)

	brand := qt.NewQWidget2()
	brandRow := qt.NewQHBoxLayout(brand)
	brandRow.SetContentsMargins(0, 0, 0, 0)
	brandRow.SetSpacing(12)
	logo := qt.NewQLabel2()
	logo.SetPixmap(w.icon.Pixmap2(46, 46))
	logo.SetFixedSize2(46, 46)
	logo.SetAccessibleName("Tipsy logo")
	brandRow.AddWidget(logo.QWidget)
	wordmark := qt.NewQLabel3("Tipsy")
	setObjectName(wordmark.QObject, "wordmark")
	brandRow.AddWidget(wordmark.QWidget)
	brandRow.AddStretch()
	side.AddWidget(brand)

	strap := qt.NewQLabel3("ROBLOX ON LINUX")
	setObjectName(strap.QObject, "eyebrowOnDark")
	side.AddWidget(strap.QWidget)
	side.AddSpacing(22)

	for i, item := range []struct {
		text string
		tip  string
	}{
		{"Home", "Play Roblox and see installation status"},
		{"Installation", "Install, update, or repair Roblox"},
		{"Settings", "Renderer, frame-rate, VSync, texture quality, and default-monitor settings"},
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
		side.AddWidget(button.QWidget)
	}
	w.nav[0].SetChecked(true)
	side.AddStretch()

	privacy := qt.NewQLabel3("Official client only\nCredentials stay inside Roblox")
	setObjectName(privacy.QObject, "sidebarNote")
	privacy.SetWordWrap(true)
	side.AddWidget(privacy.QWidget)
	build := qt.NewQLabel3("Tipsy " + version.String())
	setObjectName(build.QObject, "sidebarVersion")
	side.AddWidget(build.QWidget)

	w.stack = qt.NewQStackedWidget2()
	setObjectName(w.stack.QObject, "contentStack")
	w.stack.AddWidget(w.buildHomePage())
	w.stack.AddWidget(w.buildInstallPage())
	w.stack.AddWidget(w.buildSettingsPage())
	w.stack.AddWidget(w.buildDiagnosticsPage())

	outer.AddWidget(sidebar)
	outer.AddWidget2(w.stack.QWidget, 1)
	w.win.SetCentralWidget(root)
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
	if index < len(w.pageEntry) && w.pageEntry[index] != nil {
		w.pageEntry[index].SetFocusWithReason(qt.OtherFocusReason)
	}
}

func (w *mainWindow) addMenuBar() {
	bar := qt.NewQMenuBar2()

	fileMenu := bar.AddMenuWithTitle("&File")
	setup := fileMenu.AddActionWithText("Run &setup…")
	setup.OnTriggered(func() { w.ShowSetupWizard(false) })
	fileMenu.AddSeparator()
	quit := fileMenu.AddActionWithText("&Quit")
	quit.SetShortcut(qt.NewQKeySequence2("Ctrl+Q"))
	quit.OnTriggered(qt.QCoreApplication_Quit)

	helpMenu := bar.AddMenuWithTitle("&Help")
	diagnostics := helpMenu.AddActionWithText("System &diagnostics")
	diagnostics.OnTriggered(func() { w.selectPage(3) })
	about := helpMenu.AddActionWithText("&About Tipsy")
	about.OnTriggered(func() { showAboutMessage(w.win.QWidget) })
	aboutQt := helpMenu.AddActionWithText("About &Qt")
	aboutQt.OnTriggered(qt.QApplication_AboutQt)
	w.win.SetMenuBar(bar)
}

func (w *mainWindow) refreshSnapshot() {
	if err := w.setup.Load(context.Background()); err != nil {
		w.setupLoadErr = err
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
		}
	}
	if w.settingsClientStatus != nil {
		w.settingsClientStatus.SetText(presentation.settingsText)
		setObjectName(w.settingsClientStatus.QObject, presentation.settingsStyle)
		refreshStyle(w.settingsClientStatus.QWidget)
	}
}

func (w *mainWindow) launchRoblox() bool {
	started, err := w.startAuthorizedLaunch(context.Background(), guimodel.LaunchRequest{})
	if err != nil {
		qt.QMessageBox_Warning(w.win.QWidget, "Could not launch Roblox", err.Error())
		return false
	}
	if !started {
		return false
	}
	w.lastLaunchState = guimodel.LaunchStarting
	w.refreshLaunchState()
	return true
}

func (w *mainWindow) startAuthorizedLaunch(ctx context.Context, request guimodel.LaunchRequest) (bool, error) {
	authority, err := w.service.PrepareLaunch(ctx, false)
	if authority.DevelopmentConsentRequired {
		w.setLaunchAuthority(authority)
		if !confirmDevelopmentLaunch(w.win.QWidget) {
			if w.status != nil {
				w.status.ShowMessage2("Development launch was not authorized.", 6000)
			}
			return false, nil
		}
		authority, err = w.service.PrepareLaunch(ctx, true)
	}
	w.setLaunchAuthority(authority)
	if err != nil {
		return false, err
	}
	if err := w.launch.StartRequest(ctx, request); err != nil {
		return false, err
	}
	return true, nil
}

var confirmDevelopmentLaunch = func(parent *qt.QWidget) bool {
	buttons := qt.QMessageBox__StandardButton(int(qt.QMessageBox__Yes) | int(qt.QMessageBox__No))
	dialog := qt.NewQMessageBox6(
		qt.QMessageBox__Warning,
		"Authorize development launch",
		"This source build cannot be authenticated as an official Tipsy release. Continue in DevelopmentUnrestricted mode?\n\nThis records your explicit choice in Tipsy's owner-private configuration. The official Roblox APK and active generation will still be verified, but this Tipsy session must not be represented as OfficialVerified.",
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
		w.playAuthority.SetText("Approval required — source builds cannot launch as OfficialVerified.")
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
	refreshStyle(w.playAuthority.QWidget)
}

func (w *mainWindow) refreshLaunchState() {
	view := w.launch.View()
	if view.State == guimodel.LaunchRunning && !w.launcherHidden {
		// Roblox runs in-process, so the application event loop must remain
		// alive after its launcher surface goes away. A later runtime failure
		// restores this window and its normal last-window behavior.
		qt.QGuiApplication_SetQuitOnLastWindowClosed(false)
		w.win.Hide()
		w.launcherHidden = true
	}
	if w.playButton != nil {
		running := view.State == guimodel.LaunchStarting || view.State == guimodel.LaunchRunning
		w.playButton.SetEnabled(!running && w.setup.View().Snapshot.LaunchReady())
		if view.State == guimodel.LaunchStarting {
			w.playButton.SetText("Launching…")
			w.playState.SetText("Starting the official client in its own X11 window")
		} else if view.State == guimodel.LaunchRunning {
			w.playButton.SetText("Roblox is running")
			w.playState.SetText("The Tipsy launcher closes while Roblox is running")
		} else {
			w.playButton.SetText("Play Roblox")
			w.playState.SetText(installPresentation(w.installReadiness).idleText)
		}
	}
	if view.State == guimodel.LaunchFailed && w.lastLaunchState != guimodel.LaunchFailed {
		if w.launcherHidden {
			placeWidgetOnDisplay(w.win.QWidget, configuredDisplay(w))
			w.win.Show()
			w.launcherHidden = false
			qt.QGuiApplication_SetQuitOnLastWindowClosed(true)
		}
		qt.QMessageBox_Critical(w.win.QWidget, "Roblox closed with an error", view.Error)
		w.status.ShowMessage2("Roblox could not be launched.", 6000)
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
}

func showAboutMessage(parent *qt.QWidget) {
	qt.QMessageBox_About(parent, "About Tipsy", fmt.Sprintf(
		"<h2>Tipsy %s</h2><p>A Linux desktop runtime for the official Roblox Android x86-64 client.</p>"+
			"<p>Tipsy is licensed GPL-3.0-or-later. Roblox is not included and remains copyright Roblox Corporation.</p>"+
			"<p>The desktop application is built with Qt 6 and MIQT. X11 is the primary display target.</p>",
		version.String(),
	))
}
