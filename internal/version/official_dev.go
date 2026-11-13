// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package version

// officialReleaseCompiled is false in every build without the
// official_release build tag. Such builds are local/development builds no
// matter what runtime markers or linker metadata they carry: IsOfficialBuild
// can never return true for them.
const officialReleaseCompiled = false
