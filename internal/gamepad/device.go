// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package gamepad

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// InputNodeDir is the default evdev directory.
const InputNodeDir = "/dev/input"

// MaxGamepads caps simultaneous pads (lean single-pad build, 2026-09-12).
//
// Deterministic assignment (documented, tested): the lowest-sorted
// /dev/input/event* gamepad is player 1 (device id 1) and keeps it until it
// disconnects. A second simultaneous pad is ignored honestly (logged once,
// never delivered, never faked); unplug the first pad to use the second.
const MaxGamepads = 1

// Sentinel errors. Neither carries input content.
var (
	// ErrNoGamepad means enumeration found zero accessible gamepads.
	// This is the honest empty state, never a fake pad.
	ErrNoGamepad = errors.New("gamepad: no gamepad found (zero devices is the honest state)")
	// ErrPermissionDenied means nodes exist but cannot be opened (EACCES).
	ErrPermissionDenied = errors.New("gamepad: permission denied on /dev/input/event* (add user to input group and relogin; Flatpak needs --device=input)")
)

// PermissionHint is the actionable EACCES guidance (plan §§4/7).
const PermissionHint = "add user to input group and relogin; Flatpak needs --device=input"

// ErrorForErrno maps an open failure to its honest error: EACCES becomes
// ErrPermissionDenied with the actionable hint, anything else passes
// through. It never synthesizes a device.
func ErrorForErrno(path string, err error) error {
	if errors.Is(err, unix.EACCES) {
		return fmt.Errorf("%w: %s: %v", ErrPermissionDenied, path, err)
	}
	return fmt.Errorf("gamepad: open %s: %w", path, err)
}

// Device is an open evdev gamepad (O_RDONLY|O_NONBLOCK, never EVIOCGRAB).
type Device struct {
	path string
	fd   int
	info DeviceInfo
}

// Path returns the /dev/input/event* node.
func (d *Device) Path() string { return d.path }

// Info returns the connect-time snapshot (name/vendor/product/caps only).
func (d *Device) Info() DeviceInfo { return d.info }

// Close releases the node.
func (d *Device) Close() error {
	if d == nil || d.fd < 0 {
		return nil
	}
	fd := d.fd
	d.fd = -1
	return unix.Close(fd)
}

// ReadAvailable reads all pending input_event records without blocking.
// It returns (nil, nil) when no input is pending (EAGAIN).
func (d *Device) ReadAvailable() ([]InputEvent, error) {
	if d == nil || d.fd < 0 {
		return nil, errors.New("gamepad: device closed")
	}
	var out []InputEvent
	buf := make([]byte, EventSize*64)
	for {
		n, err := unix.Read(d.fd, buf)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				return out, nil
			}
			return out, err
		}
		if n <= 0 {
			return out, nil
		}
		// Keep only whole records; a short read tail is dropped
		// (input_event reads are record-atomic in practice).
		out = append(out, ParseInputEvents(buf[:n])...)
		if n < len(buf) {
			return out, nil
		}
	}
}

// ScanResult is one enumeration pass over dir.
type ScanResult struct {
	// Pads holds accessible gamepads, sorted by path.
	Pads []DeviceInfo
	// Denied holds node paths skipped with EACCES (content-free).
	Denied []string
	// Skipped holds non-gamepad nodes that opened fine (count only use).
	Skipped int
}

// Scan enumerates dir (default /dev/input) without grabbing anything.
// Nodes that fail EACCES are recorded in Denied, never fabricated.
// A missing dir or empty dir yields zero pads and no error.
func Scan(dir string) (ScanResult, error) {
	var res ScanResult
	if dir == "" {
		dir = InputNodeDir
	}
	nodes, err := filepath.Glob(filepath.Join(dir, "event*"))
	if err != nil {
		return res, err
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		base := filepath.Base(node)
		if !strings.HasPrefix(base, "event") {
			continue
		}
		rest := strings.TrimPrefix(base, "event")
		if rest == "" || !isDigits(rest) {
			continue
		}
		info, err := probe(node)
		if err != nil {
			if errors.Is(err, unix.EACCES) || errors.Is(err, os.ErrPermission) {
				res.Denied = append(res.Denied, node)
				continue
			}
			// Honest skip: vanished node, non-evdev, etc.
			continue
		}
		if !IsGamepadKeyBits(info.keyBits) {
			res.Skipped++
			continue
		}
		m, _ := ResolveMappingForDevice(info.info)
		info.info.Mapping = m
		res.Pads = append(res.Pads, info.info)
	}
	return res, nil
}

type probed struct {
	info    DeviceInfo
	keyBits []byte
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// OpenDevice opens one node O_RDONLY|O_NONBLOCK and snapshots its
// name/id/caps. It never issues EVIOCGRAB.
func OpenDevice(path string) (*Device, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrorForErrno(path, err)
	}
	d := &Device{path: path, fd: fd}
	info, keyBits, err := snapshot(fd, path)
	if err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.EACCES) {
			return nil, ErrorForErrno(path, err)
		}
		return nil, err
	}
	d.info = info
	_ = keyBits
	return d, nil
}

