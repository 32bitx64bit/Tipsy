// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package gamepad

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestControllerIdleReadinessFixtureIsContentFreeAndBounded(t *testing.T) {
	snap, err := ControllerIdleReadinessFixture(75 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if snap.InitialRescans != 1 {
		t.Fatalf("initial rescans = %d, want 1", snap.InitialRescans)
	}
	if snap.HotplugRescans != 0 || snap.RecoveryRescans != 0 {
		t.Fatalf("healthy idle fixture must not rescan: %+v", snap)
	}
	if snap.EvdevReady != 0 || snap.InotifyReady != 0 || snap.FrameHandoffs != 0 {
		t.Fatalf("empty fixture must not report input readiness or frames: %+v", snap)
	}
	if snap.ShutdownWake != 1 {
		t.Fatalf("fixture shutdown wakes = %d, want 1", snap.ShutdownWake)
	}
	if snap.ReadyToFrameMinNS != 0 || snap.ReadyToFrameMaxNS != 0 || snap.ReadyToFrameSumNS != 0 {
		t.Fatalf("idle fixture must not claim input latency: %+v", snap)
	}
	t.Logf("controller idle readiness aggregate: %+v", snap)
	if _, err := ControllerIdleReadinessFixture(0); err == nil {
		t.Fatal("zero fixture duration must fail closed")
	}
}

func TestReadyPumpUsesInotifyWithoutHealthyIdleRescans(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil)
	var scans atomic.Uint64
	m.ScanFn = func(string) (ScanResult, error) {
		scans.Add(1)
		return ScanResult{}, nil
	}
	diag := &ControllerReadinessDiagnostics{}
	pump := NewReadyPump(m)
	pump.Diagnostics = diag
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- pump.Run(stop) }()
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		if err := <-done; err != nil {
			t.Errorf("ready pump: %v", err)
		}
	})

	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool { return s.InitialRescans == 1 })
	before := scans.Load()
	time.Sleep(100 * time.Millisecond)
	if got := scans.Load(); got != before {
		t.Fatalf("healthy inotify watch rescanned while idle: before=%d after=%d", before, got)
	}
	if err := os.WriteFile(filepath.Join(dir, "event0"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool {
		return s.InotifyReady >= 1 && s.HotplugRescans >= 1
	})
}

func TestReadyPumpRetainsRecoveryOnlyWhenWatchUnavailable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-input-dir")
	m := NewManager(missing, nil)
	m.ScanFn = func(string) (ScanResult, error) { return ScanResult{}, nil }
	diag := &ControllerReadinessDiagnostics{}
	pump := NewReadyPump(m)
	pump.Diagnostics = diag
	pump.RecoveryRescan = 20 * time.Millisecond
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- pump.Run(stop) }()
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		if err := <-done; err != nil {
			t.Errorf("ready pump: %v", err)
		}
	})

	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool {
		return s.InitialRescans == 1 && s.RecoveryRescans >= 1
	})
	snap := diag.Snapshot()
	if snap.InotifyReady != 0 {
		t.Fatalf("unavailable watch must not claim inotify readiness: %+v", snap)
	}
}

func TestReadinessRecoveryDeadlineBacksOffAndDisarms(t *testing.T) {
	now := time.Unix(100, 0)
	recovery := readinessRecoveryDeadline{
		enabled: true,
		initial: 10 * time.Millisecond,
		maximum: 40 * time.Millisecond,
	}
	recovery.failed(now)
	if got, want := recovery.deadline, now.Add(10*time.Millisecond); !got.Equal(want) {
		t.Fatalf("first recovery deadline = %s, want %s", got, want)
	}
	recovery.failed(recovery.deadline)
	if got, want := recovery.deadline, now.Add(30*time.Millisecond); !got.Equal(want) {
		t.Fatalf("second recovery deadline = %s, want %s", got, want)
	}
	recovery.failed(recovery.deadline)
	if got, want := recovery.deadline, now.Add(70*time.Millisecond); !got.Equal(want) {
		t.Fatalf("third recovery deadline = %s, want %s", got, want)
	}
	recovery.failed(recovery.deadline)
	if got, want := recovery.deadline, now.Add(110*time.Millisecond); !got.Equal(want) {
		t.Fatalf("capped recovery deadline = %s, want %s", got, want)
	}
	if !recovery.due(recovery.deadline) {
		t.Fatal("recovery deadline must be due at its deadline")
	}
	recovery.succeeded()
	if !recovery.deadline.IsZero() || recovery.delay != 0 || recovery.pollTimeout(now) != -1 {
		t.Fatalf("successful reconciliation must disarm recovery: %+v", recovery)
	}
}

func TestReadyPumpRecoversTransientScanFailureWithHealthyWatch(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil)
	var scans atomic.Uint64
	m.ScanFn = func(string) (ScanResult, error) {
		if scans.Add(1) == 1 {
			return ScanResult{}, errors.New("transient scan failure")
		}
		return ScanResult{}, nil
	}
	diag := &ControllerReadinessDiagnostics{}
	pump := NewReadyPump(m)
	pump.Diagnostics = diag
	pump.RecoveryRescan = 10 * time.Millisecond
	stop, done := runReadyPump(t, pump)
	t.Cleanup(func() { stopReadyPump(t, stop, done) })

	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool {
		return s.InitialRescans == 1 && s.RecoveryRescans == 1 && scans.Load() == 2
	})
	assertNoLaterRecoveryPoll(t, diag, scans.Load)
}

