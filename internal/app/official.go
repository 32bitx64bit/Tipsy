// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"

	"github.com/tipsy-linux/tipsy/internal/version"
)

// releaseKind values the official build scripts stamp into the binary at
// link time (and record in the packaging build-info.json evidence). Every
// local build of every medium is development-unrestricted or unstamped.
const (
	releaseKindDevelopment       = version.ReleaseKindDevelopment
	releaseKindCandidateUnsigned = version.ReleaseKindCandidateUnsigned
	releaseKindCandidateKeyless  = version.ReleaseKindCandidateKeyless
	releaseKindRepositorySigned  = version.ReleaseKindRepositorySigned
)

// errOfficialReleaseUnavailable means this binary is not an official Tipsy
// release: it was not compiled with -tags official_release and stamped with
// official release metadata by the release build scripts.
var errOfficialReleaseUnavailable = errors.New("official release identity is unavailable: this binary was not compiled and stamped as an official Tipsy release")

func officialReleaseKind(kind string) error {
	switch kind {
	case releaseKindCandidateUnsigned, releaseKindCandidateKeyless, releaseKindRepositorySigned:
		return nil
	default:
		return errOfficialReleaseUnavailable
	}
}

// identifyOfficialRelease is the app-level release identity. It is decided at
// compile time: version.IsOfficialBuild requires the official_release build
// tag plus linker-stamped release metadata (stable channel, official
// ReleaseKind, non-dev Version). Runtime markers — the AppRun handoff
// (TIPSY_RELEASE_ARTIFACT / TIPSY_RELEASE_APPDIR), an installed
// build-info.json, install roots, or file ownership — are packaging evidence
// only and never grant authority, so a forged marker cannot turn a local
// `go build` into an OfficialVerified release. Cryptographic admission of the
// artifact stays in release CI (Sigstore) and the package manager's signature
// check at install.
func identifyOfficialRelease(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !version.IsOfficialBuild() {
		return errOfficialReleaseUnavailable
	}
	return nil
}
