// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include <errno.h>
#include <stdlib.h>
int tipsy_system(const char *command);
static int system_shim_call(const char *command, int *saved_errno) {
	errno = 0;
	int rc = tipsy_system(command);
	*saved_errno = errno;
	return rc;
}
*/
import "C"

import "unsafe"

// systemShimProbe calls the Tipsy system() shim with command (nil allowed)
// and returns its result and the guest errno it left behind.
func systemShimProbe(command *string) (int, int) {
	var cmd *C.char
	if command != nil {
		cmd = C.CString(*command)
		defer C.free(unsafe.Pointer(cmd))
	}
	var savedErrno C.int
	rc := C.system_shim_call(cmd, &savedErrno)
	return int(rc), int(savedErrno)
}
