// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import "testing"

func TestOpenSLWorkerThreadNamed(t *testing.T) {
	if got := testAudioWorkerThreadName(); got != "tip.opensles" {
		t.Fatalf("OpenSL stream worker thread name = %q, want %q", got, "tip.opensles")
	}
}