// OpenFirstGamepad opens the first accessible gamepad in dir (sorted by
// path). Zero pads → ErrNoGamepad. Nodes present but all EACCES →
// ErrPermissionDenied. Never a fake pad.
func OpenFirstGamepad(dir string) (*Device, error) {
	res, err := Scan(dir)
	if err != nil {
		return nil, err
	}
	if len(res.Pads) > 0 {
		return OpenDevice(res.Pads[0].Path)
	}
	if len(res.Denied) > 0 {
		return nil, fmt.Errorf("%w (%d node(s) denied, e.g. %s)", ErrPermissionDenied, len(res.Denied), res.Denied[0])
	}
	return nil, ErrNoGamepad
}

func probe(path string) (probed, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return probed{}, err
	}
	defer unix.Close(fd)
	info, keyBits, err := snapshot(fd, path)
	if err != nil {
		return probed{}, err
	}
	return probed{info: info, keyBits: keyBits}, nil
}

func snapshot(fd int, path string) (DeviceInfo, []byte, error) {
	name, err := ioctlGetName(fd)
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	id, err := ioctlGetID(fd)
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	evBits, err := ioctlGetBits(fd, 0, 4)
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	keyBits := []byte(nil)
	absBits := []byte(nil)
	if testBit(evBits, EvKey) {
		b, err := ioctlGetBits(fd, EvKey, (KeyMax+8)/8)
		if err != nil {
			return DeviceInfo{}, nil, err
		}
		keyBits = b
	}
	hasAbs := make(map[uint16]bool)
	abs := make(map[uint16]AbsInfo)
	if testBit(evBits, EvAbs) {
		b, err := ioctlGetBits(fd, EvAbs, (AbsMax+8)/8)
		if err != nil {
			return DeviceInfo{}, nil, err
		}
		absBits = b
		for c := uint16(0); c <= AbsMax; c++ {
			if testBit(absBits, c) {
				hasAbs[c] = true
				ai, err := ioctlGetAbs(fd, c)
				if err != nil {
					continue
				}
				abs[c] = ai
			}
		}
		_ = absBits
	}
	hasKey := make(map[uint16]bool)
	for c := uint16(0); c <= KeyMax; c++ {
		if testBit(keyBits, c) {
			hasKey[c] = true
		}
	}
	return DeviceInfo{Path: path, Name: name, ID: id, Abs: abs, HasKey: hasKey, HasAbs: hasAbs}, keyBits, nil
}

// ioctl numbers: _IOC(_IOC_READ,'E',nr,len).
func iorReq(nr uint, length uint) uintptr {
	const iocRead = 2
	return uintptr(iocRead<<30 | length<<16 | uint('E')<<8 | nr)
}

func ioctlGetName(fd int) (string, error) {
	buf := make([]byte, 256)
	req := iorReq(0x06, uint(len(buf)))
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return "", errno
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

func ioctlGetID(fd int) (DeviceID, error) {
	var raw [4]uint16
	req := iorReq(0x02, 8)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&raw[0])))
	if errno != 0 {
		return DeviceID{}, errno
	}
	return DeviceID{BusType: raw[0], Vendor: raw[1], Product: raw[2], Version: raw[3]}, nil
}

func ioctlGetBits(fd int, ev uint, length int) ([]byte, error) {
	buf := make([]byte, length)
	req := iorReq(0x20+ev, uint(length))
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return nil, errno
	}
	return buf, nil
}

func ioctlGetAbs(fd int, code uint16) (AbsInfo, error) {
	var raw [6]int32
	req := iorReq(0x40+uint(code), 24)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(&raw[0])))
	if errno != 0 {
		return AbsInfo{}, errno
	}
	return AbsInfo{Value: raw[0], Minimum: raw[1], Maximum: raw[2], Fuzz: raw[3], Flat: raw[4], Resolution: raw[5]}, nil
}

// watchInputDir installs an inotify watch on dir and calls onEvent (off the
// watch goroutine, debounced per burst) on creates, deletes, moves and
// attribute changes. It returns an error when the watch cannot be installed
// (missing dir, inotify unavailable); the caller keeps its periodic rescan
// fallback in that case. Close stop to end the watch goroutine. The callback
// must be quick and never block: it triggers Manager.Rescan.
//
// Lives here (not hotplug.go) only because inotify needs x/sys/unix Linux
// symbols while hotplug.go stays portable; the !linux twin in
// device_stub.go reports honestly unavailable.
func watchInputDir(dir string, stop <-chan struct{}, onEvent func()) error {
	if dir == "" {
		dir = InputNodeDir
	}
	if onEvent == nil {
		return errors.New("gamepad: nil watch callback")
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return err
	}
	const mask = uint32(unix.IN_CREATE | unix.IN_DELETE | unix.IN_MOVED_FROM |
		unix.IN_MOVED_TO | unix.IN_ATTRIB | unix.IN_DELETE_SELF |
		unix.IN_MOVE_SELF | unix.IN_IGNORED)
	if _, err := unix.InotifyAddWatch(fd, dir, mask); err != nil {
		_ = unix.Close(fd)
		return err
	}
	go watchInputLoop(fd, stop, onEvent)
	return nil
}

func watchInputLoop(fd int, stop <-chan struct{}, onEvent func()) {
	defer unix.Close(fd)
	buf := make([]byte, 4096)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		n, err := unix.Read(fd, buf)
		if err != nil || n <= 0 {
			continue
		}
		// Burst debounce: one directory change (create + attrib + move)
		// raises several events; drain the queue, then fire once.
		deadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) {
			m, rerr := unix.Read(fd, buf)
			if rerr != nil || m <= 0 {
				break
			}
		}
		onEvent()
	}
}
