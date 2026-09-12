// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/gamepad"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// The tests drive the production direct-pad dispatch and frame paths;
// cgo is unsupported in test files, so the fake engine natives are the
// recording C functions exposed via test hooks in gamepad.go.

func selectGamepadPath(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TIPSY_GAMEPAD_PATH", value)
	t.Setenv("TIPSY_GAMEPAD", "")
	// Isolate the persisted gamepad section: the pump resolves the
	// effective config from the real settings file, which must never leak
	// into (or out of) unit tests.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ResetGamepadInputPath()
	t.Cleanup(ResetGamepadInputPath)
}

func selectGamepadEnabled(t *testing.T, value string) {
	t.Helper()
	t.Setenv("TIPSY_GAMEPAD", value)
	t.Setenv("TIPSY_GAMEPAD_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ResetGamepadInputPath()
	t.Cleanup(ResetGamepadInputPath)
}

func wireRecordingDirectGamepadTarget(t *testing.T, env, class uintptr) {
	t.Helper()
	testDirectGamepadRecReset()
	if !SetRobloxDirectGamepadTarget(env, class,
		testDirectGamepadRecFn(gamepadRecAxis),
		testDirectGamepadRecFn(gamepadRecButton),
		testDirectGamepadRecFn(gamepadRecConnect),
		testDirectGamepadRecFn(gamepadRecDisconnect),
		testDirectGamepadRecFn(gamepadRecSetKey),
		testDirectGamepadRecFn(gamepadRecSetMotion)) {
		t.Fatal("recording direct gamepad target did not wire")
	}
	t.Cleanup(ClearRobloxDirectGamepadTarget)
}

// recEntry describes one recorded gamepad native call: kind + int args
// (a..e per kind) + float args (axis events only).
type recEntry struct {
	kind   int
	ints   [5]int
	floats [3]float32
}

func gamepadRecSnapshot() []recEntry {
	n := testDirectGamepadRecCount()
	out := make([]recEntry, 0, n)
	for i := 0; i < n; i++ {
		var e recEntry
		e.kind = testDirectGamepadRecKind(i)
		for j := 0; j < 5; j++ {
			e.ints[j] = testDirectGamepadRecInt(i, j)
		}
		for j := 0; j < 3; j++ {
			e.floats[j] = testDirectGamepadRecFloat(i, j)
		}
		out = append(out, e)
	}
	return out
}

func TestGamepadPathDefaultDirect(t *testing.T) {
	selectGamepadPath(t, "")
	if got := GamepadInputPath(); got != "direct" {
		t.Fatalf("default gamepad path = %v, want direct", got)
	}
}

// TestGamepadPathAlwaysDirect pins the lean simplification: the selector is
// parsed-but-ignored, so every value (including legacy gameactivity/both)
// still feeds direct.
func TestGamepadPathAlwaysDirect(t *testing.T) {
	for _, env := range []string{"direct", "gameactivity", "both", "bogus", ""} {
		t.Run("path="+env, func(t *testing.T) {
			selectGamepadPath(t, env)
			if got := GamepadInputPath(); got != "direct" {
				t.Fatalf("path = %v, want direct (selector ignored)", got)
			}
		})
	}
}

func TestGamepadKillSwitch(t *testing.T) {
	for _, off := range []string{"0", "off", "false", "no", "OFF"} {
		selectGamepadEnabled(t, off)
		if gamepadEnabled() {
			t.Fatalf("TIPSY_GAMEPAD=%q must disable pads", off)
		}
	}
	selectGamepadEnabled(t, "")
	if !gamepadEnabled() {
		t.Fatal("unset TIPSY_GAMEPAD must leave pads enabled")
	}
}

func TestGamepadTypeForName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want int
	}{
		{"Microsoft X-Box 360 pad", 0}, // hyphenated: the engine's own contains("XBOX") misses it too
		{"Xbox Wireless Controller", 3},
		{"Sony DualSense Wireless Controller", 2},
		{"Sony DualSense", 2},
		{"PS5 Controller", 2},
		{"Sony DualShock 4", 1},
		{"Wireless Controller PS4", 1},
		{"PlayStation Controller", 1},
		{"8BitDo SN30 Pro", 0},
		{"", 0},
	} {
		if got := GamepadTypeForName(tc.name); got != tc.want {
			t.Fatalf("GamepadTypeForName(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
	if got := GamepadTypeForDevice("GuliKit Controller XW", 0x045e); got != 3 {
		t.Fatalf("GuliKit VID 045e must be Xbox type 3, got %d", got)
	}
	if got := GamepadTypeForDevice("Microsoft X-Box 360 pad", 0x045e); got != 3 {
		t.Fatalf("hyphenated X-Box with VID 045e must be type 3, got %d", got)
	}
	if got := GamepadTypeForDevice("8BitDo SN30 Pro", 0x2dc8); got != 0 {
		t.Fatalf("non-Xbox vendor must stay type 0, got %d", got)
	}
}

// TestDirectGamepadButtonABI pins nativeGamepadButtonEvent(III)V as
// (deviceId, keyCode, down) with DOWN=1 (ground-truth hole c presumption,
// verified live in-experience).
func TestDirectGamepadButtonABI(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	before := RobloxDirectGamepadStats()

	if !DispatchRobloxDirectGamepadButton(1, 96, true) {
		t.Fatal("gamepad BUTTON_A DOWN was not delivered")
	}
	rec := gamepadRecSnapshot()
	if len(rec) != 1 || rec[0].kind != gamepadRecButton {
		t.Fatalf("recorded calls = %+v, want one button event", rec)
	}
	if rec[0].ints[0] != 1 || rec[0].ints[1] != 96 || rec[0].ints[2] != 1 {
		t.Fatalf("button event = %+v, want (dev 1, key 96, down 1)", rec[0].ints)
	}
	if !DispatchRobloxDirectGamepadButton(1, 96, false) {
		t.Fatal("gamepad BUTTON_A UP was not delivered")
	}
	rec = gamepadRecSnapshot()
	if len(rec) != 2 || rec[1].ints[2] != 0 {
		t.Fatalf("recorded calls = %+v, want UP with down=0 second", rec)
	}
	if got := RobloxDirectGamepadStats().ButtonDelivered - before.ButtonDelivered; got != 2 {
		t.Fatalf("button delivery delta = %d, want 2", got)
	}
}

// TestDirectGamepadAxisABI pins nativeGamepadAxisEvent(IIFFF)V as
// (deviceId, axisId, f1, f2, f3) with the singles pattern (axis,0,0,value)
// matching every observed hat/trigger emission.
func TestDirectGamepadAxisABI(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)

	if !DispatchRobloxDirectGamepadAxis(1, 11, 0, 0, 0.5) {
		t.Fatal("gamepad AXIS_Z was not delivered")
	}
	rec := gamepadRecSnapshot()
	if len(rec) != 1 || rec[0].kind != gamepadRecAxis {
		t.Fatalf("recorded calls = %+v, want one axis event", rec)
	}
	if rec[0].ints[0] != 1 || rec[0].ints[1] != 11 {
		t.Fatalf("axis event ids = %+v, want (dev 1, axis 11)", rec[0].ints)
	}
	if rec[0].floats[0] != 0 || rec[0].floats[1] != 0 || rec[0].floats[2] != 0.5 {
		t.Fatalf("axis floats = %v, want (0,0,0.5)", rec[0].floats)
	}
}

// TestDirectGamepadConnectDisconnectABI pins the (II)V connect with
// gamepad type and the (I)V disconnect.
func TestDirectGamepadConnectDisconnectABI(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)

	if !DispatchRobloxDirectGamepadConnect(1, 3) {
		t.Fatal("gamepad connect was not delivered")
	}
	if !DispatchRobloxDirectGamepadDisconnect(1) {
		t.Fatal("gamepad disconnect was not delivered")
	}
	rec := gamepadRecSnapshot()
	if len(rec) != 2 {
		t.Fatalf("recorded calls = %+v, want connect+disconnect", rec)
	}
	if rec[0].kind != gamepadRecConnect || rec[0].ints[0] != 1 || rec[0].ints[1] != 3 {
		t.Fatalf("connect = %+v, want (dev 1, type 3)", rec[0])
	}
	if rec[1].kind != gamepadRecDisconnect || rec[1].ints[0] != 1 {
		t.Fatalf("disconnect = %+v, want (dev 1)", rec[1])
	}
}

