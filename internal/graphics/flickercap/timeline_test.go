// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package flickercap_test

import (
	"fmt"
	"hash/fnv"
	"os"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/graphics/flickercap"
)

// TestTimeline observes the live Roblox window for a period and prints a
// per-second state timeline (black vs content, distinct frame hashes).
// Roblox-window-only evidence. Gated: TIPSY_TIMELINE=1, TIPSY_TIMELINE_SECS.
func TestTimeline(t *testing.T) {
	if os.Getenv("TIPSY_TIMELINE") != "1" {
		t.Skip("set TIPSY_TIMELINE=1")
	}
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY unset")
	}
	secs := 90
	if v := os.Getenv("TIPSY_TIMELINE_SECS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &secs)
	}
	tgt, err := flickercap.Find()
	if err != nil {
		t.Skip(err)
	}
	defer tgt.Close()

	const gw, gh = 32, 18
	grid := make([]byte, gw*gh*4)
	var cur uint64
	for i := 0; i < secs; i++ {
		states := map[uint64]int{}
		nonBlack := 0
		for j := 0; j < 4; j++ {
			if _, err := tgt.Grid(gw, gh, grid); err != nil {
				t.Fatalf("sample: %v", err)
			}
			sum := 0
			for k := 0; k < len(grid); k += 4 {
				sum += int(grid[k]) + int(grid[k+1]) + int(grid[k+2])
			}
			if sum > 1000 {
				nonBlack++
			}
			hf := fnv.New64a()
			_, _ = hf.Write(grid)
			cur = hf.Sum64()
			states[cur]++
		}
		t.Logf("t=%03ds distinct=%d nonBlackSamples=%d/4 hash=%016x", i, len(states), nonBlack, cur)
		time.Sleep(1000 * time.Millisecond)
	}
}
