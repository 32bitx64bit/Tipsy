package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/config"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func (w *mainWindow) buildHomePage() *qt.QWidget {
	page, layout, scroll := newPage("Home", "A focused place to launch, update, and tune Roblox on Linux.")

	hero, heroLayout := newCard("heroCard")
	heroLayout.SetContentsMargins(30, 26, 30, 26)
	heroLayout.SetSpacing(16)
	copy := qt.NewQWidget2()
	copyLayout := qt.NewQVBoxLayout(copy)
	copyLayout.SetContentsMargins(0, 0, 0, 0)
	copyLayout.SetSpacing(8)
	eyebrow := qt.NewQLabel3("READY WHEN YOU ARE")
	setObjectName(eyebrow.QObject, "heroEyebrow")
	copyLayout.AddWidget(eyebrow.QWidget)
	title := qt.NewQLabel3("Roblox on Linux.\nWithout the beige launcher.")
	setObjectName(title.QObject, "heroTitle")
	title.SetWordWrap(true)
	copyLayout.AddWidget(title.QWidget)
	w.playState = qt.NewQLabel3("Your Roblox sign-in remains in the client, not in Tipsy")
	setObjectName(w.playState.QObject, "heroBody")
	w.playState.SetWordWrap(true)
	w.playState.SetAccessibleName("Launch status")
	copyLayout.AddWidget(w.playState.QWidget)
	w.playAuthority = qt.NewQLabel3("Launch authority will be verified before Roblox starts.")
	setObjectName(w.playAuthority.QObject, "noticeInfo")
	w.playAuthority.SetWordWrap(true)
	w.playAuthority.SetAccessibleName("Launch security state")
	copyLayout.AddWidget(w.playAuthority.QWidget)
	copyLayout.AddSpacing(8)
	w.playButton = qt.NewQPushButton3("Play Roblox")
	setObjectName(w.playButton.QObject, "playButton")
	w.playButton.SetAccessibleName("Play Roblox")
	w.playButton.SetDefault(true)
	w.playButton.OnClicked(func() { w.launchRoblox() })
	copyLayout.AddWidget(w.playButton.QWidget)
	w.pageEntry = append(w.pageEntry, w.playButton.QWidget)
	copyLayout.AddStretch()
	heroLayout.AddWidget2(copy, 1)
	mark := qt.NewQLabel2()
	mark.SetPixmap(w.icon.Pixmap2(104, 104))
	mark.SetFixedSize2(104, 104)
	mark.SetAlignment(qt.AlignCenter)
	mark.SetAccessibleName("Tipsy logo")
	heroLayout.AddWidget(mark.QWidget)
	layout.AddWidget(hero.QWidget)

	row := qt.NewQWidget2()
	rowLayout := qt.NewQGridLayout(row)
	rowLayout.SetContentsMargins(0, 0, 0, 0)
	rowLayout.SetSpacing(16)

	installCard, installLayout := newVerticalCard("card")
	cardTop := qt.NewQHBoxLayout2()
	cardTop.AddWidget(sectionLabel("Installation").QWidget)
	cardTop.AddStretch()
	w.installBadge = qt.NewQLabel3("CHECKING")
	setObjectName(w.installBadge.QObject, "statusNeutral")
	cardTop.AddWidget(w.installBadge.QWidget)
	installLayout.AddLayout(cardTop.QLayout)
	w.installVersion = qt.NewQLabel3("Checking…")
	setObjectName(w.installVersion.QObject, "metricValue")
	installLayout.AddWidget(w.installVersion.QWidget)
	w.installDetail = qt.NewQLabel3("Reading the installed client status.")
	setObjectName(w.installDetail.QObject, "mutedText")
	w.installDetail.SetWordWrap(true)
	installLayout.AddWidget(w.installDetail.QWidget)
	manage := qt.NewQPushButton3("Manage installation")
	setObjectName(manage.QObject, "secondaryButton")
	manage.SetAccessibleDescription("Open the installation and update page")
	manage.OnClicked(func() { w.selectPage(1) })
	installLayout.AddWidget(manage.QWidget)
	rowLayout.AddWidget2(installCard.QWidget, 0, 0)

	settingsCard, settingsLayout := newVerticalCard("card")
	settingsLayout.AddWidget(sectionLabel("Graphics profile").QWidget)
	profile := w.settings.View().Draft
	profileText := settingsProfileText(profile)
	w.settingsProfile = qt.NewQLabel3(profileText)
	setObjectName(w.settingsProfile.QObject, "metricValueSmall")
	w.settingsProfile.SetWordWrap(true)
	settingsLayout.AddWidget(w.settingsProfile.QWidget)
	note := qt.NewQLabel3("Changes are written through the shared client-settings backend and take effect after a Roblox restart.")
	setObjectName(note.QObject, "mutedText")
	note.SetWordWrap(true)
	settingsLayout.AddWidget(note.QWidget)
	openSettings := qt.NewQPushButton3("Open settings")
	setObjectName(openSettings.QObject, "secondaryButton")
	openSettings.SetAccessibleDescription("Open renderer, frame-rate, VSync, texture quality, and default-monitor settings")
	openSettings.OnClicked(func() { w.selectPage(2) })
	settingsLayout.AddWidget(openSettings.QWidget)
	rowLayout.AddWidget2(settingsCard.QWidget, 0, 1)
	rowLayout.SetColumnStretch(0, 1)
	rowLayout.SetColumnStretch(1, 1)
	twoColumns := true
	row.OnResizeEvent(func(super func(event *qt.QResizeEvent), event *qt.QResizeEvent) {
		super(event)
		wantTwoColumns := event.Size().Width() >= 620
		if wantTwoColumns == twoColumns {
			return
		}
		rowLayout.RemoveWidget(installCard.QWidget)
		rowLayout.RemoveWidget(settingsCard.QWidget)
		if wantTwoColumns {
			rowLayout.AddWidget2(installCard.QWidget, 0, 0)
			rowLayout.AddWidget2(settingsCard.QWidget, 0, 1)
			rowLayout.SetColumnStretch(1, 1)
		} else {
			rowLayout.AddWidget2(installCard.QWidget, 0, 0)
			rowLayout.AddWidget2(settingsCard.QWidget, 1, 0)
			rowLayout.SetColumnStretch(1, 0)
		}
		twoColumns = wantTwoColumns
	})

	layout.AddWidget(row)
	layout.AddStretch()
	return w.registerPage(page, scroll)
}

