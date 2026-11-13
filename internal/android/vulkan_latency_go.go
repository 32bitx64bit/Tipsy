// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include "vulkan_latency.h"
#include <stdlib.h>

extern void *tipsyTestGDPA(void *device, char *name);

static void *tipsy_test_gdpa_bridge(void *device, const char *name)
{
	return tipsyTestGDPA(device, (char *)name);
}

static void *tipsy_test_gdpa_bridge_ptr(void)
{
	return (void *)&tipsy_test_gdpa_bridge;
}
*/
import "C"

import "unsafe"

// Go-facing hooks for the anti-lag unit. Tests use the tipsyTest* forms;
// production code drives the same C entry points through vulkan.c.

// ArmVulkanAntiLag opens the VK_AMD_anti_lag gate. driver must be the probe's
// driver name ("radv") and proved must reflect a successful RADV probe.
func ArmVulkanAntiLag(driver string, proved bool) bool {
	name := C.CString(driver)
	defer C.free(unsafe.Pointer(name))
	provedInt := C.int(0)
	if proved {
		provedInt = 1
	}
	return C.tipsy_vk_antilag_arm(name, provedInt) != 0
}

// VulkanAntiLagArmed reports whether anti-lag calls may happen.
func VulkanAntiLagArmed() bool { return C.tipsy_vk_antilag_armed() != 0 }

func tipsyTestLatencyReset() { C.tipsy_test_vk_latency_reset() }

func tipsyTestLatencyInstallProcs() { C.tipsy_test_vk_latency_install_test_procs() }

func tipsyTestFakeDeviceProc() unsafe.Pointer { return C.tipsy_test_vk_latency_fake_device_proc() }

// tipsyTestAdapterResolver drives the resolver vulkan.c installs in
// production with a NULL device lookup and an answering loader lookup. See
// tipsy_test_vk_latency_adapter_resolver for the result bits.
func tipsyTestAdapterResolver(device unsafe.Pointer) int {
	return int(C.tipsy_test_vk_latency_adapter_resolver(device))
}

// fakeDeviceResolver is the test stand-in for host vkGetDeviceProcAddr.
var fakeDeviceResolver func(device unsafe.Pointer, name string) unsafe.Pointer

// tipsyTestInstallDeviceResolver installs fn through the same seam vulkan.c
// uses for its host vkGetDeviceProcAddr wrapper; nil clears the resolver.
func tipsyTestInstallDeviceResolver(fn func(device unsafe.Pointer, name string) unsafe.Pointer) {
	fakeDeviceResolver = fn
	if fn == nil {
		C.tipsy_test_vk_latency_install_device_resolver(nil)
		return
	}
	C.tipsy_test_vk_latency_install_device_resolver(C.tipsy_test_gdpa_bridge_ptr())
}

// tipsyTestInstallDeviceResolverRestore drops the fake and reinstalls the
// unit's own test resolver, which answers vkAntiLagUpdateAMD for any object.
func tipsyTestInstallDeviceResolverRestore() {
	fakeDeviceResolver = nil
	tipsyTestLatencyInstallProcs()
}

// tipsyTestGDPA is the C-called side of the fake. C passes the raw device
// handle and the entry-point name verbatim.
//
//export tipsyTestGDPA
func tipsyTestGDPA(device unsafe.Pointer, name *C.char) unsafe.Pointer {
	if fakeDeviceResolver == nil || name == nil {
		return nil
	}
	return fakeDeviceResolver(device, C.GoString(name))
}

// tipsyTestResolveAntiLag resolves vkAntiLagUpdateAMD for one VkDevice through
// the installed device-proc resolver.
func tipsyTestResolveAntiLag(device unsafe.Pointer) (bool, unsafe.Pointer) {
	var update unsafe.Pointer
	ok := C.tipsy_test_vk_latency_resolve_anti_lag(device, &update) != 0
	return ok, update
}

