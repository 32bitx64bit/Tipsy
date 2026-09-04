// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package flickercap_test

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/graphics/flickercap"
)

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
