package main

import (
	"testing"

	guimodel "github.com/tipsy-linux/tipsy/internal/gui"
)

func TestCenterWindowOnRect(t *testing.T) {
	x, y := centerWindowOnRect(0, 0, 2560, 1440, 1080, 720)
	if x != (2560-1080)/2 || y != (1440-720)/2 {
		t.Fatalf("center = (%d,%d)", x, y)
	}
	x, y = centerWindowOnRect(1920, 0, 1920, 1080, 3000, 2000)
	if x != 1920 || y != 0 {
		t.Fatalf("oversized = (%d,%d)", x, y)
	}
}

func TestDesktopDisplayChoicesIncludePrimaryPointerAndSaved(t *testing.T) {
	screens := []displayScreen{
		{name: "DP-1", width: 2560, height: 1440, primary: true},
		{name: "HDMI-0", width: 1920, height: 1080},
	}
	choices := displayChoices(guimodel.DisplayPrimary, screens)
	if len(choices) != 4 || choices[0].key != guimodel.DisplayPrimary || choices[1].key != guimodel.DisplayPointer {
		t.Fatalf("choices=%+v", choices)
	}
	if choices[2].key != "DP-1" || choices[2].label != "DP-1 · 2560×1440 (main)" {
		t.Fatalf("primary output label=%+v", choices[2])
	}
	if choices[3].key != "HDMI-0" || choices[3].label != "HDMI-0 · 1920×1080" {
		t.Fatalf("secondary output label=%+v", choices[3])
	}
	missing := displayChoices("HDMI-missing", screens)
	last := missing[len(missing)-1]
	if last.key != "HDMI-missing" || last.label != "HDMI-missing (not connected)" {
		t.Fatalf("disconnected choice=%+v", last)
	}
}
