// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#cgo LDFLAGS: -lX11 -lX11-xcb -lxcb -ldl -pthread
#include "android_bridge.h"
#include <stdlib.h>
void *tipsy_dlopen(const char *filename, int flags);
void *tipsy_dlsym(void *handle, const char *symbol);
*/
import "C"

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

func init() {
	Register("libvulkan.so", lookupVulkan)
	Register("libvulkan.so.1", lookupVulkan)
}

func lookupVulkan(sym string) (uintptr, error) {
	if addr := androidLookup("libvulkan.so", sym); addr != 0 {
		return addr, nil
	}
	return 0, nil
}

// BindVulkanWSI records the X11 Display* and Window used to translate
// vkCreateAndroidSurfaceKHR onto the host XCB/Xlib WSI. display and xid must
// be the same connection and window the client will present to.
func BindVulkanWSI(display, xid uintptr) error {
	if C.tipsy_vk_bind_wsi(C.uintptr_t(display), C.uintptr_t(xid)) != 0 {
		return fmt.Errorf("graphics: vulkan WSI bind requires a live X11 display and window")
	}
	if !logging.Logger(logging.CatGraphics).Enabled(context.Background(), slog.LevelInfo) {
		SetVulkanPresentStats(false)
	}
	seedPresentRateWindow()
	logging.Logger(logging.CatGraphics).Info("Android Vulkan WSI bound to X11 window",
		"xcb", C.tipsy_vk_host_has_xcb_surface() != 0,
		"xlib", C.tipsy_vk_host_has_xlib_surface() != 0)
	return nil
}

// UnbindVulkanWSI clears the process-wide Android Vulkan WSI binding.
func UnbindVulkanWSI() {
	C.tipsy_vk_unbind_wsi()
}

func vulkanPresentModeName(mode int) string {
	switch mode {
	case 0:
		return "immediate"
	case 1:
		return "mailbox"
	case 2:
		return "fifo"
	case 3:
		return "fifo_relaxed"
	default:
		return fmt.Sprintf("%d", mode)
	}
}

// SetVulkanVSync controls present-mode filtering and swapchain create rewrites
// on the Android Vulkan adapter. Off forces IMMEDIATE when the host accepts it
// (GLES interval-0 equivalent); on forces FIFO.
func SetVulkanVSync(enabled bool) {
	value := C.int(0)
	if enabled {
		value = 1
	}
	C.tipsy_vk_set_vsync(value)
	logging.Logger(logging.CatGraphics).Info("Android Vulkan VSync policy configured", "vsync", enabled)
}

// VulkanPresentStatistics observes successful vkQueuePresentKHR calls through
// the Android Vulkan adapter. RateFPS is zero until at least two presents.
type VulkanPresentStatistics struct {
	SuccessfulPresents uint64
	Elapsed            time.Duration
	RateFPS            float64
}

var presentRateMu sync.Mutex
var presentRateLastCount uint64
var presentRateLastWall time.Time

func seedPresentRateWindow() {
	presentRateMu.Lock()
	presentRateLastCount = 0
	presentRateLastWall = time.Now()
	presentRateMu.Unlock()
}

func resetPresentRateWindow() {
	presentRateMu.Lock()
	presentRateLastCount = 0
	presentRateLastWall = time.Time{}
	presentRateMu.Unlock()
}

func resetVulkanPresentStats() {
	C.tipsy_vk_reset_present_stats()
	resetPresentRateWindow()
}

// SetVulkanPresentStats enables or disables vkQueuePresentKHR success
// counters. Production defaults to on so launch.go's 2s Info logs keep working.
// When off, present is a host call with no clock or stats atomics beyond the
// relaxed enable load.
func SetVulkanPresentStats(enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_vk_set_present_stats(v)
}

// VulkanPresentStatsEnabled reports whether successful presents increment the
// observation counter.
func VulkanPresentStatsEnabled() bool {
	return C.tipsy_vk_present_stats_enabled() != 0
}

// VulkanPresentStats returns a process-atomic observation of successful
// vkQueuePresentKHR calls made through the Android Vulkan adapter.
// Injected test timestamps (first/last ns) take precedence; otherwise rate is
// computed from counter deltas versus wall time between calls (the 2s ticker).
func VulkanPresentStats() VulkanPresentStatistics {
	var presents, firstNS, lastNS C.uint64_t
	C.tipsy_vk_present_stats(&presents, &firstNS, &lastNS)
	stats := VulkanPresentStatistics{SuccessfulPresents: uint64(presents)}
	if stats.SuccessfulPresents >= 2 && lastNS > firstNS {
		stats.Elapsed = time.Duration(uint64(lastNS) - uint64(firstNS))
		stats.RateFPS = float64(stats.SuccessfulPresents-1) / stats.Elapsed.Seconds()
		return stats
	}
	now := time.Now()
	presentRateMu.Lock()
	defer presentRateMu.Unlock()
	if presentRateLastWall.IsZero() {
		presentRateLastCount = stats.SuccessfulPresents
		presentRateLastWall = now
		return stats
	}
	elapsed := now.Sub(presentRateLastWall)
	delta := uint64(0)
	if stats.SuccessfulPresents >= presentRateLastCount {
		delta = stats.SuccessfulPresents - presentRateLastCount
	}
	presentRateLastCount = stats.SuccessfulPresents
	presentRateLastWall = now
	if elapsed > 0 && delta > 0 {
		stats.Elapsed = elapsed
		stats.RateFPS = float64(delta) / elapsed.Seconds()
	}
	return stats
}

