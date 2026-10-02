// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultLiveConfigInterval is the minimum spacing between settings-file
// stat checks on the frame path. A change is picked up within roughly this
// long after the next consumer call; there is no timer and no goroutine.
const DefaultLiveConfigInterval = 250 * time.Millisecond

// KillSwitchOff reports the TIPSY_GAMEPAD=0|off|false|no kill-switch. Unset or
// any other value leaves the switch to the persisted setting.
func KillSwitchOff(lookup func(string) (string, bool)) bool {
	if lookup == nil {
		return false
	}
	v, ok := lookup("TIPSY_GAMEPAD")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "off", "false", "no":
		return true
	}
	return false
}

// LiveSnapshot is one consistent view of the effective gamepad settings: the
// persisted section overlaid with TIPSY_GAMEPAD_DEADZONE (env wins), plus the
// TIPSY_GAMEPAD kill-switch. Comparable so a refresh can tell "unchanged".
type LiveSnapshot struct {
	Config     GamepadConfig
	KillSwitch bool
}

// Enabled reports whether the engine may see a pad: the kill-switch and the
// persisted gamepad.enabled switch must both allow it.
func (s LiveSnapshot) Enabled() bool { return !s.KillSwitch && s.Config.Enabled }

// LiveConfig serves the effective settings to a long-lived process without
// pinning them for the process lifetime. The GUI launches Roblox in the same
// process that owns the Settings page, so a once-per-process cache never sees
// a later toggle. Get re-checks the settings file at most once per interval
// (os.Stat; the file is re-read only when its identity, size or mtime changed)
// and Reload forces a fresh read. No goroutines, no timers.
//
// A file that fails to parse after a good load keeps the last good section, so
// a half-written or hand-broken edit can never silently re-enable a pad the
// user switched off. The first load falls back to defaults so a bad file
// never blocks launch (the reported error is the honest signal).
type LiveConfig struct {
	// OnError receives content-free load errors, once per distinct file
	// change. Set before first use.
	OnError func(error)

	path   func() string
	lookup func(string) (string, bool)
	clock  atomic.Pointer[liveClock]

	interval atomic.Int64
	next     atomic.Int64 // ns since base before which Get serves cur unchanged
	cur      atomic.Pointer[LiveSnapshot]

	mu       sync.Mutex
	stamp    fileStamp
	seen     bool
	file     GamepadConfig
	haveFile bool
}

// liveClock pairs the time source with its zero point so elapsed time stays
// monotonic (time.Now carries a monotonic reading) and swappable atomically.
type liveClock struct {
	now  func() time.Time
	base time.Time
}

type fileStamp struct {
	path  string
	state uint8 // 0 absent, 1 present, 2 stat error
	info  os.FileInfo
}

func (a fileStamp) same(b fileStamp) bool {
	if a.path != b.path || a.state != b.state {
		return false
	}
	if a.state != 1 {
		return true
	}
	return os.SameFile(a.info, b.info) && a.info.Size() == b.info.Size() &&
		a.info.ModTime().Equal(b.info.ModTime())
}

// NewLiveConfig returns a LiveConfig over path() (re-resolved on every check,
// so an XDG change is noticed). A nil lookup selects os.LookupEnv.
func NewLiveConfig(path func() string, lookup func(string) (string, bool)) *LiveConfig {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	c := &LiveConfig{path: path, lookup: lookup}
	c.clock.Store(&liveClock{now: time.Now, base: time.Now()})
	c.interval.Store(int64(DefaultLiveConfigInterval))
	c.file = DefaultGamepadConfig()
	return c
}

// SetInterval changes the minimum stat spacing. Zero checks on every Get
// (tests); negative is treated as zero.
func (c *LiveConfig) SetInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.interval.Store(int64(d))
}

// SetClock replaces the clock behind the stat interval (nil restores time.Now)
// and drops the cached snapshot. Test seam.
func (c *LiveConfig) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	c.clock.Store(&liveClock{now: now, base: now()})
	c.cur.Store(nil)
	c.next.Store(0)
}

// Reset drops every cached value so the next Get reads the file and the
// environment from scratch.
func (c *LiveConfig) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur.Store(nil)
	c.next.Store(0)
	c.stamp = fileStamp{}
	c.seen = false
	c.file = DefaultGamepadConfig()
	c.haveFile = false
}

func (c *LiveConfig) elapsed() int64 {
	k := c.clock.Load()
	return int64(k.now().Sub(k.base))
}

// Get returns the current snapshot. The fast path is two atomic loads and a
// clock read; the settings file is only touched when the interval has passed.
func (c *LiveConfig) Get() LiveSnapshot {
	if s := c.cur.Load(); s != nil && c.elapsed() < c.next.Load() {
		return *s
	}
	return c.refresh(false)
}

// Reload forces a fresh read of the settings file and environment,
// bypassing the interval. A launch calls it so it never trusts an earlier
// check; a window-focus transition calls it for the same reason.
func (c *LiveConfig) Reload() LiveSnapshot { return c.refresh(true) }

func (c *LiveConfig) refresh(force bool) LiveSnapshot {
	c.mu.Lock()
	if !force {
		if s := c.cur.Load(); s != nil && c.elapsed() < c.next.Load() {
			c.mu.Unlock()
			return *s
		}
	}
	snap, loadErr := c.refreshLocked(force)
	c.mu.Unlock()
	if loadErr != nil && c.OnError != nil {
		c.OnError(loadErr)
	}
	return snap
}

func (c *LiveConfig) refreshLocked(force bool) (LiveSnapshot, error) {
	path := ""
	if c.path != nil {
		path = c.path()
	}
	st := fileStamp{path: path}
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		st.state, st.info = 1, info
	case os.IsNotExist(statErr):
		st.state = 0
	default:
		st.state = 2
	}

	var loadErr error
	if force || !c.seen || !c.stamp.same(st) {
		cfg, err := LoadGamepadConfigFile(path)
		switch {
		case err == nil:
			c.file, c.haveFile = cfg, true
		case c.haveFile:
			// Keep the last good section; the stamp below suppresses a
			// re-report until the file changes again.
			loadErr = err
		default:
			c.file, c.haveFile = DefaultGamepadConfig(), true
			loadErr = err
		}
		c.stamp, c.seen = st, true
	}

	snap := LiveSnapshot{Config: c.file.WithEnv(c.lookup), KillSwitch: KillSwitchOff(c.lookup)}
	if cur := c.cur.Load(); cur == nil || *cur != snap {
		c.cur.Store(&snap)
	} else {
		snap = *cur
	}
	c.next.Store(c.elapsed() + c.interval.Load())
	return snap, loadErr
}