// xboxPadCaps builds the Xbox capability lists the pump would derive from
// the gamepad package: full probe key set incl. digital L2/R2 + MODE, and
// the stick/trigger/hat motion set (Z/RZ sticks, no GAS/BRAKE).
func xboxPadCaps() (keys []int, motions []int) {
	keys = []int{96, 97, 99, 100, 19, 20, 21, 22, 103, 102, 106, 107, 109, 108, 104, 105, 110}
	motions = []int{0, 1, 11, 14, 15, 16, 17, 18}
	return keys, motions
}

// TestGamepadCapabilitySequence pins the connect-time E() replay: every
// probed key/motion with its honest supported bit (third arg -1 for the
// motion seeds), the hat extra (dev, 15|16, 1, sup, type), and the L2/R2 +
// MODE extras only when present.
func TestGamepadCapabilitySequence(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	keys, motions := xboxPadCaps()

	if !GamepadConnected(1, 3, keys, motions) {
		t.Fatal("gamepad connect sequence failed")
	}
	rec := gamepadRecSnapshot()
	// 14 probe keys + 3 extras + 10 motion seeds + 2 hat extras + 1 connect.
	if len(rec) != 14+3+10+2+1 {
		t.Fatalf("recorded %d calls, want 30 (capabilities + connect)", len(rec))
	}
	// Probe keys carry their honest bit: DPAD_CENTER (23) is not in the
	// probe and must never appear; every H[] key is TRUE here.
	seenKey := map[int]int{}
	for _, e := range rec {
		if e.kind != gamepadRecSetKey {
			continue
		}
		if e.ints[0] != 1 || e.ints[3] != 3 {
			t.Fatalf("setkey ids = %+v, want (dev 1, type 3)", e.ints)
		}
		seenKey[e.ints[1]] = e.ints[2]
	}
	for _, k := range engineProbeKeys {
		if seenKey[k] != 1 {
			t.Fatalf("probe key %d support = %d, want 1", k, seenKey[k])
		}
	}
	for _, k := range []int{104, 105, 110} {
		if seenKey[k] != 1 {
			t.Fatalf("extra key %d support = %d, want 1 when present", k, seenKey[k])
		}
	}
	if _, ok := seenKey[23]; ok {
		t.Fatal("DPAD_CENTER (23) must never be advertised: absent from the engine probe")
	}
	// Motion seeds use third arg -1; GAS/BRAKE are honestly FALSE.
	seedArg := map[int]int{}
	seedSup := map[int]int{}
	hatExtra := map[int]int{}
	for _, e := range rec {
		if e.kind != gamepadRecSetMotion {
			continue
		}
		if e.ints[0] != 1 || e.ints[4] != 3 {
			t.Fatalf("setmotion ids = %+v, want (dev 1, type 3)", e.ints)
		}
		if e.ints[2] == -1 {
			seedArg[e.ints[1]] = e.ints[2]
			seedSup[e.ints[1]] = e.ints[3]
		} else if e.ints[2] == 1 {
			hatExtra[e.ints[1]] = e.ints[3]
		} else {
			t.Fatalf("setmotion third arg = %d, want -1 (seed) or 1 (hat extra)", e.ints[2])
		}
	}
	for _, a := range engineProbeMotions {
		arg, ok := seedArg[a]
		if !ok || arg != -1 {
			t.Fatalf("motion seed %d missing or third arg != -1", a)
		}
		wantSup := 0
		for _, m := range motions {
			if m == a {
				wantSup = 1
			}
		}
		if seedSup[a] != wantSup {
			t.Fatalf("motion seed %d support = %d, want %d", a, seedSup[a], wantSup)
		}
	}
	for _, a := range []int{15, 16} {
		if sup, ok := hatExtra[a]; !ok || sup != 1 {
			t.Fatalf("hat extra %d = %d, want present with sup 1", a, sup)
		}
	}
	// The connect event closes the sequence.
	last := rec[len(rec)-1]
	if last.kind != gamepadRecConnect || last.ints[0] != 1 || last.ints[1] != 3 {
		t.Fatalf("sequence tail = %+v, want connect (dev 1, type 3)", last)
	}
}

