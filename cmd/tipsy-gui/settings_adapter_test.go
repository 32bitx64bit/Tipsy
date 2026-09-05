// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func TestVSyncSettingsAdapterRoundTrip(t *testing.T) {
	backend := clientsettings.Settings{
		Renderer:  clientsettings.RendererOpenGL,
		FrameRate: clientsettings.FrameRate{Mode: clientsettings.FrameRateLimited, Limit: 144},
		VSync:     true,
	}
	gui := guiSettings(backend)
	if !gui.VSync || gui.Renderer != guimodel.RendererOpenGL || gui.FPSMode != guimodel.FPSLimited || gui.FrameRate != 144 {
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
	if got := backendSettings(guimodel.DefaultSettings()); got.VSync {
		t.Fatal("GUI default persisted VSync enabled")
	}
}
