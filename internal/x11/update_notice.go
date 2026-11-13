// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package x11

// UpdateNoticeFrame is one immutable, passive update-card raster request. It
// has no window, input, focus or pointer ownership; the host composites its
// pixels with the focused-text foreground before presentation.
type UpdateNoticeFrame struct {
	Visible      bool
	Text         string
	X            int
	Y            int
	Width        int
	Height       int
	Opacity      float64
	Scale        float64
	AngleDegrees float64
}
