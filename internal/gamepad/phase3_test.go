// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"math"
	"testing"
)

// Lean trigger/hat goldens (simplified 2026-09-12): one golden per path —
// the xpad Z/RZ full-pull (key+axis duality), hat+DPAD duality both
// directions, and per-axis flat honesty. Deleted vs v1: the HAT2X-trigger
// and digital-only trigger goldens (duplicate topologies; the capability
// rules themselves stay pinned in quirks_test.go and the honest-absence
// rules in the Xbox golden above).

// TestZRZTriggerFullPullGolden pins the xpad topology's key+axis duality:
// one physical pull feeds BOTH the digital BTN_TL2 edge and the full 0..1
// analog range (ground-truth §3: L2/R2 are BOTH).
func TestZRZTriggerFullPullGolden(t *testing.T) {
	info, m := xboxDevice()
	r := NewReader(info.Abs)
	f := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvKey, BtnTL2, 1),
		EncodeInputEvent(EvAbs, AbsZ, 255),
		EncodeInputEvent(EvAbs, AbsRZ, 255),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	af := MapFrame(f, 1, m, info.Abs)
	if !af.Buttons[AndroidButtonL2] {
		t.Fatalf("BTN_TL2 edge must map to BUTTON_L2: %+v", af.Buttons)
	}
	if math.Abs(float64(af.Axes[AndroidAxisLTrigger])-1) > 1e-9 {
		t.Fatalf("full Z pull must be LTRIGGER=1, got %v", af.Axes[AndroidAxisLTrigger])
	}
	if math.Abs(float64(af.Axes[AndroidAxisRTrigger])-1) > 1e-9 {
		t.Fatalf("full RZ pull must be RTRIGGER=1, got %v", af.Axes[AndroidAxisRTrigger])
	}
}

// TestPerAxisFlatHonesty pins setRange truthfulness: device flat converts to
// axis units (stick half-range, trigger span), flat==0 stays an honest 0 on
// a PRESENT axis, and unreported axes are absent (never zero-filled).
func TestPerAxisFlatHonesty(t *testing.T) {
	hasAbs, infos := xpadCaps()
	infos[AbsX] = AbsInfo{Minimum: -32768, Maximum: 32767, Flat: 512}
	infos[AbsZ] = AbsInfo{Minimum: 0, Maximum: 255, Flat: 8}
	infos[AbsY] = AbsInfo{Minimum: -32768, Maximum: 32767, Flat: 0}
	info := DeviceInfo{Name: "flat probe", Abs: infos, HasAbs: hasAbs}
	m, _ := ResolveMappingForDevice(info)
	r := NewReader(info.Abs)
	// Ranges are served for reported axes, so the stream must carry samples
	// for each probed axis before the SYN boundary.
	f := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvAbs, AbsX, 0),
		EncodeInputEvent(EvAbs, AbsY, 0),
		EncodeInputEvent(EvAbs, AbsZ, 0),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	af := MapFrame(f, 1, m, info.Abs)

	rgX := af.Ranges[AndroidAxisX]
	wantX := float32(512.0 / (65535.0 / 2))
	if math.Abs(float64(rgX.Flat-wantX)) > 1e-4 {
		t.Fatalf("AXIS_X flat must be 512/half-range ≈ %v, got %v", wantX, rgX.Flat)
	}
	rgT := af.Ranges[AndroidAxisLTrigger]
	wantT := float32(8.0 / 255)
	if math.Abs(float64(rgT.Flat-wantT)) > 1e-4 {
		t.Fatalf("LTRIGGER flat must be 8/span ≈ %v, got %v", wantT, rgT.Flat)
	}
	rgY, ok := af.Ranges[AndroidAxisY]
	if !ok {
		t.Fatal("flat==0 axis must still be PRESENT (honest zero flat), not absent")
	}
	if rgY.Flat != 0 {
		t.Fatalf("flat==0 axis must report flat 0, got %v", rgY.Flat)
	}
	if _, ok := af.Ranges[AndroidAxisGas]; ok {
		t.Fatal("unreported GAS axis must be absent, never zero-filled")
	}
	if _, ok := af.Ranges[AndroidAxisBrake]; ok {
		t.Fatal("unreported BRAKE axis must be absent, never zero-filled")
	}
}

// TestHatDpadThresholdEdges pins the hat→DPAD door at the named threshold:
// full discrete deflection fires the key, rest fires none, and the HAT axes
// always accompany the keys from the same motion.
func TestHatDpadThresholdEdges(t *testing.T) {
	info, m := xboxDevice()
	feed := func(hx, hy int32) AndroidFrame {
		r := NewReader(info.Abs)
		f := feedStream(t, r, [][]byte{
			EncodeInputEvent(EvAbs, AbsHat0X, hx),
			EncodeInputEvent(EvAbs, AbsHat0Y, hy),
			EncodeInputEvent(EvSyn, SynReport, 0),
		})
		return MapFrame(f, 1, m, info.Abs)
	}
	right := feed(1, 0)
	if right.Axes[AndroidAxisHatX] != 1 || !right.Buttons[AndroidDpadRight] {
		t.Fatalf("hat-right must drive HAT_X=1 + DPAD_RIGHT: %+v/%+v", right.Axes, right.Buttons)
	}
	if right.Buttons[AndroidDpadLeft] || right.Buttons[AndroidDpadUp] || right.Buttons[AndroidDpadDown] {
		t.Fatalf("hat-right must not fire other DPAD keys: %+v", right.Buttons)
	}
	up := feed(0, -1)
	if up.Axes[AndroidAxisHatY] != -1 || !up.Buttons[AndroidDpadUp] {
		t.Fatalf("hat-up must drive HAT_Y=-1 + DPAD_UP: %+v/%+v", up.Axes, up.Buttons)
	}
	rest := feed(0, 0)
	for _, k := range []int{AndroidDpadUp, AndroidDpadDown, AndroidDpadLeft, AndroidDpadRight} {
		if rest.Buttons[k] {
			t.Fatalf("hat rest must fire no DPAD keys, got %+v", rest.Buttons)
		}
	}
}

