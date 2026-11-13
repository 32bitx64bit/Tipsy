// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build official_release

package clientsettings

// officialFastFlagsEnforced reports whether ValidateFastFlags applies the
// curated official allowlist. Official releases bound the custom surface to
// the flags Tipsy itself generates.
const officialFastFlagsEnforced = true

// validateOfficialFastFlagsPolicy enforces the curated allowlist and numeric
// bounds on every validated custom Fast Flag list: persistence, load, and
// launch overrides all pass through ValidateFastFlags.
func validateOfficialFastFlagsPolicy(flags []FastFlag) error {
	return ValidateOfficialFastFlags(flags)
}