// TestGamepadCapabilityAbsentKeyHonest proves an absent probe key is
// advertised FALSE rather than skipped or faked TRUE.
func TestGamepadCapabilityAbsentKeyHonest(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	// Pad without START (108) and without L2/R2 extras.
	keys := []int{96, 97, 99, 100, 19, 20, 21, 22, 103, 102, 106, 107, 109}
	if !GamepadConnected(2, 0, keys, []int{0, 1}) {
		t.Fatal("gamepad connect sequence failed")
	}
	seenKey := map[int]int{}
	for _, e := range gamepadRecSnapshot() {
		if e.kind == gamepadRecSetKey {
			seenKey[e.ints[1]] = e.ints[2]
		}
	}
	if seenKey[108] != 0 {
		t.Fatalf("absent START support = %d, want honest 0", seenKey[108])
	}
	for _, k := range []int{104, 105} {
		if _, ok := seenKey[k]; ok {
			t.Fatalf("absent extra key %d must not be advertised at all", k)
		}
	}
}

// xboxFrame builds one synthetic Xbox Android frame: BUTTON_A + DPAD_LEFT
// held, left stick half-right, right stick up (Z/RZ), full left trigger,
// hat-right at full deflection. The RX/RY mirror value must never reach
// the direct feed (engine never reads it).
func xboxFrame() gamepad.AndroidFrame {
	return gamepad.AndroidFrame{
		DeviceID: 1,
		Buttons:  map[int]bool{96: true, 21: true, 104: true},
		Axes: map[int]float32{
			0: 0.5, 1: 0, 11: 0.5, 12: 0.5, 13: -1, 14: -1,
			15: 1, 16: 0, 17: 1,
		},
	}
}

func connectXboxForTest(t *testing.T) {
	t.Helper()
	keys, motions := xboxPadCaps()
	if !GamepadConnected(1, 3, keys, motions) {
		t.Fatal("gamepad connect sequence failed")
	}
	testDirectGamepadRecReset()
}

// TestHandleGamepadFrameXboxGolden pins the frame feed: buttons/DPAD as
// ButtonEvent edges, hats/triggers as (0,0,v) singles, sticks as the DEX
// pair packing (x,-y,0) / (z,-rz,0) on both axis ids of the pair, RX/RY
// mirror suppressed, and rest frames refiring nothing.
func TestHandleGamepadFrameXboxGolden(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)

	handleGamepadFrame(xboxFrame())
	rec := gamepadRecSnapshot()
	buttons := map[int]int{}
	axes := map[int][3]float32{}
	for _, e := range rec {
		switch e.kind {
		case gamepadRecButton:
			if e.ints[0] != 1 {
				t.Fatalf("button device = %d, want stable pad id 1", e.ints[0])
			}
			buttons[e.ints[1]] = e.ints[2]
		case gamepadRecAxis:
			if e.ints[0] != 1 {
				t.Fatalf("axis device = %d, want stable pad id 1", e.ints[0])
			}
			axes[e.ints[1]] = [3]float32{e.floats[0], e.floats[1], e.floats[2]}
		default:
			t.Fatalf("unexpected recorded kind %d in frame feed", e.kind)
		}
	}
	// Buttons incl. DPAD-as-keys and the digital L2 edge.
	for _, k := range []int{96, 21, 104} {
		if buttons[k] != 1 {
			t.Fatalf("button %d = %d, want DOWN edge", k, buttons[k])
		}
	}
	want := map[int][3]float32{
		0:  {0.5, 0, 0}, // left (X, -Y, 0) with Y=0
		1:  {0.5, 0, 0},
		11: {0.5, 1, 0}, // right (Z, -RZ, 0) with Z=0.5, RZ=-1
		14: {0.5, 1, 0},
		15: {0, 0, 1}, // hat X single
		16: {0, 0, 0}, // hat Y single, negated 0
		17: {0, 0, 1}, // LTRIGGER single
	}
	for a, w := range want {
		got, ok := axes[a]
		if !ok || got != w {
			t.Fatalf("axis %d = (%v,%t), want %v", a, got, ok, w)
		}
	}
	for _, a := range []int{12, 13} {
		if _, ok := axes[a]; ok {
			t.Fatalf("axis %d must not be emitted: engine never reads RX/RY", a)
		}
	}
	// Identical rest frame refires nothing (ACTION_MOVE batching).
	testDirectGamepadRecReset()
	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("rest frame emitted %d events, want 0", n)
	}
	// Release frame emits UPs only for held buttons, axes unchanged.
	released := xboxFrame()
	released.Buttons = map[int]bool{21: true, 104: true}
	handleGamepadFrame(released)
	rec = gamepadRecSnapshot()
	if len(rec) != 1 || rec[0].kind != gamepadRecButton || rec[0].ints[1] != 96 || rec[0].ints[2] != 0 {
		t.Fatalf("release recorded = %+v, want single BUTTON_A UP", rec)
	}
}