func testVulkanProcIsWrapped(name string) bool {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return C.tipsy_test_vk_proc_is_wrapped(cName) != 0
}

func testVulkanProcIsHostPassthrough(name string) bool {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return C.tipsy_test_vk_proc_is_host_passthrough(cName) != 0
}

func testVulkanAndroidSurfaceAdvertised(hostNames []string) bool {
	if len(hostNames) == 0 {
		return C.tipsy_test_vk_android_surface_advertised(nil, 0) != 0
	}
	cNames := make([]*C.char, len(hostNames))
	ptrs := make([]*C.char, len(hostNames))
	for i, name := range hostNames {
		cNames[i] = C.CString(name)
		ptrs[i] = cNames[i]
	}
	defer func() {
		for _, p := range cNames {
			C.free(unsafe.Pointer(p))
		}
	}()
	return C.tipsy_test_vk_android_surface_advertised((**C.char)(unsafe.Pointer(&ptrs[0])), C.uint32_t(len(ptrs))) != 0
}

func testVulkanRewriteEnabledExtensions(in []string, hasXcb, hasXlib bool) ([]string, bool) {
	if len(in) == 0 {
		return nil, true
	}
	cIn := make([]*C.char, len(in))
	inPtrs := make([]*C.char, len(in))
	outPtrs := make([]*C.char, len(in))
	for i, name := range in {
		cIn[i] = C.CString(name)
		inPtrs[i] = cIn[i]
	}
	defer func() {
		for _, p := range cIn {
			C.free(unsafe.Pointer(p))
		}
	}()
	xcb := C.int(0)
	xlib := C.int(0)
	if hasXcb {
		xcb = 1
	}
	if hasXlib {
		xlib = 1
	}
	rc := C.tipsy_test_vk_rewrite_enabled_extensions(
		(**C.char)(unsafe.Pointer(&inPtrs[0])), C.uint32_t(len(inPtrs)),
		xcb, xlib, (**C.char)(unsafe.Pointer(&outPtrs[0])))
	if rc != 0 {
		return nil, false
	}
	out := make([]string, len(in))
	for i := range out {
		out[i] = C.GoString(outPtrs[i])
	}
	return out, true
}

func testVulkanCreateSwapchainPolicy(vsync bool, requested uint32, policyOK, fallbackOK bool) (first, second uint32, calls int, result int32) {
	cVSync := C.int(0)
	if vsync {
		cVSync = 1
	}
	policyResult := C.int32_t(0)
	if !policyOK {
		policyResult = -3
	}
	fallbackResult := C.int32_t(0)
	if !fallbackOK {
		fallbackResult = -3
	}
	var firstMode, secondMode C.uint32_t
	var nCalls C.int
	var final C.int32_t
	C.tipsy_test_vk_create_swapchain_policy(cVSync, C.uint32_t(requested), policyResult, fallbackResult,
		&firstMode, &secondMode, &nCalls, &final)
	return uint32(firstMode), uint32(secondMode), int(nCalls), int32(final)
}

func testVulkanFilterPresentModes(in []uint32, vsync bool) []uint32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]uint32, len(in))
	n := C.uint32_t(len(out))
	cVSync := C.int(0)
	if vsync {
		cVSync = 1
	}
	C.tipsy_test_vk_filter_present_modes((*C.uint32_t)(unsafe.Pointer(&in[0])), C.uint32_t(len(in)),
		cVSync, (*C.uint32_t)(unsafe.Pointer(&out[0])), &n)
	return out[:n]
}

func testVulkanDlopen() (handle, gipa uintptr) {
	cName := C.CString("libvulkan.so")
	defer C.free(unsafe.Pointer(cName))
	h := C.tipsy_dlopen(cName, 1)
	if h == nil {
		return 0, 0
	}
	cSym := C.CString("vkGetInstanceProcAddr")
	defer C.free(unsafe.Pointer(cSym))
	p := C.tipsy_dlsym(h, cSym)
	return uintptr(h), uintptr(p)
}

func testVulkanRecordPresent(nowNS uint64) {
	C.tipsy_test_vk_record_present(C.uint64_t(nowNS))
}

func testVulkanNotePresentSuccess() {
	C.tipsy_test_vk_note_present_success()
}

func vulkanVSyncEnabled() bool {
	return C.tipsy_vk_vsync_enabled() != 0
}

func vulkanWSIBound() bool {
	return C.tipsy_vk_wsi_bound() != 0
}
