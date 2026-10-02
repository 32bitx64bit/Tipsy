// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/gamepad"
)

// These tests pin the live controller switch: the GUI launches Roblox in the
// same process that owns Settings, so the persisted gamepad.enabled value must
// reach a running (and a relaunched) session without a process restart. None
// of them calls ResetGamepadInputPath between the cache fill and the rewrite:
// that call is exactly what production never makes.

type gamepadFakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *gamepadFakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *gamepadFakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// useGamepadFakeClock puts the live config on a manual clock so the stat
// interval is deterministic. selectGamepadConfigFile's cleanup restores the
// real clock.
func useGamepadFakeClock(t *testing.T) *gamepadFakeClock {
	t.Helper()
	clk := &gamepadFakeClock{t: time.Now()}
	gamepadLive.SetClock(clk.now)
	t.Cleanup(func() { gamepadLive.SetClock(nil) })
	return clk
}

// rewriteGamepadConfig replaces the isolated settings file in place (the
// XDG_CONFIG_HOME planted by selectGamepadConfigFile) with body.
func rewriteGamepadConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(config.Paths().ConfigFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	gamepadCfgOn  = `{"gamepad":{"enabled":true}}`
	gamepadCfgOff = `{"gamepad":{"enabled":false}}`
)

func gamepadRecKindCount(kind int) int {
	n := 0
	for _, e := range gamepadRecSnapshot() {
		if e.kind == kind {
			n++
		}
	}
	return n
}

func gamepadPumpState() (running, armed bool, dir string) {
	gamepadPump.mu.Lock()
	defer gamepadPump.mu.Unlock()
	return gamepadPump.running, gamepadPump.armed, gamepadPump.dir
}

func gamepadWithheld() bool {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	return gamepadState.withheld != nil
}

func gamepadAnnounced() bool {
	gamepadState.mu.Lock()
	defer gamepadState.mu.Unlock()
	return gamepadState.announced
}

// frameWithButtons is xboxFrame plus extra pressed Android keycodes, so a
// frame that follows xboxFrame produces a fresh edge on the wire.
func frameWithButtons(extra ...int) gamepad.AndroidFrame {
	f := xboxFrame()
	for _, k := range extra {
		f.Buttons[k] = true
	}
	return f
}

// assertWithdrawn checks the unplug-shaped sequence: only UP buttons, only
// zeroed axes, and exactly one disconnect as the last call.
func assertWithdrawn(t *testing.T, wantUps map[int]bool) {
	t.Helper()
	rec := gamepadRecSnapshot()
	if len(rec) == 0 || rec[len(rec)-1].kind != gamepadRecDisconnect {
		t.Fatalf("withdraw tail = %+v, want a disconnect event last", rec)
	}
	if n := gamepadRecKindCount(gamepadRecDisconnect); n != 1 {
		t.Fatalf("withdraw emitted %d disconnects, want exactly 1", n)
	}
	ups := map[int]bool{}
	sawAxisZero := false
	for _, e := range rec[:len(rec)-1] {
		switch e.kind {
		case gamepadRecButton:
			if e.ints[2] != 0 {
				t.Fatalf("withdraw emitted a non-UP button %+v", e)
			}
			ups[e.ints[1]] = true
		case gamepadRecAxis:
			if e.floats[0] != 0 || e.floats[1] != 0 || e.floats[2] != 0 {
				t.Fatalf("withdraw emitted a nonzero axis %+v", e)
			}
			sawAxisZero = true
		default:
			t.Fatalf("withdraw emitted unexpected call kind %d", e.kind)
		}
	}
	for k := range wantUps {
		if !ups[k] {
			t.Fatalf("withdraw missed UP for held key %d (ups=%v)", k, ups)
		}
	}
	if !sawAxisZero {
		t.Fatal("withdraw missed zeroed axes")
	}
}

