// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

const (
	// DisplayPrimary pins new windows to the current main monitor.
	DisplayPrimary = "primary"
	// DisplayPointer leaves placement to the window manager (typically the
	// pointer), matching Tipsy's previous unpositioned XCreateWindow path.
	DisplayPointer = "pointer"
)

// Output is one connected XRandR CRTC/output pair.
type Output struct {
	Name          string
	X, Y          int
	Width, Height int
	Primary       bool
}

func normalizeDisplay(display string) string {
	if display == "" {
		return DisplayPrimary
	}
	return display
}

func isPointerDisplay(display string) bool {
	return normalizeDisplay(display) == DisplayPointer
}

// ResolvePlacement returns the root coordinates at which a width×height window
// should be created for display. force is false for follow-mouse / empty
// output lists so the window manager keeps its pointer-based placement.
func ResolvePlacement(display string, width, height int, outputs []Output) (x, y int, force bool) {
	if width < 1 || height < 1 || isPointerDisplay(display) || len(outputs) == 0 {
		return 0, 0, false
	}
	out, ok := pickOutput(normalizeDisplay(display), outputs)
	if !ok {
		return 0, 0, false
	}
	x, y = centerOnOutput(out, width, height)
	return x, y, true
}

func pickOutput(display string, outputs []Output) (Output, bool) {
	if len(outputs) == 0 {
		return Output{}, false
	}
	if display != DisplayPrimary {
		for _, out := range outputs {
			if out.Name == display {
				return out, true
			}
		}
	}
	for _, out := range outputs {
		if out.Primary {
			return out, true
		}
	}
	return outputs[0], true
}

func centerOnOutput(out Output, width, height int) (x, y int) {
	x, y = out.X, out.Y
	if out.Width > width {
		x += (out.Width - width) / 2
	}
	if out.Height > height {
		y += (out.Height - height) / 2
	}
	return x, y
}
