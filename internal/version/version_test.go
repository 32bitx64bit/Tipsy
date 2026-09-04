// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package version

import "testing"

func TestString(t *testing.T) {
	t.Parallel()
	if String() != Version {
		t.Fatalf("String() = %q, Version = %q", String(), Version)
	}
	if Version == "" {
		t.Fatal("Version must not be empty")
	}
}

func TestDefaultDevVersion(t *testing.T) {
	t.Parallel()
	if String() != "0.0.0-dev" {
		t.Fatalf("default String() = %q, want 0.0.0-dev (override via ldflags)", String())
	}
}
