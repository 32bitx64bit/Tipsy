// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package gamepad

import "errors"

// InputNodeDir is the default evdev directory (Linux only).
const InputNodeDir = "/dev/input"

// MaxGamepads caps simultaneous pads (lean single-pad build). Mirrors
// device.go: lowest-sorted path is player 1, a second pad is ignored
// honestly, never faked.
const MaxGamepads = 1

var (
	// ErrNoGamepad means enumeration found zero accessible gamepads.
	ErrNoGamepad = errors.New("gamepad: no gamepad found (zero devices is the honest state)")
	// ErrPermissionDenied means nodes exist but cannot be opened.
	ErrPermissionDenied = errors.New("gamepad: permission denied on /dev/input/event* (add user to input group and relogin; Flatpak needs --device=input)")
)

// PermissionHint is the actionable EACCES guidance.
const PermissionHint = "add user to input group and relogin; Flatpak needs --device=input"

// ErrorForErrno maps an open failure to its honest error.
func ErrorForErrno(path string, err error) error {
	return err
}

// Device is unavailable off Linux.
type Device struct{}

// Path returns the node path.
func (d *Device) Path() string { return "" }

// Info returns an empty snapshot.
func (d *Device) Info() DeviceInfo { return DeviceInfo{} }

// Close is a no-op off Linux.
func (d *Device) Close() error { return nil }

// ReadAvailable is unavailable off Linux.
func (d *Device) ReadAvailable() ([]InputEvent, error) { return nil, ErrNoGamepad }

// ScanResult is one enumeration pass over dir.
type ScanResult struct {
	Pads    []DeviceInfo
	Denied  []string
	Skipped int
}

// Scan always finds zero pads off Linux.
func Scan(dir string) (ScanResult, error) { return ScanResult{}, nil }

// OpenDevice is unavailable off Linux.
func OpenDevice(path string) (*Device, error) { return nil, ErrNoGamepad }

// OpenFirstGamepad is unavailable off Linux.
func OpenFirstGamepad(dir string) (*Device, error) { return nil, ErrNoGamepad }

// watchInputDir is unavailable off Linux: the caller keeps its periodic
// rescan fallback. Mirrors device.go.
func watchInputDir(dir string, stop <-chan struct{}, onEvent func()) error {
	return errors.New("gamepad: inotify unavailable off Linux")
}
