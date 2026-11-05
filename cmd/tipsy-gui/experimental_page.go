// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"sync"

	qt "github.com/mappu/miqt/qt6"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

// probeAntiLagSupport is a variable so tests never spawn the test binary as
// the isolated anti-lag helper (the GUI binary handles that command in main).
var probeAntiLagSupport = graphics.ProbeAntiLagSupport

// antiLagProbe caches one probe result for this GUI process. The card starts
// it only for an anti-lag opt-in, so a slow driver probe never blocks the UI
// thread and a page rebuild does not run it again.
var antiLagProbe struct {
	once    sync.Once
	done    chan struct{}
	support graphics.AntiLagSupport
}

func startAntiLagProbe() <-chan struct{} {
	antiLagProbe.once.Do(func() {
		antiLagProbe.done = make(chan struct{})
		go func() {
			antiLagProbe.support = probeAntiLagSupport()
			close(antiLagProbe.done)
		}()
	})
	return antiLagProbe.done
}

func cachedAntiLagProbe() (graphics.AntiLagSupport, bool) {
	select {
	case <-antiLagProbe.done:
		return antiLagProbe.support, true
	default:
		return graphics.AntiLagSupport{}, false
	}
}

// antiLagProbeNeeded reports whether the card may start the driver probe.
// Anti-lag is off by default and the probe is expensive, so the GUI runs it
// only for a stored opt-in; enabling the toggle starts it directly.
func antiLagProbeNeeded(settings guimodel.Settings, settingsErr error) bool {
	return settingsErr == nil && settings.AntiLag
}

// experimentalCardWidgets keeps the card's controls so bindSettings can render
// a load or reset without rebuilding the page.
type experimentalCardWidgets struct {
	antiLag *qt.QCheckBox
	note    *qt.QLabel
	support graphics.AntiLagSupport
	probed  bool
	probing bool
}

// antiLagEditable reports whether the toggle may currently be clicked. Before
// the probe runs the switch is enabled so the user can request it; after a
// failed probe it stays disabled because this driver cannot support it.
func (c *experimentalCardWidgets) antiLagEditable() bool {
	if c.probing {
		return false
	}
	return c.support.Available || !c.probed
}

// setInteractive locks the card when the settings document could not be read.
// Anti-lag keeps its probe availability rule.
func (c *experimentalCardWidgets) setInteractive(enabled bool) {
	if c.antiLag == nil {
		return
	}
	c.antiLag.SetEnabled(enabled && c.antiLagEditable())
}

// buildExperimentalPerformanceCard renders the last Settings card. Anti-lag is
// the only opt-in arm and applies to the next Roblox launch; nothing here
// changes how Roblox renders.
func (w *mainWindow) buildExperimentalPerformanceCard() *qt.QFrame {
	card, cardLayout := newVerticalCard("card")
	cardLayout.AddWidget(sectionLabel("Experimental performance").QWidget)

	intro := qt.NewQLabel3("One opt-in experiment. Leave it off unless a measurement shows a win. Apply changes and restart Roblox to use a new choice.")
	intro.SetWordWrap(true)
	setObjectName(intro.QObject, "mutedText")
	cardLayout.AddWidget(intro.QWidget)

	requested := w.settings != nil && antiLagProbeNeeded(w.settings.View().Draft, w.settingsErr)
	if requested {
		startAntiLagProbe()
	}
	support, probed := cachedAntiLagProbe()
	if !requested {
		support, probed = graphics.AntiLagSupport{}, false
	}
	w.experimental.support = support
	w.experimental.probed = probed
	w.experimental.probing = requested && !probed

	reason := antiLagReason(support)
	w.experimental.antiLag = newExperimentalToggle(
		antiLagToggleText(support, probed),
		"AMD anti-lag",
		"VK_AMD_anti_lag lowers input latency on a compatible RADV driver. Off by default; check to opt in for the next launch. The probe must prove support.",
		antiLagTooltip(support, probed, reason),
	)
	w.experimental.antiLag.SetEnabled(w.settingsErr == nil && w.experimental.antiLagEditable())
	cardLayout.AddWidget(w.experimental.antiLag.QWidget)

	w.experimental.note = qt.NewQLabel3(antiLagStatusText(support, probed, reason))
	w.experimental.note.SetWordWrap(true)
	w.experimental.note.SetAccessibleName("AMD anti-lag status")
	setObjectName(w.experimental.note.QObject, "mutedText")
	cardLayout.AddWidget(w.experimental.note.QWidget)

	w.experimental.antiLag.OnToggled(func(checked bool) { w.antiLagToggled(checked) })

	// A probe that already finished can prove this driver cannot support
	// anti-lag while the stored draft still opts in. Clear that opt-in exactly
	// as the asynchronous failure path does, so the disabled, unchecked toggle
	// never disagrees with a draft that Apply could still persist.
	if w.settings != nil && probed && !support.Available && w.settings.View().Draft.AntiLag {
		w.setExperimentalDraftAntiLag(false)
	}

	if w.experimental.probing {
		w.pollAntiLagProbe()
	}

	return card
}

