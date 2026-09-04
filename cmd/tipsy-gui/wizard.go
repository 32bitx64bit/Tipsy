package main

import (
	"context"
	"fmt"
	"html"
	"path/filepath"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func (w *mainWindow) ShowSetupWizard(firstRun bool) {
	_ = w.setup.Load(context.Background())
	wizard := qt.NewQWizard(w.win.QWidget)
	wizard.SetWindowTitle("Set up Tipsy")
	wizard.SetWindowIcon(w.icon)
	wizard.SetWizardStyle(qt.QWizard__ModernStyle)
	wizard.SetTitleFormat(qt.RichText)
	wizard.SetSubTitleFormat(qt.RichText)
	wizard.SetOption2(qt.QWizard__NoBackButtonOnStartPage, true)
	wizard.SetOption2(qt.QWizard__NoBackButtonOnLastPage, true)
	wizard.SetButtonText(qt.QWizard__NextButton, "Continue")
	wizard.SetButtonText(qt.QWizard__FinishButton, "Open Tipsy")
	wizard.Resize(900, 610)
	wizard.SetMinimumSize2(720, 520)
	wizard.SetPixmap(qt.QWizard__LogoPixmap, w.icon.Pixmap2(64, 64))
	sidebar := w.buildWizardSide(firstRun)
	wizard.SetSideWidget(sidebar.widget)

	welcome := w.buildWelcomeWizardPage(firstRun)
	doctor := w.buildDoctorWizardPage()
	source, automatic, local, localPaths, choose, selectedFiles := w.buildSourceWizardPage()
	install, progress, phase, message, errorLabel, cancelInstall, retry := w.buildInstallWizardPage()
	ready := w.buildReadyWizardPage(wizard)

	welcomeID := wizard.AddPage(welcome)
	_ = welcomeID
	doctorID := wizard.AddPage(doctor)
	_ = doctorID
	sourceID := wizard.AddPage(source)
	installID := wizard.AddPage(install)
	readyID := wizard.AddPage(ready)
	styleWizardButtons(wizard)
	updateWizardSteps(sidebar.steps, 0)

	updateSourceControls := func() {
		choose.SetEnabled(local.IsChecked())
	}
	automatic.OnToggled(func(bool) { updateSourceControls() })
	local.OnToggled(func(bool) { updateSourceControls() })
	updateSourceControls()

	source.OnValidatePage(func(_ func() bool) bool {
		request := guimodel.InstallRequest{Mode: guimodel.InstallAutomatic}
		if local.IsChecked() {
			request.Mode = guimodel.InstallLocal
			request.LocalPaths = append([]string(nil), (*localPaths)...)
		}
		if err := request.Validate(w.setup.View().Automatic); err != nil {
			qt.QMessageBox_Warning(wizard.QWidget, "Choose a package source", err.Error())
			return false
		}
		if err := w.setup.SetRequest(request); err != nil {
			qt.QMessageBox_Warning(wizard.QWidget, "Setup is busy", err.Error())
			return false
		}
		return true
	})

	choose.OnClicked(func() {
		paths := qt.QFileDialog_GetOpenFileNames4(wizard.QWidget, "Choose official Roblox APK or bundle", "", apkFileFilter)
		if len(paths) == 0 {
			return
		}
		*localPaths = append((*localPaths)[:0], paths...)
		var names []string
		for _, path := range paths {
			names = append(names, filepath.Base(path))
		}
		local.SetChecked(true)
		choose.SetText("Choose different files")
		choose.SetToolTip(strings.Join(paths, "\n"))
		selectedFiles.SetText("Selected: " + strings.Join(names, ", "))
		selectedFiles.Show()
		if len(names) == 1 {
			choose.SetAccessibleDescription("Selected " + names[0])
		} else {
			choose.SetAccessibleDescription(fmt.Sprintf("Selected %d package files", len(names)))
		}
	})

	var lastState guimodel.SetupState
	refreshInstall := func() {
		view := w.setup.View()
		progress.SetValue(view.Progress.Percent)
		phaseText := view.Progress.Phase
		if phaseText == "" {
			phaseText = "Preparing"
		}
		phase.SetText(phaseText)
		message.SetText(view.Progress.Message)
		switch view.State {
		case guimodel.SetupRunning:
			errorLabel.Hide()
			cancelInstall.Show()
			cancelInstall.SetEnabled(true)
			retry.Hide()
		case guimodel.SetupFailed, guimodel.SetupCancelled:
			errorLabel.SetText(view.Error)
			setObjectName(errorLabel.QObject, "noticeError")
			refreshStyle(errorLabel.QWidget)
			errorLabel.Show()
			cancelInstall.Hide()
			retry.Show()
			wizard.Button(qt.QWizard__BackButton).SetEnabled(true)
		case guimodel.SetupComplete:
			errorLabel.SetText("Package identity verified. Installation is ready.")
			setObjectName(errorLabel.QObject, "noticeSuccess")
			refreshStyle(errorLabel.QWidget)
			errorLabel.Show()
			cancelInstall.Hide()
			retry.Hide()
			wizard.Button(qt.QWizard__BackButton).SetEnabled(false)
		}
		if view.State != lastState {
			lastState = view.State
			w.resetWizardPage(install)
			install.CompleteChanged()
		}
	}

	startInstall := func() {
		if err := w.setup.Start(context.Background()); err != nil {
			errorLabel.SetText(err.Error())
			errorLabel.Show()
		}
		refreshInstall()
	}
	install.OnInitializePage(func(super func()) {
		super()
		wizard.Button(qt.QWizard__BackButton).SetEnabled(false)
		startInstall()
	})
	install.OnIsComplete(func(_ func() bool) bool {
		return w.setup.View().State == guimodel.SetupComplete
	})
	cancelInstall.OnClicked(func() {
		cancelInstall.SetEnabled(false)
		message.SetText("Cancelling safely…")
		w.setup.Cancel()
	})
	retry.OnClicked(startInstall)

	timer := qt.NewQTimer2(wizard.QObject)
	timer.OnTimeout(refreshInstall)
	timer.Start(100)
	wizard.OnCurrentIdChanged(func(id int) {
		step := 0
		for index, pageID := range []int{welcomeID, doctorID, sourceID, installID, readyID} {
			if id == pageID {
				step = index
				break
			}
		}
		updateWizardSteps(sidebar.steps, step)
		for index, pageID := range []int{welcomeID, doctorID, sourceID, installID, readyID} {
			if id == pageID {
				w.resetWizardPage([]*qt.QWizardPage{welcome, doctor, source, install, ready}[index])
				break
			}
		}
		if id == installID {
			refreshInstall()
		}
		if id == sourceID {
			wizard.Button(qt.QWizard__BackButton).SetEnabled(true)
		}
		if id == readyID {
			w.refreshSnapshot()
		}
	})
	wizard.OnRejected(func() {
		timer.Stop()
		w.setup.Cancel()
	})
	wizard.OnAccepted(func() {
		timer.Stop()
		w.refreshSnapshot()
	})

	wizard.Exec()
}

type wizardSidebar struct {
	widget *qt.QWidget
	steps  []*qt.QLabel
}

func (w *mainWindow) buildWizardSide(firstRun bool) wizardSidebar {
	side := qt.NewQWidget2()
	setObjectName(side.QObject, "wizardSide")
	side.SetFixedWidth(218)
	layout := qt.NewQVBoxLayout(side)
	layout.SetContentsMargins(24, 24, 24, 22)
	layout.SetSpacing(8)
	logo := qt.NewQLabel2()
	logo.SetPixmap(w.icon.Pixmap2(64, 64))
	logo.SetFixedSize2(64, 64)
	logo.SetAccessibleName("Tipsy logo")
	layout.AddWidget(logo.QWidget)
	title := qt.NewQLabel3("Make Linux\nfeel at home.")
	setObjectName(title.QObject, "wizardSideTitle")
	layout.AddWidget(title.QWidget)
	layout.AddSpacing(10)
	var stepLabels []*qt.QLabel
	for index, text := range []string{"Welcome", "System check", "Roblox package", "Install", "Ready"} {
		stepLabel := qt.NewQLabel3(fmt.Sprintf("%02d   %s", index+1, text))
		setObjectName(stepLabel.QObject, "wizardStep")
		stepLabel.SetAccessibleName("Setup step: " + text)
		layout.AddWidget(stepLabel.QWidget)
		stepLabels = append(stepLabels, stepLabel)
	}
	layout.AddStretch()
	return wizardSidebar{widget: side, steps: stepLabels}
}

func updateWizardSteps(steps []*qt.QLabel, active int) {
	for index, label := range steps {
		name := "wizardStep"
		if index < active {
			name = "wizardStepDone"
		} else if index == active {
			name = "wizardStepActive"
		}
		setObjectName(label.QObject, name)
		refreshStyle(label.QWidget)
	}
}

func styleWizardButtons(wizard *qt.QWizard) {
	for _, button := range []*qt.QAbstractButton{wizard.Button(qt.QWizard__NextButton), wizard.Button(qt.QWizard__FinishButton)} {
		setObjectName(button.QObject, "primaryButton")
		refreshStyle(button.QWidget)
	}
	for _, button := range []*qt.QAbstractButton{wizard.Button(qt.QWizard__BackButton), wizard.Button(qt.QWizard__CancelButton)} {
		setObjectName(button.QObject, "secondaryButton")
		refreshStyle(button.QWidget)
	}
}

func (w *mainWindow) buildWelcomeWizardPage(firstRun bool) *qt.QWizardPage {
	page := qt.NewQWizardPage2()
	layout := w.newWizardPageLayout(page, "Welcome to Tipsy", "A desktop home for the official Roblox Android x86-64 client on Linux.")
	layout.SetSpacing(16)
	intro := "This assistant checks your system, helps you choose a lawful Roblox package, verifies it, and prepares the client for launch."
	if !firstRun {
		intro = "Use this assistant to update, reinstall, or repair the official client. Your persistent Roblox app data lives outside the replaceable runtime installation."
	}
	text := qt.NewQLabel3(intro)
	text.SetWordWrap(true)
	setObjectName(text.QObject, "wizardLead")
	layout.AddWidget(text.QWidget)
	card := qt.NewQLabel3("<b>What Tipsy does</b><br><br>• Runs local readiness checks<br>• Accepts APK, APKM, XAPK, ZIP, or split packages<br>• Verifies the package before extraction<br>• Keeps Roblox sign-in inside the official client")
	card.SetTextFormat(qt.RichText)
	card.SetWordWrap(true)
	setObjectName(card.QObject, "wizardInfoCard")
	layout.AddWidget(card.QWidget)
	layout.AddStretch()
	return page
}

func (w *mainWindow) buildDoctorWizardPage() *qt.QWizardPage {
	page := qt.NewQWizardPage2()
	layout := w.newWizardPageLayout(page, "System readiness", "These checks are local and contain no Roblox credentials or account data.")
	layout.SetSpacing(12)
	report, err := w.service.Doctor(context.Background())
	if err != nil {
		label := qt.NewQLabel3("Readiness checks could not be completed:<br><br>" + html.EscapeString(err.Error()))
		label.SetWordWrap(true)
		setObjectName(label.QObject, "noticeError")
		layout.AddWidget(label.QWidget)
	} else {
		for _, check := range report.Checks {
			marker := "•"
			suffix := ""
			switch check.Status {
			case guimodel.CheckReady:
				marker = "✓"
			case guimodel.CheckWarning:
				marker = "!"
			case guimodel.CheckBlocked:
				marker = "×"
			}
			if check.Remedy != "" {
				suffix = "<br><span style='color:#66758f'>Next: " + html.EscapeString(check.Remedy) + "</span>"
			}
			label := qt.NewQLabel3(fmt.Sprintf("<b>%s&nbsp;&nbsp;%s</b><br>%s%s", marker, html.EscapeString(check.Name), html.EscapeString(check.Detail), suffix))
			label.SetTextFormat(qt.RichText)
			label.SetWordWrap(true)
			setObjectName(label.QObject, "wizardCheck")
			layout.AddWidget(label.QWidget)
		}
	}
	layout.AddStretch()
	return page
}

func (w *mainWindow) buildSourceWizardPage() (*qt.QWizardPage, *qt.QRadioButton, *qt.QRadioButton, *[]string, *qt.QPushButton, *qt.QLabel) {
	page := qt.NewQWizardPage2()
	layout := w.newWizardPageLayout(page, "Install Roblox", "Choose where the official package comes from. Verification happens before extraction.")
	layout.SetSpacing(14)
	automaticAvailability := w.setup.View().Automatic
	automatic := qt.NewQRadioButton3("Automatic download")
	automatic.SetAccessibleName("Automatic download")
	automatic.SetAccessibleDescription(automaticExplanation(automaticAvailability))
	automatic.SetEnabled(automaticAvailability.Available)
	layout.AddWidget(automatic.QWidget)
	autoDescription := qt.NewQLabel3(automaticExplanation(automaticAvailability))
	autoDescription.SetWordWrap(true)
	setObjectName(autoDescription.QObject, "wizardOptionDetail")
	layout.AddWidget(autoDescription.QWidget)

	local := qt.NewQRadioButton3("Choose APK or bundle")
	local.SetAccessibleName("Choose APK or bundle")
	layout.AddWidget(local.QWidget)
	localDescription := qt.NewQLabel3("Select package files you obtained lawfully. Tipsy supports a base APK, a bundle archive, or a complete split set.")
	localDescription.SetWordWrap(true)
	setObjectName(localDescription.QObject, "wizardOptionDetail")
	layout.AddWidget(localDescription.QWidget)
	choose := qt.NewQPushButton3("Choose package files…")
	setObjectName(choose.QObject, "secondaryButton")
	choose.SetAccessibleName("Choose Roblox package files")
	layout.AddWidget(choose.QWidget)
	selected := qt.NewQLabel3("")
	selected.SetWordWrap(true)
	selected.SetTextInteractionFlags(qt.TextSelectableByMouse)
	setObjectName(selected.QObject, "wizardOptionDetail")
	selected.Hide()
	layout.AddWidget(selected.QWidget)

	security := qt.NewQLabel3("Tipsy does not call an unofficial source ‘trusted.’ Automatic setup is enabled only when the backend can identify and verify a configured lawful provider.")
	security.SetWordWrap(true)
	setObjectName(security.QObject, "noticeInfo")
	layout.AddWidget(security.QWidget)
	layout.AddStretch()

	if automaticAvailability.Available {
		automatic.SetChecked(true)
	} else {
		local.SetChecked(true)
	}
	paths := &[]string{}
	return page, automatic, local, paths, choose, selected
}

func (w *mainWindow) buildInstallWizardPage() (*qt.QWizardPage, *qt.QProgressBar, *qt.QLabel, *qt.QLabel, *qt.QLabel, *qt.QPushButton, *qt.QPushButton) {
	page := qt.NewQWizardPage2()
	layout := w.newWizardPageLayout(page, "Preparing Roblox", "Download, package checks, verification, and extraction are performed by the shared setup backend.")
	layout.SetSpacing(14)
	phase := qt.NewQLabel3("Preparing")
	setObjectName(phase.QObject, "wizardProgressTitle")
	layout.AddWidget(phase.QWidget)
	message := qt.NewQLabel3("Waiting to start…")
	message.SetWordWrap(true)
	setObjectName(message.QObject, "mutedText")
	layout.AddWidget(message.QWidget)
	progress := qt.NewQProgressBar2()
	progress.SetRange(0, 100)
	progress.SetValue(0)
	progress.SetTextVisible(false)
	progress.SetAccessibleName("Installation progress")
	layout.AddWidget(progress.QWidget)
	errorLabel := qt.NewQLabel3("")
	errorLabel.SetWordWrap(true)
	setObjectName(errorLabel.QObject, "noticeError")
	errorLabel.Hide()
	layout.AddWidget(errorLabel.QWidget)
	actions := qt.NewQHBoxLayout2()
	cancel := qt.NewQPushButton3("Cancel installation")
	setObjectName(cancel.QObject, "secondaryButton")
	cancel.SetAccessibleDescription("Stop setup without replacing account information")
	actions.AddWidget(cancel.QWidget)
	retry := qt.NewQPushButton3("Try again")
	setObjectName(retry.QObject, "primaryButton")
	retry.SetAccessibleDescription("Retry installation with the selected package")
	retry.Hide()
	actions.AddWidget(retry.QWidget)
	actions.AddStretch()
	layout.AddLayout(actions.QLayout)
	layout.AddStretch()
	return page, progress, phase, message, errorLabel, cancel, retry
}

func (w *mainWindow) buildReadyWizardPage(wizard *qt.QWizard) *qt.QWizardPage {
	page := qt.NewQWizardPage2()
	layout := w.newWizardPageLayout(page, "Tipsy is ready", "The official client is installed. You can launch now or adjust settings first.")
	page.SetFinalPage(true)
	layout.SetSpacing(16)
	mark := qt.NewQLabel2()
	mark.SetPixmap(w.icon.Pixmap2(112, 112))
	mark.SetFixedSize2(112, 112)
	mark.SetAccessibleName("Tipsy logo")
	layout.AddWidget(mark.QWidget)
	ready := qt.NewQLabel3("<b>Installation complete.</b><br>Your persistent Roblox app data is stored separately from files replaced by client updates.")
	ready.SetTextFormat(qt.RichText)
	ready.SetWordWrap(true)
	setObjectName(ready.QObject, "noticeSuccess")
	layout.AddWidget(ready.QWidget)
	launch := qt.NewQPushButton3("Launch Roblox")
	setObjectName(launch.QObject, "playButton")
	launch.SetAccessibleName("Launch Roblox now")
	launch.OnClicked(func() {
		w.launchRoblox()
		wizard.Accept()
	})
	layout.AddWidget(launch.QWidget)
	layout.AddStretch()
	return page
}

func (w *mainWindow) newWizardPageLayout(page *qt.QWizardPage, title, subtitle string) *qt.QVBoxLayout {
	// Keep headings inside the page instead of Qt's style-dependent native
	// wizard header. ModernStyle may paint that header either light or dark
	// depending on the platform theme, which cannot provide invariant contrast.
	page.SetTitle("")
	page.SetSubTitle("")
	setObjectName(page.QObject, "wizardPage")
	root := qt.NewQVBoxLayout(page.QWidget)
	root.SetContentsMargins(0, 0, 0, 0)
	scroll := qt.NewQScrollArea2()
	setObjectName(scroll.QObject, "wizardPageScroll")
	scroll.SetWidgetResizable(true)
	scroll.SetFrameShape(qt.QFrame__NoFrame)
	scroll.SetFocusPolicy(qt.NoFocus)
	scroll.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	scroll.SetAlignment(qt.AlignHCenter | qt.AlignTop)
	content := qt.NewQWidget2()
	setObjectName(content.QObject, "wizardPageContent")
	content.SetMaximumWidth(720)
	layout := qt.NewQVBoxLayout(content)
	layout.SetContentsMargins(22, 20, 22, 16)
	layout.SetSpacing(8)
	titleLabel := qt.NewQLabel3(title)
	setObjectName(titleLabel.QObject, "wizardPageTitle")
	titleLabel.SetAccessibleName(title)
	layout.AddWidget(titleLabel.QWidget)
	subtitleLabel := qt.NewQLabel3(subtitle)
	setObjectName(subtitleLabel.QObject, "wizardPageSubtitle")
	subtitleLabel.SetWordWrap(true)
	layout.AddWidget(subtitleLabel.QWidget)
	layout.AddSpacing(8)
	scroll.SetWidget(content)
	root.AddWidget(scroll.QWidget)
	if w.wizardScrolls == nil {
		w.wizardScrolls = make(map[*qt.QWizardPage]*qt.QScrollArea)
	}
	w.wizardScrolls[page] = scroll
	return layout
}

func (w *mainWindow) resetWizardPage(page *qt.QWizardPage) {
	if scroll := w.wizardScrolls[page]; scroll != nil {
		scroll.VerticalScrollBar().SetValue(0)
		scroll.HorizontalScrollBar().SetValue(0)
	}
}
