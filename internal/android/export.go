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
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

func androidLog() *slog.Logger {
	return logging.Logger(logging.CatAndroid)
}

func init() {
	logging.SetDebugChangeListener(syncAndroidDebugLogFlag)
}

func syncAndroidDebugLogFlag(enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_android_set_debug_log(v)
}

var logWriteCalls atomic.Uint64

func testLogWriteCalls() uint64 {
	return logWriteCalls.Load()
}

func resetTestLogCounters() {
	logWriteCalls.Store(0)
	C.tipsy_android_reset_log_counters()
}

func testAndroidLogSkipCount() uint64 {
	return uint64(C.tipsy_android_log_skip_count())
}

func testAndroidLogPrint(prio int, tag, text string) int {
	cTag := C.CString(tag)
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cTag))
	defer C.free(unsafe.Pointer(cText))
	return int(C.tipsy_test_android_log_print(C.int(prio), cTag, cText))
}

func testAndroidLogWrite(prio int, tag, text string) int {
	cTag := C.CString(tag)
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cTag))
	defer C.free(unsafe.Pointer(cText))
	return int(C.tipsy_test_android_log_write(C.int(prio), cTag, cText))
}

func testAndroidLogVPrint(prio int, tag, text string) int {
	cTag := C.CString(tag)
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cTag))
	defer C.free(unsafe.Pointer(cText))
	return int(C.tipsy_test_android_log_vprint(C.int(prio), cTag, cText))
}

func testAndroidLogAssert(cond, tag, text string) {
	cCond := C.CString(cond)
	cTag := C.CString(tag)
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cCond))
	defer C.free(unsafe.Pointer(cTag))
	defer C.free(unsafe.Pointer(cText))
	C.tipsy_test_android_log_assert(cCond, cTag, cText)
}

func testAndroidLogBufWrite(bufID, prio int, tag, text string) int {
	cTag := C.CString(tag)
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cTag))
	defer C.free(unsafe.Pointer(cText))
	return int(C.tipsy_test_android_log_buf_write(C.int(bufID), C.int(prio), cTag, cText))
}

//export GoAndroid_LogWrite
func GoAndroid_LogWrite(prio C.int, tag, text *C.char) {
	logWriteCalls.Add(1)
	p := int(prio)
	log := androidLog()
	if p != 2 && p != 3 {
		syncAndroidDebugLogFlag(logging.DebugEnabled())
	}
	if p == 2 || p == 3 { // VERBOSE, DEBUG
		tagStr := C.GoString(tag)
		if tagStr != "rbx.JNIRobloxSettings" && !log.Enabled(context.Background(), slog.LevelDebug) {
			return
		}
		msg := "[" + tagStr + "] " + C.GoString(text)
		if tagStr == "rbx.JNIRobloxSettings" {
			log.Info(msg)
			return
		}
		log.Debug(msg)
		return
	}
	msg := "[" + C.GoString(tag) + "] " + C.GoString(text)
	switch p {
	case 5: // WARN
		log.Warn(msg)
	case 6, 7: // ERROR, FATAL
		log.Error(msg)
	default:
		log.Info(msg)
	}
}

//export GoAndroid_LogMissing
func GoAndroid_LogMissing(name *C.char) {
	n := C.GoString(name)
	androidLog().Error("[android] missing native symbol: " + n)
}

//export GoAndroid_LogEGLSwapInterval
func GoAndroid_LogEGLSwapInterval(vsync, requested, effective, primaryOK, primaryError, fallbackOK, fallbackError C.int) {
	log := logging.Logger(logging.CatGraphics)
	if primaryOK != 0 {
		log.Info("Android EGL VSync policy accepted",
			"vsync", vsync != 0, "requested", int(requested), "effective", int(effective))
		return
	}
	if requested != effective {
		if fallbackOK != 0 {
			log.Error("Android EGL VSync policy rejected; preserved client interval",
				"vsync", vsync != 0, "requested", int(requested), "policyInterval", int(effective),
				"policyError", fmt.Sprintf("0x%04x", uint32(primaryError)),
				"fallback", int(requested), "fallbackAccepted", true)
			return
		}
		log.Error("Android EGL VSync policy and client-interval fallback rejected",
			"vsync", vsync != 0, "requested", int(requested), "policyInterval", int(effective),
			"policyError", fmt.Sprintf("0x%04x", uint32(primaryError)),
			"fallbackError", "left for client eglGetError")
		return
	}
	log.Error("Android EGL VSync policy rejected",
		"vsync", vsync != 0, "requested", int(requested), "effective", int(effective),
		"eglError", "left for client eglGetError")
}

