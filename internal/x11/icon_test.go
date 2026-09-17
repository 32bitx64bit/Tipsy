// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestWindowIconARGBCachesPackedBuffer(t *testing.T) {
	first, err := windowIconARGB()
	if err != nil {
		t.Fatalf("windowIconARGB: %v", err)
	}
	if len(first) < 3 {
		t.Fatalf("icon item count = %d, want width + height + pixels", len(first))
	}
	second, err := windowIconARGB()
	if err != nil {
		t.Fatalf("second windowIconARGB: %v", err)
	}
	if len(second) != len(first) || &second[0] != &first[0] {
		t.Fatal("windowIconARGB returned a new decode buffer")
	}

	var failed error
	allocs := testing.AllocsPerRun(100, func() {
		got, err := windowIconARGB()
		if err != nil {
			failed = err
			return
		}
		if len(got) != len(first) || &got[0] != &first[0] {
			failed = fmt.Errorf("windowIconARGB allocated a new decode buffer")
		}
	})
	if failed != nil {
		t.Fatal(failed)
	}
	if allocs != 0 {
		t.Fatalf("windowIconARGB allocated %.2f times per call after Once, want 0", allocs)
	}
}

func TestWindowIconARGBFitsWMSize(t *testing.T) {
	icon, err := windowIconARGB()
	if err != nil {
		t.Fatalf("windowIconARGB: %v", err)
	}
	width, height := int(icon[0]), int(icon[1])
	if width < 1 || height < 1 {
		t.Fatalf("icon dimensions = %dx%d, want a real branding bitmap", width, height)
	}
	if width > windowIconMaxEdge || height > windowIconMaxEdge {
		t.Fatalf("icon dimensions = %dx%d, want long edge <= %d", width, height, windowIconMaxEdge)
	}
	if got, want := len(icon), 2+width*height; got != want {
		t.Fatalf("icon item count = %d, want %d", got, want)
	}
	if width != windowIconMaxEdge || height != windowIconMaxEdge {
		t.Fatalf("embedded branding downscale = %dx%d, want %dx%d", width, height, windowIconMaxEdge, windowIconMaxEdge)
	}
	nonzero := false
	for _, pixel := range icon[2:] {
		if pixel != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("packed icon is blank; branding must stay visible to the window manager")
	}
}

func TestPackWindowIconARGBFailsClosed(t *testing.T) {
	if _, err := packWindowIconARGB(nil); err == nil {
		t.Fatal("packWindowIconARGB(nil) = nil, want decode error")
	}
	if _, err := packWindowIconARGB([]byte("not a png")); err == nil {
		t.Fatal("packWindowIconARGB(garbage) = nil, want decode error")
	}

	empty, err := encodeTestPNG(t, image.NewNRGBA(image.Rect(0, 0, 0, 0)))
	if err == nil {
		if packed, packErr := packWindowIconARGB(empty); packErr == nil {
			t.Fatalf("packWindowIconARGB(0x0) = %d items, want invalid-size error", len(packed))
		}
	}
}

func TestPackWindowIconARGBDownscalesAndKeepsSmallImages(t *testing.T) {
	small := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	fillNRGBA(small, color.NRGBA{R: 12, G: 34, B: 56, A: 255})
	smallPNG := mustEncodeTestPNG(t, small)
	packed, err := packWindowIconARGB(smallPNG)
	if err != nil {
		t.Fatalf("pack 8x8: %v", err)
	}
	if packed[0] != 8 || packed[1] != 8 {
		t.Fatalf("8x8 stayed %dx%d, want no upscale", packed[0], packed[1])
	}
	want := uint32(255)<<24 | uint32(12)<<16 | uint32(34)<<8 | uint32(56)
	for i, pixel := range packed[2:] {
		if pixel != want {
			t.Fatalf("8x8 pixel %d = %#x, want %#x", i, pixel, want)
		}
	}

	large := image.NewNRGBA(image.Rect(0, 0, 80, 40))
	fillNRGBA(large, color.NRGBA{R: 200, G: 10, B: 20, A: 255})
	packed, err = packWindowIconARGB(mustEncodeTestPNG(t, large))
	if err != nil {
		t.Fatalf("pack 80x40: %v", err)
	}
	if packed[0] != uint32(windowIconMaxEdge) || packed[1] != uint32(windowIconMaxEdge)/2 {
		t.Fatalf("80x40 downscale = %dx%d, want %dx%d", packed[0], packed[1], windowIconMaxEdge, windowIconMaxEdge/2)
	}
	want = uint32(255)<<24 | uint32(200)<<16 | uint32(10)<<8 | uint32(20)
	if packed[2] != want {
		t.Fatalf("downscaled pixel = %#x, want %#x", packed[2], want)
	}
	if got, wantLen := len(packed), 2+int(packed[0])*int(packed[1]); got != wantLen {
		t.Fatalf("downscaled item count = %d, want %d", got, wantLen)
	}
}

func fillNRGBA(img *image.NRGBA, c color.NRGBA) {
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func mustEncodeTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	pngBytes, err := encodeTestPNG(t, img)
	if err != nil {
		t.Fatal(err)
	}
	return pngBytes
}

func encodeTestPNG(t *testing.T, img image.Image) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