// tipsyTestFakeDevice returns a live C allocation used as a stand-in
// VkDevice. A real allocation keeps the pointer valid and GC-visible.
func tipsyTestFakeDevice() unsafe.Pointer {
	return C.calloc(1, 64)
}

func tipsyTestFreeDevice(device unsafe.Pointer) { C.free(device) }

func tipsyTestArmAntiLag(driver string, proved bool, device unsafe.Pointer) bool {
	name := C.CString(driver)
	defer C.free(unsafe.Pointer(name))
	provedInt := C.int(0)
	if proved {
		provedInt = 1
	}
	return C.tipsy_test_vk_latency_arm(name, provedInt, device) != 0
}

func tipsyTestAntiLagUpdate(device unsafe.Pointer) bool {
	return C.tipsy_vk_antilag_update(device) != 0
}

func tipsyTestAntiLagUpdateCount() int { return int(C.tipsy_test_vk_antilag_update_count()) }

func tipsyTestAntiLagSoftCloses() int { return int(C.tipsy_test_vk_antilag_soft_closes()) }

func tipsyTestAntiLagMode() int { return int(C.tipsy_test_vk_antilag_last_mode()) }

func tipsyTestAntiLagMaxFPS() uint32 { return uint32(C.tipsy_test_vk_antilag_last_max_fps()) }

func tipsyTestAntiLagPresentationNull() bool {
	return C.tipsy_test_vk_antilag_last_presentation_null() != 0
}

func tipsyTestPrepareDeviceCreate(supportsExt, clientHasStruct, clientEnabled bool) (bool, bool) {
	b2i := func(b bool) C.int {
		if b {
			return 1
		}
		return 0
	}
	var added C.int
	prepared := C.tipsy_test_vk_latency_prepare_device_create(
		b2i(supportsExt), b2i(clientHasStruct), b2i(clientEnabled), &added) != 0
	return prepared, added != 0
}

// tipsyTestPresentLoop runs the production present entry iterations times with
// no cgo overhead between them.
func tipsyTestPresentLoop(queue unsafe.Pointer, iterations uint32) {
	C.tipsy_test_vk_latency_present_loop(queue, C.uint32_t(iterations))
}

// tipsyTestLatencyABI returns how many of entries disagree with the values the
// C unit actually uses.
func tipsyTestLatencyABI(entries []struct {
	name  string
	value uint32
}) int {
	expected := make([]C.TipsyVkLatencyAbiEntry, len(entries))
	names := make([]*C.char, len(entries))
	defer func() {
		for _, name := range names {
			C.free(unsafe.Pointer(name))
		}
	}()
	for i := range entries {
		names[i] = C.CString(entries[i].name)
		expected[i].name = names[i]
		expected[i].value = C.uint32_t(entries[i].value)
	}
	if len(expected) == 0 {
		return int(C.tipsy_test_vk_latency_abi(nil, 0))
	}
	return int(C.tipsy_test_vk_latency_abi(&expected[0], C.uint32_t(len(expected))))
}

func tipsyTestInstanceCreationBegin() bool { return C.tipsy_vk_instance_creation_begin() != 0 }

func tipsyTestInstanceCreationClosed() bool {
	return C.tipsy_vk_instance_creation_closed() != 0
}

func tipsyTestLogShaderCache() { C.tipsy_vk_latency_log_shader_cache() }

func tipsyTestLatencyChainFinds(nodes, target uint32) bool {
	return C.tipsy_test_vk_latency_chain_finds(C.uint32_t(nodes), C.uint32_t(target)) != 0
}

func tipsyTestClassify(driver string, vendor uint32, listed, software bool) (string, bool) {
	name := C.CString(driver)
	defer C.free(unsafe.Pointer(name))
	b2i := func(b bool) C.int {
		if b {
			return 1
		}
		return 0
	}
	var out [64]C.char
	var accept C.int
	C.tipsy_vk_antilag_classify(name, C.uint32_t(vendor), b2i(listed), b2i(software),
		&out[0], C.size_t(len(out)), &accept)
	return C.GoString(&out[0]), accept != 0
}