// TestGamepadLiveSwitchOffWithdrawsRunningPad is the reported bug end to end:
// the settings cache is populated with enabled=true, the file is rewritten to
// enabled=false (no ResetGamepadInputPath), and the running session's pad goes
// away: held buttons released, axes zeroed, one disconnect, then silence.
func TestGamepadLiveSwitchOffWithdrawsRunningPad(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	clk := useGamepadFakeClock(t)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)
	if !gamepadFileEnabled() {
		t.Fatal("precondition: cache populated with enabled=true")
	}
	handleGamepadFrame(xboxFrame())
	if testDirectGamepadRecCount() == 0 {
		t.Fatal("precondition: frames flow while the switch is on")
	}

	rewriteGamepadConfig(t, gamepadCfgOff)

	// Inside the stat interval the file is not touched: bounded staleness is
	// the price of no per-frame syscalls.
	testDirectGamepadRecReset()
	handleGamepadFrame(frameWithButtons(97))
	if gamepadRecKindCount(gamepadRecButton) != 1 || gamepadRecKindCount(gamepadRecDisconnect) != 0 {
		t.Fatalf("inside the interval the cached switch must still apply, got %+v", gamepadRecSnapshot())
	}

	clk.advance(gamepad.DefaultLiveConfigInterval + time.Millisecond)
	testDirectGamepadRecReset()
	before := RobloxDirectGamepadStats()
	handleGamepadFrame(frameWithButtons(97))
	assertWithdrawn(t, map[int]bool{96: true, 21: true, 104: true, 97: true})
	if RobloxDirectGamepadStats().DisconnectDelivered != before.DisconnectDelivered+1 {
		t.Fatal("withdraw must count exactly one disconnect delivery")
	}
	if gamepadAnnounced() {
		t.Fatal("pad must no longer be announced after the switch went off")
	}

	// Further frames, however much the pad moves, never reach the engine and
	// never repeat the disconnect.
	testDirectGamepadRecReset()
	dropped := RobloxDirectGamepadStats().Dropped
	for i := 0; i < 5; i++ {
		clk.advance(gamepad.DefaultLiveConfigInterval + time.Millisecond)
		handleGamepadFrame(frameWithButtons(97, 100))
	}
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("frames after withdraw emitted %d engine calls, want 0", n)
	}
	if RobloxDirectGamepadStats().Dropped != dropped+5 {
		t.Fatal("frames after withdraw must count as dropped")
	}
	if GamepadDisconnected(1) {
		t.Fatal("a withdrawn pad has nothing left to disconnect")
	}
}

// TestGamepadLiveSwitchOnReannouncesWithoutRestart is the re-enable path: the
// withdrawn pad is announced again (capability replay + connect) on the next
// frame once the switch is back on, and held buttons arrive as fresh DOWNs.
func TestGamepadLiveSwitchOnReannouncesWithoutRestart(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	clk := useGamepadFakeClock(t)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)
	handleGamepadFrame(xboxFrame())

	rewriteGamepadConfig(t, gamepadCfgOff)
	clk.advance(time.Second)
	handleGamepadFrame(xboxFrame())
	if gamepadAnnounced() || !gamepadWithheld() {
		t.Fatal("precondition: pad withdrawn and remembered as withheld")
	}

	rewriteGamepadConfig(t, gamepadCfgOn)
	clk.advance(time.Second)
	testDirectGamepadRecReset()
	before := RobloxDirectGamepadStats()
	handleGamepadFrame(xboxFrame())

	rec := gamepadRecSnapshot()
	if len(rec) == 0 || rec[0].kind == gamepadRecButton || rec[0].kind == gamepadRecAxis {
		t.Fatalf("re-announce must replay capabilities before any input, got %+v", rec)
	}
	if n := gamepadRecKindCount(gamepadRecConnect); n != 1 {
		t.Fatalf("re-enable emitted %d connect events, want 1", n)
	}
	if RobloxDirectGamepadStats().ConnectDelivered != before.ConnectDelivered+1 {
		t.Fatal("re-enable must count one connect delivery")
	}
	downs := map[int]bool{}
	for _, e := range rec {
		if e.kind == gamepadRecButton && e.ints[2] == 1 {
			downs[e.ints[1]] = true
		}
	}
	for _, k := range []int{96, 21, 104} {
		if !downs[k] {
			t.Fatalf("re-enabled frame missed a fresh DOWN for held key %d (downs=%v)", k, downs)
		}
	}
	if gamepadWithheld() || !gamepadAnnounced() {
		t.Fatal("pad must be announced and no longer withheld")
	}
	testDirectGamepadRecReset()
	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("rest frame after re-announce emitted %d calls, want 0", n)
	}
}