func TestPackGamepadAxisDEX(t *testing.T) {
	axes := map[int]float32{0: 0.5, 1: -1, 11: 0.25, 14: 0.5, 15: 1, 16: -1, 17: 0.7, 18: 0.3}
	left, ok := packGamepadAxis(0, axes)
	if !ok || left != (axisSample{0.5, 1, 0}) {
		t.Fatalf("left X pack = %+v ok=%t, want (0.5,1,0) from (X,-Y,0)", left, ok)
	}
	ly, ok := packGamepadAxis(1, axes)
	if !ok || ly != left {
		t.Fatalf("left Y pack = %+v ok=%t, want the same pair as AXIS_X", ly, ok)
	}
	right, ok := packGamepadAxis(11, axes)
	if !ok || right != (axisSample{0.25, -0.5, 0}) {
		t.Fatalf("right Z pack = %+v ok=%t, want (0.25,-0.5,0)", right, ok)
	}
	rz, ok := packGamepadAxis(14, axes)
	if !ok || rz != right {
		t.Fatalf("right RZ pack = %+v ok=%t, want the same pair as AXIS_Z", rz, ok)
	}
	hatY, ok := packGamepadAxis(16, axes)
	if !ok || hatY != (axisSample{0, 0, 1}) {
		t.Fatalf("HAT_Y pack = %+v ok=%t, want (0,0,1) from -(-1)", hatY, ok)
	}
	hatX, ok := packGamepadAxis(15, axes)
	if !ok || hatX != (axisSample{0, 0, 1}) {
		t.Fatalf("HAT_X pack = %+v ok=%t, want (0,0,1)", hatX, ok)
	}
	lt, ok := packGamepadAxis(17, axes)
	if !ok || lt != (axisSample{0, 0, 0.7}) {
		t.Fatalf("LTRIGGER pack = %+v ok=%t, want (0,0,0.7)", lt, ok)
	}
	if _, ok := packGamepadAxis(12, axes); ok {
		t.Fatal("RX must not pack: engine never reads it")
	}
}

// TestHandleGamepadFrameL2R2Duality pins the ground-truth duality: one
// physical trigger pull feeds both the BUTTON_L2/R2 key edge and the
// L/RTRIGGER analog axis.
func TestHandleGamepadFrameL2R2Duality(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)

	handleGamepadFrame(gamepad.AndroidFrame{
		DeviceID: 1,
		Buttons:  map[int]bool{104: true, 105: true},
		Axes:     map[int]float32{17: 0.7, 18: 0.3},
	})
	var sawKeyL2, sawKeyR2, sawAxisL, sawAxisR bool
	for _, e := range gamepadRecSnapshot() {
		switch e.kind {
		case gamepadRecButton:
			if e.ints[1] == 104 && e.ints[2] == 1 {
				sawKeyL2 = true
			}
			if e.ints[1] == 105 && e.ints[2] == 1 {
				sawKeyR2 = true
			}
		case gamepadRecAxis:
			if e.ints[1] == 17 && e.floats[2] == 0.7 {
				sawAxisL = true
			}
			if e.ints[1] == 18 && e.floats[2] == 0.3 {
				sawAxisR = true
			}
		}
	}
	if !sawKeyL2 || !sawKeyR2 || !sawAxisL || !sawAxisR {
		t.Fatalf("L2/R2 duality incomplete: keyL2=%t keyR2=%t axisL=%t axisR=%t",
			sawKeyL2, sawKeyR2, sawAxisL, sawAxisR)
	}
}

// TestGamepadFocusLossQuiet proves the shared focus gate end to end: focus
// loss synthesizes UP + zero axes, unfocused frames stay quiet without
// disturbing the baseline, and focus gain resumes with fresh DOWN edges.
func TestGamepadFocusLossQuiet(t *testing.T) {
	selectGamepadPath(t, "direct")
	selectPointerPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	stubPointerLock(t, func(bool) (bool, error) { return true, nil })
	connectXboxForTest(t)

	handleGamepadFrame(xboxFrame())
	testDirectGamepadRecReset()
	before := RobloxDirectGamepadStats()

	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: false})
	rec := gamepadRecSnapshot()
	ups := map[int]bool{}
	zeros := map[int]bool{}
	for _, e := range rec {
		switch e.kind {
		case gamepadRecButton:
			if e.ints[2] != 0 {
				t.Fatalf("focus loss emitted non-UP button event %+v", e)
			}
			ups[e.ints[1]] = true
		case gamepadRecAxis:
			if e.floats[0] != 0 || e.floats[1] != 0 || e.floats[2] != 0 {
				t.Fatalf("focus loss emitted nonzero axis event %+v", e)
			}
			zeros[e.ints[1]] = true
		default:
			t.Fatalf("focus loss emitted unexpected kind %+v", e)
		}
	}
	for _, k := range []int{96, 21, 104} {
		if !ups[k] {
			t.Fatalf("focus loss missed UP for held button %d (ups=%v)", k, ups)
		}
	}
	for _, a := range []int{0, 1, 11, 14, 15, 17} {
		if !zeros[a] {
			t.Fatalf("focus loss missed zero for nonzero axis %d (zeros=%v)", a, zeros)
		}
	}
	// Unfocused frames are quiet: counted as dropped, never delivered.
	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != len(rec) {
		t.Fatalf("unfocused frame delivered %d events, want quiet", n-len(rec))
	}
	st := RobloxDirectGamepadStats()
	if st.Dropped == before.Dropped {
		t.Fatal("unfocused frame was not counted as dropped")
	}
	// Focus gain resumes: the same physical state re-announces as fresh
	// DOWN edges (no stuck-button, no missed press).
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputFocus, FocusGained: true})
	n := testDirectGamepadRecCount()
	handleGamepadFrame(xboxFrame())
	rec = gamepadRecSnapshot()[n:]
	downs := map[int]bool{}
	for _, e := range rec {
		if e.kind == gamepadRecButton && e.ints[2] == 1 {
			downs[e.ints[1]] = true
		}
	}
	for _, k := range []int{96, 21, 104} {
		if !downs[k] {
			t.Fatalf("post-focus frame missed fresh DOWN for %d (downs=%v)", k, downs)
		}
	}
}

