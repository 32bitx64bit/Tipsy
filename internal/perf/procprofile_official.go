// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package perf

// StartFromEnv is a no-op in official_release builds: the TIPSY_PPROF_DIR
// process-profile knob is not compiled in.
func StartFromEnv() (func(), error) {
	return func() {}, nil
}
