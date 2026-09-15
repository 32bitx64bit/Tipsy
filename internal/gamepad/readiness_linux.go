// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package gamepad

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ControllerReadinessSnapshot is a content-free, opt-in accounting boundary
// for the readiness pump. It counts poll sources, rescans, and SYN_REPORT
// frame handoffs; it never contains a device name, event code, axis value, or
// button value.
//
// ReadyToFrame* is the elapsed host work from observing an evdev-ready fd to
// returning from OnFrame for a completed SYN_REPORT. It is not physical input
// latency, kernel event timestamp latency, or engine-consumption latency:
// evdev timestamps are intentionally discarded and the engine has no delivery
// acknowledgement at this boundary.
type ControllerReadinessSnapshot struct {
	InitialRescans  uint64
	HotplugRescans  uint64
	RecoveryRescans uint64
	EvdevReady      uint64
	InotifyReady    uint64
	ShutdownWake    uint64
	FrameHandoffs   uint64

	ReadyToFrameMinNS uint64
	ReadyToFrameMaxNS uint64
	ReadyToFrameSumNS uint64
}

// ControllerReadinessDiagnostics enables the snapshot accounting above. A nil
// pointer is the normal production path and adds neither timing reads nor
// counter locking. It is for instrumented captures only; clean visual input
// acceptance must run with Diagnostics nil.
type ControllerReadinessDiagnostics struct {
	mu sync.Mutex
	s  ControllerReadinessSnapshot
}

// Snapshot returns a content-free copy of the recorded readiness aggregates.
func (d *ControllerReadinessDiagnostics) Snapshot() ControllerReadinessSnapshot {
	if d == nil {
		return ControllerReadinessSnapshot{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.s
}

func (d *ControllerReadinessDiagnostics) rescan(kind readinessRescanKind) {
	if d == nil {
		return
	}
	d.mu.Lock()
	switch kind {
	case readinessInitial:
		d.s.InitialRescans++
	case readinessHotplug:
		d.s.HotplugRescans++
	case readinessRecovery:
		d.s.RecoveryRescans++
	}
	d.mu.Unlock()
}

func (d *ControllerReadinessDiagnostics) evdevReady() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.s.EvdevReady++
	d.mu.Unlock()
}

func (d *ControllerReadinessDiagnostics) inotifyReady() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.s.InotifyReady++
	d.mu.Unlock()
}

func (d *ControllerReadinessDiagnostics) shutdownWake() {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.s.ShutdownWake++
	d.mu.Unlock()
}

func (d *ControllerReadinessDiagnostics) frameHandoff(elapsed time.Duration) {
	if d == nil {
		return
	}
	ns := uint64(elapsed)
	d.mu.Lock()
	d.s.FrameHandoffs++
	d.s.ReadyToFrameSumNS += ns
	if d.s.ReadyToFrameMinNS == 0 || ns < d.s.ReadyToFrameMinNS {
		d.s.ReadyToFrameMinNS = ns
	}
	if ns > d.s.ReadyToFrameMaxNS {
		d.s.ReadyToFrameMaxNS = ns
	}
	d.mu.Unlock()
}

type readinessRescanKind uint8

const (
	readinessInitial readinessRescanKind = iota
	readinessHotplug
	readinessRecovery
)

const (
	defaultControllerRecoveryRescan = time.Second
	maxControllerRecoveryBackoff    = 30 * time.Second
)

// readinessRecoveryDeadline arms a single poll timeout after a failed
// reconciliation. Each consecutive failure backs off to a bounded delay; a
// successful reconciliation disarms it immediately so an otherwise healthy
// inotify watch returns to an infinite poll.
//
// This is deliberately separate from an unavailable-watch retry. A working
// watch does not prove that a Scan/Open race has resolved, and a failed Scan
// or Open must therefore retain its own recovery deadline.
type readinessRecoveryDeadline struct {
	enabled  bool
	initial  time.Duration
	maximum  time.Duration
	delay    time.Duration
	deadline time.Time
}

