// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package perf

import (
	"os"
	"strings"
)

// EnvProfileDir enables process-wide profiles on the real tipsy binary.
// The directory is created with mode 0700. Profiles contain function
// names and addresses only, never a log of user input, cookies, or
// account state.
const EnvProfileDir = "TIPSY_PPROF_DIR"

// StartFromEnv starts process-wide profiling when TIPSY_PPROF_DIR is set.
func StartFromEnv() (func(), error) {
	dir := strings.TrimSpace(os.Getenv(EnvProfileDir))
	if dir == "" {
		return func() {}, nil
	}
	return StartProcessProfile(dir, ProfileOptionsFromEnv())
}
