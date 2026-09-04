// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#cgo LDFLAGS: -lpthread
#include "call.h"
*/
import "C"

func call0(fn uintptr) {
	if fn == 0 {
		return
	}
	C.tipsy_call0(ptrFromUintptr(fn))
}

func callIFunc(fn uintptr) uintptr {
	if fn == 0 {
		return 0
	}
	return uintptr(C.tipsy_call0_ret(ptrFromUintptr(fn)))
}

// CallJNIOnLoad invokes JNI_OnLoad(vm, reserved) using the SysV AMD64 ABI.
func CallJNIOnLoad(fn, vm, reserved uintptr) int32 {
	if fn == 0 {
		return -1
	}
	return int32(C.tipsy_call_jni_onload(ptrFromUintptr(fn), ptrFromUintptr(vm), ptrFromUintptr(reserved)))
}

// CallP8 invokes fn(a0..a7) as SysV AMD64 pointer arguments and returns int64.
func CallP8(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) int64 {
	if fn == 0 {
		return 0
	}
	return int64(C.tipsy_call_p8(
		ptrFromUintptr(fn),
		ptrFromUintptr(a0), ptrFromUintptr(a1), ptrFromUintptr(a2), ptrFromUintptr(a3),
		ptrFromUintptr(a4), ptrFromUintptr(a5), ptrFromUintptr(a6), ptrFromUintptr(a7),
	))
}

// CallP3 invokes fn(a0, a1, a2) as void(*)(void*,void*,void*).
func CallP3(fn, a0, a1, a2 uintptr) {
	if fn == 0 {
		return
	}
	C.tipsy_call_p3(ptrFromUintptr(fn), ptrFromUintptr(a0), ptrFromUintptr(a1), ptrFromUintptr(a2))
}

// ParkPollFutexAddr is the C helper that slices Roblox 0x29cfa14.
func ParkPollFutexAddr() uintptr {
	return uintptr(C.tipsy_park_poll_futex_addr())
}