func (w *mainWindow) buildInstallPage() *qt.QWidget {
	page, layout, scroll := newPage("Installation", "Install the official Android x86-64 client, or refresh an existing installation.")

	card, cardLayout := newVerticalCard("card")
	cardTop := qt.NewQHBoxLayout2()
	cardTop.AddWidget(sectionLabel("Roblox client").QWidget)
	cardTop.AddStretch()
	w.installPageBadge = qt.NewQLabel3("CHECKING")
	setObjectName(w.installPageBadge.QObject, "statusNeutral")
	cardTop.AddWidget(w.installPageBadge.QWidget)
	cardLayout.AddLayout(cardTop.QLayout)
	w.installPageVer = qt.NewQLabel3("Checking…")
	setObjectName(w.installPageVer.QObject, "metricValueSmall")
	w.installPageVer.SetWordWrap(true)
	cardLayout.AddWidget(w.installPageVer.QWidget)
	w.installPageDetail = qt.NewQLabel3("Reading the installed client status.")
	setObjectName(w.installPageDetail.QObject, "mutedText")
	w.installPageDetail.SetWordWrap(true)
	cardLayout.AddWidget(w.installPageDetail.QWidget)
	description := qt.NewQLabel3("Tipsy verifies package identity and architecture before extraction. Account data is kept separately, so updating the client does not replace your sign-in storage.")
	setObjectName(description.QObject, "bodyText")
	description.SetWordWrap(true)
	cardLayout.AddWidget(description.QWidget)
	cardLayout.AddSpacing(8)
	setup := qt.NewQPushButton3("Open setup assistant")
	setObjectName(setup.QObject, "primaryButton")
	setup.SetAccessibleDescription("Install, update, or repair the official Roblox client")
	setup.OnClicked(func() { w.ShowSetupWizard(false) })
	cardLayout.AddWidget(setup.QWidget)
	w.pageEntry = append(w.pageEntry, setup.QWidget)
	layout.AddWidget(card.QWidget)

	sourceCard, sourceLayout := newVerticalCard("card")
	sourceLayout.AddWidget(sectionLabel("Package sources").QWidget)
	automatic := w.setup.View().Automatic
	autoTitle := "Automatic download"
	if automatic.Available && automatic.SourceName != "" {
		autoTitle += " · " + automatic.SourceName
	}
	auto := qt.NewQLabel3("<b>" + html.EscapeString(autoTitle) + "</b><br>" + html.EscapeString(automaticExplanation(automatic)))
	auto.SetTextFormat(qt.RichText)
	auto.SetWordWrap(true)
	setObjectName(auto.QObject, "sourceRow")
	sourceLayout.AddWidget(auto.QWidget)
	local := qt.NewQLabel3("<b>Choose APK or bundle</b><br>Use APK, APKM, XAPK, ZIP, or a split set that you obtained lawfully. Tipsy never asks for Roblox credentials.")
	local.SetTextFormat(qt.RichText)
	local.SetWordWrap(true)
	setObjectName(local.QObject, "sourceRow")
	sourceLayout.AddWidget(local.QWidget)
	layout.AddWidget(sourceCard.QWidget)
	layout.AddStretch()
	return w.registerPage(page, scroll)
}