func newReadinessRecoveryDeadline(interval time.Duration, enabled bool) readinessRecoveryDeadline {
	maximum := maxControllerRecoveryBackoff
	if interval > maximum {
		maximum = interval
	}
	return readinessRecoveryDeadline{
		enabled: enabled,
		initial: interval,
		maximum: maximum,
	}
}

func (r *readinessRecoveryDeadline) failed(now time.Time) {
	if !r.enabled {
		return
	}
	if r.delay == 0 {
		r.delay = r.initial
	} else if r.delay < r.maximum {
		if r.delay > r.maximum/2 {
			r.delay = r.maximum
		} else {
			r.delay *= 2
		}
	}
	r.deadline = now.Add(r.delay)
}

func (r *readinessRecoveryDeadline) succeeded() {
	r.delay = 0
	r.deadline = time.Time{}
}

func (r readinessRecoveryDeadline) due(now time.Time) bool {
	return !r.deadline.IsZero() && !r.deadline.After(now)
}

func (r readinessRecoveryDeadline) pollTimeout(now time.Time) int {
	if r.deadline.IsZero() {
		return -1
	}
	remaining := r.deadline.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int((remaining + time.Millisecond - 1) / time.Millisecond)
}

func pollTimeoutAt(now, deadline time.Time) int {
	if deadline.IsZero() {
		return -1
	}
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int((remaining + time.Millisecond - 1) / time.Millisecond)
}

func earlierPollTimeout(current, candidate int) int {
	if candidate < 0 || (current >= 0 && current <= candidate) {
		return current
	}
	return candidate
}

// ReadyPump owns one Manager's device reads, close/reopen transitions, and
// inotify/recovery reconciliations for the lifetime of Run. No other
// goroutine may call that Manager's Rescan, Close, or read its current Device
// while Run is active. This is what prevents a read racing a close after an
// unplug or event-node reuse.
//
// Run waits in poll(2) on the current evdev fd, the /dev/input inotify fd, and
// an eventfd written on shutdown. It has no idle ticker. A one-shot timeout is
// armed only while an inotify watch cannot be installed or a reconciliation
// has failed; a successful reconciliation disarms it immediately.
type ReadyPump struct {
	Manager *Manager

	// OnFrame receives completed SYN_REPORT frames in source order. It runs on
	// the readiness-owner goroutine and must return promptly.
	OnFrame func(Pad, *Frame)
	// OnRescanError receives content-free scan/open failures. It also runs on
	// the readiness-owner goroutine and must return promptly.
	OnRescanError func(error)
	// Diagnostics is nil for clean acceptance. When non-nil it records only
	// the aggregate defined by ControllerReadinessSnapshot.
	Diagnostics *ControllerReadinessDiagnostics
	// RecoveryRescan is the initial one-shot recovery delay for unavailable
	// inotify watches and failed Scan/Open reconciliations. Consecutive failed
	// reconciliations back off to a bounded delay; any success disarms them.
	// Zero selects one second; a negative value disables timed recovery
	// (shutdown still wakes immediately).
	RecoveryRescan time.Duration
}

// NewReadyPump returns the readiness owner for manager. Callers configure the
// Manager's connect/disconnect callbacks before Run, then leave all manager
// I/O ownership to this pump until Run returns.
func NewReadyPump(manager *Manager) *ReadyPump {
	return &ReadyPump{Manager: manager}
}

func (p *ReadyPump) recoveryInterval() (time.Duration, bool) {
	if p.RecoveryRescan < 0 {
		return 0, false
	}
	if p.RecoveryRescan == 0 {
		return defaultControllerRecoveryRescan, true
	}
	return p.RecoveryRescan, true
}

