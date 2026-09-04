// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"bytes"
	_ "embed"
	"fmt"
	"image/color"
	"image/png"
	"sync"
)

// tipsyIconPNG is a build-time copy of the canonical repository branding.
// Keeping the window icon in the binary makes it available to the window
// manager when Tipsy is launched from a desktop file, AppDir, or arbitrary
// working directory.
//
//go:embed tipsy.png
var tipsyIconPNG []byte

var (
	iconOnce sync.Once
	iconData []uint32
	iconErr  error
)

// windowIconARGB returns one EWMH _NET_WM_ICON image: width, height, followed
// by non-premultiplied ARGB pixels in row-major order.
func windowIconARGB() ([]uint32, error) {
	iconOnce.Do(func() {
		decoded, err := png.Decode(bytes.NewReader(tipsyIconPNG))
		if err != nil {
			iconErr = fmt.Errorf("x11: decode embedded Tipsy icon: %w", err)
			return
		}
		bounds := decoded.Bounds()
		width, height := bounds.Dx(), bounds.Dy()
		if width < 1 || height < 1 {
			iconErr = fmt.Errorf("x11: embedded Tipsy icon has invalid size %dx%d", width, height)
			return
		}
		iconData = make([]uint32, 2+width*height)
		iconData[0], iconData[1] = uint32(width), uint32(height)
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				p := color.NRGBAModel.Convert(decoded.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
				iconData[2+y*width+x] = uint32(p.A)<<24 | uint32(p.R)<<16 | uint32(p.G)<<8 | uint32(p.B)
			}
		}
	})
	if iconErr != nil {
		return nil, iconErr
	}
	return iconData, nil
}