func (w *mainWindow) buildSettingsPage() *qt.QWidget {
	page, layout, scroll := newPage("Settings", "Tune the official client without hand-editing XML or launch flags.")

	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Graphics and performance").QWidget)

	form := qt.NewQFormLayout2()
	form.SetHorizontalSpacing(24)
	form.SetVerticalSpacing(16)
	form.SetRowWrapPolicy(qt.QFormLayout__WrapLongRows)
	form.SetFieldGrowthPolicy(qt.QFormLayout__AllNonFixedFieldsGrow)
	w.settingsRenderer = qt.NewQComboBox2()
	rendererModel := qt.NewQStandardItemModel3(w.win.QObject)
	for _, option := range w.settings.View().RendererOptions {
		item := qt.NewQStandardItem()
		item.SetText(rendererOptionText(option))
		item.SetEnabled(option.Available)
		if option.Reason != "" {
			item.SetToolTip(option.Reason)
		}
		rendererModel.AppendRowWithItem(item)
	}
	w.settingsRenderer.SetModel(rendererModel.QAbstractItemModel)
	w.settingsRenderer.SetAccessibleName("Rendering backend")
	w.settingsRenderer.SetToolTip("Vulkan requires a Vulkan bridge and working host driver")
	w.settingsRenderer.SetMaximumWidth(560)
	w.pageEntry = append(w.pageEntry, w.settingsRenderer.QWidget)
	form.AddRow3("Renderer", w.settingsRenderer.QWidget)

	w.settingsFPSMode = qt.NewQComboBox2()
	w.settingsFPSMode.AddItems([]string{"Automatic (recommended)", "Limited (30–240 FPS)", "Unlimited (experimental request)"})
	w.settingsFPSMode.SetAccessibleName("Frame-rate mode")
	w.settingsFPSMode.SetAccessibleDescription("Limited accepts 30 to 240 FPS. Unlimited sends an experimental uncapped request; actual FPS depends on the client, CPU, GPU, and driver, and no frame rate is guaranteed.")
	w.settingsFPSMode.SetToolTip("Unlimited is an experimental uncapped request, not a guaranteed frame rate.")
	w.settingsFPSMode.SetMaximumWidth(560)
	form.AddRow3("Frame-rate mode", w.settingsFPSMode.QWidget)

	w.settingsFPS = qt.NewQSpinBox2()
	w.settingsFPS.SetRange(guimodel.MinFrameRate, guimodel.MaxFrameRate)
	w.settingsFPS.SetSingleStep(10)
	w.settingsFPS.SetSuffix(" FPS")
	w.settingsFPS.SetButtonSymbols(qt.QAbstractSpinBox__NoButtons)
	w.settingsFPS.SetAccessibleName("Frame-rate limit")
	w.settingsFPS.SetToolTip("Set a finite client frame-rate limit from 30 to 240 FPS")
	w.settingsFPS.SetMaximumWidth(240)
	form.AddRow3("Limit", w.settingsFPS.QWidget)

	w.settingsVSync = qt.NewQCheckBox3(vsyncToggleText(false))
	setObjectName(w.settingsVSync.QObject, "vsyncToggle")
	w.settingsVSync.SetAccessibleName("VSync")
	w.settingsVSync.SetAccessibleDescription("Optional vertical synchronization; disabled by default. Disabling it can allow tearing and lets the independent frame-rate cap run above the monitor refresh rate.")
	w.settingsVSync.SetToolTip("Disabled by default. Enable to synchronize presentation to the active monitor refresh rate; this is independent of the frame-rate cap.")
	form.AddRow3("VSync", w.settingsVSync.QWidget)

	w.settingsLowTexture = qt.NewQCheckBox3(lowTextureToggleText(false))
	setObjectName(w.settingsLowTexture.QObject, "vsyncToggle")
	w.settingsLowTexture.SetAccessibleName("Low texture mode")
	w.settingsLowTexture.SetAccessibleDescription("Uses lower-resolution textures to save memory and VRAM. Disabled by default so Roblox loads high-quality textures.")
	w.settingsLowTexture.SetToolTip("Off by default (high-quality textures). Enable to use lower-resolution textures and save memory/VRAM. Changing this requires a Roblox restart.")
	form.AddRow3("Low texture mode", w.settingsLowTexture.QWidget)

	w.settingsDiscordPresence = qt.NewQCheckBox3(discordPresenceToggleText(true))
	setObjectName(w.settingsDiscordPresence.QObject, "vsyncToggle")
	w.settingsDiscordPresence.SetAccessibleName("Discord Rich Presence")
	w.settingsDiscordPresence.SetAccessibleDescription("Shows the current Roblox experience on Discord as Game name - Tipsy. Enabled by default. Applies while Roblox is running.")
	w.settingsDiscordPresence.SetToolTip("Enabled by default. Friends see the experience name with Tipsy branding. Applies without restarting Roblox.")
	form.AddRow3("Discord Rich Presence", w.settingsDiscordPresence.QWidget)

	w.settingsDiscordJoin = qt.NewQCheckBox3(discordJoinToggleText(false))
	setObjectName(w.settingsDiscordJoin.QObject, "vsyncToggle")
	w.settingsDiscordJoin.SetAccessibleName("Show Join button")
	w.settingsDiscordJoin.SetAccessibleDescription("Adds a Discord Join button that opens the public Roblox experience page. Disabled by default. Requires Discord Rich Presence.")
	w.settingsDiscordJoin.SetToolTip("Off by default. Friends see a Join button that opens https://www.roblox.com/games/{placeId}, not a private server. Discord does not show that button on your own status. Applies without restarting Roblox.")
	form.AddRow3("Show Join button", w.settingsDiscordJoin.QWidget)

	w.settingsDisplay = qt.NewQComboBox2()
	w.settingsDisplay.SetAccessibleName("Default monitor")
	w.settingsDisplay.SetAccessibleDescription("Choose the monitor Tipsy uses for the launcher and Roblox windows. Main monitor is the default. Follow mouse restores window-manager placement from the pointer.")
	w.settingsDisplay.SetToolTip("Main monitor is the default. Follow mouse restores the previous pointer-based window-manager placement.")
	w.settingsDisplay.SetMaximumWidth(560)
	form.AddRow3("Default monitor", w.settingsDisplay.QWidget)

	w.settingsStartFullscreen = qt.NewQCheckBox3(startFullscreenToggleText(false))
	setObjectName(w.settingsStartFullscreen.QObject, "vsyncToggle")
	w.settingsStartFullscreen.SetAccessibleName("Start Roblox fullscreen")
	w.settingsStartFullscreen.SetAccessibleDescription("When enabled, Tipsy asks the desktop to fullscreen each new Roblox window. This is a Tipsy host startup preference; it does not mirror or change Roblox's in-app fullscreen toggle.")
	w.settingsStartFullscreen.SetToolTip("Off by default. Applies to the next Roblox window and does not mirror Roblox's in-app fullscreen toggle.")
	form.AddRow3("Start Roblox fullscreen", w.settingsStartFullscreen.QWidget)
	cardLayout.AddLayout(form.QLayout)

	w.settingsHint = qt.NewQLabel3("Roblox must be restarted before renderer, frame-rate, VSync, or texture quality changes take effect. Discord Rich Presence applies while Roblox is running. The default monitor applies to the next Tipsy or Roblox window; Start Roblox fullscreen applies to the next Roblox window.")
	w.settingsHint.SetWordWrap(true)
	setObjectName(w.settingsHint.QObject, "noticeInfo")
	cardLayout.AddWidget(w.settingsHint.QWidget)

	actions := qt.NewQHBoxLayout2()
	actions.AddStretch()
	w.settingsReset = qt.NewQPushButton3("Reset defaults")
	setObjectName(w.settingsReset.QObject, "secondaryButton")
	w.settingsReset.SetAccessibleDescription("Restore automatic renderer and frame-rate defaults with VSync disabled, high-quality textures, Discord Rich Presence on, Join button off, new Roblox windows windowed, and windows on the main monitor")
	w.settingsReset.OnClicked(w.resetSettings)
	actions.AddWidget(w.settingsReset.QWidget)
	w.settingsApply = qt.NewQPushButton3("Apply changes")
	setObjectName(w.settingsApply.QObject, "primaryButton")
	w.settingsApply.SetAccessibleDescription("Save renderer, frame-rate, VSync, texture quality, Discord, default-monitor, and Tipsy fullscreen-start settings")
	w.settingsApply.OnClicked(w.applySettings)
	actions.AddWidget(w.settingsApply.QWidget)
	cardLayout.AddLayout(actions.QLayout)
	layout.AddWidget(card.QWidget)

	if w.settingsErr != nil {
		w.settingsHint.SetText("Settings could not be loaded: " + w.settingsErr.Error())
		setObjectName(w.settingsHint.QObject, "noticeError")
		w.settingsRenderer.SetEnabled(false)
		w.settingsFPSMode.SetEnabled(false)
		w.settingsFPS.SetEnabled(false)
		w.settingsVSync.SetEnabled(false)
		w.settingsLowTexture.SetEnabled(false)
		w.settingsDiscordPresence.SetEnabled(false)
		w.settingsDiscordJoin.SetEnabled(false)
		w.settingsDisplay.SetEnabled(false)
		w.settingsStartFullscreen.SetEnabled(false)
		w.settingsApply.SetEnabled(false)
		w.settingsReset.SetEnabled(false)
		w.populateDisplayChoices(guimodel.DisplayPrimary)
	} else {
		w.bindSettings(w.settings.View().Draft)
		w.settingsRenderer.OnCurrentIndexChanged(func(int) { w.settingsEdited() })
		w.settingsFPSMode.OnCurrentIndexChanged(func(int) { w.settingsEdited() })
		w.settingsFPS.OnValueChanged(func(int) { w.settingsEdited() })
		w.settingsVSync.OnToggled(func(enabled bool) {
			w.settingsVSync.SetText(vsyncToggleText(enabled))
			w.settingsEdited()
		})
		w.settingsLowTexture.OnToggled(func(enabled bool) {
			w.settingsLowTexture.SetText(lowTextureToggleText(enabled))
			w.settingsEdited()
		})
		w.settingsDiscordPresence.OnToggled(func(enabled bool) {
			w.settingsDiscordPresence.SetText(discordPresenceToggleText(enabled))
			w.settingsEdited()
		})
		w.settingsDiscordJoin.OnToggled(func(enabled bool) {
			w.settingsDiscordJoin.SetText(discordJoinToggleText(enabled))
			w.settingsEdited()
		})
		w.settingsDisplay.OnCurrentIndexChanged(func(int) { w.settingsEdited() })
		w.settingsStartFullscreen.OnToggled(func(enabled bool) {
			w.settingsStartFullscreen.SetText(startFullscreenToggleText(enabled))
			w.settingsEdited()
		})
		w.updateSettingsControls()
	}

	limits, limitsLayout := newVerticalCard("subtleCard")
	limitsLayout.AddWidget(sectionLabel("Graphics and performance notes").QWidget)
	limitsText := qt.NewQLabel3("<b>Auto</b> leaves Roblox's frame-rate choice alone. <b>Limited</b> supports 30–240 FPS. <b>Unlimited</b> sends an experimental uncapped request. Actual FPS depends on the client, CPU, GPU, and driver; no particular frame rate is guaranteed. Higher rates can increase power use and instability.<br><br><b>VSync</b> is disabled by default and is independent of the frame-rate cap. Enabling it synchronizes presentation to the active monitor refresh rate. Leaving it disabled permits above-refresh presentation but can cause visible tearing.<br><br><b>Low texture mode</b> is off by default so Roblox loads high-quality textures. Enable it to use lower-resolution textures and save memory/VRAM. Changing texture quality requires a Roblox restart.<br><br><b>Discord Rich Presence</b> is on by default and shows the current experience as Game name - Tipsy, with Tipsy branding on the artwork. The optional Join button opens the public Roblox experience page. Both apply while Roblox is running and never include account cookies or join tickets.<br><br><b>Default monitor</b> pins the Tipsy launcher and Roblox windows to the current main display. Choose a named monitor to keep them there even if the desktop primary changes. <b>Follow mouse</b> restores the previous window-manager placement from the pointer.<br><br><b>Vulkan</b> is visible for future compatibility but disabled until Tipsy has a Vulkan bridge and a working host driver.")
	limitsText.SetTextFormat(qt.RichText)
	limitsText.SetWordWrap(true)
	setObjectName(limitsText.QObject, "mutedText")
	limitsLayout.AddWidget(limitsText.QWidget)
	layout.AddWidget(limits.QWidget)
	layout.AddStretch()
	return w.registerPage(page, scroll)
}

