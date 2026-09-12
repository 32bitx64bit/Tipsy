// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"errors"
	"sort"
	"sync"
)

// Manager is the lean single-pad hotplug owner (simplified 2026-09-12): one
// open pad with a stable device id, an inotify watch plus rescan helper, and
// connect/disconnect callbacks.
//
// Contract:
//   - Zero devices means empty: Pads() is empty, Current()==nil, no
//     synthesized pad.
//   - The lowest-sorted /dev/input/event* gamepad is player 1 (id 1) until
//     it disconnects. A second simultaneous pad is ignored honestly (logged
//     once, never delivered, never faked).
//   - Connect runs the E() capability replay + typed connect downstream;
//     disconnect synthesizes UP for all held buttons plus zeroed axes in the
//     same frame, so the engine never keeps a stuck button after unplug or
//     focus loss.
//   - A vanished fd is closed exactly once, a new node opens
//     O_RDONLY|O_NONBLOCK (never EVIOCGRAB), and a same-path device swap
//     (kernel eventX reuse) reopens the slot instead of serving stale caps.
//
// Deleted vs v1 (see gamepad-simplify-2026-09-12.md): players 2..4.
//
// Concurrency: the pump and watch goroutines both call Rescan; mu guards all
// slot state. Log/OnConnect/OnDisconnect fire without holding mu.
type Manager struct {
	// Dir is the evdev directory (default InputNodeDir).
	Dir string
	// Log receives content-free connect/disconnect lines. May be nil.
	Log func(msg string)
	// OnConnect fires after unlock for the newly opened pad. The pump
	// wires it to the E() replay + typed connect. May be nil.
	OnConnect func(info DeviceInfo, devID int)
	// OnDisconnect fires after unlock for the withdrawn pad. The pump
	// wires it to UP synthesis + disconnect. May be nil.
	OnDisconnect func(devID int)
	// ScanFn overrides Scan for tests (virtual nodes / recorded inotify
	// without hardware). Nil means Scan.
	ScanFn func(dir string) (ScanResult, error)
	// OpenFn overrides OpenDevice for tests (virtual nodes without
	// hardware). Nil means OpenDevice.
	OpenFn func(path string) (*Device, error)

	mu            sync.Mutex
	slot          *padSlot // the single open pad, nil when no pad
	ignoredLogged bool     // second-pad spam guard (log once per streak)
}

// padSlot is the open pad.
type padSlot struct {
	dev     *Device
	reader  *Reader
	mapping Mapping
	info    DeviceInfo
}

// Pad is a snapshot of the connected pad.
type Pad struct {
	DevID   int
	Path    string
	Info    DeviceInfo
	Mapping Mapping
}

// singlePadID is the only stable device id the lean build assigns.
const singlePadID = 1

// NewManager returns a single-pad manager over dir.
func NewManager(dir string, log func(string)) *Manager {
	if dir == "" {
		dir = InputNodeDir
	}
	return &Manager{Dir: dir, Log: log}
}

func (m *Manager) logf(msg string) {
	if m.Log != nil {
		m.Log(msg)
	}
}

func (m *Manager) scan() (ScanResult, error) {
	if m.ScanFn != nil {
		return m.ScanFn(m.Dir)
	}
	return Scan(m.Dir)
}

func (m *Manager) open(path string) (*Device, error) {
	if m.OpenFn != nil {
		return m.OpenFn(path)
	}
	return OpenDevice(path)
}

// Pads returns the connected pad (at most one), if any.
func (m *Manager) Pads() []Pad {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil {
		return nil
	}
	return []Pad{{DevID: singlePadID, Path: m.slot.info.Path, Info: m.slot.info, Mapping: m.slot.mapping}}
}

// DeviceIDs returns the connected stable id (empty when no pad).
func (m *Manager) DeviceIDs() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil {
		return nil
	}
	return []int{singlePadID}
}

// Current reports the open device, or nil when no pad is connected. Nil is
// the honest no-pad state, never a fake pad.
func (m *Manager) Current() *Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil {
		return nil
	}
	return m.slot.dev
}

// Mapping returns the pad's mapping (zero when no pad).
func (m *Manager) Mapping() Mapping {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil {
		return Mapping{}
	}
	return m.slot.mapping
}

// DeviceID returns the stable nonzero id (0 = none).
func (m *Manager) DeviceID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil {
		return 0
	}
	return singlePadID
}

// Reader returns the pad's stream reader (nil when no pad).
func (m *Manager) Reader() *Reader {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil {
		return nil
	}
	return m.slot.reader
}

// Slot returns the snapshot for one stable device id, or false when the pad
// is not connected. The pump uses it to feed frames.
func (m *Manager) Slot(devID int) (Pad, *Device, *Reader, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil || devID != singlePadID {
		return Pad{}, nil, nil, false
	}
	return Pad{DevID: singlePadID, Path: m.slot.info.Path, Info: m.slot.info, Mapping: m.slot.mapping},
		m.slot.dev, m.slot.reader, true
}

