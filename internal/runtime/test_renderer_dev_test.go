// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64 && !official_release

package runtime

import "testing"

func TestTestOpenGLRequestIsExactAndProcessLocal(t *testing.T) {
	for _, value := range []string{"", "opengl", "vulkan", "auto", "opengl ", "OpenGL", "gl"} {
		value := value
		requested, err := testOpenGLRequested(func(string) string { return value })
		if value == "opengl" {
			if err != nil || !requested {
				t.Fatalf("opengl request = %t, %v", requested, err)
			}
			continue
		}
		if value == "" {
			if err != nil || requested {
				t.Fatalf("empty request = %t, %v", requested, err)
			}
			continue
		}
		if err == nil || requested {
			t.Errorf("invalid %q request = %t, %v", value, requested, err)
		}
	}
	if requested, err := testOpenGLRequested(nil); err != nil || requested {
		t.Fatalf("nil environment request = %t, %v", requested, err)
	}
}