func antiLagReason(support graphics.AntiLagSupport) string {
	reason := strings.TrimSpace(support.Reason)
	if reason == "" {
		reason = "the driver probe did not prove VK_AMD_anti_lag on RADV"
	}
	return reason
}

// antiLagToggled starts the driver probe the first time the user requests
// anti-lag. The opt-in is not folded into the settings draft until the probe
// proves support.
func (w *mainWindow) antiLagToggled(checked bool) {
	if w.experimental.antiLag == nil || w.settingsSyncing {
		return
	}
	if checked && !w.experimental.probed {
		w.experimental.probing = true
		w.experimental.antiLag.SetEnabled(false)
		if w.experimental.note != nil {
			w.experimental.note.SetText("Checking this driver for VK_AMD_anti_lag…")
		}
		startAntiLagProbe()
		w.pollAntiLagProbe()
		return
	}
	w.experimentalEdited()
}

// pollAntiLagProbe waits for the background probe and installs its result.
func (w *mainWindow) pollAntiLagProbe() {
	timer := qt.NewQTimer2(w.win.QObject)
	timer.SetInterval(100)
	timer.OnTimeout(func() {
		support, ok := cachedAntiLagProbe()
		if !ok {
			return
		}
		timer.Stop()
		w.showAntiLagSupport(support)
	})
	// Start2 starts with the interval set above; QTimer.Start requires an
	// explicit interval.
	timer.Start2()
}

// showAntiLagSupport installs a finished background probe. An unavailable
// driver clears the draft opt-in so Tipsy never saves an unproven choice.
func (w *mainWindow) showAntiLagSupport(support graphics.AntiLagSupport) {
	w.experimental.support = support
	w.experimental.probed = true
	w.experimental.probing = false
	reason := antiLagReason(support)
	if w.experimental.antiLag != nil {
		checked := support.Available && w.experimental.antiLag.IsChecked()
		w.experimental.antiLag.SetText(antiLagToggleText(support, true))
		w.experimental.antiLag.SetToolTip(antiLagTooltip(support, true, reason))
		w.experimental.antiLag.SetEnabled(support.Available && w.settingsErr == nil)
		w.setExperimentalDraftAntiLag(checked)
	}
	if w.experimental.note != nil {
		w.experimental.note.SetText(antiLagStatusText(support, true, reason))
	}
}

// setExperimentalDraftAntiLag mirrors a proven probe result into the shared
// settings draft without saving and without flipping any other control.
func (w *mainWindow) setExperimentalDraftAntiLag(checked bool) {
	if w.settings == nil || w.experimental.antiLag == nil {
		return
	}
	draft := w.settings.View().Draft
	syncing := w.settingsSyncing
	w.settingsSyncing = true
	w.setCheckBoxChecked(w.experimental.antiLag, checked)
	w.settingsSyncing = syncing
	if draft.AntiLag == checked {
		return
	}
	draft.AntiLag = checked
	w.settings.Edit(draft)
	w.updateSettingsControls()
}

