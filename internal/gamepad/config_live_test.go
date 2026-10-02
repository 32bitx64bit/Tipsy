// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type liveTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *liveTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *liveTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newLiveFixture builds a LiveConfig over a settings file in t.TempDir() (never
// the real config), a fake clock, and an env map.
func newLiveFixture(t *testing.T, body string) (*LiveConfig, string, *liveTestClock, map[string]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{}
	c := NewLiveConfig(func() string { return path }, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	clk := &liveTestClock{t: time.Now()}
	c.SetClock(clk.now)
	return c, path, clk, env
}

func TestKillSwitchOff(t *testing.T) {
	for v, want := range map[string]bool{
		"0": true, "off": true, "OFF": true, "false": true, "no": true, " No ": true,
		"1": false, "on": false, "": false, "yes": false, "bogus": false,
	} {
		got := KillSwitchOff(func(string) (string, bool) { return v, true })
		if got != want {
			t.Errorf("KillSwitchOff(%q) = %v, want %v", v, got, want)
		}
	}
	if KillSwitchOff(func(string) (string, bool) { return "", false }) {
		t.Error("unset must not trip the kill-switch")
	}
	if KillSwitchOff(nil) {
		t.Error("nil lookup must not trip the kill-switch")
	}
}

// TestLiveConfigServesCachedWithinInterval proves the frame-path cost model:
// inside the interval Get never touches the file, so a rewrite is not seen
// until the interval passes.
func TestLiveConfigServesCachedWithinInterval(t *testing.T) {
	c, path, clk, _ := newLiveFixture(t, `{"gamepad":{"enabled":true}}`)
	if !c.Get().Enabled() {
		t.Fatal("initial snapshot should be enabled")
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clk.advance(DefaultLiveConfigInterval - time.Millisecond)
	if !c.Get().Enabled() {
		t.Fatal("rewrite inside the interval must not be observed (no stat)")
	}
	clk.advance(2 * time.Millisecond)
	if c.Get().Enabled() {
		t.Fatal("rewrite after the interval must be observed")
	}
}

// TestLiveConfigReloadBypassesInterval proves a launch/focus Reload never
// trusts the interval.
func TestLiveConfigReloadBypassesInterval(t *testing.T) {
	c, path, _, _ := newLiveFixture(t, `{"gamepad":{"enabled":true}}`)
	_ = c.Get()
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.Get().Enabled() != true {
		t.Fatal("precondition: Get inside interval is cached")
	}
	if c.Reload().Enabled() {
		t.Fatal("Reload must see the rewritten file immediately")
	}
	if c.Get().Enabled() {
		t.Fatal("Get after Reload must serve the reloaded snapshot")
	}
}

// TestLiveConfigDetectsAtomicReplaceSameSizeSameMtime proves a rename-based
// write (the production writer) is noticed even when size and mtime match:
// the inode changed.
func TestLiveConfigDetectsAtomicReplaceSameSizeSameMtime(t *testing.T) {
	c, path, clk, _ := newLiveFixture(t, `{"gamepad":{"deadzone":0.1}}`)
	if got := c.Get().Config.Deadzone; got != 0.1 {
		t.Fatalf("deadzone = %v, want 0.1", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(`{"gamepad":{"deadzone":0.3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	if got := c.Get().Config.Deadzone; got != 0.3 {
		t.Fatalf("deadzone after atomic replace = %v, want 0.3", got)
	}
}

func TestLiveConfigMissingFileIsDefaultsAndAppearanceIsSeen(t *testing.T) {
	c, path, clk, _ := newLiveFixture(t, "")
	snap := c.Get()
	if !snap.Enabled() || snap.Config != DefaultGamepadConfig() {
		t.Fatalf("missing file must be defaults, got %+v", snap)
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	if c.Get().Enabled() {
		t.Fatal("a settings file that appears later must be honored")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	if !c.Get().Enabled() {
		t.Fatal("a removed settings file must fall back to defaults")
	}
}

// TestLiveConfigEnvStillWins proves file < env survives live reloads: the env
// deadzone masks later file edits, and the kill-switch is re-read per refresh.
func TestLiveConfigEnvStillWins(t *testing.T) {
	c, path, clk, env := newLiveFixture(t, `{"gamepad":{"deadzone":0.1}}`)
	env["TIPSY_GAMEPAD_DEADZONE"] = "0.2"
	if got := c.Reload().Config.Deadzone; got != 0.2 {
		t.Fatalf("env deadzone must beat file, got %v", got)
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":{"deadzone":0.4}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	if got := c.Get().Config.Deadzone; got != 0.2 {
		t.Fatalf("env deadzone must still win after a file edit, got %v", got)
	}
	delete(env, "TIPSY_GAMEPAD_DEADZONE")
	clk.advance(time.Second)
	if got := c.Get().Config.Deadzone; got != 0.4 {
		t.Fatalf("with env gone the live file value must show, got %v", got)
	}
	env["TIPSY_GAMEPAD"] = "off"
	clk.advance(time.Second)
	s := c.Get()
	if !s.KillSwitch || s.Enabled() {
		t.Fatalf("kill-switch must disable regardless of the file, got %+v", s)
	}
}

// TestLiveConfigBadEditKeepsLastGood proves a corrupt edit can never flip a
// disabled pad back on, reports once per change, and recovers on a good edit.
func TestLiveConfigBadEditKeepsLastGood(t *testing.T) {
	c, path, clk, _ := newLiveFixture(t, `{"gamepad":{"enabled":false}}`)
	var errs int
	c.OnError = func(error) { errs++ }
	if c.Get().Enabled() {
		t.Fatal("precondition: disabled")
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		clk.advance(time.Second)
		if c.Get().Enabled() {
			t.Fatal("corrupt edit must keep the last good (disabled) section")
		}
	}
	if errs != 1 {
		t.Fatalf("OnError fired %d times, want once per file change", errs)
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clk.advance(time.Second)
	if !c.Get().Enabled() {
		t.Fatal("a later good edit must be honored")
	}
}

// TestLiveConfigFirstLoadBadFileFallsBackToDefaults pins "a bad file never
// blocks launch".
func TestLiveConfigFirstLoadBadFileFallsBackToDefaults(t *testing.T) {
	c, _, _, _ := newLiveFixture(t, `{"gamepad":`)
	var got error
	c.OnError = func(err error) { got = err }
	snap := c.Get()
	if !snap.Enabled() || snap.Config != DefaultGamepadConfig() {
		t.Fatalf("first-load error must yield defaults, got %+v", snap)
	}
	if got == nil || errors.Is(got, os.ErrNotExist) {
		t.Fatalf("first-load error must be reported, got %v", got)
	}
}

func TestLiveConfigResetForgetsEverything(t *testing.T) {
	c, path, _, _ := newLiveFixture(t, `{"gamepad":{"enabled":false}}`)
	if c.Get().Enabled() {
		t.Fatal("precondition: disabled")
	}
	if err := os.WriteFile(path, []byte(`{"gamepad":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Reset()
	if !c.Get().Enabled() {
		t.Fatal("Reset must force a fresh read")
	}
}

// TestLiveConfigConcurrentGet is a race-detector exercise of the fast and slow
// paths together.
func TestLiveConfigConcurrentGet(t *testing.T) {
	c, path, _, _ := newLiveFixture(t, `{"gamepad":{"enabled":true}}`)
	c.SetInterval(0)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = c.Get()
				if j%50 == 0 {
					_ = c.Reload()
				}
			}
		}()
	}
	for j := 0; j < 20; j++ {
		body := `{"gamepad":{"enabled":true}}`
		if j%2 == 0 {
			body = `{"gamepad":{"enabled":false}}`
		}
		_ = os.WriteFile(path, []byte(body), 0o600)
	}
	wg.Wait()
}

// TestLiveConfigFastPathIsAllocFree pins the per-frame cost: inside the
// interval Get is atomic loads and a clock read, no allocation and no file I/O.
func TestLiveConfigFastPathIsAllocFree(t *testing.T) {
	c, _, _, _ := newLiveFixture(t, `{"gamepad":{"enabled":true}}`)
	_ = c.Get()
	if n := testing.AllocsPerRun(1000, func() { _ = c.Get() }); n != 0 {
		t.Fatalf("Get fast path allocates %v per call, want 0", n)
	}
}
