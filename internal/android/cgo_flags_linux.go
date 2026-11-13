// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

/*
#cgo CFLAGS: -D_GNU_SOURCE
#cgo linux LDFLAGS: -Wl,-z,relro,-z,now

#include "android_bridge.h"
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

static void *tipsy_test_rtld_default_lookup(const char *name)
{
	return dlsym(RTLD_DEFAULT, name);
}

static int tipsy_test_dladdr(uintptr_t addr, Dl_info *info)
{
	return dladdr((const void *)addr, info);
}
*/
import "C"

import "unsafe"

func hostDlsymForTest(name string) uintptr {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return uintptr(C.tipsy_host_dlsym(cName))
}

func hostDlsymLibraryForTest(lib, name string) uintptr {
	cLib := C.CString(lib)
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cLib))
	defer C.free(unsafe.Pointer(cName))
	return uintptr(C.tipsy_host_dlsym_library(cLib, cName))
}

func rtldDefaultLookupForTest(name string) uintptr {
	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))
	return uintptr(C.tipsy_test_rtld_default_lookup(cName))
}

func dladdrOwnerForTest(addr uintptr) (fname string, fbase uintptr, ok bool) {
	if addr == 0 {
		return "", 0, false
	}
	var info C.Dl_info
	if C.tipsy_test_dladdr(C.uintptr_t(addr), &info) == 0 || info.dli_fname == nil {
		return "", 0, false
	}
	return C.GoString(info.dli_fname), uintptr(info.dli_fbase), true
}
