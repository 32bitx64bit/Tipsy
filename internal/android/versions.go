// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"fmt"
)

// VersionCapabilityReason identifies why the Android compatibility provider
// refused an exact GNU-versioned import. The resolver deliberately does not
// infer compatibility from a same-name host symbol.
type VersionCapabilityReason string

const (
	VersionCapabilityUnknownSymbol VersionCapabilityReason = "symbol is not in the owned compatibility surface"
	VersionCapabilityWrongVersion  VersionCapabilityReason = "GNU version is not supported for this symbol"
	VersionCapabilityUnresolvable  VersionCapabilityReason = "declared compatibility symbol could not be resolved"
)

// VersionedSymbolError reports a failed exact (SONAME, symbol, GNU version)
// capability lookup. Library is never normalized: callers must supply the
// exact DT_NEEDED SONAME authenticated by Loader.
type VersionedSymbolError struct {
	Library string
	Symbol  string
	Version string
	Reason  VersionCapabilityReason
	Err     error
}

func (e *VersionedSymbolError) Error() string {
	if e == nil {
		return "android: versioned symbol compatibility failure"
	}
	message := fmt.Sprintf("android: versioned symbol compatibility failure: %s:%s@%s: %s",
		e.Library, e.Symbol, e.Version, e.Reason)
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}

func (e *VersionedSymbolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type versionedLibraryCapability struct {
	baseVersion string
	symbols     []string
	overrides   map[string]string
}

// These descriptors are the Android ABI surface Tipsy owns. A version name is
// attached to each symbol registration rather than accepted as a library-wide
// allowlist. LIBC_N and LIBC_O are limited to the bionic symbols that actually
// use those namespaces in the supported official client.
var versionedLibraryCapabilities = map[string]versionedLibraryCapability{
	"libc.so": {
		baseVersion: "LIBC",
		symbols:     libcSymbols,
		overrides: map[string]string{
			"__fread_chk":      "LIBC_N",
			"__fwrite_chk":     "LIBC_N",
			"__write_chk":      "LIBC_N",
			"in6addr_any":      "LIBC_N",
			"in6addr_loopback": "LIBC_N",
			"__sendto_chk":     "LIBC_O",
		},
	},
	"libm.so": {
		baseVersion: "LIBC",
		symbols:     libmSymbols,
	},
	"libdl.so": {
		baseVersion: "LIBC",
		symbols: []string{
			"dlopen", "dlsym", "dlclose", "dlerror", "dladdr", "dl_iterate_phdr",
		},
	},
}

var versionedSymbolCapabilities = buildVersionedSymbolCapabilities()

func buildVersionedSymbolCapabilities() map[string]map[string]string {
	capabilities := make(map[string]map[string]string, len(versionedLibraryCapabilities))
	for library, descriptor := range versionedLibraryCapabilities {
		symbols := make(map[string]string, len(descriptor.symbols))
		for _, symbol := range descriptor.symbols {
			if symbol == "" {
				continue
			}
			version := descriptor.baseVersion
			if override := descriptor.overrides[symbol]; override != "" {
				version = override
			}
			symbols[symbol] = version
		}
		// Version-specific data exports may not otherwise appear in a generic
		// function list, but remain explicit per-symbol capabilities.
		for symbol, version := range descriptor.overrides {
			symbols[symbol] = version
		}
		capabilities[library] = symbols
	}
	return capabilities
}

// LookupVersion implements loader.VersionedResolver. It refuses unknown
// owners and versions before invoking the existing resolver, so a global or
// same-name host symbol cannot widen the authenticated compatibility surface.
func (p *provider) LookupVersion(library, symbol, version string) (uintptr, error) {
	versions := versionedSymbolCapabilities[library]
	want, ok := versions[symbol]
	if !ok {
		return 0, &VersionedSymbolError{
			Library: library,
			Symbol:  symbol,
			Version: version,
			Reason:  VersionCapabilityUnknownSymbol,
		}
	}
	if version != want {
		return 0, &VersionedSymbolError{
			Library: library,
			Symbol:  symbol,
			Version: version,
			Reason:  VersionCapabilityWrongVersion,
		}
	}
	addr := androidLookup(library, symbol)
	if addr == 0 {
		hostSymbol := symbol
		if alternate := bionicAliases[symbol]; alternate != "" {
			hostSymbol = alternate
		}
		addr = hostDlsymLibrary(library, hostSymbol)
	}
	if addr == 0 {
		return 0, &VersionedSymbolError{
			Library: library,
			Symbol:  symbol,
			Version: version,
			Reason:  VersionCapabilityUnresolvable,
		}
	}
	return addr, nil
}