// TestGamepadDisconnectSynthesis proves unplug withdraws the pad: UP for
// held buttons, zeroed axes, then the disconnect event; a second
// disconnect for the same id is honestly ignored.
func TestGamepadDisconnectSynthesis(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectXboxForTest(t)

	handleGamepadFrame(xboxFrame())
	testDirectGamepadRecReset()

	if !GamepadDisconnected(1) {
		t.Fatal("disconnect was not accepted")
	}
	rec := gamepadRecSnapshot()
	if len(rec) == 0 || rec[len(rec)-1].kind != gamepadRecDisconnect {
		t.Fatalf("disconnect tail = %+v, want disconnect event last", rec)
	}
	sawUp, sawZero := false, false
	for _, e := range rec[:len(rec)-1] {
		switch e.kind {
		case gamepadRecButton:
			if e.ints[2] != 0 {
				t.Fatalf("disconnect emitted non-UP %+v", e)
			}
			sawUp = true
		case gamepadRecAxis:
			if e.floats[0] != 0 || e.floats[1] != 0 || e.floats[2] != 0 {
				t.Fatalf("disconnect emitted nonzero axis %+v", e)
			}
			sawZero = true
		}
	}
	if !sawUp || !sawZero {
		t.Fatalf("disconnect missed synthesis: up=%t zero=%t", sawUp, sawZero)
	}
	if GamepadDisconnected(1) {
		t.Fatal("second disconnect for a withdrawn pad must be ignored")
	}
	if GamepadDisconnected(9) {
		t.Fatal("disconnect for an unknown pad id must be ignored")
	}
}

// TestGamepadNoPadMeansNoDevice proves the honest empty state two ways:
// frames for an unannounced pad never emit, and the evdev pump over an
// empty directory announces nothing.
func TestGamepadNoPadMeansNoDevice(t *testing.T) {
	selectGamepadPath(t, "direct")
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)

	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("unannounced-pad frame emitted %d events, want 0", n)
	}
	before := RobloxDirectGamepadStats()

	dir := t.TempDir()
	if !StartRobloxDirectGamepadPumpDir(dir) {
		t.Fatal("pump did not start over an empty dir")
	}
	time.Sleep(50 * time.Millisecond)
	StopRobloxDirectGamepadPump()
	after := RobloxDirectGamepadStats()
	if after.ConnectDelivered != before.ConnectDelivered {
		t.Fatal("empty-dir pump announced a pad: fake device")
	}
	if after.Dropped != before.Dropped {
		t.Fatalf("empty-dir pump dropped %d frames, want silence",
			after.Dropped-before.Dropped)
	}
}

// TestGamepadConnectParkedWithoutTarget proves delivery parks (drops,
// never queues) when libroblox exports are missing.
func TestGamepadConnectParkedWithoutTarget(t *testing.T) {
	selectGamepadPath(t, "direct")
	testDirectGamepadRecReset()
	ClearRobloxDirectGamepadTarget()
	t.Cleanup(ClearRobloxDirectGamepadTarget)
	before := RobloxDirectGamepadStats()

	keys, motions := xboxPadCaps()
	if GamepadConnected(1, 3, keys, motions) {
		t.Fatal("connect without a wired target must park, not succeed")
	}
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("parked connect emitted %d calls, want 0", n)
	}
	if RobloxDirectGamepadStats().Dropped == before.Dropped {
		t.Fatal("parked connect was not counted as dropped")
	}
}

// TestGamepadLegacyPathStillDelivers pins the honest behavior change:
// legacy gameactivity/both values are ignored (one warn in production) and
// pads still feed direct — never silently dropped.
func TestGamepadLegacyPathStillDelivers(t *testing.T) {
	for _, mode := range []string{"gameactivity", "both"} {
		t.Run(mode, func(t *testing.T) {
			selectGamepadPath(t, mode)
			wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
			keys, motions := xboxPadCaps()
			if !GamepadConnected(1, 3, keys, motions) {
				t.Fatalf("%s path must still connect pads (selector ignored)", mode)
			}
			testDirectGamepadRecReset()
			handleGamepadFrame(xboxFrame())
			if n := testDirectGamepadRecCount(); n == 0 {
				t.Fatalf("%s path emitted 0 pad calls, want direct delivery", mode)
			}
		})
	}
}

