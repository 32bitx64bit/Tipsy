// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64 && tipsy_input_batch

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include "native_input_batch.h"
#include "call.h"
#include <stdlib.h>

// Observation record written by the fake guest on whichever thread runs it.
// Plain C storage: never a pointer into Go memory.
struct owned_probe {
	JNIEnv *env_arg;
	JNIEnv *seen_current;
	int seen_on_main;
	int go_calls;
	int nested_rc;
	int nested_current_ok;
};

extern int goOwnedProbeCallback(JNIEnv *env);

static int ownedNestedFake(JNIEnv *env, void *data)
{
	struct owned_probe *p = (struct owned_probe *)data;
	p->nested_current_ok = (tipsy_jni_current_env() == env);
	return 77;
}

static int ownedFakeGuest(JNIEnv *env, void *data)
{
	struct owned_probe *p = (struct owned_probe *)data;
	p->env_arg = env;
	p->seen_current = tipsy_jni_current_env();
	p->seen_on_main = tipsy_on_native_main();
	p->go_calls += (int)goOwnedProbeCallback(env);
	// Owner re-entry must execute inline on the same thread.
	p->nested_rc = tipsy_jni_run_owned(env, ownedNestedFake, data);
	return 41;
}

static int runOwnedProbe(JNIEnv *env, struct owned_probe *p)
{
	return tipsy_jni_run_owned(env, ownedFakeGuest, (void *)p);
}
*/
import "C"

import (
	"sync/atomic"
	"unsafe"

	_ "github.com/tipsy-linux/tipsy/internal/loader"
)

//export goOwnedProbeCallback
func goOwnedProbeCallback(env *C.JNIEnv) C.int {
	ownedProbeGoCalls.Add(1)
	if env == nil {
		return 0
	}
	return 1
}

var ownedProbeGoCalls atomic.Int32

const (
	ownedBatchOK          = int(C.TIPSY_BATCH_OK)
	ownedBatchInvalid     = int(C.TIPSY_BATCH_INVALID)
	ownedBatchMouseLocked = uint32(C.TIPSY_BATCH_MOUSE_LOCKED)
	ownedBatchMax         = int(C.TIPSY_INPUT_BATCH_MAX)
	ownedJNIErr           = int(C.JNI_ERR)
)

type ownedProbeResult struct {
	rc            int
	goCalls       int32
	envArg        uintptr
	seenCurrent   uintptr
	seenOnMain    bool
	guestGoCalls  int
	nestedRC      int
	nestedCurrent bool
}

func runOwnedProbeForTest(env uintptr) ownedProbeResult {
	var probe C.struct_owned_probe
	ownedProbeGoCalls.Store(0)
	rc := C.runOwnedProbe((*C.JNIEnv)(unsafe.Pointer(env)), &probe)
	return ownedProbeResult{
		rc:            int(rc),
		goCalls:       ownedProbeGoCalls.Load(),
		envArg:        uintptr(unsafe.Pointer(probe.env_arg)),
		seenCurrent:   uintptr(unsafe.Pointer(probe.seen_current)),
		seenOnMain:    probe.seen_on_main != 0,
		guestGoCalls:  int(probe.go_calls),
		nestedRC:      int(probe.nested_rc),
		nestedCurrent: probe.nested_current_ok != 0,
	}
}
