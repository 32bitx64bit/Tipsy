// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && !official_release

package runtime

import "fmt"

// testRendererEnv is a process-only visual-compatibility control; the sole
// accepted value is `opengl`. It never writes the Tipsy settings document or
// Roblox's durable settings files.
const testRendererEnv = "TIPSY_TEST_RENDERER"

func testOpenGLRequested(getenv func(string) string) (bool, error) {
	if getenv == nil {
		return false, nil
	}
	switch getenv(testRendererEnv) {
	case "":
		return false, nil
	case "opengl":
		return true, nil
	default:
		return false, fmt.Errorf("%s only accepts opengl", testRendererEnv)
	}
}
