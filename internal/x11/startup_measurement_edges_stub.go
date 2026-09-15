// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package x11

func startupMeasurementSetMapObserver(w *Window, enabled bool) {
	_ = w
	_ = enabled
}

func startupMeasurementMapObserved(w *Window) bool {
	_ = w
	return false
}

func startupMeasurementEnableDrawableObservation(w *Window) bool {
	_ = w
	return false
}

func startupMeasurementDisableDrawableObservation(w *Window) { _ = w }

func startupMeasurementArmDrawableObservation(w *Window) bool {
	_ = w
	return false
}

func startupMeasurementTakeDrawableObservation(w *Window) bool {
	_ = w
	return false
}

func startupMeasurementDispatchVerticalScroll(w *Window) bool {
	_ = w
	return false
}
