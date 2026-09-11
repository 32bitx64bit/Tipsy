// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#include <stdint.h> // for uintptr_t (declarations only; see //export rule)
*/
import "C"

import "runtime/cgo"

// GoEGLHandoff is invoked once by the C sentinel swap thread when it retires
// on the first observed client-presented frame. It is never called by the
// 125 ms probes, by a run=0 stop, or by a start/swap failure, so it is one
// event per EGL instance, not a poll.
//
// It must not block the C pthread, so the wake is a non-blocking token on the
// instance's capacity-1 coalesced channel. The uintptr is a cgo.Handle for
// that channel; StopSwapThread deletes the handle only after pthread_join, and
// the C thread can only call this before returning, so Value can never see a
// deleted handle.
//
//export GoEGLHandoff
func GoEGLHandoff(handle C.uintptr_t) {
	if handle == 0 {
		return
	}
	wake, ok := cgo.Handle(handle).Value().(chan struct{})
	if !ok || wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}
