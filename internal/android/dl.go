// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "android_bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"sync"
	"unsafe"
)

type registeredMod struct {
	soname string
	lookup LookupFunc
	handle unsafe.Pointer
}

var (
	regMu      sync.Mutex
	registry   = map[string]*registeredMod{}
	handleByH  = map[unsafe.Pointer]*registeredMod{}
	globalMods []*registeredMod

	dlErrMu sync.Mutex
	dlErr   string
)

func setDLError(msg string) {
	dlErrMu.Lock()
	dlErr = msg
	dlErrMu.Unlock()
}

func getDLError() string {
	dlErrMu.Lock()
	defer dlErrMu.Unlock()
	return dlErr
}

// Register adds a soname → lookup function used by libdl.so dlopen/dlsym.
// The ELF loader should Register each loaded Android module.
func Register(soname string, lookup LookupFunc) {
	if soname == "" || lookup == nil {
		return
	}
	soname = pathBase(soname)
	regMu.Lock()
	defer regMu.Unlock()
	invalidateSymbolCache()
	if existing, ok := registry[soname]; ok {
		existing.lookup = lookup
		return
	}
	cs := C.CString(soname)
	h := C.tipsy_dlhandle_new(cs)
	C.free(unsafe.Pointer(cs))
	m := &registeredMod{soname: soname, lookup: lookup, handle: h}
	registry[soname] = m
	handleByH[h] = m
	globalMods = append(globalMods, m)
}

// RegisterImage publishes a mmap'd Android ELF so dl_iterate_phdr can find
// it (host glibc only knows dlopen'd objects). Needed for C++ unwind.
// It also copies executable segment bounds for opt-in caller attribution.
// Re-registering a load bias replaces its metadata and invalidates caches.
// The mapping must stay live until UnregisterImage, and all guest use and
// concurrent unwind walks must end before unregistering and unmapping it.
func RegisterImage(loadBias uintptr, path string) {
	if loadBias == 0 {
		return
	}
	cs := C.CString(path)
	C.tipsy_register_image(C.uintptr_t(loadBias), cs)
	C.free(unsafe.Pointer(cs))
}

// UnregisterImage removes a mapped image before its owner unmaps it. A client
// kept mapped for live engine workers must remain registered as well.
func UnregisterImage(loadBias uintptr) {
	C.tipsy_unregister_image(C.uintptr_t(loadBias))
}

func dlIterateCount() int {
	return int(C.tipsy_dl_iterate_count())
}

func testImageGeneration() uint64 {
	return uint64(C.tipsy_image_generation())
}

func testImageCodeRange(addr uintptr) (start, end uintptr, moduleClass int) {
	var cStart, cEnd C.uintptr_t
	var gen C.uint64_t
	moduleClass = int(C.tipsy_image_code_range(C.uintptr_t(addr), &cStart, &cEnd, &gen))
	return uintptr(cStart), uintptr(cEnd), moduleClass
}

func pathBase(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}

func dlRegistryLookup(sym string) uintptr {
	regMu.Lock()
	mods := append([]*registeredMod(nil), globalMods...)
	regMu.Unlock()
	for _, m := range mods {
		if m.lookup == nil {
			continue
		}
		if p, err := m.lookup(sym); err == nil && p != 0 {
			return p
		}
	}
	return 0
}

func dlRegistryLookupLib(lib, sym string) uintptr {
	regMu.Lock()
	m := registry[pathBase(lib)]
	regMu.Unlock()
	if m == nil || m.lookup == nil {
		return 0
	}
	p, err := m.lookup(sym)
	if err != nil {
		return 0
	}
	return p
}

func lookupHandle(handle unsafe.Pointer, sym string) uintptr {
	if handle == nil || uintptr(handle) == ^uintptr(0) {
		if p := dlRegistryLookup(sym); p != 0 {
			return p
		}
		return hostDlsym(sym)
	}
	regMu.Lock()
	m := handleByH[handle]
	regMu.Unlock()
	if m == nil || m.lookup == nil {
		return 0
	}
	p, err := m.lookup(sym)
	if err != nil {
		return 0
	}
	return p
}

func openRegistered(filename string) unsafe.Pointer {
	if filename == "" {
		return nil
	}
	base := pathBase(filename)
	regMu.Lock()
	m := registry[base]
	regMu.Unlock()
	if m != nil {
		setDLError("")
		return m.handle
	}
	setDLError("dlopen: " + base + ": not in Android module registry")
	return nil
}
