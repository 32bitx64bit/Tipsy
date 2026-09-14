// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/mappu/miqt/qt6"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func newHostOverheadApp(t *testing.T) *qt.QApplication {
	t.Helper()
	t.Setenv("QT_QPA_PLATFORM", "offscreen")
	root := t.TempDir()
	for _, dir := range []string{"CONFIG", "DATA", "CACHE", "STATE"} {
		t.Setenv("XDG_"+dir+"_HOME", filepath.Join(root, dir))
	}
	t.Setenv("TIPSY_ICON_PATH", filepath.Join("..", "..", "tipsy.png"))
	app := qt.NewQApplication([]string{"tipsy-host-overhead-test"})
	qt.QApplication_SetStyleWithStyle("Fusion")
	return app
}

func TestOwnerThreadQueueCoalescesLatestStateInOrder(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()

	parent := qt.NewQWidget2()
	defer parent.Delete()
	metrics := &guiRuntimeMetrics{}
	var mu sync.Mutex
	state := 0
	var observed []int
	notifier, err := newOwnerThreadNotifier(parent.QObject, func() {
		mu.Lock()
		observed = append(observed, state)
		mu.Unlock()
	}, metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer notifier.close()

	state = 1
	notifier.notify()
	state = 2
	notifier.notify()
	notifier.notify()
	if got := metrics.snapshot(); got.OwnerQueued != 1 || got.OwnerCoalesced != 2 || got.OwnerDispatched != 0 {
		t.Fatalf("pre-dispatch queue metrics=%+v", got)
	}
	waitGUI(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(observed) == 1
	})
	state = 3
	notifier.notify()
	waitGUI(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(observed) == 2
	})
	mu.Lock()
	got := append([]int(nil), observed...)
	mu.Unlock()
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("queued owner dispatch order=%v, want latest coalesced state then next state", got)
	}
	if got := metrics.snapshot(); got.OwnerDispatched != 2 {
		t.Fatalf("dispatch metrics=%+v", got)
	} else {
		t.Logf("owner queue metrics: %+v", got)
	}
}

func TestHiddenLauncherStopsVisibleTimerButHandlesClientExitAndError(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()

	service := newProgressService()
	close(service.snapshotGate)
	close(service.prepareGate)
	service.failAfterStart = true
	service.launchError = errors.New("synthetic post-start error")
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()
	win.Show()
	if got := win.metrics.snapshot(); got.AppearanceStarts != 1 {
		t.Fatalf("visible launcher did not enable its appearance cadence: %+v", got)
	}
	if !win.launchRoblox() {
		t.Fatal("synthetic launch was rejected")
	}
	waitGUI(t, func() bool { return service.launches.Load() == 1 })
	close(service.launchGate)
	waitGUI(t, func() bool { return win.launcherHidden })
	if got := win.metrics.snapshot(); got.AppearanceStops == 0 {
		t.Fatalf("visible-only appearance cadence was not stopped while launcher was hidden: %+v", got)
	}
	close(service.finish)
	waitGUI(t, func() bool { return win.launchFailure && win.win.IsVisible() })
	if win.metrics.snapshot().AppearanceStarts < 2 || win.playState.Text() != safeRuntimeFailure {
		t.Fatalf("post-start failure did not restore actionable launcher: metrics=%+v state=%q", win.metrics.snapshot(), win.playState.Text())
	}
	metrics := win.metrics.snapshot()
	if metrics.AppearanceStarts < 2 || metrics.AppearanceStops == 0 || metrics.OwnerDispatched < 2 {
		t.Fatalf("hide/show queue cadence metrics=%+v", metrics)
	}
	t.Logf("hidden launcher metrics: %+v", metrics)

	// Use the existing hidden launcher for a fresh acknowledged run so the
	// clean-exit branch cannot be masked by the failure-recovery presentation.
	clean := newProgressService()
	close(clean.snapshotGate)
	close(clean.prepareGate)
	cleanWin := newMainWindow(clean, brandIcon())
	defer cleanWin.win.Delete()
	var quits atomic.Int32
	cleanWin.quit = func() { quits.Add(1) }
	cleanWin.Show()
	if !cleanWin.launchRoblox() {
		t.Fatal("clean synthetic launch was rejected")
	}
	waitGUI(t, func() bool { return clean.launches.Load() == 1 })
	close(clean.launchGate)
	waitGUI(t, func() bool { return cleanWin.launcherHidden })
	close(clean.finish)
	waitGUI(t, func() bool { return quits.Load() == 1 })
}

