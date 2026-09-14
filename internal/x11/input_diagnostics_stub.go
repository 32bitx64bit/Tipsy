// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux || !cgo

package x11

// SetInputDrainDiagnostics is a no-op on builds without the native X11
// backend. It still resets the Go-side gate so tests keep the same API.
func SetInputDrainDiagnostics(enabled bool) { inputDrainDiagnosticsOn.Store(enabled) }

func InputDrainDiagnosticsEnabled() bool { return inputDrainDiagnosticsEnabled() }

func InputDrainSnapshot(reset bool) InputDrainStats {
	windowLockWait, goDrain := inputDrainGoSnapshot(reset)
	return InputDrainStats{GoWindowLockWait: windowLockWait, GoDrain: goDrain}
}
