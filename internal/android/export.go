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
	"fmt"
	"log/slog"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

func androidLog() *slog.Logger {
	return logging.Logger(logging.CatAndroid)
}

//export GoAndroid_LogWrite
func GoAndroid_LogWrite(prio C.int, tag, text *C.char) {
	msg := fmt.Sprintf("[%s] %s", C.GoString(tag), C.GoString(text))
	tagStr := C.GoString(tag)
	switch int(prio) {
	case 2, 3: // VERBOSE, DEBUG
		if tagStr == "rbx.JNIRobloxSettings" {
			androidLog().Info(msg)
			return
		}
		androidLog().Debug(msg)
	case 5: // WARN
		androidLog().Warn(msg)
	case 6, 7: // ERROR, FATAL
		androidLog().Error(msg)
	default:
		androidLog().Info(msg)
	}
}

//export GoAndroid_LogMissing
func GoAndroid_LogMissing(name *C.char) {
	n := C.GoString(name)
	androidLog().Error("[android] missing native symbol: " + n)
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
