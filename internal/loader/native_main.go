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

// EnsureNativeMain starts the 64 MiB Android-main pthread (once).
func EnsureNativeMain() {
	C.tipsy_start_native_main()
}

// OnNativeMain reports whether the caller is the Android-main pthread.
func OnNativeMain() bool {
	return C.tipsy_on_native_main() != 0
}

func testMarkMainAddr() uintptr {
	return uintptr(C.tipsy_test_mark_main_addr())
}

func testTookMain() bool {
	return C.tipsy_test_took_main() != 0
}
