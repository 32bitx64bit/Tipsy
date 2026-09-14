// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package gamepad

import (
	"errors"
	"time"
)

// ControllerReadinessSnapshot mirrors the Linux readiness aggregate. No
// readiness source is available off Linux.
type ControllerReadinessSnapshot struct {
	InitialRescans    uint64
	HotplugRescans    uint64
	RecoveryRescans   uint64
	EvdevReady        uint64
	InotifyReady      uint64
	ShutdownWake      uint64
	FrameHandoffs     uint64
	ReadyToFrameMinNS uint64
	ReadyToFrameMaxNS uint64
	ReadyToFrameSumNS uint64
}

// ControllerReadinessDiagnostics is unavailable off Linux.
type ControllerReadinessDiagnostics struct{}

// Snapshot reports zero readiness sources off Linux.
func (d *ControllerReadinessDiagnostics) Snapshot() ControllerReadinessSnapshot {
	return ControllerReadinessSnapshot{}
}

// ReadyPump is unavailable off Linux.
type ReadyPump struct {
	Manager        *Manager
	OnFrame        func(Pad, *Frame)
	OnRescanError  func(error)
	Diagnostics    *ControllerReadinessDiagnostics
	RecoveryRescan time.Duration
}

// NewReadyPump constructs an unavailable readiness pump off Linux.
func NewReadyPump(manager *Manager) *ReadyPump { return &ReadyPump{Manager: manager} }

// Run fails honestly because evdev/inotify readiness is Linux-only.
func (p *ReadyPump) Run(stop <-chan struct{}) error {
	return errors.New("gamepad: readiness pump unavailable off Linux")
}

// ControllerIdleReadinessFixture fails honestly off Linux.
func ControllerIdleReadinessFixture(duration time.Duration) (ControllerReadinessSnapshot, error) {
	return ControllerReadinessSnapshot{}, errors.New("gamepad: idle readiness fixture unavailable off Linux")
}