func TestReadyPumpRecoversTransientOpenFailureWithHealthyWatch(t *testing.T) {
	dir := t.TempDir()
	info := virtualPadInfo("/virtual/event0", "ready-recovery-pad")
	m := NewManager(dir, nil)
	var scans atomic.Uint64
	m.ScanFn = func(string) (ScanResult, error) {
		scans.Add(1)
		return ScanResult{Pads: []DeviceInfo{info}}, nil
	}
	var opens atomic.Uint64
	m.OpenFn = func(string) (*Device, error) {
		if opens.Add(1) == 1 {
			return nil, errors.New("transient open failure")
		}
		return &Device{path: info.Path, fd: -1, info: info}, nil
	}
	diag := &ControllerReadinessDiagnostics{}
	pump := NewReadyPump(m)
	pump.Diagnostics = diag
	pump.RecoveryRescan = 10 * time.Millisecond
	stop, done := runReadyPump(t, pump)
	t.Cleanup(func() { stopReadyPump(t, stop, done) })

	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool {
		return s.InitialRescans == 1 && s.RecoveryRescans == 1 &&
			scans.Load() == 2 && opens.Load() == 2 && m.Current() != nil
	})
	assertNoLaterRecoveryPoll(t, diag, scans.Load)
}

func TestReadyPumpKeepsSynReportOrderAndDisconnectsOnHUP(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	readFD, writeFD := fds[0], fds[1]
	t.Cleanup(func() {
		_ = unix.Close(readFD)
		_ = unix.Close(writeFD)
	})

	info := virtualPadInfo("/virtual/event0", "ready-fixture-pad")
	var present atomic.Bool
	present.Store(true)
	m := NewManager(t.TempDir(), nil)
	m.ScanFn = func(string) (ScanResult, error) {
		if !present.Load() {
			return ScanResult{}, nil
		}
		return ScanResult{Pads: []DeviceInfo{info}}, nil
	}
	m.OpenFn = func(string) (*Device, error) {
		return &Device{path: info.Path, fd: readFD, info: info}, nil
	}
	disconnected := make(chan int, 1)
	m.OnDisconnect = func(devID int) { disconnected <- devID }
	diag := &ControllerReadinessDiagnostics{}
	pump := NewReadyPump(m)
	pump.Diagnostics = diag
	frames := make(chan *Frame, 2)
	pump.OnFrame = func(_ Pad, frame *Frame) { frames <- frame }
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- pump.Run(stop) }()
	t.Cleanup(func() {
		select {
		case <-stop:
		default:
			close(stop)
		}
		if err := <-done; err != nil {
			t.Errorf("ready pump: %v", err)
		}
	})

	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool { return s.InitialRescans == 1 })
	stream := append(EncodeInputEvent(EvKey, BtnSouth, 1), EncodeInputEvent(EvSyn, SynReport, 0)...)
	if n, err := unix.Write(writeFD, stream); err != nil || n != len(stream) {
		t.Fatalf("write evdev fixture: n=%d err=%v", n, err)
	}
	select {
	case frame := <-frames:
		if !frame.Buttons[BtnSouth] {
			t.Fatalf("SYN_REPORT frame lost held button: %+v", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ready evdev fd did not deliver SYN_REPORT frame")
	}

	// The pipe HUP models a vanished readable node. ReadyPump alone drops and
	// closes it, invokes the normal disconnect release callback, then rescans.
	present.Store(false)
	if err := unix.Close(writeFD); err != nil {
		t.Fatal(err)
	}
	writeFD = -1
	select {
	case id := <-disconnected:
		if id != singlePadID {
			t.Fatalf("disconnect id = %d, want %d", id, singlePadID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HUP must synthesize the normal disconnect path")
	}
	awaitReadiness(t, diag, func(s ControllerReadinessSnapshot) bool {
		return s.EvdevReady >= 1 && s.FrameHandoffs == 1 &&
			s.ReadyToFrameMinNS > 0 && s.ReadyToFrameMaxNS >= s.ReadyToFrameMinNS
	})
}

func awaitReadiness(t *testing.T, diagnostics *ControllerReadinessDiagnostics, want func(ControllerReadinessSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if want(diagnostics.Snapshot()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("readiness condition was not observed")
}

func runReadyPump(t *testing.T, pump *ReadyPump) (chan struct{}, chan error) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- pump.Run(stop) }()
	return stop, done
}

func stopReadyPump(t *testing.T, stop chan struct{}, done chan error) {
	t.Helper()
	select {
	case <-stop:
	default:
		close(stop)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ready pump: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("ready pump did not stop")
	}
}

func assertNoLaterRecoveryPoll(t *testing.T, diagnostics *ControllerReadinessDiagnostics, scanCount func() uint64) {
	t.Helper()
	before := diagnostics.Snapshot()
	beforeScans := scanCount()
	time.Sleep(75 * time.Millisecond)
	after := diagnostics.Snapshot()
	if got := scanCount(); got != beforeScans {
		t.Fatalf("successful healthy-watch recovery kept rescanning: before=%d after=%d", beforeScans, got)
	}
	if after.RecoveryRescans != before.RecoveryRescans {
		t.Fatalf("successful healthy-watch recovery kept polling: before=%+v after=%+v", before, after)
	}
}