//export GoAndroid_LogVulkanPresentMode
func GoAndroid_LogVulkanPresentMode(vsync, requested, effective, primaryOK, primaryError, fallbackOK, fallbackError C.int) {
	log := logging.Logger(logging.CatGraphics)
	requestedName := vulkanPresentModeName(int(requested))
	effectiveName := vulkanPresentModeName(int(effective))
	if primaryOK != 0 {
		log.Info("Android Vulkan VSync policy accepted",
			"vsync", vsync != 0, "requested", requestedName, "effective", effectiveName)
		return
	}
	if requested != effective {
		if fallbackOK != 0 {
			log.Error("Android Vulkan VSync policy rejected; preserved client present mode",
				"vsync", vsync != 0, "requested", requestedName, "policyMode", effectiveName,
				"policyResult", int32(primaryError),
				"fallback", requestedName, "fallbackAccepted", true)
			return
		}
		log.Error("Android Vulkan VSync policy and client present-mode fallback rejected",
			"vsync", vsync != 0, "requested", requestedName, "policyMode", effectiveName,
			"policyResult", int32(primaryError),
			"fallbackResult", int32(fallbackError))
		return
	}
	log.Error("Android Vulkan VSync policy rejected",
		"vsync", vsync != 0, "requested", requestedName, "effective", effectiveName,
		"result", int32(primaryError))
}

//export GoAndroid_LogAudio
func GoAndroid_LogAudio(event, detail *C.char) {
	logging.Logger(logging.CatAudio).Info("[audio] "+C.GoString(event), "detail", C.GoString(detail))
}

//export GoAndroid_AbortMessage
func GoAndroid_AbortMessage(msg *C.char) {
	androidLog().Error("[android] abort message: " + C.GoString(msg))
}

//export GoAndroid_AssetOpen
func GoAndroid_AssetOpen(filename *C.char, mode C.int) unsafe.Pointer {
	_ = mode
	name := C.GoString(filename)
	b, err := openAssetBytes(name)
	if err != nil {
		androidLog().Info("[android] AAssetManager_open: " + name)
		return nil
	}
	return assetFromBytes(b)
}

//export GoAndroid_dlopen
func GoAndroid_dlopen(filename *C.char, flags C.int) unsafe.Pointer {
	_ = flags
	name := C.GoString(filename)
	return openRegistered(name)
}

//export GoAndroid_dlsym
func GoAndroid_dlsym(handle unsafe.Pointer, symbol *C.char) unsafe.Pointer {
	sym := C.GoString(symbol)
	p := lookupHandle(handle, sym)
	if p == 0 {
		setDLError("dlsym: undefined symbol: " + sym)
		return nil
	}
	setDLError("")
	up := p
	return *(*unsafe.Pointer)(unsafe.Pointer(&up))
}

//export GoAndroid_dlclose
func GoAndroid_dlclose(handle unsafe.Pointer) C.int {
	if handle == nil {
		return 0
	}
	return 0
}

var dlerrorC *C.char

//export GoAndroid_dlerror
func GoAndroid_dlerror() *C.char {
	regMu.Lock()
	msg := dlErr
	regMu.Unlock()
	if dlerrorC != nil {
		C.free(unsafe.Pointer(dlerrorC))
		dlerrorC = nil
	}
	if msg == "" {
		return nil
	}
	dlerrorC = C.CString(msg)
	return dlerrorC
}
