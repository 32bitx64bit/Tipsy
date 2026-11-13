// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && official_release

package runtime

// testOpenGLRequested is a no-op in official_release builds: the
// TIPSY_TEST_RENDERER visual-test hook is not compiled in, so no environment
// value can request a transient renderer.
func testOpenGLRequested(getenv func(string) string) (bool, error) {
	_ = getenv
	return false, nil
}
