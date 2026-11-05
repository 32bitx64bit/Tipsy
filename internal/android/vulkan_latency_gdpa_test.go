// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"testing"
	"unsafe"
)

// fakeDeviceGDPA is the host's vkCreateDevice and vkGetDeviceProcAddr in one
// value. create is the host create call; getProcAddr is the lookup. The lookup
// rejects every handle but the one create returned and counts the rejection.
// cgo is unavailable in tests, so the adapter reaches this through
// tipsyTestInstallDeviceResolver, which vulkan.c installs by the same seam it
// uses for the host vkGetDeviceProcAddr wrapper.
type fakeDeviceGDPA struct {
	only    unsafe.Pointer
	created unsafe.Pointer
	lookups int
	failed  int
	// resolve mirrors a loader that lists the extension in the chain but
	// cannot hand back vkAntiLagUpdateAMD for the new device.
	resolve bool
}

// newFakeDeviceGDPA allocates the one handle the fake accepts.
func newFakeDeviceGDPA(t *testing.T) *fakeDeviceGDPA {
	t.Helper()
	f := &fakeDeviceGDPA{only: tipsyTestFakeDevice()}
	t.Cleanup(func() { tipsyTestFreeDevice(f.only) })
	return f
}

// create records the host vkCreateDevice result. On failure no handle is
// accepted, matching a driver that never produced a VkDevice.
func (f *fakeDeviceGDPA) create(succeed bool) {
	if succeed {
		f.created = f.only
		return
	}
	f.created = nil
}

func (f *fakeDeviceGDPA) device() unsafe.Pointer { return f.only }

// getProcAddr answers vkAntiLagUpdateAMD for the created device only.
func (f *fakeDeviceGDPA) getProcAddr(device unsafe.Pointer, name string) unsafe.Pointer {
	if f.created == nil || device != f.created {
		f.failed++
		return nil
	}
	f.lookups++
	if name != "vkAntiLagUpdateAMD" || !f.resolve {
		return nil
	}
	return tipsyTestFakeDeviceProc()
}

// installFakeDeviceGDPA makes f the latency unit's device resolver for one
// test. Call it after resetLatencyTest: cleanups run last-registered first, so
// this one puts the unit's test resolver back before resetLatencyTest runs.
func installFakeDeviceGDPA(t *testing.T, f *fakeDeviceGDPA) {
	t.Helper()
	tipsyTestInstallDeviceResolver(f.getProcAddr)
	t.Cleanup(tipsyTestInstallDeviceResolverRestore)
}

// TestAdapterResolverNeverFallsBackToTheLoader drives the resolver vulkan.c
// installs in production through the latency unit's real resolve hook. The
// fake host vkGetDeviceProcAddr returns NULL while the fake instance/loader
// lookup answers every name, so a resolver that still fell back would hand
// back an entry instead of closing the feature.
func TestAdapterResolverNeverFallsBackToTheLoader(t *testing.T) {
	t.Setenv("TIPSY_ANTILAG", "1")
	resetLatencyTest(t)
	device := armTestAntiLag(t)

	if got := tipsyTestAdapterResolver(device); got != 0 {
		t.Fatalf("adapter resolver bits = %d; want 0 (anti-lag must close, no instance fallback)", got)
	}
	if VulkanAntiLagArmed() {
		t.Fatal("a NULL device lookup must close anti-lag even when the loader answers")
	}
	if got := tipsyTestAntiLagSoftCloses(); got != 1 {
		t.Fatalf("soft closes = %d; want 1", got)
	}
}
