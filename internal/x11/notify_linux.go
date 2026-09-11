// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
// Declaration-only preamble: this file uses //export, so it must not
// contain C definitions (they would be compiled twice).
*/
import "C"

// inputWake is the coalesced Go-side signal that the C ring has work.
// Capacity 1: extra C notifies drop until Pump acks and the launch loop
// has consumed the token.
var inputWake = make(chan struct{}, 1)

// refreshWake is the coalesced Go-side signal that the window's refresh
// generation moved (client move/resize, map/reparent, or RandR change).
// Capacity 1: extra C notifies drop until the launch loop consumes the token
// and reads RefreshVersion. The version store precedes every notify, so a
// dropped token can only hide a generation the pending token will read.
var refreshWake = make(chan struct{}, 1)

//export GoX11_Notify
func GoX11_Notify() {
	select {
	case inputWake <- struct{}{}:
	default:
	}
}

//export GoX11_RefreshNotify
func GoX11_RefreshNotify() {
	select {
	case refreshWake <- struct{}{}:
	default:
	}
}

// InputReady is the coalesced wake for the launch loop. It receives a token
// when the C input ring goes from empty to non-empty, and on CLOSE/RESIZE
// or an X I/O error. Extra wakes are dropped while a token is already pending.
func (w *Window) InputReady() <-chan struct{} {
	if w == nil {
		return nil
	}
	return inputWake
}

// RefreshReady is the coalesced wake for display-refresh changes. The launch
// loop reads RefreshVersion after receiving the token. Extra wakes are
// dropped while a token is already pending.
func (w *Window) RefreshReady() <-chan struct{} {
	if w == nil {
		return nil
	}
	return refreshWake
}
