// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func TestAutomaticExplanationSurfacesProviderVerification(t *testing.T) {
	t.Parallel()
	got := automaticExplanation(guimodel.AutomaticAvailability{
		Available:   true,
		SourceName:  "APKPure",
		Explanation: "APKPure is unofficial transport. Tipsy then verifies the official Roblox package name, APK signatures, and x86-64 libroblox.so.",
	})
	if !strings.Contains(got, "unofficial") || !strings.Contains(got, "x86-64") || !strings.Contains(got, "signatures") {
		t.Fatalf("explanation=%q", got)
	}
}

func TestLowTextureModeSettingsAdapterRoundTrip(t *testing.T) {
	backend := clientsettings.Settings{
		Renderer:       clientsettings.RendererOpenGL,
		FrameRate:      clientsettings.FrameRate{Mode: clientsettings.FrameRateLimited, Limit: 144},
		LowTextureMode: true,
		Display:        clientsettings.DisplayPrimary,
	}
	gui := guiSettings(backend)
	if !gui.LowTextureMode || gui.VSync || gui.Renderer != guimodel.RendererOpenGL || gui.FPSMode != guimodel.FPSLimited || gui.FrameRate != 144 {
		t.Fatalf("backend to GUI settings=%+v", gui)
	}
	if got := backendSettings(gui); got != backend {
		t.Fatalf("GUI round trip=%+v want=%+v", got, backend)
	}
}

func TestLowTextureModeSettingsAdapterDefaultsOff(t *testing.T) {
	gui := guiSettings(clientsettings.Default())
	if gui.LowTextureMode {
		t.Fatal("backend default rendered low texture mode")
	}
	if got := backendSettings(guimodel.DefaultSettings()); got.LowTextureMode {
		t.Fatalf("GUI default persisted unexpected settings=%+v", got)
	}
}

func TestVSyncSettingsAdapterRoundTrip(t *testing.T) {
	backend := clientsettings.Settings{
		Renderer:  clientsettings.RendererOpenGL,
		FrameRate: clientsettings.FrameRate{Mode: clientsettings.FrameRateLimited, Limit: 144},
		VSync:     true,
		Display:   clientsettings.DisplayPrimary,
	}
	gui := guiSettings(backend)
	if !gui.VSync || gui.Renderer != guimodel.RendererOpenGL || gui.FPSMode != guimodel.FPSLimited || gui.FrameRate != 144 || gui.Display != guimodel.DisplayPrimary {
		t.Fatalf("backend to GUI settings=%+v", gui)
	}
	if got := backendSettings(gui); got != backend {
		t.Fatalf("GUI round trip=%+v want=%+v", got, backend)
	}
}

func TestVSyncSettingsAdapterDefaultsOff(t *testing.T) {
	gui := guiSettings(clientsettings.Default())
	if gui.VSync {
		t.Fatal("backend default rendered VSync enabled")
	}
	if got := backendSettings(guimodel.DefaultSettings()); got.VSync || got.LowTextureMode || got.Display != clientsettings.DisplayPrimary {
		t.Fatalf("GUI default persisted unexpected settings=%+v", got)
	}
}

func TestDisplaySettingsAdapterRoundTrip(t *testing.T) {
	backend := clientsettings.Settings{
		Renderer:  clientsettings.RendererAuto,
		FrameRate: clientsettings.FrameRate{Mode: clientsettings.FrameRateAuto},
		Display:   clientsettings.DisplayPointer,
	}
	gui := guiSettings(backend)
	if gui.Display != guimodel.DisplayPointer {
		t.Fatalf("pointer display GUI=%+v", gui)
	}
	if got := backendSettings(gui); got != backend {
		t.Fatalf("pointer round trip=%+v want=%+v", got, backend)
	}
	named := backend
	named.Display = "DP-1"
	if got := backendSettings(guiSettings(named)); got != named {
		t.Fatalf("named output round trip=%+v want=%+v", got, named)
	}
	if got := guiSettings(clientsettings.Settings{}); got.Display != guimodel.DisplayPrimary {
		t.Fatalf("missing display did not become primary: %+v", got)
	}
}