// Run serves frames until stop closes. It owns manager reads and device
// lifecycle until it returns, then closes the final device once. stop is
// bridged to an eventfd so shutdown interrupts an otherwise infinite poll.
func (p *ReadyPump) Run(stop <-chan struct{}) error {
	if p == nil || p.Manager == nil {
		return errors.New("gamepad: ReadyPump requires a Manager")
	}
	wakeFD, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		return fmt.Errorf("gamepad: shutdown eventfd: %w", err)
	}
	defer unix.Close(wakeFD)
	finished := make(chan struct{})
	defer close(finished)
	if stop != nil {
		go relayShutdownWake(stop, finished, wakeFD)
	}
	defer p.Manager.Close()

	interval, recoveryEnabled := p.recoveryInterval()
	recovery := newReadinessRecoveryDeadline(interval, recoveryEnabled)

	watch, watchErr := openInputWatch(p.Manager.Dir)
	if watchErr != nil {
		p.reportRescanError(watchErr)
	}
	defer func() {
		if watch != nil {
			_ = watch.Close()
		}
	}()
	// A failed opening attempt gets one immediate retry below. Later attempts
	// are paced by watchRetry; its zero value means the immediate retry is
	// pending, not a poll timeout.
	watchRetryPending := watch == nil
	var watchRetry time.Time
	rescan := func(kind readinessRescanKind) {
		if err := p.rescan(kind); err != nil {
			recovery.failed(time.Now())
			return
		}
		recovery.succeeded()
	}
	rescan(readinessInitial)

	for {
		now := time.Now()
		if watch == nil && watchRetryPending && !watchRetry.After(now) {
			retryingWatch := !watchRetry.IsZero()
			if next, err := openInputWatch(p.Manager.Dir); err == nil {
				watch = next
				watchRetryPending = false
				watchRetry = time.Time{}
				// The watch is installed before this scan, closing the recovery
				// race without an always-running rescan.
				rescan(readinessRecovery)
			} else {
				p.reportRescanError(err)
				if retryingWatch {
					// Keep the legacy unavailable-watch reconciliation alive,
					// but only on its bounded retry deadline.
					rescan(readinessRecovery)
				}
				if recoveryEnabled {
					watchRetry = now.Add(interval)
				} else {
					watchRetryPending = false
				}
			}
		}

		pollFDs := make([]unix.PollFd, 0, 3)
		pollFDs = append(pollFDs, unix.PollFd{Fd: int32(wakeFD), Events: unix.POLLIN})
		watchIndex := -1
		if watch != nil {
			watchIndex = len(pollFDs)
			pollFDs = append(pollFDs, unix.PollFd{Fd: int32(watch.fd), Events: unix.POLLIN | unix.POLLERR | unix.POLLHUP})
		}
		var dev *Device
		if _, current, _, ok := p.Manager.Slot(singlePadID); ok && current != nil && current.fd >= 0 {
			dev = current
			pollFDs = append(pollFDs, unix.PollFd{Fd: int32(dev.fd), Events: unix.POLLIN | unix.POLLERR | unix.POLLHUP})
		}
		deviceIndex := len(pollFDs) - 1
		if dev == nil {
			deviceIndex = -1
		}

		timeout := recovery.pollTimeout(now)
		if watch == nil && watchRetryPending {
			timeout = earlierPollTimeout(timeout, pollTimeoutAt(now, watchRetry))
		}
		n, pollErr := unix.Poll(pollFDs, timeout)
		if pollErr != nil {
			if errors.Is(pollErr, unix.EINTR) {
				continue
			}
			return fmt.Errorf("gamepad: readiness poll: %w", pollErr)
		}
		if n == 0 {
			now = time.Now()
			// Let the top of the loop recreate a due watch first so its
			// reconciliation is protected from the watcher gap. If that is
			// not due, this was a Scan/Open recovery deadline.
			if watch == nil && watchRetryPending && !watchRetry.After(now) {
				continue
			}
			if recovery.due(now) {
				rescan(readinessRecovery)
			}
			continue
		}
		if pollFDs[0].Revents != 0 {
			drainEventfd(wakeFD)
			p.Diagnostics.shutdownWake()
			return nil
		}
		if watchIndex >= 0 && pollFDs[watchIndex].Revents != 0 {
			p.Diagnostics.inotifyReady()
			changed, invalid, err := watch.Drain()
			if changed {
				rescan(readinessHotplug)
			}
			if err != nil || invalid {
				if err != nil {
					p.reportRescanError(err)
				}
				_ = watch.Close()
				watch = nil
				watchRetryPending = true
				watchRetry = time.Time{}
			}
		}
		if deviceIndex >= 0 && pollFDs[deviceIndex].Revents != 0 {
			// A hotplug rescan above may have closed/replaced this fd. Never
			// read a stale descriptor.
			if _, current, _, ok := p.Manager.Slot(singlePadID); ok && current == dev {
				if rescanned, err := p.consumeReadyDevice(dev, pollFDs[deviceIndex].Revents); rescanned {
					if err != nil {
						recovery.failed(time.Now())
					} else {
						recovery.succeeded()
					}
				}
			}
		}
	}
}