// TestGamepadKillSwitchFeed proves TIPSY_GAMEPAD=0 stops the pump and the
// frame path.
func TestGamepadKillSwitchFeed(t *testing.T) {
	selectGamepadEnabled(t, "0")
	testDirectGamepadRecReset()
	if StartRobloxDirectGamepadPump() {
		StopRobloxDirectGamepadPump()
		t.Fatal("pump must refuse to start under the kill-switch")
	}
	before := RobloxDirectGamepadStats()
	handleGamepadFrame(xboxFrame())
	if n := testDirectGamepadRecCount(); n != 0 {
		t.Fatalf("killed frame path emitted %d calls, want 0", n)
	}
	if RobloxDirectGamepadStats().Dropped == before.Dropped {
		t.Fatal("killed frame was not counted as dropped")
	}
}

// selectGamepadCalibration sets the direct path plus one calibration
// environment for Phase 3 tests. A nil env means defaults (no deadzone
// floor, no inversion).
func selectGamepadCalibration(t *testing.T, env map[string]string) {
	t.Helper()
	t.Setenv("TIPSY_GAMEPAD_PATH", "direct")
	t.Setenv("TIPSY_GAMEPAD", "")
	t.Setenv("TIPSY_GAMEPAD_DEADZONE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for k, v := range env {
		t.Setenv(k, v)
	}
	ResetGamepadInputPath()
	t.Cleanup(ResetGamepadInputPath)
}

// TestGamepadCalibrationEnvParsing pins the lean CLI/env parity surface:
// one TIPSY_GAMEPAD_DEADZONE floor (clamped 0..0.5, invalid ignored),
// defaulting to the device-flat baseline when unset. Removed v1 per-stick
// and invert keys are ignored.
func TestGamepadCalibrationEnvParsing(t *testing.T) {
	selectGamepadCalibration(t, map[string]string{
		"TIPSY_GAMEPAD_DEADZONE":       "0.2",
		"TIPSY_GAMEPAD_DEADZONE_RIGHT": "0.35",
		"TIPSY_GAMEPAD_INVERT_Y":       "1",
	})
	cfg := gamepadCalibration()
	if cfg.Deadzone != 0.2 {
		t.Fatalf("global deadzone = %v, want 0.2", cfg.Deadzone)
	}
	if cfg.EffectiveDeadzone() != 0.2 {
		t.Fatalf("effective floor = %v, want the single global 0.2", cfg.EffectiveDeadzone())
	}
}

func TestGamepadCalibrationEnvDefaults(t *testing.T) {
	selectGamepadCalibration(t, nil)
	cfg := gamepadCalibration()
	if want := gamepad.DefaultGamepadConfig(); cfg != want {
		t.Fatalf("unset calibration env must yield defaults, got %+v", cfg)
	}
}

func TestGamepadCalibrationEnvInvalidIgnored(t *testing.T) {
	selectGamepadCalibration(t, map[string]string{
		"TIPSY_GAMEPAD_DEADZONE": "huge",
	})
	cfg := gamepadCalibration()
	if cfg.Deadzone != 0 {
		t.Fatalf("invalid calibration env must be ignored honestly, got %+v", cfg)
	}
}

// calibrationPumpFrame builds one normalized left-stick Frame the way the
// evdev pump would: AbsX raw 3000 (≈0.09, above the device flat) and AbsY
// half deflection (≈0.5).
func calibrationPumpFrame(t *testing.T) (*gamepad.Frame, gamepad.Mapping, map[uint16]gamepad.AbsInfo) {
	t.Helper()
	infos := map[uint16]gamepad.AbsInfo{
		gamepad.AbsX: {Minimum: -32768, Maximum: 32767, Flat: 255},
		gamepad.AbsY: {Minimum: -32768, Maximum: 32767, Flat: 255},
	}
	m := gamepad.Mapping{Name: "test-left-only", RightX: gamepad.NoAxis, RightY: gamepad.NoAxis}
	r := gamepad.NewReader(infos)
	var f *gamepad.Frame
	for _, ev := range []gamepad.InputEvent{
		{Type: gamepad.EvAbs, Code: gamepad.AbsX, Value: 3000},
		{Type: gamepad.EvAbs, Code: gamepad.AbsY, Value: 16383},
		{Type: gamepad.EvSyn, Code: gamepad.SynReport},
	} {
		if fr := r.Feed(ev); fr != nil {
			f = fr
		}
	}
	if f == nil {
		t.Fatal("stream produced no frame")
	}
	return f, m, infos
}

// pumpFrameCalibrated replays the pump's per-Frame application order:
// ApplyCalibration with the env-derived config, then MapFrame, then the
// focus-gated frame handler.
func pumpFrameCalibrated(f *gamepad.Frame, m gamepad.Mapping, infos map[uint16]gamepad.AbsInfo) {
	gamepad.ApplyCalibration(f, m, gamepadCalibration())
	handleGamepadFrame(gamepad.MapFrame(f, 1, m, infos))
}

func recordedAxisTriples() map[int][3]float32 {
	axes := map[int][3]float32{}
	for _, e := range gamepadRecSnapshot() {
		if e.kind == gamepadRecAxis {
			axes[e.ints[1]] = [3]float32{e.floats[0], e.floats[1], e.floats[2]}
		}
	}
	return axes
}

// TestGamepadCalibrationPipelineDeadzone proves the JNI application part end
// to end: with TIPSY_GAMEPAD_DEADZONE=0.2 the ≈0.09 X deflection is floored
// to a 0 axis event while the ≈0.5 Y deflection passes through.
func TestGamepadCalibrationPipelineDeadzone(t *testing.T) {
	selectGamepadCalibration(t, map[string]string{"TIPSY_GAMEPAD_DEADZONE": "0.2"})
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectPadForTest(t, 1, 3)
	testDirectGamepadRecReset()

	f, m, infos := calibrationPumpFrame(t)
	pumpFrameCalibrated(f, m, infos)
	axes := recordedAxisTriples()
	left0, ok := axes[0]
	if !ok {
		t.Fatal("calibrated left stick did not emit AXIS_X")
	}
	if left0[0] != 0 {
		t.Fatalf("calibrated AXIS_X f1 = %v, want floored 0", left0[0])
	}
	if math.Abs(float64(left0[1])+0.5) > 0.002 || left0[2] != 0 {
		t.Fatalf("calibrated left pack = %v, want (0,-0.5,0)", left0)
	}
	if axes[1] != left0 {
		t.Fatalf("AXIS_Y pack = %v, want the same pair as AXIS_X %v", axes[1], left0)
	}
}

