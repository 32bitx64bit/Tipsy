// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

func TestAuditGamepadKeyUnion(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	scratch := make([]int, 0, 128)
	for i := 0; i < 10000; i++ {
		held, current := map[int]bool{}, map[int]bool{}
		for j := 0; j < 40; j++ {
			if rng.Intn(2) == 0 {
				held[rng.Intn(64)] = rng.Intn(2) == 0
			}
			if rng.Intn(2) == 0 {
				current[rng.Intn(64)] = rng.Intn(2) == 0
			}
		}
		union := map[int]bool{}
		for k := range held {
			union[k] = true
		}
		for k, v := range current {
			if v {
				union[k] = true
			}
		}
		want := make([]int, 0, len(union))
		for k := range union {
			want = append(want, k)
		}
		sort.Ints(want)
		scratch = collectGamepadKeys(scratch, held, current)
		if !reflect.DeepEqual(scratch, want) {
			t.Fatalf("got %v want %v", scratch, want)
		}
	}
}
func TestAuditGamepadKeyUnionWarmAllocations(t *testing.T) {
	held, current := map[int]bool{1: true, 2: true}, map[int]bool{2: false, 3: true}
	scratch := make([]int, 0, 16)
	if n := testing.AllocsPerRun(1000, func() { scratch = collectGamepadKeys(scratch, held, current) }); n != 0 {
		t.Fatalf("warm allocations: %v", n)
	}
}
