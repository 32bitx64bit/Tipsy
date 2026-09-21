// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

/*
// Declaration-only preamble: this file uses //export, so it must not
// contain C definitions.
*/
import "C"

// inputWake is the coalesced Go-side signal that the C ring has work.
// Capacity 1: extra C notifies drop until Pump acks and the launch loop
// consumes the token.
var inputWake = make(chan struct{}, 1)

// refreshWake is the coalesced Go-side signal that the window's refresh
// generation moved. Capacity 1: the version store precedes every notify,
// so a dropped token can only hide a generation the pending token reads.
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
// or an X I/O error.
func (w *Window) InputReady() <-chan struct{} {
	if w == nil {
		return nil
	}
	return inputWake
}

// RefreshReady is the coalesced wake for display-refresh changes. The launch
// loop reads RefreshVersion after receiving the token.
func (w *Window) RefreshReady() <-chan struct{} {
	if w == nil {
		return nil
	}
	return refreshWake
}
