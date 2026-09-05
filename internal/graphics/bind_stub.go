// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package graphics

import "github.com/tipsy-linux/tipsy/internal/x11"

func platformDisplayRefreshRates(xdisplay, xid uintptr) (float64, []float32) {
	_ = xdisplay
	_ = xid
	return 0, nil
}

// BindEGL reports that native EGL is unavailable on this build.
func BindEGL(x *x11.Window) (*EGL, error) {
	_ = x
	return nil, ErrUnavailable
}

// Swap reports that native EGL is unavailable on this build.
func (e *EGL) Swap() error {
	if e == nil {
		return ErrClosed
	}
	return ErrUnavailable
}

// ReleaseCurrent reports that native EGL is unavailable on this build.
func (e *EGL) ReleaseCurrent() error {
	if e == nil {
		return ErrClosed
	}
	return ErrUnavailable
}

// StartSwapThread reports that native EGL is unavailable on this build.
func (e *EGL) StartSwapThread() error {
	if e == nil {
		return ErrClosed
	}
	return ErrUnavailable
}

// StopSwapThread is a no-op on builds without native EGL.
func (e *EGL) StopSwapThread() error {
	if e == nil {
		return nil
	}
	return nil
}

func (e *EGL) swapHandedOffLocked() (retired bool, probes uint64, failed uint64) {
	return false, 0, 0
}

// Close is a no-op on builds without native EGL.
func (e *EGL) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.display = 0
	e.surface = 0
	e.context = 0
	return nil
}

func (e *EGL) clearRGBA(r, g, b, a float32) error {
	_ = r
	_ = g
	_ = b
	_ = a
	return ErrUnavailable
}
