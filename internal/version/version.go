// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package version

// Version is 0.0.0-dev unless overridden at link time:
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.Version=1.2.3"
var Version = "0.0.0-dev"

// ChannelStable and ChannelDevelopment are the two release channels a build
// can belong to. The channel decides which desktop identity the build
// integrates under (Tipsy vs Tipsy-Dev) so a developer's day-to-day install
// and their work-in-progress builds never fight over the same launcher.
const (
	ChannelStable      = "stable"
	ChannelDevelopment = "dev"
)

// Channel is dev unless the official packaging stamps stable at link time:
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.Channel=stable"
//
// An unstamped `go build` is therefore always a development build.
var Channel = ChannelDevelopment

func String() string {
	return Version
}

// Development reports whether this binary is a development-channel build.
func Development() bool {
	return Channel != ChannelStable
}