func (w *mainWindow) bindSettings(settings guimodel.Settings) {
	w.settingsSyncing = true
	defer func() { w.settingsSyncing = false }()
	switch settings.Renderer {
	case guimodel.RendererOpenGL:
		w.settingsRenderer.SetCurrentIndex(1)
	case guimodel.RendererVulkan:
		// Unsupported persisted values are displayed honestly but cannot be
		// newly selected or applied by this build.
		w.settingsRenderer.SetCurrentIndex(2)
	default:
		w.settingsRenderer.SetCurrentIndex(0)
	}
	switch settings.FPSMode {
	case guimodel.FPSLimited:
		w.settingsFPSMode.SetCurrentIndex(1)
	case guimodel.FPSUnlimited:
		w.settingsFPSMode.SetCurrentIndex(2)
	default:
		w.settingsFPSMode.SetCurrentIndex(0)
	}
	if settings.FrameRate >= guimodel.MinFrameRate && settings.FrameRate <= guimodel.MaxFrameRate {
		w.settingsFPS.SetValue(settings.FrameRate)
	} else {
		w.settingsFPS.SetValue(60)
	}
	w.settingsVSync.SetChecked(settings.VSync)
	w.settingsVSync.SetText(vsyncToggleText(settings.VSync))
	w.settingsLowTexture.SetChecked(settings.LowTextureMode)
	w.settingsLowTexture.SetText(lowTextureToggleText(settings.LowTextureMode))
	w.settingsDiscordPresence.SetChecked(settings.DiscordRichPresence)
	w.settingsDiscordPresence.SetText(discordPresenceToggleText(settings.DiscordRichPresence))
	w.settingsDiscordJoin.SetChecked(settings.DiscordJoinButton)
	w.settingsDiscordJoin.SetText(discordJoinToggleText(settings.DiscordJoinButton))
	w.populateDisplayChoices(settings.Display)
	w.settingsStartFullscreen.SetChecked(settings.StartFullscreen)
	w.settingsStartFullscreen.SetText(startFullscreenToggleText(settings.StartFullscreen))
}

