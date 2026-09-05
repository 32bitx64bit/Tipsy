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
	"time"
	"unsafe"
)

const (
	BionicSyncThreadRBXWorker = iota
	BionicSyncThreadMain
	BionicSyncThreadOther
	bionicSyncThreads
)

const (
	BionicSyncModuleRoblox = iota
	BionicSyncModuleOther
	BionicSyncModuleUnknown
	bionicSyncModules
)

const (
	BionicSyncMutexLock = iota
	BionicSyncMutexTryLock
	BionicSyncMutexTimedLock
	BionicSyncMutexUnlock
	BionicSyncCondSignal
	BionicSyncCondBroadcast
	BionicSyncPthreadSetAffinity
	BionicSyncSchedYield
	BionicSyncSchedGetAffinity
	BionicSyncSchedSetAffinity
	BionicSyncNice
	bionicSyncOps
)

// BionicSyncPathStats is an opt-in aggregate for one guest ABI export. Calls,
// errors, and observable try/timed-lock contention are exact; duration is
// sampled once per 64 calls.
type BionicSyncPathStats struct {
	Calls           uint64
	Contention      uint64
	Errors          uint64
	Samples         uint64
	SampledDuration time.Duration
	MaxDuration     time.Duration
}

// BionicSyncStats separates guest crossings by C OS thread-name and the
// return-address module class. It never contains addresses or arguments.
// Thread names refresh every 64 crossings, so renaming can lag by 63 calls.
type BionicSyncStats struct {
	paths [bionicSyncThreads][bionicSyncModules][bionicSyncOps]BionicSyncPathStats
}

// Path returns one fixed aggregate bucket or an empty value for invalid input.
func (s BionicSyncStats) Path(threadClass, moduleClass, operation int) BionicSyncPathStats {
	if threadClass < 0 || threadClass >= bionicSyncThreads ||
		moduleClass < 0 || moduleClass >= bionicSyncModules ||
		operation < 0 || operation >= bionicSyncOps {
		return BionicSyncPathStats{}
	}
	return s.paths[threadClass][moduleClass][operation]
}

// Aggregate returns one export's totals across thread and module classes.
func (s BionicSyncStats) Aggregate(operation int) BionicSyncPathStats {
	var total BionicSyncPathStats
	if operation < 0 || operation >= bionicSyncOps {
		return total
	}
	for threadClass := 0; threadClass < bionicSyncThreads; threadClass++ {
		for moduleClass := 0; moduleClass < bionicSyncModules; moduleClass++ {
			got := s.paths[threadClass][moduleClass][operation]
			total.Calls += got.Calls
			total.Contention += got.Contention
			total.Errors += got.Errors
			total.Samples += got.Samples
			total.SampledDuration += got.SampledDuration
			if got.MaxDuration > total.MaxDuration {
				total.MaxDuration = got.MaxDuration
			}
		}
	}
	return total
}

// SetBionicSyncDiagnostics must run before the Android client resolves libc.
// Disabled leaves all candidate guest imports bound directly to glibc, so it
// adds no wrapper, clock, classification, or atomic work to normal launches.
func SetBionicSyncDiagnostics(enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_bionic_sync_set_enabled(v)
}

func bionicSyncPath(raw C.TipsyBionicSyncPathStats) BionicSyncPathStats {
	return BionicSyncPathStats{
		Calls:           uint64(raw.calls),
		Contention:      uint64(raw.contention),
		Errors:          uint64(raw.errors),
		Samples:         uint64(raw.samples),
		SampledDuration: time.Duration(raw.sampled_ns),
		MaxDuration:     time.Duration(raw.max_ns),
	}
}

// BionicSyncSnapshot reads aggregate counters. A reset exchanges each atomic
// counter independently, so a live client can keep recording without losing
// call increments between consecutive interval snapshots.
func BionicSyncSnapshot(reset bool) BionicSyncStats {
	var raw C.TipsyBionicSyncStats
	r := C.int(0)
	if reset {
		r = 1
	}
	C.tipsy_bionic_sync_snapshot(&raw, r)
	var stats BionicSyncStats
	for threadClass := 0; threadClass < bionicSyncThreads; threadClass++ {
		for moduleClass := 0; moduleClass < bionicSyncModules; moduleClass++ {
			for operation := 0; operation < bionicSyncOps; operation++ {
				stats.paths[threadClass][moduleClass][operation] =
					bionicSyncPath(raw.path[threadClass][moduleClass][operation])
			}
		}
	}
	return stats
}

func testBionicSyncClockCalls() uint64 {
	return uint64(C.tipsy_test_bionic_sync_clock_calls())
}

func testBionicSyncResetTLS() {
	C.tipsy_test_bionic_sync_reset_tls()
}

func testBionicSyncMutex() int {
	return int(C.tipsy_test_bionic_sync_mutex())
}

func testBionicSyncCondition() int {
	return int(C.tipsy_test_bionic_sync_condition())
}

func testBionicSyncHostDladdr(address uintptr) bool {
	return C.tipsy_test_bionic_sync_host_dladdr(C.uintptr_t(address)) != 0
}

func testBionicSyncNamedCall(entry, function uintptr, name string) int {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	return int(C.tipsy_test_bionic_sync_named_call(C.uintptr_t(entry), C.uintptr_t(function), cs))
}

func testBionicSyncModuleClass(address uintptr) int {
	return int(C.tipsy_test_bionic_sync_module_class(C.uintptr_t(address)))
}

func testBionicSyncRenameCall(entry, function uintptr) int {
	return int(C.tipsy_test_bionic_sync_rename_call(C.uintptr_t(entry), C.uintptr_t(function)))
}