// TestGamepadLiveStaleCachePumpRefusal proves every launch reads the persisted
// switch fresh: the cache is filled with enabled=true, the file is rewritten to
// enabled=false with no ResetGamepadInputPath, and the next launch's pump start
// is refused (and the reverse).
func TestGamepadLiveStaleCachePumpRefusal(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	if !gamepadFileEnabled() {
		t.Fatal("precondition: cache populated with enabled=true")
	}
	rewriteGamepadConfig(t, gamepadCfgOff)
	if StartRobloxDirectGamepadPumpDir(t.TempDir()) {
		StopRobloxDirectGamepadPump()
		t.Fatal("relaunch must refuse the pump after the switch went off")
	}
	if running, _, _ := gamepadPumpState(); running {
		t.Fatal("refused start must leave no pump running")
	}

	rewriteGamepadConfig(t, gamepadCfgOn)
	if !StartRobloxDirectGamepadPumpDir(t.TempDir()) {
		t.Fatal("relaunch must start the pump after the switch went back on")
	}
	StopRobloxDirectGamepadPump()
}

// TestGamepadLiveFocusGainReconcilesIdlePad proves the switch also applies to a
// pad that is not moving: regaining window focus (how a user comes back from the
// Settings window) withdraws it, and turning it back on re-announces it, with
// no pad input and no waiting for the stat interval.
func TestGamepadLiveFocusGainReconcilesIdlePad(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	useGamepadFakeClock(t) // clock never advances: only the focus reload can see the edit
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)
	handleGamepadFrame(xboxFrame())

	gamepadNoteFocus(false)
	rewriteGamepadConfig(t, gamepadCfgOff)
	testDirectGamepadRecReset()
	gamepadNoteFocus(true)
	// Focus loss already released every held button; the withdraw adds the
	// disconnect (and any zeroed axes are already zero).
	if n := gamepadRecKindCount(gamepadRecDisconnect); n != 1 {
		t.Fatalf("focus-gain with the switch off emitted %d disconnects, want 1 (%+v)", n, gamepadRecSnapshot())
	}
	if gamepadAnnounced() {
		t.Fatal("idle pad must be withdrawn on focus gain")
	}

	rewriteGamepadConfig(t, gamepadCfgOn)
	testDirectGamepadRecReset()
	gamepadNoteFocus(true)
	if n := gamepadRecKindCount(gamepadRecConnect); n != 1 {
		t.Fatalf("focus-gain with the switch on emitted %d connects, want 1", n)
	}
	if !gamepadAnnounced() {
		t.Fatal("idle pad must be re-announced on focus gain")
	}
	testDirectGamepadRecReset()
	gamepadNoteFocus(true)
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("repeated focus-gain emitted %d calls, want 0 (idempotent)", n)
	}
}

// TestGamepadLiveHeldButtonReleasedOnFocusGainWithdraw proves a button held at
// the moment the switch goes off is released to the engine even though the pad
// sends no further event.
func TestGamepadLiveHeldButtonReleasedOnFocusGainWithdraw(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	useGamepadFakeClock(t)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)
	handleGamepadFrame(xboxFrame())

	rewriteGamepadConfig(t, gamepadCfgOff)
	testDirectGamepadRecReset()
	gamepadNoteFocus(true) // focus never left: still focused, buttons still held
	assertWithdrawn(t, map[int]bool{96: true, 21: true, 104: true})
}

