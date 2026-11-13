// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

// TestMain keeps every GUI test from re-executing the test binary as the
// isolated anti-lag helper. The production binary answers that command in
// main before Qt starts.
func TestMain(m *testing.M) {
	probeAntiLagSupport = func() graphics.AntiLagSupport {
		return graphics.AntiLagSupport{Reason: "anti-lag probe disabled in GUI tests"}
	}
	// No GUI test may reach the release API: every window built here starts
	// with a fake fetch, and tests that need a result replace it per window.
	updateCheckFetch = fakeReleaseFetch
	os.Exit(m.Run())
}

func TestAntiLagRowReflectsProbe(t *testing.T) {
	if got := antiLagToggleText(graphics.AntiLagSupport{}, false); strings.Contains(got, "unavailable") {
		t.Fatalf("unprobed label=%q", got)
	}
	if got := antiLagTooltip(graphics.AntiLagSupport{}, false, ""); !strings.Contains(got, "probe") {
		t.Fatalf("unprobed tooltip=%q", got)
	}
	if got := antiLagStatusText(graphics.AntiLagSupport{}, false, ""); !strings.Contains(got, "off") {
		t.Fatalf("unprobed status=%q", got)
	}

	unavailable := graphics.AntiLagSupport{Reason: "driver is not VK_DRIVER_ID_MESA_RADV"}
	if got := antiLagToggleText(unavailable, true); !strings.Contains(got, "unavailable") {
		t.Fatalf("unavailable label=%q", got)
	}
	if got := antiLagTooltip(unavailable, true, unavailable.Reason); got != unavailable.Reason {
		t.Fatalf("unavailable tooltip=%q", got)
	}
	if got := antiLagStatusText(unavailable, true, unavailable.Reason); !strings.Contains(got, unavailable.Reason) {
		t.Fatalf("unavailable status=%q", got)
	}

	available := graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true, Driver: "RADV NAVI32"}
	if got := antiLagToggleText(available, true); !strings.Contains(got, "VK_AMD_anti_lag") {
		t.Fatalf("available label=%q", got)
	}
	if got := antiLagTooltip(available, true, ""); !strings.Contains(got, "ENABLE_LAYER_MESA_ANTI_LAG") {
		t.Fatalf("layer tooltip=%q", got)
	}
	if got := antiLagStatusText(available, true, ""); !strings.Contains(got, "RADV NAVI32") {
		t.Fatalf("available status=%q", got)
	}
}

func TestExperimentalSettingsDifferCoversAntiLagOnly(t *testing.T) {
	saved := clientsettings.Settings{}
	if experimentalSettingsDiffer(saved, saved) {
		t.Fatal("identical settings demanded a restart")
	}
	wanted := saved
	wanted.AntiLag = true
	if !experimentalSettingsDiffer(saved, wanted) {
		t.Fatal("anti-lag change did not demand a restart")
	}
}

// TestCachedUnavailableProbeClearsDraftOptIn covers the build-time race: the
// probe finished before the card was built and reported an unsupported driver,
// so the stored opt-in must leave the shared draft and the disabled toggle must
// show unchecked instead of disagreeing with a draft Apply could persist.
func TestCachedUnavailableProbeClearsDraftOptIn(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()

	// Install a finished, unavailable probe result without starting a goroutine.
	antiLagProbe.once = sync.Once{}
	antiLagProbe.support = graphics.AntiLagSupport{Reason: "driver is not VK_DRIVER_ID_MESA_RADV"}
	antiLagProbe.once.Do(func() {
		antiLagProbe.done = make(chan struct{})
		close(antiLagProbe.done)
	})
	defer func() {
		antiLagProbe.once = sync.Once{}
		antiLagProbe.done = nil
		antiLagProbe.support = graphics.AntiLagSupport{}
	}()

	settings := guimodel.DefaultSettings()
	settings.AntiLag = true
	win := newMainWindow(&persistedSettingsService{settings: settings}, brandIcon())
	defer win.win.Delete()

	if win.settingsErr != nil {
		t.Fatalf("load settings: %v", win.settingsErr)
	}
	if win.settings.View().Draft.AntiLag {
		t.Fatal("cached unavailable probe left the draft anti-lag opt-in set")
	}
	if win.experimental.antiLag == nil {
		t.Fatal("anti-lag toggle was not built")
	}
	if win.experimental.antiLag.IsChecked() {
		t.Fatal("cached unavailable probe left the anti-lag toggle checked")
	}
	if win.experimental.antiLag.IsEnabled() {
		t.Fatal("cached unavailable probe left the anti-lag toggle enabled")
	}
}

func TestAntiLagProbeRunsOnlyWhenRequested(t *testing.T) {
	if antiLagProbeNeeded(guimodel.Settings{}, nil) {
		t.Fatal("the probe ran while anti-lag was off")
	}
	if !antiLagProbeNeeded(guimodel.Settings{AntiLag: true}, nil) {
		t.Fatal("the probe was skipped for a stored opt-in")
	}
	if antiLagProbeNeeded(guimodel.Settings{AntiLag: true}, os.ErrNotExist) {
		t.Fatal("the probe ran after the settings document failed to load")
	}
}