// TestDpadButtonsDriveHatAxes pins the reverse door: DPAD button presses
// without hat motion drive the HAT axes so both doors fire from one motion.
func TestDpadButtonsDriveHatAxes(t *testing.T) {
	info, m := xboxDevice()
	r := NewReader(info.Abs)
	f := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvKey, BtnDpadLeft, 1),
		EncodeInputEvent(EvKey, BtnDpadDown, 1),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	af := MapFrame(f, 1, m, info.Abs)
	if !af.Buttons[AndroidDpadLeft] || !af.Buttons[AndroidDpadDown] {
		t.Fatalf("DPAD buttons must map to keys: %+v", af.Buttons)
	}
	if af.Axes[AndroidAxisHatX] != -1 || af.Axes[AndroidAxisHatY] != 1 {
		t.Fatalf("DPAD buttons must drive HAT axes (-1,+1), got %v/%v",
			af.Axes[AndroidAxisHatX], af.Axes[AndroidAxisHatY])
	}
}

// xpadFrame builds a normalized xpad Frame with small stick deflections, a
// trigger pull, and hat deflection for calibration tests.
func xpadFrame() (*Frame, Mapping) {
	_, m := xboxDevice()
	return &Frame{
		Buttons: map[uint16]bool{},
		Axes: map[uint16]float64{
			AbsX: 0.10, AbsY: 0.40, AbsRX: 0.30, AbsRY: -0.60,
			AbsZ: 0.90, AbsRZ: 0.05, AbsHat0X: 1, AbsHat0Y: 0,
		},
	}, m
}

// TestApplyCalibrationDeadzoneFloor pins the single global floor: samples
// at or under the floor zero on BOTH sticks, samples above pass through
// unrescaled, and triggers/hats are untouched.
func TestApplyCalibrationDeadzoneFloor(t *testing.T) {
	f, m := xpadFrame()
	f.Axes[m.RightX] = 0.15 // under-floor sample on the right stick too
	cfg := DefaultGamepadConfig()
	cfg.SetDeadzone(0.2)
	ApplyCalibration(f, m, cfg)

	if f.Axes[AbsX] != 0 {
		t.Fatalf("left X=0.10 under 0.2 must zero, got %v", f.Axes[AbsX])
	}
	if f.Axes[AbsY] != 0.40 {
		t.Fatalf("left Y=0.40 above 0.2 must pass unrescaled, got %v", f.Axes[AbsY])
	}
	if f.Axes[m.RightX] != 0 {
		t.Fatalf("right X=0.15 under the same 0.2 must zero, got %v", f.Axes[m.RightX])
	}
	if f.Axes[AbsRY] != -0.60 {
		t.Fatalf("right Y=-0.60 above 0.2 must pass unrescaled, got %v", f.Axes[AbsRY])
	}
	if f.Axes[AbsZ] != 0.90 || f.Axes[AbsRZ] != 0.05 {
		t.Fatalf("triggers must be untouched by stick calibration, got %v/%v", f.Axes[AbsZ], f.Axes[AbsRZ])
	}
	if f.Axes[AbsHat0X] != 1 || f.Axes[AbsHat0Y] != 0 {
		t.Fatalf("hats must be untouched by stick calibration, got %v/%v", f.Axes[AbsHat0X], f.Axes[AbsHat0Y])
	}
}

// TestApplyCalibrationZeroFloorIsBaseline pins that a zero user floor changes
// nothing (the device-flat baseline from the Reader stands as-is).
func TestApplyCalibrationZeroFloorIsBaseline(t *testing.T) {
	f, m := xpadFrame()
	before := make(map[uint16]float64, len(f.Axes))
	for c, v := range f.Axes {
		before[c] = v
	}
	ApplyCalibration(f, m, DefaultGamepadConfig())
	for c, v := range before {
		if f.Axes[c] != v {
			t.Fatalf("zero calibration must not move axis %d: %v → %v", c, v, f.Axes[c])
		}
	}
}

// TestApplyCalibrationAbsentStaysAbsent pins no zero-fill: codes missing from
// the Frame are never added, and nil frames are a safe no-op.
func TestApplyCalibrationAbsentStaysAbsent(t *testing.T) {
	_, m := xboxDevice()
	f := &Frame{Buttons: map[uint16]bool{}, Axes: map[uint16]float64{AbsZ: 0.7}}
	cfg := DefaultGamepadConfig()
	cfg.SetDeadzone(0.5)
	ApplyCalibration(f, m, cfg)
	if _, ok := f.Axes[AbsX]; ok {
		t.Fatal("absent stick codes must stay absent, never zero-filled")
	}
	if f.Axes[AbsZ] != 0.7 {
		t.Fatalf("trigger must be untouched, got %v", f.Axes[AbsZ])
	}
	ApplyCalibration(nil, m, cfg) // must not panic
}