func (w *mainWindow) populateDisplayChoices(selected string) {
	if w.settingsDisplay == nil {
		return
	}
	w.settingsDisplay.Clear()
	w.settingsDisplayKeys = nil
	selected = guimodel.NormalizeDisplay(selected)
	matched := 0
	for i, choice := range desktopDisplayChoices(selected) {
		w.settingsDisplay.AddItem(choice.label)
		w.settingsDisplayKeys = append(w.settingsDisplayKeys, choice.key)
		if choice.key == selected {
			matched = i
		}
	}
	w.settingsDisplay.SetCurrentIndex(matched)
}

func (w *mainWindow) settingsEdited() {
	if w.settingsRenderer == nil || w.settingsVSync == nil || w.settingsLowTexture == nil || w.settingsDiscordPresence == nil || w.settingsDiscordJoin == nil || w.settingsStartFullscreen == nil || w.settingsSyncing {
		return
	}
	renderer := guimodel.RendererAuto
	if w.settingsRenderer.CurrentIndex() == 1 {
		renderer = guimodel.RendererOpenGL
	} else if w.settingsRenderer.CurrentIndex() == 2 {
		renderer = guimodel.RendererVulkan
	}
	fpsMode := guimodel.FPSAuto
	switch w.settingsFPSMode.CurrentIndex() {
	case 1:
		fpsMode = guimodel.FPSLimited
	case 2:
		fpsMode = guimodel.FPSUnlimited
	}
	display := guimodel.DisplayPrimary
	if w.settingsDisplay != nil {
		if idx := w.settingsDisplay.CurrentIndex(); idx >= 0 && idx < len(w.settingsDisplayKeys) {
			display = w.settingsDisplayKeys[idx]
		}
	}
	w.settings.Edit(guimodel.Settings{Renderer: renderer, FPSMode: fpsMode, FrameRate: w.settingsFPS.Value(), VSync: w.settingsVSync.IsChecked(), LowTextureMode: w.settingsLowTexture.IsChecked(), Display: display, StartFullscreen: w.settingsStartFullscreen.IsChecked(), DiscordRichPresence: w.settingsDiscordPresence.IsChecked(), DiscordJoinButton: w.settingsDiscordJoin.IsChecked()})
	w.updateSettingsControls()
}