type optionalLookupService struct {
	visualService
	controllerErr, microphoneErr     atomic.Bool
	controllerCalls, microphoneCalls atomic.Int32
}

func (s *optionalLookupService) ControllerPads(context.Context) (guimodel.ControllerState, error) {
	s.controllerCalls.Add(1)
	if s.controllerErr.Load() {
		return guimodel.ControllerState{}, errors.New("synthetic controller service unavailable")
	}
	return s.visualService.ControllerPads(context.Background())
}

func (s *optionalLookupService) MicrophoneStatus(context.Context) (guimodel.MicrophoneState, error) {
	s.microphoneCalls.Add(1)
	if s.microphoneErr.Load() {
		return guimodel.MicrophoneState{}, errors.New("synthetic microphone service unavailable")
	}
	return s.visualService.MicrophoneStatus(context.Background())
}

func TestOptionalDiagnosticLookupsBackOffAndRecover(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()
	service := &optionalLookupService{}
	service.controllerErr.Store(true)
	service.microphoneErr.Store(true)
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()
	now := time.Unix(1234, 0)
	win.now = func() time.Time { return now }

	win.selectPage(2)
	if service.controllerCalls.Load() != 1 || service.microphoneCalls.Load() != 1 {
		t.Fatalf("initial optional lookups controller=%d microphone=%d", service.controllerCalls.Load(), service.microphoneCalls.Load())
	}
	win.selectPage(2)
	if service.controllerCalls.Load() != 1 || service.microphoneCalls.Load() != 1 {
		t.Fatal("failed automatic lookups ignored their retry backoff")
	}
	if got := win.metrics.snapshot(); got.OptionalDeferred != 2 {
		t.Fatalf("backoff metrics=%+v", got)
	}

	service.controllerErr.Store(false)
	service.microphoneErr.Store(false)
	win.refreshControllerPads()   // explicit Refresh bypasses automatic backoff
	win.refreshMicrophoneStatus() // a consent change is an explicit status refresh
	if service.controllerCalls.Load() != 2 || service.microphoneCalls.Load() != 2 {
		t.Fatalf("explicit recovery lookup controller=%d microphone=%d", service.controllerCalls.Load(), service.microphoneCalls.Load())
	}
	win.selectPage(2)
	if service.controllerCalls.Load() != 3 || service.microphoneCalls.Load() != 3 {
		t.Fatal("successful recovery did not reset automatic lookup backoff")
	}
	t.Logf("optional lookup metrics: %+v controllerCalls=%d microphoneCalls=%d", win.metrics.snapshot(), service.controllerCalls.Load(), service.microphoneCalls.Load())
}

type persistedSettingsService struct {
	visualService
	mu       sync.Mutex
	settings guimodel.Settings
}

func (s *persistedSettingsService) LoadSettings(context.Context) (guimodel.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, nil
}

func (s *persistedSettingsService) ApplySettings(_ context.Context, settings guimodel.Settings) (guimodel.ApplyResult, error) {
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return guimodel.ApplyResult{RestartRequired: true}, nil
}

func TestSettingsHydrationSkipsUnchangedSettersAndPersists(t *testing.T) {
	app := newHostOverheadApp(t)
	defer app.Delete()
	settings := guimodel.DefaultSettings()
	settings.FPSMode = guimodel.FPSLimited
	settings.FrameRate = 144
	service := &persistedSettingsService{settings: settings}
	win := newMainWindow(service, brandIcon())
	defer win.win.Delete()

	before := win.metrics.snapshot()
	win.bindSettings(settings)
	win.bindSettings(settings)
	win.refreshSettingsProfile()
	win.refreshSettingsProfile()
	after := win.metrics.snapshot()
	if after.WidgetWrites != before.WidgetWrites || after.WidgetSkipped <= before.WidgetSkipped || after.SettingsRefreshSkipped < before.SettingsRefreshSkipped+2 {
		t.Fatalf("unchanged settings hydration performed presentation work: before=%+v after=%+v", before, after)
	}
	t.Logf("settings hydration metrics: before=%+v after=%+v", before, after)

	win.settingsVSync.SetChecked(true)
	win.applySettings()
	fresh := newMainWindow(service, brandIcon())
	defer fresh.win.Delete()
	if !fresh.settings.View().Saved.VSync || !fresh.settingsVSync.IsChecked() {
		t.Fatalf("persisted settings did not hydrate into a fresh launcher: saved=%+v checked=%v", fresh.settings.View().Saved, fresh.settingsVSync.IsChecked())
	}
}