// TestGamepadCalibrationPipelineBaseline proves unset calibration env leaves
// the device-flat baseline byte-identical: the ≈0.09 deflection still emits.
func TestGamepadCalibrationPipelineBaseline(t *testing.T) {
	selectGamepadCalibration(t, nil)
	wireRecordingDirectGamepadTarget(t, 0x1234, 0x5678)
	connectPadForTest(t, 1, 3)
	testDirectGamepadRecReset()

	f, m, infos := calibrationPumpFrame(t)
	pumpFrameCalibrated(f, m, infos)
	axes := recordedAxisTriples()
	v, ok := axes[0]
	if !ok || math.Abs(float64(v[0])-0.09) > 0.01 {
		t.Fatalf("baseline AXIS_X f1 = (%v,%t), want uncalibrated ≈0.09", v, ok)
	}
	if math.Abs(float64(v[1])+0.5) > 0.002 || v[2] != 0 {
		t.Fatalf("baseline left pack = %v, want (≈0.09,-0.5,0)", v)
	}
}

// newGamepadMotionObject builds a standalone gamepad MotionEvent for getter
// tests (production direct delivery never routes through these objects).
func newGamepadMotionObject(t *testing.T, vm *VM, deviceID int32, axes map[int32]float32) int64 {
	t.Helper()
	cls := vm.ensureClassLocked(motionEventClass)
	vm.mu.Lock()
	o := vm.newObjectLocked(cls)
	vm.resetGamepadMotionEventLocked(o, deviceID, sourceJoystick, axes, 100, 250)
	id := o.id
	vm.mu.Unlock()
	return id
}

// TestGamepadDispatchAxisGetters pins the extended getAxisValue vocabulary:
// Z/RZ, the RX/RY mirror, hats, and triggers answer per-object values;
// unmapped axes stay 0; and plain pointer objects still answer 0 for pad
// axes (keyboard/mouse behavior identical).
func TestGamepadDispatchAxisGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	axes := map[int32]float32{
		0: 0.5, 1: -0.25, 11: 0.5, 12: 0.5, 13: -1, 14: -1,
		15: 1, 16: 0, 17: 0.7, 18: 0.3,
	}
	objID := newGamepadMotionObject(t, vm, 7, axes)

	axis := func(t *testing.T, a int32) float32 {
		t.Helper()
		v, handled := testEventGetterII(vm, objID, motionEventClass, "getAxisValue", "(II)F", a, 0)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		return math.Float32frombits(uint32(v))
	}
	for a, want := range axes {
		if got := axis(t, a); got != want {
			t.Fatalf("getAxisValue(%d) = %v, want %v", a, got, want)
		}
	}
	// Historical reads mirror the current sample (no history batching).
	for _, a := range []int32{11, 14, 15, 17} {
		v, handled := testEventGetterIII(vm, objID, motionEventClass, "getHistoricalAxisValue", "(III)F", a, 0, 0)
		if !handled {
			t.Fatal("getHistoricalAxisValue(III)F not handled")
		}
		if got, want := math.Float32frombits(uint32(v)), axes[a]; got != want {
			t.Fatalf("getHistoricalAxisValue(%d) = %v, want %v", a, got, want)
		}
	}
	// Unmapped axis (e.g. AXIS_VSCROLL 9) stays honestly 0.
	if got := axis(t, 9); got != 0 {
		t.Fatalf("getAxisValue(9) = %v, want 0", got)
	}
	// Truthful source + stable per-pad device id.
	v, handled := testEventGetter(vm, objID, motionEventClass, "getSource", "()I", -1)
	if !handled || int32(v) != sourceJoystick {
		t.Fatalf("gamepad move getSource = %#x, want %#x", uint32(v), uint32(sourceJoystick))
	}
	v, handled = testEventGetter(vm, objID, motionEventClass, "getDeviceId", "()I", -1)
	if !handled || int32(v) != 7 {
		t.Fatalf("gamepad move getDeviceId = %d, want stable pad id 7", int32(v))
	}

	// Plain pointer objects are unaffected: pad axes answer 0.
	vm.mu.Lock()
	po := vm.newMotionEventLocked(motionActionMove, 3.5, 7.25, 100, 250)
	pointerID := po.id
	vm.mu.Unlock()
	for _, a := range []int32{11, 14, 15, 16, 17, 18} {
		v, handled := testEventGetterII(vm, pointerID, motionEventClass, "getAxisValue", "(II)F", a, 0)
		if !handled {
			t.Fatal("getAxisValue(II)F not handled")
		}
		if got := math.Float32frombits(uint32(v)); got != 0 {
			t.Fatalf("pointer getAxisValue(%d) = %v, want 0 (mouse path untouched)", a, got)
		}
	}
}

// newGamepadKeyObject builds a standalone gamepad KeyEvent for getter tests.
func newGamepadKeyObject(t *testing.T, vm *VM, keyCode, deviceID, source int32, pressed bool) int64 {
	t.Helper()
	cls := vm.ensureClassLocked(keyEventClass)
	vm.mu.Lock()
	o := vm.newObjectLocked(cls)
	vm.resetGamepadKeyEventLocked(o, keyCode, deviceID, source, pressed, 100, 250, 42)
	id := o.id
	vm.mu.Unlock()
	return id
}

