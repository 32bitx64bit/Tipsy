// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#include <stddef.h>
int tipsy_test_audio_worker_thread_name(char *out, size_t cap);
*/
import "C"

import "unsafe"

// testAudioWorkerThreadName spawns one real OpenSL stream worker and returns
// the name it set on itself, or "" if it never became "tip.opensles".
func testAudioWorkerThreadName() string {
	buf := make([]byte, 32)
	if C.tipsy_test_audio_worker_thread_name((*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf))) != 0 {
		return ""
	}
	return C.GoString((*C.char)(unsafe.Pointer(&buf[0])))
}