func relayShutdownWake(stop <-chan struct{}, finished <-chan struct{}, wakeFD int) {
	select {
	case <-stop:
		var one [8]byte
		binary.NativeEndian.PutUint64(one[:], 1)
		_, _ = unix.Write(wakeFD, one[:])
	case <-finished:
	}
}

func drainEventfd(fd int) {
	var buf [8]byte
	for {
		_, err := unix.Read(fd, buf[:])
		if err != nil {
			return
		}
	}
}

func (p *ReadyPump) reportRescanError(err error) {
	if err != nil && p.OnRescanError != nil {
		p.OnRescanError(err)
	}
}

func (p *ReadyPump) rescan(kind readinessRescanKind) error {
	p.Diagnostics.rescan(kind)
	if _, err := p.Manager.Rescan(); err != nil {
		p.reportRescanError(err)
		return err
	}
	return nil
}

func (p *ReadyPump) consumeReadyDevice(dev *Device, revents int16) (rescanned bool, rescanErr error) {
	if revents&unix.POLLIN != 0 {
		p.Diagnostics.evdevReady()
	}
	started := time.Time{}
	if p.Diagnostics != nil {
		started = time.Now()
	}
	pad, current, reader, ok := p.Manager.Slot(singlePadID)
	if !ok || current != dev || reader == nil {
		return false, nil
	}
	events, err := dev.ReadAvailable()
	if err == nil {
		for _, event := range events {
			if frame := reader.Feed(event); frame != nil {
				if p.OnFrame != nil {
					p.OnFrame(pad, frame)
				}
				if !started.IsZero() {
					p.Diagnostics.frameHandoff(time.Since(started))
				}
			}
		}
	}
	if err != nil || revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
		// The same readiness owner drops, closes, synthesizes disconnect, and
		// rescans. No separate watch goroutine can race the read above.
		p.Manager.dropCurrent()
		return true, p.rescan(readinessHotplug)
	}
	return false, nil
}

// ControllerIdleReadinessFixture is a bounded, content-free owner seam for
// internal/perf. It creates a private empty watched directory, runs a clean
// readiness pump for duration, and returns only aggregate readiness counters.
// The fixture never opens /dev/input, reads input data, or reports a physical
// input latency. Process CPU/RSS/context-switch metadata must come from the
// existing direct-test-binary perf runner, and its context switches must not
// be relabeled as controller wakeups.
func ControllerIdleReadinessFixture(duration time.Duration) (ControllerReadinessSnapshot, error) {
	if duration <= 0 || duration > 5*time.Second {
		return ControllerReadinessSnapshot{}, fmt.Errorf("gamepad: idle readiness duration must be in (0, 5s], got %s", duration)
	}
	dir, err := os.MkdirTemp("", "tipsy-gamepad-idle-")
	if err != nil {
		return ControllerReadinessSnapshot{}, err
	}
	defer os.RemoveAll(dir)
	diagnostics := &ControllerReadinessDiagnostics{}
	pump := NewReadyPump(NewManager(dir, nil))
	pump.Diagnostics = diagnostics
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- pump.Run(stop) }()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	<-timer.C
	close(stop)
	if err := <-done; err != nil {
		return ControllerReadinessSnapshot{}, err
	}
	return diagnostics.Snapshot(), nil
}
