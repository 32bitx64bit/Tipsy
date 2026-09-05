// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#include "stutter_diag.h"
#include <stdlib.h>
*/
import "C"

import "unsafe"

const (
	JNIThreadRBXWorker = iota
	JNIThreadMain
	JNIThreadOther
	jniThreadClasses
)

const (
	JNICALLInstance = iota
	JNICALLStatic
	JNICALLNonvirtual
	jniCallFamilies
)

// JNIStutterStats counts Java-call-family entries at the C bridge, before the
// cgo callback can obscure the originating native OS thread name.
type JNIStutterStats struct {
	Calls [jniThreadClasses][jniCallFamilies]uint64
}

func SetStutterDiagnostics(enabled bool) {
	v := C.int(0)
	if enabled {
		v = 1
	}
	C.tipsy_jni_stutter_diag_set_enabled(v)
}

func StutterSnapshot(reset bool) JNIStutterStats {
	var raw C.TipsyJNIStutterStats
	r := C.int(0)
	if reset {
		r = 1
	}
	C.tipsy_jni_stutter_diag_snapshot(&raw, r)
	flat := (*[jniThreadClasses * jniCallFamilies]C.uint64_t)(unsafe.Pointer(&raw.calls[0][0]))
	var out JNIStutterStats
	for threadClass := 0; threadClass < jniThreadClasses; threadClass++ {
		for family := 0; family < jniCallFamilies; family++ {
			out.Calls[threadClass][family] = uint64(flat[threadClass*jniCallFamilies+family])
		}
	}
	return out
}

func testJNIThreadClass(name string) int {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	return int(C.tipsy_jni_test_classify_thread_name(cname))
}

func testJNIThreadNameLookups() uint64 {
	return uint64(C.tipsy_jni_test_thread_name_lookups())
}

func testJNIResetTLS() {
	C.tipsy_jni_test_reset_tls()
}

func testJNIRecord(family int) {
	C.tipsy_jni_stutter_diag_record(C.int(family))
}

func testJNIRecordOnNamedThread(name string, family int, calls uint64) int {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	return int(C.tipsy_jni_test_record_on_named_thread(cname, C.int(family), C.uint64_t(calls)))
}