// TestGamepadLiveEnableMidGameStartsPump covers a launch with the switch off:
// nothing opens /dev/input, the session stays armed, and turning the switch on
// while the game runs starts the pump when the window regains focus. Turning
// it back off parks the pump; a stopped session never restarts one.
func TestGamepadLiveEnableMidGameStartsPump(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOff, nil)
	useGamepadFakeClock(t)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	dir := t.TempDir()
	if StartRobloxDirectGamepadPumpDir(dir) {
		StopRobloxDirectGamepadPump()
		t.Fatal("launch with the switch off must not start the pump")
	}
	if running, armed, got := gamepadPumpState(); running || !armed || got != dir {
		t.Fatalf("pump state running=%v armed=%v dir=%q, want not running, armed, %q", running, armed, got, dir)
	}
	gamepadNoteFocus(true)
	if running, _, _ := gamepadPumpState(); running {
		t.Fatal("focus gain with the switch still off must not start the pump")
	}

	rewriteGamepadConfig(t, gamepadCfgOn)
	gamepadNoteFocus(true)
	t.Cleanup(StopRobloxDirectGamepadPump)
	if running, _, _ := gamepadPumpState(); !running {
		t.Fatal("focus gain after switching on must start the pump")
	}

	rewriteGamepadConfig(t, gamepadCfgOff)
	gamepadNoteFocus(true)
	if running, armed, _ := gamepadPumpState(); running || !armed {
		t.Fatalf("switching off must park the pump but keep the session armed (running=%v armed=%v)", running, armed)
	}

	rewriteGamepadConfig(t, gamepadCfgOn)
	gamepadNoteFocus(true)
	if running, _, _ := gamepadPumpState(); !running {
		t.Fatal("switching back on must restart the pump")
	}

	StopRobloxDirectGamepadPump()
	gamepadNoteFocus(true)
	if running, armed, _ := gamepadPumpState(); running || armed {
		t.Fatal("an explicitly stopped session must never restart its pump")
	}
}

// TestGamepadLiveKillSwitchBlocksPumpRestart keeps the env kill-switch above
// the file: TIPSY_GAMEPAD=0 means focus gain never starts a pump.
func TestGamepadLiveKillSwitchBlocksPumpRestart(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, map[string]string{"TIPSY_GAMEPAD": "0"})
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	if StartRobloxDirectGamepadPumpDir(t.TempDir()) {
		StopRobloxDirectGamepadPump()
		t.Fatal("kill-switch launch must not start the pump")
	}
	gamepadNoteFocus(true)
	if running, _, _ := gamepadPumpState(); running {
		StopRobloxDirectGamepadPump()
		t.Fatal("focus gain must not start a pump under the kill-switch")
	}
}

// TestGamepadLiveNoRestartWithoutTarget proves the pump is never revived into
// a session whose engine target is gone.
func TestGamepadLiveNoRestartWithoutTarget(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOff, nil)
	useGamepadFakeClock(t)
	ClearRobloxDirectGamepadTarget()
	if StartRobloxDirectGamepadPumpDir(t.TempDir()) {
		StopRobloxDirectGamepadPump()
		t.Fatal("precondition: launch with the switch off must not start the pump")
	}
	t.Cleanup(StopRobloxDirectGamepadPump)
	rewriteGamepadConfig(t, gamepadCfgOn)
	gamepadNoteFocus(true)
	if running, _, _ := gamepadPumpState(); running {
		t.Fatal("focus gain must not restart a pump with no live engine target")
	}
}

// TestGamepadLiveHotplugWhileOffIsWithheld proves a pad plugged in while the
// switch is off never reaches the engine, an unplug forgets it (no ghost on
// re-enable), and a replug while off appears only once the switch is back on.
func TestGamepadLiveHotplugWhileOffIsWithheld(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	clk := useGamepadFakeClock(t)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)

	rewriteGamepadConfig(t, gamepadCfgOff)
	gamepadNoteFocus(true) // withdraw
	if !gamepadWithheld() {
		t.Fatal("precondition: withdrawn pad is remembered")
	}
	if GamepadDisconnected(1) {
		t.Fatal("unplug of an already-withdrawn pad has nothing to deliver")
	}
	if gamepadWithheld() {
		t.Fatal("unplug must forget the withheld pad")
	}
	rewriteGamepadConfig(t, gamepadCfgOn)
	testDirectGamepadRecReset()
	gamepadNoteFocus(true)
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("re-enable with no pad plugged emitted %d calls: ghost pad", n)
	}

	rewriteGamepadConfig(t, gamepadCfgOff)
	clk.advance(time.Second)
	keys, motions := xboxPadCaps()
	if GamepadConnected(1, 3, keys, motions) {
		t.Fatal("replug while the switch is off must be withheld")
	}
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("withheld replug emitted %d calls, want 0", n)
	}
	rewriteGamepadConfig(t, gamepadCfgOn)
	gamepadNoteFocus(true)
	if n := gamepadRecKindCount(gamepadRecConnect); n != 1 {
		t.Fatalf("re-enable emitted %d connects for the replugged pad, want 1", n)
	}
}

