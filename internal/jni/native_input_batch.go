// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64 && tipsy_input_batch

package jni

/*
#include "native_input_batch.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// Experimental transport only. No current input path invokes this helper.
// Gate integration until owner-thread, reentrancy, focus and shutdown tests pass.
type nativeInputCommand struct {
	kind      uint32
	fn, class uintptr
	i         [5]int32
	f         [4]float32
}

func executeNativeInputBatch(env uintptr, commands []nativeInputCommand) (completed int, mouseLocked bool, status int) {
	if len(commands) > int(C.TIPSY_INPUT_BATCH_MAX) {
		return 0, false, int(C.TIPSY_BATCH_INVALID)
	}
	if len(commands) == 0 {
		return 0, false, int(C.TIPSY_BATCH_OK)
	}
	var native [C.TIPSY_INPUT_BATCH_MAX]C.TipsyInputCommand
	for n, c := range commands {
		native[n].kind = C.uint32_t(c.kind)
		native[n].fn = C.uintptr_t(c.fn)
		native[n].clazz = C.uintptr_t(c.class)
		for j, v := range c.i {
			native[n].i[j] = C.int32_t(v)
		}
		for j, v := range c.f {
			native[n].f[j] = C.float(v)
		}
	}
	var result C.TipsyInputBatchResult
	rc := C.tipsy_jni_input_batch((*C.JNIEnv)(unsafe.Pointer(env)), &native[0], C.size_t(len(commands)), &result)
	// C and native Main borrow this pointer only until the synchronous call ends.
	runtime.KeepAlive(&native)
	return int(result.completed), result.mouse_locked != 0, int(rc)
}
