// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestManagerNoPadRescanIsEmpty(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	changed, err := m.Rescan()
	if err != nil {
		t.Fatalf("empty dir rescan: %v", err)
	}
	if changed {
		t.Fatal("empty dir must not report a change")
	}
	if m.Current() != nil {
		t.Fatal("zero devices must mean nil current, never a fake pad")
	}
	if m.DeviceID() != 0 {
		t.Fatalf("zero devices must mean id 0, got %d", m.DeviceID())
	}
	if len(m.DeviceIDs()) != 0 || len(m.Pads()) != 0 {
		t.Fatal("zero devices must mean empty pad lists")
	}
	if m.DisconnectSnapshot() != nil {
		t.Fatal("no pad must yield no disconnect frame")
	}
}

func TestManagerDisconnectSynthesis(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	// Inject live-equivalent state without hardware (same package).
	abs := stickAbs()
	r := NewReader(abs)
	r.Feed(InputEvent{Type: EvKey, Code: BtnSouth, Value: 1})
	r.Feed(InputEvent{Type: EvAbs, Code: AbsX, Value: 16383})
	r.Feed(InputEvent{Type: EvSyn, Code: SynReport})
	m.slot = &padSlot{
		reader:  r,
		mapping: Mapping{Name: "xpad", RightX: AbsRX, RightY: AbsRY, TriggerL: AbsZ, TriggerR: AbsRZ},
		info:    DeviceInfo{Path: "/dev/input/event9", Name: "virtual pad", Abs: abs},
	}

	snap := m.DisconnectSnapshot()
	if snap == nil {
		t.Fatal("active pad must yield a disconnect frame")
	}
	if !snap.Disconnect {
		t.Fatal("disconnect frame must be marked")
	}
	if len(snap.Buttons) != 0 {
		t.Fatalf("disconnect must release all buttons (UP synthesis), got %v", snap.Buttons)
	}
	for a, v := range snap.Axes {
		if v != 0 {
			t.Fatalf("disconnect axis %d must be zero, got %v", a, v)
		}
	}
	if len(snap.Axes) == 0 {
		t.Fatal("disconnect must zero axes in the same frame")
	}
	if m.DisconnectSnapshotFor(9) != nil {
		t.Fatal("unknown pad id must yield no snapshot (honest, never fake)")
	}
}

// virtualPadInfo builds a virtual Xbox-class pad snapshot (no hardware).
func virtualPadInfo(path, name string) DeviceInfo {
	has, infos := xpadCaps()
	keys := map[uint16]bool{
		BtnSouth: true, BtnEast: true, BtnWest: true, BtnNorth: true,
		BtnDpadUp: true, BtnDpadDown: true, BtnDpadLeft: true, BtnDpadRight: true,
		BtnTL: true, BtnTR: true, BtnTL2: true, BtnTR2: true,
		BtnThumbl: true, BtnThumbr: true, BtnSelect: true, BtnStart: true, BtnMode: true,
	}
	return DeviceInfo{
		Path: path, Name: name,
		ID:     DeviceID{BusType: 3, Vendor: 0x045e, Product: 0x028e},
		Abs:    infos,
		HasKey: keys, HasAbs: has,
	}
}

// TestManagerSecondPadHonestlyIgnored pins the lean single-pad contract:
// the lowest-sorted pad is player 1 and a second simultaneous pad is
// ignored (logged once, never delivered); unplugging the first promotes the
// second to player 1.
func TestManagerSecondPadHonestlyIgnored(t *testing.T) {
	present := map[string]string{
		"/dev/input/event2": "Second Pad",
		"/dev/input/event1": "First Pad",
	}
	var logs []string
	m := NewManager("/virtual-input-for-test", func(msg string) { logs = append(logs, msg) })
	m.ScanFn = func(dir string) (ScanResult, error) {
		var res ScanResult
		for path, name := range present {
			res.Pads = append(res.Pads, virtualPadInfo(path, name))
		}
		sort.Slice(res.Pads, func(i, j int) bool { return res.Pads[i].Path < res.Pads[j].Path })
		return res, nil
	}
	m.OpenFn = func(path string) (*Device, error) {
		return &Device{path: path, fd: -1, info: virtualPadInfo(path, present[path])}, nil
	}

	var connected []int
	m.OnConnect = func(info DeviceInfo, devID int) { connected = append(connected, devID) }
	if changed, err := m.Rescan(); err != nil || !changed {
		t.Fatalf("first rescan must connect: changed=%v err=%v", changed, err)
	}
	if got := m.DeviceIDs(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("single pad must be player 1, got %v", got)
	}
	if cur := m.Current(); cur == nil || cur.Path() != "/dev/input/event1" {
		t.Fatalf("lowest-sorted pad must win, got %+v", cur)
	}
	if len(logs) == 0 {
		t.Fatal("second pad must be logged as ignored")
	}
	nLogs := len(logs)
	if changed, err := m.Rescan(); err != nil || changed {
		t.Fatalf("steady rescan must be quiet: changed=%v err=%v", changed, err)
	}
	for _, line := range logs[nLogs:] {
		t.Fatalf("ignore line must log once per streak, got %q", line)
	}
	// Unplug the first pad: the second promotes to player 1.
	delete(present, "/dev/input/event1")
	var disconnected []int
	m.OnDisconnect = func(devID int) { disconnected = append(disconnected, devID) }
	if changed, err := m.Rescan(); err != nil || !changed {
		t.Fatalf("unplug rescan must change: changed=%v err=%v", changed, err)
	}
	if cur := m.Current(); cur == nil || cur.Path() != "/dev/input/event2" {
		t.Fatalf("second pad must promote to player 1, got %+v", cur)
	}
	if len(disconnected) != 1 || disconnected[0] != 1 {
		t.Fatalf("replacement must fire one disconnect for player 1, got %v", disconnected)
	}
	if len(connected) != 2 {
		t.Fatalf("replacement must fire a second connect, got %v", connected)
	}
}

func TestManagerRescanDeniedIsHonest(t *testing.T) {
	m := NewManager("/nonexistent-dir-for-gamepad-test", nil)
	changed, err := m.Rescan()
	if err != nil {
		t.Fatalf("missing dir must not error, got %v", err)
	}
	if changed || m.Current() != nil {
		t.Fatal("missing dir must stay empty")
	}
}

func TestManagerCloseIsSafeEmpty(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	if err := m.Close(); err != nil {
		t.Fatalf("close empty: %v", err)
	}
	_ = errors.Is
}

func TestManagerWatchFiresOnVirtualNodes(t *testing.T) {
	// Recorded inotify on a virtual dir: creating and deleting event*
	// files triggers rescans without hardware.
	dir := t.TempDir()
	calls := 0
	m := NewManager(dir, nil)
	m.ScanFn = func(d string) (ScanResult, error) {
		calls++
		return ScanResult{}, nil
	}
	stop := make(chan struct{})
	defer close(stop)
	if err := m.StartWatch(stop); err != nil {
		t.Skipf("inotify unavailable: %v", err)
	}
	before := calls
	if err := os.WriteFile(filepath.Join(dir, "event0"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for calls == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls == before {
		t.Fatal("creating event0 must trigger a watch rescan")
	}
	before = calls
	if err := os.Remove(filepath.Join(dir, "event0")); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for calls == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if calls == before {
		t.Fatal("deleting event0 must trigger a watch rescan")
	}
}

func TestManagerWatchMissingDirIsHonest(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	stop := make(chan struct{})
	defer close(stop)
	if err := m.StartWatch(stop); err == nil {
		t.Fatal("watch on a missing dir must fail honestly (periodic fallback stays)")
	}
}
