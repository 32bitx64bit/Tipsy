// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package flickercap_test

import (
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/graphics/flickercap"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// TestGridUnpacksPaintedVisual exercises tipsy_fcap_unpack against a real
// X11 window: channel extraction must follow the visual's masks rather than
// assuming 8-bit RGBX at fixed 16/8/0 shifts. White-in -> 255 RGB out.
func TestGridUnpacksPaintedVisual(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY unset")
	}
	w, err := x11.Open("flickercap mask test", 64, 64)
	if err != nil {
		if errors.Is(err, x11.ErrUnavailable) || errors.Is(err, x11.ErrNoDisplay) {
			t.Skip(err)
		}
		t.Fatalf("x11.Open: %v", err)
	}
	defer w.Close()
	// XMapWindow is asynchronous; Attach requires an already-viewable window.
	var tgt *flickercap.Target
	attachDeadline := time.Now().Add(2 * time.Second)
	for tgt == nil && time.Now().Before(attachDeadline) {
		tgt, err = flickercap.Attach(w.XID())
		if err != nil {
			_ = w.Pump()
			time.Sleep(10 * time.Millisecond)
		}
	}
	if tgt == nil {
		t.Skipf("attach unusable: %v", err)
	}
	defer tgt.Close()
	if err := tgt.PaintWhite(); err != nil {
		t.Fatalf("paint: %v", err)
	}
	grid := make([]byte, 2*2*4)
	var lastErr error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, lastErr = tgt.Grid(2, 2, grid); lastErr == nil {
			break
		}
		_ = w.Pump()
		time.Sleep(10 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("grid: %v", lastErr)
	}
	for i := 0; i < 4; i++ {
		r, g, b, a := grid[i*4], grid[i*4+1], grid[i*4+2], grid[i*4+3]
		if r != 0xff || g != 0xff || b != 0xff || a != 0 {
			t.Fatalf("pixel %d = %d,%d,%d,%d; want 255,255,255,0", i, r, g, b, a)
		}
	}
}

// TestLiveLoginFlickerCapture measures whether the live Roblox window's
// presented content alternates between distinct frames (flicker). It is
// Roblox-window-only evidence (XGetImage on that window, never the desktop).
// Gated behind TIPSY_FLICKER=1.
func TestLiveLoginFlickerCapture(t *testing.T) {
	if os.Getenv("TIPSY_FLICKER") != "1" {
		t.Skip("set TIPSY_FLICKER=1 to capture the live Roblox window")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY unset")
	}
	outDir := os.Getenv("TIPSY_FLICKER_OUT")
	if outDir == "" {
		outDir = "/tmp/kilo/flicker"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tgt, err := flickercap.Find()
	if err != nil {
		t.Skip(err)
	}
	defer tgt.Close()
	t.Logf("found Roblox window %dx%d", tgt.Width, tgt.Height)

	const gw, gh = 32, 18
	const frames = 48
	hashes := make(map[uint64]int) // hash -> first frame index
	var seq []uint64
	grid := make([]byte, gw*gh*4)

	for i := 0; i < frames; i++ {
		if i > 0 {
			time.Sleep(80 * time.Millisecond)
		}
		if _, err := tgt.Grid(gw, gh, grid); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		hf := fnv.New64a()
		_, _ = hf.Write(grid)
		sum := hf.Sum64()
		if _, seen := hashes[sum]; !seen {
			hashes[sum] = i
			seq = append(seq, sum)
		}
	}

	changes := 0
	for i := 1; i < len(seq); i++ {
		if seq[i] != seq[i-1] {
			changes++
		}
	}
	var sb []byte
	for i, s := range seq {
		sb = append(sb, fmt.Sprintf("distinct-frame-%d first-at=%dms\n", i, hashes[s]*80)...)
	}
	_ = os.WriteFile(filepath.Join(outDir, "summary.txt"), sb, 0o644)
	t.Logf("frames=%d distinct=%d transitions=%d", frames, len(hashes), changes)
	if len(hashes) > 1 {
		t.Logf("FLICKER PRESENT: content alternates between %d distinct frames", len(hashes))
	} else {
		t.Logf("STABLE: single distinct frame across all samples")
	}

	// Save up to 3 representative full-window PNGs (Roblox window only).
	for k, hi := range seq {
		if k >= 3 {
			break
		}
		raw, err := tgt.Full()
		if err != nil {
			t.Logf("full grab for distinct frame %d: %v", k, err)
			continue
		}
		img := image.NewNRGBA(image.Rect(0, 0, tgt.Width, tgt.Height))
		copy(img.Pix, raw)
		name := filepath.Join(outDir, fmt.Sprintf("distinct-frame-%d.png", k))
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		_ = f.Close()
		t.Logf("saved %s (first seen at %dms)", name, hashes[hi]*80)
	}
}
