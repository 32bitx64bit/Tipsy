// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && official_release

package runtime

import (
	"os"
	"testing"
)

func TestTestOpenGLRequestIsIgnoredInOfficialRelease(t *testing.T) {
	for _, value := range []string{"opengl", "vulkan", "auto", "1", "OpenGL"} {
		t.Setenv("TIPSY_TEST_RENDERER", value)
		requested, err := testOpenGLRequested(os.Getenv)
		if err != nil || requested {
			t.Fatalf("%q request = %t, %v; official_release must ignore the hook", value, requested, err)
		}
	}
}
