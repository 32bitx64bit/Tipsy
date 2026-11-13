// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package version

// officialReleaseCompiled is true only in builds compiled with
// -tags official_release. Official identity additionally requires the
// release metadata stamped by the official build scripts; see IsOfficialBuild.
const officialReleaseCompiled = true