// Rescan reconciles the open handle with the directory: it opens the
// lowest-sorted gamepad when none is open, drops the handle whose node
// vanished, and reopens the slot when the same-path device identity changed
// (kernel eventX reuse). It returns true when the open set changed. EACCES
// never fabricates a pad; when no pad is open and nodes exist but all are
// denied it records the honest permission error.
func (m *Manager) Rescan() (bool, error) {
	res, err := m.scan()
	if err != nil {
		return false, err
	}
	sort.Slice(res.Pads, func(i, j int) bool { return res.Pads[i].Path < res.Pads[j].Path })

	m.mu.Lock()
	cur := m.slot
	var want *DeviceInfo
	if len(res.Pads) > 0 {
		want = &res.Pads[0]
	}
	if len(res.Pads) > 1 && !m.ignoredLogged {
		m.ignoredLogged = true
		m.mu.Unlock()
		m.logf("gamepad: ignoring second pad (single-pad lean build; unplug the first pad to use it)")
		m.mu.Lock()
	} else if len(res.Pads) <= 1 {
		m.ignoredLogged = false
	}

	switch {
	case want == nil:
		// No pad wanted: drop the open one, if any.
		if cur == nil {
			m.mu.Unlock()
			break
		}
		m.logf("gamepad: disconnect " + sanitizeName(cur.info.Name))
		_ = cur.dev.Close()
		m.slot = nil
		m.mu.Unlock()
		if m.OnDisconnect != nil {
			m.OnDisconnect(singlePadID)
		}
		return true, nil
	case cur != nil && cur.info.Path == want.Path &&
		cur.info.Name == want.Name && cur.info.ID == want.ID:
		// Same pad still there: nothing to do.
		m.mu.Unlock()
		return false, nil
	default:
		// New pad, vanished-then-replaced pad, or same-path device swap:
		// close the old handle (replug keeps player 1) and open below.
		if cur != nil {
			if cur.info.Path == want.Path {
				m.logf("gamepad: replug " + sanitizeName(want.Name))
			} else {
				m.logf("gamepad: disconnect " + sanitizeName(cur.info.Name))
			}
			_ = cur.dev.Close()
			m.slot = nil
		}
		dropped := cur != nil
		m.mu.Unlock()
		d, oerr := m.open(want.Path)
		if oerr != nil {
			// Honest skip: the node vanished or denied between Scan and
			// open. It reconciles on the next pass; never a fake pad.
			if dropped && m.OnDisconnect != nil {
				m.OnDisconnect(singlePadID)
			}
			return dropped, oerr
		}
		info := d.Info()
		mp, line := ResolveMappingForDevice(info)
		r := NewReader(info.Abs)
		r.SetMapping(mp)
		m.mu.Lock()
		// A concurrent Rescan may have filled the slot; never double-open.
		if m.slot != nil {
			m.mu.Unlock()
			_ = d.Close()
			return dropped, nil
		}
		m.slot = &padSlot{dev: d, reader: r, mapping: mp, info: info}
		m.mu.Unlock()
		m.logf(line)
		if dropped && m.OnDisconnect != nil {
			m.OnDisconnect(singlePadID)
		}
		if m.OnConnect != nil {
			m.OnConnect(info, singlePadID)
		}
		return true, nil
	}

	m.mu.Lock()
	empty := m.slot == nil
	m.mu.Unlock()
	if empty && len(res.Pads) == 0 && len(res.Denied) > 0 {
		return false, errors.Join(ErrPermissionDenied, errors.New(res.Denied[0]))
	}
	return false, nil
}

// StartWatch starts an inotify watch on Dir: directory creates, deletes,
// moves and attribute changes trigger an immediate Rescan (which fires
// OnConnect/OnDisconnect). It returns an error when the watch cannot be
// installed (missing dir, inotify unavailable); the caller keeps its
// periodic rescan fallback in that case. Close stop to end the watch
// goroutine. Idempotent per call; each call spawns one goroutine.
func (m *Manager) StartWatch(stop <-chan struct{}) error {
	return watchInputDir(m.Dir, stop, func() {
		_, _ = m.Rescan()
	})
}

// Close releases the open device, if any.
func (m *Manager) Close() error {
	m.mu.Lock()
	s := m.slot
	m.slot = nil
	m.ignoredLogged = false
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.dev.Close()
}

// DisconnectSnapshot synthesizes the disconnect frame for the pad's
// currently held state: UP for every held button plus zeroed axes in the
// same frame, so the engine never keeps a stuck button after unplug or focus
// loss. It returns nil when no pad is active.
func (m *Manager) DisconnectSnapshot() *AndroidFrame {
	return m.DisconnectSnapshotFor(singlePadID)
}

// DisconnectSnapshotFor synthesizes the same UP-plus-zeroed-axes frame for
// the single stable device id. It returns nil when that pad is connected
// under a different id or not at all.
func (m *Manager) DisconnectSnapshotFor(devID int) *AndroidFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot == nil || m.slot.reader == nil || devID != singlePadID {
		return nil
	}
	return disconnectSnapshotFor(m.slot.reader, singlePadID, m.slot.mapping)
}

func disconnectSnapshotFor(r *Reader, devID int, mapping Mapping) *AndroidFrame {
	held := r.Held()
	var axes []uint16
	for c := range r.raw {
		axes = append(axes, c)
	}
	raw := DisconnectFrame(held, axes)
	af := MapFrame(raw, devID, mapping, r.Abs)
	af.Disconnect = true
	return &af
}
