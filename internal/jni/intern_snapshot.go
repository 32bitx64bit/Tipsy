// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

// appendInternedSnapshot requires a serialized writer (methodMu/fieldMu).
// A reader sees only the published length and immutable elements below it.
// Appending beyond that length is safe even when the backing array is shared;
// never reslice a reader snapshot to capacity or mutate published entries.
func appendInternedSnapshot[T any](old *[]*T, info *T) (*[]*T, uint32) {
	var next []*T
	if old == nil {
		next = make([]*T, 1, 32) // slot zero is invalid
	} else {
		next = *old
	}
	slot := len(next)
	next = append(next, info)
	return &next, uint32(slot)
}
