// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package syssoname names the Android system libraries that the loader serves
// only through its Resolver. It is a dependency-free leaf so static callers
// (setup preflight) can classify a DT_NEEDED entry exactly as the loader does
// without importing the loader.
package syssoname

import "strings"

// Bionic / NDK system libraries are Resolver-only even if a file exists next
// to the module (never mmap a host or APK copy of libc.so as the Android DSO).
var names = map[string]struct{}{
	"libc.so": {}, "libm.so": {}, "libdl.so": {}, "liblog.so": {},
	"libz.so": {}, "libandroid.so": {}, "libEGL.so": {}, "libGLESv1_CM.so": {},
	"libGLESv2.so": {}, "libGLESv3.so": {}, "libvulkan.so": {},
	"libjnigraphics.so": {}, "libmediandk.so": {}, "libOpenMAXAL.so": {},
	"libOpenSLES.so": {}, "libaaudio.so": {}, "libnativewindow.so": {},
	"libc++.so": {}, "libstdc++.so": {},
}

// Is reports whether the final path element of a DT_NEEDED entry is an Android
// system library.
func Is(soname string) bool {
	if i := strings.LastIndex(soname, "/"); i >= 0 {
		soname = soname[i+1:]
	}
	_, ok := names[soname]
	return ok
}