func (w *mainWindow) updateSettingsControls() {
	view := w.settings.View()
	w.settingsFPS.SetEnabled(view.Draft.FPSMode == guimodel.FPSLimited)
	if w.settingsDiscordJoin != nil && w.settingsDiscordPresence != nil {
		w.settingsDiscordJoin.SetEnabled(view.Draft.DiscordRichPresence)
	}
	canApply := view.Dirty && view.ValidationError == ""
	w.settingsApply.SetEnabled(canApply)
	if view.ValidationError != "" {
		w.settingsHint.SetText(view.ValidationError)
		if reason := unavailableRendererReason(view.RendererOptions, view.Draft.Renderer); reason != "" {
			w.settingsHint.SetText(rendererName(view.Draft.Renderer) + " is not available: " + reason)
		}
		setObjectName(w.settingsHint.QObject, "noticeWarning")
	} else if view.Dirty && view.Draft.FPSMode == guimodel.FPSUnlimited {
		w.settingsHint.SetText("Unlimited sends an experimental uncapped request. Actual FPS depends on the client, CPU, GPU, and driver; no frame rate is guaranteed. Restart required.")
		setObjectName(w.settingsHint.QObject, "noticeWarning")
	} else if view.Dirty && guimodel.DiscordOnlyChange(view.Draft, view.Saved) {
		w.settingsHint.SetText("Changes are not saved yet. Discord Rich Presence applies while Roblox is running.")
		setObjectName(w.settingsHint.QObject, "noticeWarning")
	} else if view.Dirty {
		w.settingsHint.SetText("Changes are not saved yet. Applying them requires a Roblox restart.")
		setObjectName(w.settingsHint.QObject, "noticeWarning")
	} else if view.ApplyNote != "" {
		message := view.ApplyNote
		if view.RestartRequired {
			message += " Restart Roblox to apply the saved choice."
		}
		w.settingsHint.SetText(message)
		setObjectName(w.settingsHint.QObject, "noticeWarning")
	} else if view.RestartRequired {
		w.settingsHint.SetText("Changes saved. Restart Roblox to apply them.")
		setObjectName(w.settingsHint.QObject, "noticeSuccess")
	} else {
		w.settingsHint.SetText("Roblox must be restarted before renderer, frame-rate, VSync, or texture quality changes take effect. Discord Rich Presence applies while Roblox is running. The default monitor applies to the next Tipsy or Roblox window; Start Roblox fullscreen applies to the next Roblox window.")
		setObjectName(w.settingsHint.QObject, "noticeInfo")
	}
	refreshStyle(w.settingsHint.QWidget)
	if !view.Dirty {
		w.refreshSettingsProfile()
	}
}

func rendererOptionText(option guimodel.RendererOption) string {
	var text string
	switch option.Renderer {
	case guimodel.RendererAuto:
		text = "Auto (recommended)"
	case guimodel.RendererOpenGL:
		text = "OpenGL"
	case guimodel.RendererVulkan:
		text = "Vulkan"
	default:
		text = "Unknown renderer"
	}
	if !option.Available {
		text += " — not available in this build"
	}
	return text
}

func unavailableRendererReason(options []guimodel.RendererOption, renderer guimodel.Renderer) string {
	for _, option := range options {
		if option.Renderer == renderer && !option.Available {
			return option.Reason
		}
	}
	return ""
}

func (w *mainWindow) applySettings() {
	result, err := w.settings.Apply(context.Background())
	if err != nil {
		qt.QMessageBox_Critical(w.win.QWidget, "Could not save settings", err.Error())
		return
	}
	w.updateSettingsControls()
	placeWidgetOnDisplay(w.win.QWidget, configuredDisplay(w))
	if result.RestartRequired {
		w.status.ShowMessage2("Settings saved — restart Roblox to apply them.", 7000)
	} else {
		w.status.ShowMessage2("Settings saved.", 5000)
	}
}