// TestGamepadLiveRelaunchStartsWithFreshDelivery proves a second launch in the
// same process (the GUI case) does not inherit the first engine instance's
// announced pad: its connect event must reach the new instance.
func TestGamepadLiveRelaunchStartsWithFreshDelivery(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)
	handleGamepadFrame(xboxFrame()) // pad announced with buttons held

	// New engine instance wired into the same process, as a relaunch does.
	wireRecordingDirectGamepadTarget(t, 0x9abc, 0xdef0)
	keys, motions := xboxPadCaps()
	if !GamepadConnected(1, 3, keys, motions) {
		t.Fatal("relaunch connect failed")
	}
	if n := gamepadRecKindCount(gamepadRecConnect); n != 1 {
		t.Fatalf("relaunched engine saw %d connect events, want 1 (stale announced pad swallowed it)", n)
	}
	testDirectGamepadRecReset()
	handleGamepadFrame(xboxFrame())
	if gamepadRecKindCount(gamepadRecButton) == 0 {
		t.Fatal("held buttons must arrive as fresh DOWNs on the relaunched engine")
	}
}

// TestGamepadLiveCalibrationFollowsFileUntilEnvWins pins that deadzone/layout
// edits also reach a running session, and that env still beats the file.
func TestGamepadLiveCalibrationFollowsFileUntilEnvWins(t *testing.T) {
	selectGamepadConfigFile(t, `{"gamepad":{"deadzone":0.1}}`, nil)
	clk := useGamepadFakeClock(t)
	if got := gamepadCalibration().Deadzone; got != 0.1 {
		t.Fatalf("deadzone = %v, want 0.1", got)
	}
	rewriteGamepadConfig(t, `{"gamepad":{"deadzone":0.3,"faceButtonLayout":"switch"}}`)
	clk.advance(time.Second)
	cfg := gamepadCalibration()
	if cfg.Deadzone != 0.3 || cfg.FaceButtonLayout != gamepad.FaceButtonLayoutSwitch {
		t.Fatalf("live calibration = %+v, want deadzone 0.3 and the switch layout", cfg)
	}

	selectGamepadConfigFile(t, `{"gamepad":{"deadzone":0.1}}`, map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.2"})
	clk = useGamepadFakeClock(t)
	rewriteGamepadConfig(t, `{"gamepad":{"deadzone":0.4}}`)
	clk.advance(time.Second)
	if got := gamepadCalibration().Deadzone; got != 0.2 {
		t.Fatalf("env deadzone must keep winning over the live file, got %v", got)
	}
}

// TestGamepadLiveCorruptEditNeverReenables proves a broken settings edit keeps
// the last good value instead of silently flipping a disabled pad back on.
func TestGamepadLiveCorruptEditNeverReenables(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOff, nil)
	clk := useGamepadFakeClock(t)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	if gamepadFileEnabled() {
		t.Fatal("precondition: disabled")
	}
	rewriteGamepadConfig(t, `{"gamepad":`)
	clk.advance(time.Second)
	if gamepadFileEnabled() {
		t.Fatal("a corrupt settings edit must keep the last good (disabled) value")
	}
	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("frame under a corrupt edit emitted %d calls, want 0", n)
	}
}

// TestGamepadLiveSwitchChurnWithRunningPump races focus-gain reconciles
// (pump stop/start) against file rewrites; it is a lifecycle/race-detector
// exercise and must end with the pump running after the last enable.
func TestGamepadLiveSwitchChurnWithRunningPump(t *testing.T) {
	selectGamepadConfigFile(t, gamepadCfgOn, nil)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	if !StartRobloxDirectGamepadPumpDir(t.TempDir()) {
		t.Fatal("pump did not start")
	}
	t.Cleanup(StopRobloxDirectGamepadPump)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // frames from another goroutine, like the pump's own callback
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				handleGamepadFrame(xboxFrame())
			}
		}
	}()
	for i := 0; i < 25; i++ {
		rewriteGamepadConfig(t, gamepadCfgOff)
		gamepadNoteFocus(true)
		rewriteGamepadConfig(t, gamepadCfgOn)
		gamepadNoteFocus(true)
	}
	close(stop)
	wg.Wait()
	if running, armed, _ := gamepadPumpState(); !running || !armed {
		t.Fatalf("after the last enable the pump must run (running=%v armed=%v)", running, armed)
	}
}
