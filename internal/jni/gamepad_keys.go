// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "sort"

// collectGamepadKeys returns the sorted union of all held-map keys and pressed
// input keys. Reuse dst only while gamepadState.mu is held. Do not retain this
// scratch in guest code or across an asynchronous dispatch.
func collectGamepadKeys(dst []int, held, current map[int]bool) []int {
	dst = dst[:0]
	for k := range held {
		dst = append(dst, k)
	}
	for k, pressed := range current {
		if !pressed {
			continue
		}
		if _, exists := held[k]; !exists {
			dst = append(dst, k)
		}
	}
	sort.Ints(dst)
	return dst
}