func newExperimentalToggle(text, accessibleName, description, tooltip string) *qt.QCheckBox {
	box := qt.NewQCheckBox3(text)
	setObjectName(box.QObject, "vsyncToggle")
	box.SetAccessibleName(accessibleName)
	box.SetAccessibleDescription(description)
	box.SetToolTip(tooltip)
	return box
}

func antiLagToggleText(support graphics.AntiLagSupport, probed bool) string {
	if !probed {
		return "AMD anti-lag"
	}
	if support.Available {
		return "AMD anti-lag (VK_AMD_anti_lag)"
	}
	return "AMD anti-lag (unavailable)"
}

func antiLagTooltip(support graphics.AntiLagSupport, probed bool, reason string) string {
	if !probed {
		return "Off by default. Check to probe this driver for VK_AMD_anti_lag and opt in for the next Roblox launch."
	}
	if !support.Available {
		return reason
	}
	tooltip := "Off by default. Check to opt in for the next Roblox launch; the probe proved VK_AMD_anti_lag on this RADV driver."
	if support.NeedsLayerEnv {
		tooltip += " Tipsy enables ENABLE_LAYER_MESA_ANTI_LAG for the launch."
	}
	return tooltip
}

func antiLagStatusText(support graphics.AntiLagSupport, probed bool, reason string) string {
	if !probed {
		return "AMD anti-lag is off. Tipsy checks the driver only when you enable anti-lag."
	}
	if !support.Available {
		return "AMD anti-lag unavailable: " + reason + "."
	}
	if support.NeedsLayerEnv {
		status := "AMD anti-lag is available"
		if driver := strings.TrimSpace(support.Driver); driver != "" {
			status += " on " + driver
		}
		return status + ". This Mesa driver exposes VK_AMD_anti_lag only with ENABLE_LAYER_MESA_ANTI_LAG=1; Tipsy sets it for the launch unless you set the layer yourself."
	}
	if driver := strings.TrimSpace(support.Driver); driver != "" {
		return "AMD anti-lag available on " + driver + "."
	}
	return "AMD anti-lag is available on this driver."
}

// experimentalSettingsDiffer reports a changed anti-lag opt-in. The backend
// saves it as a document-only change, so the GUI asks for a relaunch itself:
// the choice only takes effect at the next Roblox launch.
func experimentalSettingsDiffer(saved, wanted clientsettings.Settings) bool {
	return saved.AntiLag != wanted.AntiLag
}

// bindExperimentalSettings renders the stored draft in the card. bindSettings
// calls it while settingsSyncing is set, so no programmatic toggle is saved.
func (w *mainWindow) bindExperimentalSettings(settings guimodel.Settings) {
	if w.experimental.antiLag == nil {
		return
	}
	support := w.experimental.support
	probed := w.experimental.probed
	reason := antiLagReason(support)
	checked := settings.AntiLag
	if probed && !support.Available {
		checked = false
	}
	w.setCheckBoxChecked(w.experimental.antiLag, checked)
	w.experimental.antiLag.SetText(antiLagToggleText(support, probed))
	w.experimental.antiLag.SetToolTip(antiLagTooltip(support, probed, reason))
	if w.experimental.note != nil {
		w.experimental.note.SetText(antiLagStatusText(support, probed, reason))
	}
	w.experimental.setInteractive(true)
}

// experimentalEdited folds the anti-lag switch into the shared Settings draft.
// The top-level Apply changes button stores the draft and the launch path
// exports it, so a card toggle never overwrites an unrelated draft edit.
func (w *mainWindow) experimentalEdited() {
	if w.settings == nil || w.experimental.antiLag == nil || w.settingsSyncing {
		return
	}
	if !w.experimental.probed || !w.experimental.support.Available {
		// Never fold an unproven opt-in into the draft.
		return
	}
	draft := w.settings.View().Draft
	draft.AntiLag = w.experimental.antiLag.IsChecked()
	w.settings.Edit(draft)
	w.updateSettingsControls()
}
