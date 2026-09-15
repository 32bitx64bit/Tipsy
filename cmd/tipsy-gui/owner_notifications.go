// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"sync"
	"sync/atomic"

	qt "github.com/mappu/miqt/qt6"
	"golang.org/x/sys/unix"
)

// ownerThreadNotifier turns a background-model state edge into one queued Qt
// event. Widgets are never touched by the worker: the QSocketNotifier runs the
// callback on the QObject owner's event loop instead. One unread byte is also
// the coalescing token, so a burst of worker updates cannot create a busy GUI
// wake loop.
type ownerThreadNotifier struct {
	mu      sync.Mutex
	readFD  int
	writeFD int
	queued  bool
	closed  bool

	notifier *qt.QSocketNotifier
	dispatch func()
	metrics  *guiRuntimeMetrics
}

func newOwnerThreadNotifier(parent *qt.QObject, dispatch func(), metrics *guiRuntimeMetrics) (*ownerThreadNotifier, error) {
	if parent == nil || dispatch == nil {
		return nil, errors.New("owner-thread notifier requires a Qt owner and dispatch")
	}
	fds := []int{-1, -1}
	if err := unix.Pipe2(fds, unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		return nil, err
	}
	n := &ownerThreadNotifier{
		readFD:   fds[0],
		writeFD:  fds[1],
		dispatch: dispatch,
		metrics:  metrics,
	}
	n.notifier = qt.NewQSocketNotifier4(uintptr(n.readFD), qt.QSocketNotifier__Read, parent)
	if !n.notifier.IsValid() {
		_ = unix.Close(n.readFD)
		_ = unix.Close(n.writeFD)
		n.notifier.Delete()
		return nil, errors.New("Qt could not watch the owner-thread notification pipe")
	}
	n.notifier.OnActivated(func(qt.QSocketDescriptor, qt.QSocketNotifier__Type) { n.drain() })
	return n, nil
}

func (n *ownerThreadNotifier) notify() {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	if n.queued {
		if n.metrics != nil {
			n.metrics.ownerCoalesced.Add(1)
		}
		return
	}
	// Keep the lock through the nonblocking one-byte write. Closing the pipe
	// after releasing it could otherwise race an in-flight notifier write.
	for {
		written, err := unix.Write(n.writeFD, []byte{1})
		if err == nil && written == 1 {
			n.queued = true
			if n.metrics != nil {
				n.metrics.ownerQueued.Add(1)
			}
			return
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			// An unread byte is already sufficient to dispatch the latest model
			// state. Keep the coalescing token even if the kernel pipe is full.
			n.queued = true
			if n.metrics != nil {
				n.metrics.ownerCoalesced.Add(1)
			}
		}
		return
	}
}

func (n *ownerThreadNotifier) drain() {
	if n == nil {
		return
	}
	var byteBuf [32]byte
	terminal := false
	for {
		read, err := unix.Read(n.readFD, byteBuf[:])
		if read > 0 {
			continue
		}
		if err == nil {
			// A readable pipe with no bytes is EOF. Do not spin or dispatch a
			// stale callback after its writer has gone away.
			terminal = true
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			break
		}
		// A descriptor failure is terminal too. Concurrent normal close sets
		// closed below and therefore remains a no-op here.
		terminal = true
		break
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.queued = false
	n.mu.Unlock()
	if terminal {
		n.close()
		return
	}
	if n.metrics != nil {
		n.metrics.ownerDispatched.Add(1)
	}
	n.dispatch()
}

func (n *ownerThreadNotifier) close() {
	if n == nil {
		return
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	n.mu.Unlock()
	if n.notifier != nil {
		n.notifier.SetEnabled(false)
		n.notifier.Delete()
	}
	_ = unix.Close(n.readFD)
	_ = unix.Close(n.writeFD)
}

// guiRuntimeMetrics is intentionally content-free. It is a test seam for
// cadence and avoided presentation work, not client or gameplay telemetry.
type guiRuntimeMetrics struct {
	ownerQueued, ownerCoalesced, ownerDispatched atomic.Uint64
	widgetWrites, widgetSkipped                  atomic.Uint64
	appearanceStarts, appearanceStops            atomic.Uint64
	settingsRefreshes, settingsRefreshSkipped    atomic.Uint64
	optionalCalls, optionalDeferred              atomic.Uint64
}

type guiRuntimeMetricsSnapshot struct {
	OwnerQueued, OwnerCoalesced, OwnerDispatched uint64
	WidgetWrites, WidgetSkipped                  uint64
	AppearanceStarts, AppearanceStops            uint64
	SettingsRefreshes, SettingsRefreshSkipped    uint64
	OptionalCalls, OptionalDeferred              uint64
}

func (m *guiRuntimeMetrics) snapshot() guiRuntimeMetricsSnapshot {
	if m == nil {
		return guiRuntimeMetricsSnapshot{}
	}
	return guiRuntimeMetricsSnapshot{
		OwnerQueued:            m.ownerQueued.Load(),
		OwnerCoalesced:         m.ownerCoalesced.Load(),
		OwnerDispatched:        m.ownerDispatched.Load(),
		WidgetWrites:           m.widgetWrites.Load(),
		WidgetSkipped:          m.widgetSkipped.Load(),
		AppearanceStarts:       m.appearanceStarts.Load(),
		AppearanceStops:        m.appearanceStops.Load(),
		SettingsRefreshes:      m.settingsRefreshes.Load(),
		SettingsRefreshSkipped: m.settingsRefreshSkipped.Load(),
		OptionalCalls:          m.optionalCalls.Load(),
		OptionalDeferred:       m.optionalDeferred.Load(),
	}
}
