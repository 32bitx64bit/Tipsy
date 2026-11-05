// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"strings"
	"sync"
)

var (
	hostSurfaceOnce sync.Once
	hostSurface     map[string]struct{}
)

// declaredHostSurface is every libc/libm/libdl name Tipsy declares it backs
// with the host C library (or an alias of one). Built from the same tables
// LookupVersion consults, so the two cannot drift.
func declaredHostSurface() map[string]struct{} {
	hostSurfaceOnce.Do(func() {
		hostSurface = make(map[string]struct{})
		for _, symbols := range versionedSymbolCapabilities {
			for symbol := range symbols {
				hostSurface[symbol] = struct{}{}
			}
		}
		for symbol := range bionicAliases {
			hostSurface[symbol] = struct{}{}
		}
	})
	return hostSurface
}

// graphicsEntryPoint reports names the provider resolves against the live
// host EGL/GLES/Vulkan driver (tipsy_egl_dlsym, tipsy_gles_dlsym,
// tipsy_vk_dlsym). Whether the driver exports one depends on the host, not on
// this build, and asking would dlopen the driver, so it is not decidable here.
func graphicsEntryPoint(symbol string) bool {
	if strings.HasPrefix(symbol, "egl") || strings.HasPrefix(symbol, "vk") {
		return true
	}
	return len(symbol) > 2 && strings.HasPrefix(symbol, "gl") && symbol[2] >= 'A' && symbol[2] <= 'Z'
}

// SupportsImport reports whether the in-process Android provider can bind an
// undefined import of a client library. library and version are the GNU
// version requirement's DT_NEEDED soname and version name, or empty for an
// unversioned import. It mirrors Provider().Lookup (unversioned) and
// LookupVersion (versioned) as a pure query:
//
//   - nothing is dlopen'd, no host library is probed, no client code runs;
//   - nothing is logged and the provider's lookup cache is not touched.
//
// Names backed by the host C library or host graphics driver (see
// declaredHostSurface and graphicsEntryPoint) are reported as supported
// without asking the host; everything Tipsy itself owns (the compatibility
// symbol table, the versioned capability tables) is checked exactly.
func SupportsImport(library, symbol, version string) bool {
	if symbol == "" {
		return false
	}
	if version != "" {
		if versionedSymbolCapabilities[library][symbol] != version {
			return false
		}
		if library == "libdl.so" {
			// hostDlsymLibrary serves only libc, libm and libz, so libdl
			// names must come from the compatibility table.
			return tableHas(library, symbol)
		}
		return true
	}
	// Provider.Lookup tries each DT_NEEDED library and then "", and "" matches
	// the compatibility table by name alone, so the owning library is moot.
	return tableHas("", symbol) || graphicsEntryPoint(symbol) || hasDeclared(symbol)
}

func hasDeclared(symbol string) bool {
	_, ok := declaredHostSurface()[symbol]
	return ok
}

// tableHas queries only the static compatibility table. tipsy_android_lookup
// falls through to host graphics dlopen for "vk" names with an empty library
// and for libEGL/libGLESv2/libvulkan, so those are never passed in.
func tableHas(library, symbol string) bool {
	if strings.HasPrefix(symbol, "vk") {
		return false
	}
	switch library {
	case "libEGL.so", "libGLESv2.so", "libvulkan.so", "libvulkan.so.1":
		return false
	}
	return androidLookup(library, symbol) != 0
}
