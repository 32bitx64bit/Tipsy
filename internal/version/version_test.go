// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package version

import (
	"strings"
	"testing"
)

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

// stamp saves the linker-stamped metadata and restores it after the test.
func stamp(t *testing.T, v, channel, kind, commit string) {
	t.Helper()
	prevVersion, prevChannel, prevKind, prevCommit := Version, Channel, ReleaseKind, SourceCommit
	t.Cleanup(func() {
		Version, Channel, ReleaseKind, SourceCommit = prevVersion, prevChannel, prevKind, prevCommit
	})
	Version, Channel, ReleaseKind, SourceCommit = v, channel, kind, commit
}

func TestIsOfficialBuildRequiresTagAndStampedMetadata(t *testing.T) {
	// An unstamped build is never official, in either build mode.
	stamp(t, "0.0.0-dev", ChannelDevelopment, "", "")
	if IsOfficialBuild() {
		t.Fatal("unstamped build must not be official")
	}

	// Fully stamped official metadata: official only when the binary was
	// compiled with -tags official_release (officialReleaseCompiled).
	stamp(t, "1.2.3", ChannelStable, ReleaseKindCandidateKeyless, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if got := IsOfficialBuild(); got != officialReleaseCompiled {
		t.Fatalf("IsOfficialBuild() = %v, want %v for stamped metadata", got, officialReleaseCompiled)
	}

	for _, kind := range []string{ReleaseKindCandidateUnsigned, ReleaseKindCandidateKeyless, ReleaseKindRepositorySigned} {
		stamp(t, "1.2.3", ChannelStable, kind, "")
		if got := IsOfficialBuild(); got != officialReleaseCompiled {
			t.Fatalf("ReleaseKind %q: IsOfficialBuild() = %v, want %v", kind, got, officialReleaseCompiled)
		}
	}
}

func TestIsOfficialBuildRejectsInvalidMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		channel string
		kind    string
	}{
		{"dev channel", "1.2.3", ChannelDevelopment, ReleaseKindCandidateKeyless},
		{"unknown channel", "1.2.3", "nightly", ReleaseKindCandidateKeyless},
		{"dev version", "dev", ChannelStable, ReleaseKindCandidateKeyless},
		{"default dev version", "0.0.0-dev", ChannelStable, ReleaseKindCandidateKeyless},
		{"dev prerelease", "1.2.3-dev", ChannelStable, ReleaseKindCandidateKeyless},
		{"empty version", "", ChannelStable, ReleaseKindCandidateKeyless},
		{"empty kind", "1.2.3", ChannelStable, ""},
		{"development kind", "1.2.3", ChannelStable, ReleaseKindDevelopment},
		{"unknown kind", "1.2.3", ChannelStable, "release-official"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stamp(t, tc.version, tc.channel, tc.kind, "")
			if IsOfficialBuild() {
				t.Fatalf("IsOfficialBuild() = true for %+v", tc)
			}
		})
	}
}

func TestProvenanceAndUserAgentMarkUnofficialBuilds(t *testing.T) {
	stamp(t, "0.0.0-dev", ChannelDevelopment, "", "")
	if IsOfficialBuild() {
		t.Fatal("unstamped build must not be official")
	}
	if got := Provenance(); got != UnofficialBuildLabel {
		t.Fatalf("Provenance() = %q, want %q", got, UnofficialBuildLabel)
	}
	if ua := UserAgent(); !strings.Contains(ua, UnofficialBuildLabel) {
		t.Fatalf("UserAgent() = %q must carry the unofficial marker", ua)
	}

	stamp(t, "1.2.3", ChannelStable, ReleaseKindRepositorySigned, "")
	if officialReleaseCompiled {
		if got := Provenance(); got != "Official Release" {
			t.Fatalf("Provenance() = %q, want Official Release", got)
		}
		if ua := UserAgent(); ua != "Tipsy-Linux" {
			t.Fatalf("official UserAgent() = %q, want Tipsy-Linux", ua)
		}
	} else if got := Provenance(); got != UnofficialBuildLabel {
		t.Fatalf("local build Provenance() = %q, want %q", got, UnofficialBuildLabel)
	}
}