func (w *mainWindow) resetSettings() {
	settings, err := w.settings.Reset(context.Background())
	if err != nil {
		qt.QMessageBox_Critical(w.win.QWidget, "Could not reset settings", err.Error())
		return
	}
	w.bindSettings(settings)
	w.updateSettingsControls()
	placeWidgetOnDisplay(w.win.QWidget, configuredDisplay(w))
	w.status.ShowMessage2("Settings reset to safe defaults.", 5000)
}

func (w *mainWindow) buildDiagnosticsPage() *qt.QWidget {
	page, layout, scroll := newPage("Diagnostics", "A privacy-safe readiness view for your display, graphics stack, and installation.")

	summaryCard, summaryLayout := newVerticalCard("card")
	top := qt.NewQHBoxLayout2()
	top.AddWidget(sectionLabel("System readiness").QWidget)
	top.AddStretch()
	run := qt.NewQPushButton3("Run checks")
	setObjectName(run.QObject, "secondaryButton")
	run.SetAccessibleDescription("Refresh the local system-readiness report")
	run.OnClicked(w.runDoctor)
	top.AddWidget(run.QWidget)
	w.pageEntry = append(w.pageEntry, run.QWidget)
	summaryLayout.AddLayout(top.QLayout)
	w.doctorSummary = qt.NewQLabel3("Run checks to review system readiness.")
	w.doctorSummary.SetTextFormat(qt.RichText)
	w.doctorSummary.SetWordWrap(true)
	w.doctorSummary.SetTextInteractionFlags(qt.TextBrowserInteraction)
	setObjectName(w.doctorSummary.QObject, "doctorSummary")
	summaryLayout.AddWidget(w.doctorSummary.QWidget)
	w.doctorDetails = qt.NewQPlainTextEdit2()
	w.doctorDetails.SetReadOnly(true)
	w.doctorDetails.SetPlaceholderText("Technical check details appear here.")
	w.doctorDetails.SetMinimumHeight(170)
	w.doctorDetails.SetAccessibleName("Technical check details")
	applyMonoFont(w.doctorDetails)
	summaryLayout.AddWidget(w.doctorDetails.QWidget)
	layout.AddWidget(summaryCard.QWidget)

	pathsCard, pathsLayout := newVerticalCard("card")
	pathsLayout.AddWidget(sectionLabel("Useful locations").QWidget)
	paths := config.Paths()
	grid := qt.NewQGridLayout2()
	grid.SetHorizontalSpacing(12)
	grid.SetVerticalSpacing(12)
	grid.AddWidget2(pathTitle("Logs").QWidget, 0, 0)
	grid.AddWidget2(pathValue(paths.LogDir).QWidget, 0, 1)
	openLogs := qt.NewQPushButton3("Open")
	setObjectName(openLogs.QObject, "secondaryButton")
	openLogs.SetAccessibleName("Open logs folder")
	openLogs.OnClicked(func() { openLocalDirectory(w.win.QWidget, paths.LogDir) })
	grid.AddWidget2(openLogs.QWidget, 0, 2)
	grid.AddWidget2(pathTitle("Configuration").QWidget, 1, 0)
	grid.AddWidget2(pathValue(filepath.Dir(paths.ConfigFile)).QWidget, 1, 1)
	openConfig := qt.NewQPushButton3("Open")
	setObjectName(openConfig.QObject, "secondaryButton")
	openConfig.SetAccessibleName("Open configuration folder")
	openConfig.OnClicked(func() { openLocalDirectory(w.win.QWidget, filepath.Dir(paths.ConfigFile)) })
	grid.AddWidget2(openConfig.QWidget, 1, 2)
	grid.SetColumnStretch(1, 1)
	pathsLayout.AddLayout(grid.QLayout)
	privacy := qt.NewQLabel3("Tipsy diagnostics never display passwords, cookies, tokens, or Roblox account storage.")
	privacy.SetWordWrap(true)
	setObjectName(privacy.QObject, "noticeInfo")
	pathsLayout.AddWidget(privacy.QWidget)
	layout.AddWidget(pathsCard.QWidget)
	layout.AddStretch()
	return w.registerPage(page, scroll)
}

func (w *mainWindow) runDoctor() {
	if w.doctorSummary == nil {
		return
	}
	report, err := w.service.Doctor(context.Background())
	if err != nil {
		w.doctorSummary.SetText("<b>Checks could not be completed.</b>")
		w.doctorDetails.SetPlainText(err.Error())
		return
	}
	var rows []string
	var details []string
	for _, check := range report.Checks {
		color, marker := "#66758f", "•"
		switch check.Status {
		case guimodel.CheckReady:
			color, marker = "#14805e", "✓"
		case guimodel.CheckWarning:
			color, marker = "#946500", "!"
		case guimodel.CheckBlocked:
			color, marker = "#bd3155", "×"
		}
		rows = append(rows, fmt.Sprintf("<p><span style='color:%s;font-size:18px'><b>%s</b></span>&nbsp;&nbsp;<b>%s</b><br><span style='color:#66758f'>%s</span></p>", color, marker, html.EscapeString(check.Name), html.EscapeString(check.Detail)))
		line := check.Name + ": " + check.Detail
		if check.Remedy != "" {
			line += "\n  Next: " + check.Remedy
		}
		details = append(details, line)
	}
	if len(rows) == 0 {
		rows = append(rows, "<p>No readiness checks were returned.</p>")
	}
	headline := "Some items need attention"
	if report.Ready {
		headline = "Ready to run Tipsy"
	}
	w.doctorSummary.SetText("<h3>" + headline + "</h3>" + strings.Join(rows, ""))
	w.doctorDetails.SetPlainText(strings.Join(details, "\n\n"))
	w.status.ShowMessage2("System checks complete.", 5000)
}

