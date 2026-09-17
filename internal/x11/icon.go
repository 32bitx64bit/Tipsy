// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"
)

// tipsyIconPNG is a build-time copy of the canonical repository branding.
// Keeping the window icon in the binary makes it available to the window
// manager when Tipsy is launched from a desktop file, AppDir, or arbitrary
// working directory. The retained EWMH buffer is a WM-sized downscale of
// this PNG, not the full-resolution decode.
//
//go:embed tipsy.png
var tipsyIconPNG []byte

// windowIconMaxEdge is the long edge of the packed _NET_WM_ICON image.
// Window-manager title-bar and task-switcher icons do not need the 512×512
// branding bitmap; keeping that decode resident was the 1.19 MiB Go inuse
// hotspot in windowIconARGB.
const windowIconMaxEdge = 64

var (
	iconOnce sync.Once
	iconData []uint32
	iconErr  error
)

// windowIconARGB returns one EWMH _NET_WM_ICON image: width, height, followed
// by non-premultiplied ARGB pixels in row-major order. The packed buffer is
// built once from the embedded PNG and reused for every Map/Open.
func windowIconARGB() ([]uint32, error) {
	iconOnce.Do(func() {
		iconData, iconErr = packWindowIconARGB(tipsyIconPNG)
	})
	if iconErr != nil {
		return nil, iconErr
	}
	return iconData, nil
}

func packWindowIconARGB(pngBytes []byte) ([]uint32, error) {
	decoded, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("x11: decode embedded Tipsy icon: %w", err)
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 1 || height < 1 {
		return nil, fmt.Errorf("x11: embedded Tipsy icon has invalid size %dx%d", width, height)
	}
	dstW, dstH := scaleWindowIconSize(width, height)
	packed := make([]uint32, 2+dstW*dstH)
	packed[0], packed[1] = uint32(dstW), uint32(dstH)
	for y := 0; y < dstH; y++ {
		srcY0 := bounds.Min.Y + y*height/dstH
		srcY1 := bounds.Min.Y + (y+1)*height/dstH
		if srcY1 <= srcY0 {
			srcY1 = srcY0 + 1
		}
		for x := 0; x < dstW; x++ {
			srcX0 := bounds.Min.X + x*width/dstW
			srcX1 := bounds.Min.X + (x+1)*width/dstW
			if srcX1 <= srcX0 {
				srcX1 = srcX0 + 1
			}
			packed[2+y*dstW+x] = averageIconARGB(decoded, srcX0, srcY0, srcX1, srcY1)
		}
	}
	return packed, nil
}

func scaleWindowIconSize(width, height int) (int, int) {
	if width <= windowIconMaxEdge && height <= windowIconMaxEdge {
		return width, height
	}
	if width >= height {
		h := (height*windowIconMaxEdge + width/2) / width
		if h < 1 {
			h = 1
		}
		return windowIconMaxEdge, h
	}
	w := (width*windowIconMaxEdge + height/2) / height
	if w < 1 {
		w = 1
	}
	return w, windowIconMaxEdge
}

func averageIconARGB(img image.Image, x0, y0, x1, y1 int) uint32 {
	var rSum, gSum, bSum, aSum uint64
	n := uint64((x1 - x0) * (y1 - y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			rSum += uint64(p.R)
			gSum += uint64(p.G)
			bSum += uint64(p.B)
			aSum += uint64(p.A)
		}
	}
	return uint32(aSum/n)<<24 | uint32(rSum/n)<<16 | uint32(gSum/n)<<8 | uint32(bSum/n)
}
