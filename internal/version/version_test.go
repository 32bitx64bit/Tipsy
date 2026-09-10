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

func TestUnstampedBuildIsDevelopmentChannel(t *testing.T) {
	if Channel != ChannelDevelopment || !Development() {
		t.Fatalf("Channel = %q, Development() = %v; an unstamped build must be dev", Channel, Development())
	}
	t.Cleanup(func() { Channel = ChannelDevelopment })
	Channel = ChannelStable
	if Development() {
		t.Fatal("Development() must be false once Channel is stamped stable")
	}
	Channel = "nightly"
	if !Development() {
		t.Fatal("any channel other than stable is a development build")
	}
}
