// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later
package setupsvc

import (
	"context"

	"github.com/tipsy-linux/tipsy/internal/android"
)

// testHooks injects the production Android import check, as the composition
// root does. Tests that need a runtime directory set Service.RuntimeDir.
func testHooks() Hooks { return Hooks{SupportsImport: android.SupportsImport} }

// FindMissingClientImports lists, sorted by name, the non-weak undefined
// dynamic imports of <runtimeDir>/lib/x86_64/libroblox.so that nothing can
// supply. Resolution mirrors the loader's resolveUndef: exports of bundled
// DT_NEEDED libraries, then the Tipsy Android provider (android.SupportsImport).
// Weak imports bind to zero, as in the loader, and are never reported.
//
// A bundled DT_NEEDED library absent from lib/x86_64 is not an import, so it is
// not named here; ValidateExtractedClientImports reports it as fatal.
func FindMissingClientImports(ctx context.Context, runtimeDir string) ([]MissingImport, error) {
	missing, _, err := findClientImports(ctx, runtimeDir, android.SupportsImport)
	return missing, err
}

// UnavailableSource is a Source for builds with no lawful APK source configured.
type UnavailableSource struct {
	Name     string
	Reason   string
	LegalURL string
}

func (s UnavailableSource) Availability(context.Context) SourceAvailability {
	reason := s.Reason
	if reason == "" {
		reason = "No official public direct APK source is available; choose APK files obtained through your own authorized account."
	}
	return SourceAvailability{Available: false, Name: s.Name, Reason: reason, LegalURL: s.LegalURL}
}

func (s UnavailableSource) Acquire(context.Context, string, ProgressFunc) ([]string, error) {
	a := s.Availability(context.Background())
	return nil, setupError(ErrSourceUnavailable, "automatic setup", a.Reason, nil)
}