func settingsProfileText(settings guimodel.Settings) string {
	return rendererDisplay(settings.Renderer) + " · " + fpsDisplay(settings) + " · " + vsyncDisplay(settings) + " · " + textureDisplay(settings) + " · " + discordDisplay(settings) + " · " + displayTargetDisplay(settings) + " · " + fullscreenStartDisplay(settings)
}

func rendererDisplay(renderer guimodel.Renderer) string {
	switch renderer {
	case guimodel.RendererOpenGL:
		return "OpenGL"
	case guimodel.RendererVulkan:
		return "Vulkan unavailable"
	default:
		return "Auto renderer"
	}
}

func rendererName(renderer guimodel.Renderer) string {
	switch renderer {
	case guimodel.RendererOpenGL:
		return "OpenGL"
	case guimodel.RendererVulkan:
		return "Vulkan"
	default:
		return "Auto"
	}
}

func fpsDisplay(settings guimodel.Settings) string {
	switch settings.FPSMode {
	case guimodel.FPSLimited:
		return fmt.Sprintf("%d FPS", settings.FrameRate)
	case guimodel.FPSUnlimited:
		return "Uncapped request (experimental)"
	default:
		return "Automatic FPS"
	}
}

func vsyncDisplay(settings guimodel.Settings) string {
	if settings.VSync {
		return "VSync on"
	}
	return "VSync off"
}

func vsyncToggleText(enabled bool) string {
	if enabled {
		return "✓ VSync enabled — presentation follows the active monitor refresh rate"
	}
	return "VSync off — presentation is not synchronized to the monitor refresh rate"
}

func textureDisplay(settings guimodel.Settings) string {
	if settings.LowTextureMode {
		return "Low textures"
	}
	return "High textures"
}

func lowTextureToggleText(enabled bool) string {
	if enabled {
		return "✓ Low texture mode — lower-resolution textures to save memory and VRAM"
	}
	return "High-quality textures — default; uses more memory and VRAM"
}

func discordDisplay(settings guimodel.Settings) string {
	if !settings.DiscordRichPresence {
		return "Discord off"
	}
	if settings.DiscordJoinButton {
		return "Discord + Join"
	}
	return "Discord"
}

func discordPresenceToggleText(enabled bool) string {
	if enabled {
		return "✓ Discord Rich Presence — friends see Game name - Tipsy"
	}
	return "Discord Rich Presence off — Tipsy will not set a Discord activity"
}

func discordJoinToggleText(enabled bool) string {
	if enabled {
		return "✓ Join button — opens the public Roblox experience page"
	}
	return "Join button off — Discord activity has no Join control"
}

func fullscreenStartDisplay(settings guimodel.Settings) string {
	if settings.StartFullscreen {
		return "Fullscreen on launch"
	}
	return "Windowed on launch"
}

func startFullscreenToggleText(enabled bool) string {
	if enabled {
		return "✓ Start Roblox fullscreen — Tipsy asks the desktop to fullscreen new Roblox windows"
	}
	return "Start Roblox fullscreen off — new Roblox windows open windowed"
}

func displayTargetDisplay(settings guimodel.Settings) string {
	switch guimodel.NormalizeDisplay(settings.Display) {
	case guimodel.DisplayPointer:
		return "Follow mouse"
	case guimodel.DisplayPrimary:
		return "Main monitor"
	default:
		return settings.Display
	}
}

func automaticExplanation(a guimodel.AutomaticAvailability) string {
	if a.Available {
		if a.Explanation != "" {
			return a.Explanation
		}
		return "Downloads from the verified source selected by the setup backend."
	}
	if a.Reason != "" {
		return "Unavailable: " + a.Reason
	}
	return "Unavailable until a package provider is configured."
}

func pathTitle(text string) *qt.QLabel {
	label := qt.NewQLabel3(text)
	setObjectName(label.QObject, "formLabel")
	return label
}

func pathValue(text string) *qt.QLabel {
	label := qt.NewQLabel3(text)
	setObjectName(label.QObject, "pathValue")
	label.SetTextInteractionFlags(qt.TextSelectableByMouse)
	label.SetWordWrap(true)
	return label
}

func openLocalDirectory(parent *qt.QWidget, path string) {
	if path == "" {
		qt.QMessageBox_Warning(parent, "Location unavailable", "This location is not configured.")
		return
	}
	if _, err := os.Stat(path); err != nil {
		qt.QMessageBox_Warning(parent, "Location unavailable", "The directory does not exist yet:\n"+path)
		return
	}
	url := qt.QUrl_FromLocalFile(path)
	if !qt.QDesktopServices_OpenUrl(url) {
		qt.QMessageBox_Warning(parent, "Could not open location", path)
	}
}