// TestGamepadDispatchKeyGetters pins button KeyEvent fields: keycode,
// action edges, source (GAMEPAD for buttons, DPAD for hat keys), and the
// stable per-pad device id.
func TestGamepadDispatchKeyGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	objID := newGamepadKeyObject(t, vm, 96, 7, sourceGamepad, true)
	probe := func(t *testing.T, id int64, name, sig string) uintptr {
		t.Helper()
		v, handled := testEventGetter(vm, id, keyEventClass, name, sig, -1)
		if !handled {
			t.Fatalf("%s%s not handled", name, sig)
		}
		return v
	}
	if got := int32(probe(t, objID, "getKeyCode", "()I")); got != 96 {
		t.Fatalf("getKeyCode = %d, want BUTTON_A 96", got)
	}
	if got := int32(probe(t, objID, "getAction", "()I")); got != keyEventActionDown {
		t.Fatalf("getAction = %d, want DOWN 0", got)
	}
	if got := int32(probe(t, objID, "getSource", "()I")); got != sourceGamepad {
		t.Fatalf("button getSource = %#x, want GAMEPAD %#x", uint32(got), uint32(sourceGamepad))
	}
	if got := int32(probe(t, objID, "getDeviceId", "()I")); got != 7 {
		t.Fatalf("button getDeviceId = %d, want stable pad id 7", got)
	}

	upID := newGamepadKeyObject(t, vm, 21, 7, sourceDpad, false)
	if got := int32(probe(t, upID, "getAction", "()I")); got != keyEventActionUp {
		t.Fatalf("DPAD getAction = %d, want UP 1", got)
	}
	if got := int32(probe(t, upID, "getSource", "()I")); got != sourceDpad {
		t.Fatalf("DPAD getSource = %#x, want DPAD %#x", uint32(got), uint32(sourceDpad))
	}
	if got := int32(probe(t, upID, "getKeyCode", "()I")); got != 21 {
		t.Fatalf("DPAD getKeyCode = %d, want DPAD_LEFT 21", got)
	}
}

// Lean single-pad contract (simplified 2026-09-12): the second-pad
// coexistence/focus/stuck-button tests are deleted vs v1 — one pad is
// served (see TestManagerSecondPadHonestlyIgnored for the ignore rule).

func connectPadForTest(t *testing.T, dev, typ int32) {
	t.Helper()
	keys, motions := xboxPadCaps()
	if !GamepadConnected(dev, typ, keys, motions) {
		t.Fatalf("gamepad %d connect sequence failed", dev)
	}
}

// TestGamepadInputDeviceGettersHonest pins the structured stub-dispatch
// contract for InputDevice getters: none is claimed by the implemented
// dispatch families, so every one falls through to the content-free
// [jni] stub-dispatch diagnostic with an honest zero/empty failure — never
// fake children (no phantom pads, no synthesized MotionRanges).
func TestGamepadInputDeviceGettersHonest(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	cls := vm.ensureClassLocked("android/view/InputDevice")
	o := vm.newObjectLocked(cls)
	id := o.id
	vm.mu.Unlock()
	unhandled := func(name, sig string, args ...int32) {
		t.Helper()
		var handled bool
		switch len(args) {
		case 0:
			_, handled = testEventGetter(vm, id, "android/view/InputDevice", name, sig, -1)
		case 1:
			_, handled = testEventGetter(vm, id, "android/view/InputDevice", name, sig, args[0])
		case 2:
			_, handled = testEventGetterII(vm, id, "android/view/InputDevice", name, sig, args[0], args[1])
		default:
			t.Fatalf("too many args for %s%s", name, sig)
		}
		if handled {
			t.Fatalf("%s%s must stay on the honest stub path, never a fake child", name, sig)
		}
	}
	// Enumeration / identity (ground truth §4: getDeviceIds never called by
	// the engine; listener hotplug drives connect/disconnect instead).
	// getName/getClass/toString stay on the generic class-identity core
	// handlers (they answer the class identity, never a pad child), so only
	// the pad-specific getters are pinned here.
	unhandled("getDeviceIds", "()[I")
	unhandled("getDevice", "(I)Landroid/view/InputDevice;", 1)
	unhandled("getSources", "()I")
	unhandled("getDescriptor", "()Ljava/lang/String;")
	unhandled("getControllerNumber", "()I")
	unhandled("getVendorId", "()I")
	unhandled("getProductId", "()I")
	// Capability queries (E() runs through the direct capability setters,
	// never through these Java objects).
	unhandled("supportsSource", "(I)Z", 0x401)
	unhandled("hasKeys", "([I)Z")
	unhandled("getMotionRange", "(I)Landroid/view/InputDevice$MotionRange;", 0)
	unhandled("getMotionRange", "(II)Landroid/view/InputDevice$MotionRange;", 0, 1)
	unhandled("getMotionRanges", "()Ljava/util/List;")
	// Vibration (ground truth §4: no gamepad-rumble door in this client).
	unhandled("getVibrator", "()Landroid/os/Vibrator;")
	unhandled("hasVibrator", "()Z")
	// Listener registration (engine-side manager wiring, not a Tipsy object).
	unhandled("registerInputDeviceListener", "(Landroid/hardware/input/InputManager$InputDeviceListener;Landroid/os/Handler;)V")
	unhandled("unregisterInputDeviceListener", "(Landroid/hardware/input/InputManager$InputDeviceListener;)V")
}
