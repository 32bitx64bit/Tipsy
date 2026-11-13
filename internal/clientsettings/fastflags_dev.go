// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !official_release

package clientsettings

// officialFastFlagsEnforced reports whether ValidateFastFlags applies the
// curated official allowlist. Development builds keep the full typed surface
// so R&D and user experiments can name any well-formed flag.
const officialFastFlagsEnforced = false

// validateOfficialFastFlagsPolicy is a no-op outside official_release builds.
func validateOfficialFastFlagsPolicy(flags []FastFlag) error { return nil }
