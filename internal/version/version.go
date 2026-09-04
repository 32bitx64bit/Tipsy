// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package version

// Version is 0.0.0-dev unless overridden at link time:
//
//	go build -ldflags "-X github.com/tipsy-linux/tipsy/internal/version.Version=1.2.3"
var Version = "0.0.0-dev"

func String() string {
	return Version
}
