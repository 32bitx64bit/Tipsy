// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import "testing"

func TestResolvePlacementPrimaryNamedPointerAndFallback(t *testing.T) {
	t.Parallel()
	outputs := []Output{
		{Name: "HDMI-0", X: 1920, Y: 0, Width: 1920, Height: 1080},
		{Name: "DP-1", X: 0, Y: 0, Width: 2560, Height: 1440, Primary: true},
	}

	x, y, force := ResolvePlacement(DisplayPrimary, 1280, 720, outputs)
	if !force || x != (2560-1280)/2 || y != (1440-720)/2 {
		t.Fatalf("primary = (%d,%d) force=%t", x, y, force)
	}
	x, y, force = ResolvePlacement("", 1280, 720, outputs)
	if !force || x != (2560-1280)/2 || y != (1440-720)/2 {
		t.Fatalf("empty display did not become primary: (%d,%d) force=%t", x, y, force)
	}
	x, y, force = ResolvePlacement("HDMI-0", 1280, 720, outputs)
	if !force || x != 1920+(1920-1280)/2 || y != (1080-720)/2 {
		t.Fatalf("named output = (%d,%d) force=%t", x, y, force)
	}
	x, y, force = ResolvePlacement("missing", 1280, 720, outputs)
	if !force || x != (2560-1280)/2 || y != (1440-720)/2 {
		t.Fatalf("missing name did not fall back to primary: (%d,%d) force=%t", x, y, force)
	}
	x, y, force = ResolvePlacement(DisplayPointer, 1280, 720, outputs)
	if force || x != 0 || y != 0 {
		t.Fatalf("pointer = (%d,%d) force=%t, want unforced origin", x, y, force)
	}
	x, y, force = ResolvePlacement(DisplayPrimary, 3000, 2000, outputs)
	if !force || x != 0 || y != 0 {
		t.Fatalf("oversized window = (%d,%d) force=%t, want output origin", x, y, force)
	}
	if _, _, force = ResolvePlacement(DisplayPrimary, 1280, 720, nil); force {
		t.Fatal("empty output list forced placement")
	}
	if _, _, force = ResolvePlacement(DisplayPrimary, 0, 720, outputs); force {
		t.Fatal("invalid size forced placement")
	}
}
