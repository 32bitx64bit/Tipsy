// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"math"
	"testing"
)

// feedStream decodes a recorded byte stream through a Reader and returns
// the last frame before end of stream.
func feedStream(t *testing.T, r *Reader, stream [][]byte) *Frame {
	t.Helper()
	var last *Frame
	var buf []byte
	for _, rec := range stream {
		buf = append(buf, rec...)
	}
	for _, ev := range ParseInputEvents(buf) {
		if f := r.Feed(ev); f != nil {
			last = f
		}
	}
	if last == nil {
		t.Fatal("stream produced no SYN frame")
	}
	return last
}

func xboxDevice() (DeviceInfo, Mapping) {
	hasAbs, infos := xpadCaps()
	hasKey := map[uint16]bool{
		BtnSouth: true, BtnEast: true, BtnNorth: true, BtnWest: true,
		BtnTL: true, BtnTR: true, BtnTL2: true, BtnTR2: true,
		BtnSelect: true, BtnStart: true, BtnMode: true,
		BtnThumbl: true, BtnThumbr: true,
		BtnDpadUp: true, BtnDpadDown: true, BtnDpadLeft: true, BtnDpadRight: true,
	}
	info := DeviceInfo{
		Name: "Microsoft X-Box 360 pad",
		ID:   DeviceID{BusType: 3, Vendor: 0x045e, Product: 0x028e},
		Abs:  infos, HasAbs: hasAbs, HasKey: hasKey,
	}
	m, _ := ResolveMappingForDevice(info)
	return info, m
}

// TestPositionalDiamondMapping pins BTN compass → BUTTON letters.

// TestXboxGoldenRecordedStream builds a recorded Xbox input_event stream:
// A press + left-stick half-right + right-stick up + full left trigger +
// hat-right, each followed by SYN. The Android frame must carry the
// positional BUTTON_A, X/Z-mirrored sticks, LTRIGGER, and hat/DPAD duality.
func TestXboxGoldenRecordedStream(t *testing.T) {
	info, m := xboxDevice()
	r := NewReader(info.Abs)
	stream := [][]byte{
		EncodeInputEvent(EvKey, BtnSouth, 1),
		EncodeInputEvent(EvSyn, SynReport, 0),
		EncodeInputEvent(EvAbs, AbsX, 16383),
		EncodeInputEvent(EvAbs, AbsRX, 16383),
		EncodeInputEvent(EvAbs, AbsRY, -32768),
		EncodeInputEvent(EvAbs, AbsZ, 255),
		EncodeInputEvent(EvAbs, AbsHat0X, 1),
		EncodeInputEvent(EvSyn, SynReport, 0),
	}
	f := feedStream(t, r, stream)
	af := MapFrame(f, 1, m, info.Abs)

	if !af.Buttons[AndroidButtonA] {
		t.Fatalf("BTN_SOUTH must map to BUTTON_A: %+v", af.Buttons)
	}
	if math.Abs(float64(af.Axes[AndroidAxisX])-0.5) > 0.01 {
		t.Fatalf("AXIS_X ≈0.5, got %v", af.Axes[AndroidAxisX])
	}
	// Right stick feeds Z/RZ and mirrors RX/RY from the same sample.
	if math.Abs(float64(af.Axes[AndroidAxisZ])-0.5) > 0.01 {
		t.Fatalf("AXIS_Z ≈0.5, got %v", af.Axes[AndroidAxisZ])
	}
	if math.Abs(float64(af.Axes[AndroidAxisRX])-0.5) > 0.01 {
		t.Fatalf("AXIS_RX mirror ≈0.5, got %v", af.Axes[AndroidAxisRX])
	}
	if af.Axes[AndroidAxisRZ] != -1 || af.Axes[AndroidAxisRY] != -1 {
		t.Fatalf("RY up must be -1 on RZ+RY, got %v/%v", af.Axes[AndroidAxisRZ], af.Axes[AndroidAxisRY])
	}
	if math.Abs(float64(af.Axes[AndroidAxisLTrigger])-1) > 1e-9 {
		t.Fatalf("full left trigger must be 1, got %v", af.Axes[AndroidAxisLTrigger])
	}
	// Hat/DPAD duality: HAT_X axis plus DPAD_RIGHT key from one motion.
	if af.Axes[AndroidAxisHatX] != 1 {
		t.Fatalf("HAT_X must be 1, got %v", af.Axes[AndroidAxisHatX])
	}
	if !af.Buttons[AndroidDpadRight] {
		t.Fatalf("hat-right must also set DPAD_RIGHT: %+v", af.Buttons)
	}
	// Honest ranges: reported axes present, unreported axes absent (never zero-filled).
	if _, ok := af.Ranges[AndroidAxisGas]; ok {
		t.Fatal("unreported GAS axis must be absent, never zero-filled")
	}
	if rg, ok := af.Ranges[AndroidAxisX]; !ok || rg.Min != -1 || rg.Max != 1 {
		t.Fatalf("AXIS_X range must be honest -1..1, got %+v", rg)
	}
	if rg, ok := af.Ranges[AndroidAxisLTrigger]; !ok || rg.Min != 0 || rg.Max != 1 {
		t.Fatalf("LTRIGGER range must be honest 0..1, got %+v", rg)
	}
}

// TestPositionalDiamondMapping pins BTN compass → BUTTON letters.
// (The duplicate PS-family recorded stream is deleted vs v1: one golden per
// path — the Xbox stream above covers buttons, sticks, triggers, hats,
// duality, and honest ranges. The sony label itself stays pinned in
// quirks_test.go.)
func TestPositionalDiamondMapping(t *testing.T) {
	cases := []struct {
		ev   uint16
		want int
	}{
		{BtnSouth, AndroidButtonA},
		{BtnEast, AndroidButtonB},
		{BtnWest, AndroidButtonX},
		{BtnNorth, AndroidButtonY},
		{BtnTL, AndroidButtonL1},
		{BtnTR, AndroidButtonR1},
		{BtnTL2, AndroidButtonL2},
		{BtnTR2, AndroidButtonR2},
		{BtnThumbl, AndroidButtonThumbL},
		{BtnThumbr, AndroidButtonThumbR},
		{BtnStart, AndroidButtonStart},
		{BtnSelect, AndroidButtonSelect},
		{BtnMode, AndroidButtonMode},
		{KeyMenu, AndroidButtonStart},
		{KeyBack, AndroidButtonSelect},
	}
	for _, c := range cases {
		got, ok := EvdevButtonToAndroid(c.ev)
		if !ok || got != c.want {
			t.Fatalf("evdev %#x: want Android %d, got %d,%v", c.ev, c.want, got, ok)
		}
	}
}

func TestKeySourcesAreTruthful(t *testing.T) {
	if KeySource(AndroidDpadUp) != AndroidSourceDpad {
		t.Fatal("DPAD keys must use SOURCE_DPAD")
	}
	if KeySource(AndroidButtonA) != AndroidSourceGamepad {
		t.Fatal("pad buttons must use SOURCE_GAMEPAD")
	}
	if KeySource(AndroidButtonA) == KeySource(AndroidDpadUp) {
		t.Fatal("gamepad and dpad sources must not be combined")
	}
}

func TestSupportedSetsFollowDevice(t *testing.T) {
	info, m := xboxDevice()
	keys := SupportedKeys(info.HasKey)
	needKeys := []int{AndroidButtonA, AndroidButtonL1, AndroidButtonStart, AndroidDpadUp}
	for _, k := range needKeys {
		found := false
		for _, g := range keys {
			if g == k {
				found = true
			}
		}
		if !found {
			t.Fatalf("supported keys must include %d: %v", k, keys)
		}
	}
	mots := SupportedMotions(m, info.HasAbs)
	for _, a := range []int{AndroidAxisX, AndroidAxisY, AndroidAxisZ, AndroidAxisRZ, AndroidAxisLTrigger, AndroidAxisRTrigger, AndroidAxisHatX, AndroidAxisHatY} {
		found := false
		for _, g := range mots {
			if g == a {
				found = true
			}
		}
		if !found {
			t.Fatalf("supported motions must include axis %d: %v", a, mots)
		}
	}
	// A stick-less trigger-less pad advertises neither.
	bare := SupportedMotions(Mapping{RightX: NoAxis, RightY: NoAxis, TriggerL: NoAxis, TriggerR: NoAxis}, map[uint16]bool{})
	if len(bare) != 0 {
		t.Fatalf("bare pad must advertise zero motions, got %v", bare)
	}
}

func gulikitCaps() (map[uint16]bool, map[uint16]AbsInfo) {
	has := map[uint16]bool{AbsX: true, AbsY: true, AbsRX: true, AbsRY: true, AbsZ: true, AbsRZ: true, AbsHat0X: true, AbsHat0Y: true}
	infos := map[uint16]AbsInfo{
		AbsX:     {Value: 31533, Minimum: 0, Maximum: 65535, Fuzz: 255, Flat: 4095},
		AbsY:     {Value: 35079, Minimum: 0, Maximum: 65535, Fuzz: 255, Flat: 4095},
		AbsRX:    {Value: 30737, Minimum: 0, Maximum: 65535, Fuzz: 255, Flat: 4095},
		AbsRY:    {Value: 34497, Minimum: 0, Maximum: 65535, Fuzz: 255, Flat: 4095},
		AbsZ:     {Value: 0, Minimum: 0, Maximum: 1023, Fuzz: 3, Flat: 63},
		AbsRZ:    {Value: 0, Minimum: 0, Maximum: 1023, Fuzz: 3, Flat: 63},
		AbsHat0X: {Minimum: -1, Maximum: 1},
		AbsHat0Y: {Minimum: -1, Maximum: 1},
	}
	return has, infos
}

func gulikitDevice() (DeviceInfo, Mapping) {
	hasAbs, infos := gulikitCaps()
	hasKey := map[uint16]bool{
		BtnSouth: true, BtnEast: true, BtnC: true, BtnNorth: true, BtnWest: true, BtnZ: true,
		BtnTL: true, BtnTR: true, BtnTL2: true, BtnTR2: true,
		KeyMenu: true,
	}
	info := DeviceInfo{
		Name: "GuliKit Controller XW",
		ID:   DeviceID{BusType: 5, Vendor: 0x045e, Product: 0x02e0},
		Abs:  infos, HasAbs: hasAbs, HasKey: hasKey,
	}
	m, _ := ResolveMappingForDevice(info)
	return info, m
}

// TestGuliKitUnsignedStickRest pins the live GuliKit Bluetooth Xbox node:
// 0..65535 sticks must rest at 0 (not ~0.5 trigger-normalized), Z/RZ stay
// 0..1 triggers, KEY_MENU is START, and HAT0 advertises DPAD keys.
func TestGuliKitUnsignedStickRest(t *testing.T) {
	info, m := gulikitDevice()
	if m.RightX != AbsRX || m.TriggerL != AbsZ {
		t.Fatalf("GuliKit must keep xpad topology, got right=%v/%v triggers=%v/%v", m.RightX, m.RightY, m.TriggerL, m.TriggerR)
	}
	r := NewReader(info.Abs)
	stream := [][]byte{
		EncodeInputEvent(EvAbs, AbsX, 31533),
		EncodeInputEvent(EvAbs, AbsY, 35079),
		EncodeInputEvent(EvAbs, AbsRX, 30737),
		EncodeInputEvent(EvAbs, AbsRY, 34497),
		EncodeInputEvent(EvAbs, AbsZ, 0),
		EncodeInputEvent(EvAbs, AbsRZ, 0),
		EncodeInputEvent(EvKey, KeyMenu, 1),
		EncodeInputEvent(EvSyn, SynReport, 0),
	}
	f := feedStream(t, r, stream)
	af := MapFrame(f, 1, m, info.Abs)
	if !af.Buttons[AndroidButtonStart] {
		t.Fatalf("KEY_MENU must map to BUTTON_START: %+v", af.Buttons)
	}
	for _, a := range []int{AndroidAxisX, AndroidAxisY, AndroidAxisZ, AndroidAxisRZ} {
		if v := af.Axes[a]; v != 0 {
			t.Fatalf("GuliKit rest axis %d must be 0, got %v (unsigned sticks were trigger-normalized)", a, v)
		}
	}
	if got := af.Ranges[AndroidAxisX].Flat; math.Abs(float64(got)-DefaultDeadzone) > 0.002 {
		t.Fatalf("GuliKit advertised stick flat = %v, want capped %v so engine |v|<=flat cannot eat gyro-to-stick", got, DefaultDeadzone)
	}
	// Gyro-scale right-stick mix: 3000 counts inside device flat 4095.
	r2 := NewReader(info.Abs)
	gyro := feedStream(t, r2, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 32767+3000),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	ag := MapFrame(gyro, 1, m, info.Abs)
	if math.Abs(float64(ag.Axes[AndroidAxisZ])) < 0.05 {
		t.Fatalf("GuliKit gyro-scale RX must reach AXIS_Z, got %v", ag.Axes[AndroidAxisZ])
	}
	// Toward geometric centre (the hard side with a mid-range origin):
	// RX rest 30737 +3000 and RY rest 34497 −3000 must both move.
	r3 := NewReader(info.Abs)
	hard := feedStream(t, r3, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 30737+3000),
		EncodeInputEvent(EvAbs, AbsRY, 34497-3000),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	ah := MapFrame(hard, 1, m, info.Abs)
	if ah.Axes[AndroidAxisZ] <= 0 || math.Abs(float64(ah.Axes[AndroidAxisZ])) < 0.05 {
		t.Fatalf("gyro yaw toward geo centre must reach +AXIS_Z, got %v", ah.Axes[AndroidAxisZ])
	}
	if ah.Axes[AndroidAxisRZ] >= 0 || math.Abs(float64(ah.Axes[AndroidAxisRZ])) < 0.05 {
		t.Fatalf("gyro pitch toward geo centre must reach −AXIS_RZ, got %v", ah.Axes[AndroidAxisRZ])
	}
	keys := SupportedKeysForDevice(info)
	need := []int{
		AndroidButtonA, AndroidButtonB, AndroidButtonX, AndroidButtonY,
		AndroidButtonL1, AndroidButtonR1, AndroidButtonThumbL, AndroidButtonThumbR,
		AndroidButtonStart, AndroidButtonSelect, AndroidDpadUp,
	}
	for _, k := range need {
		found := false
		for _, g := range keys {
			if g == k {
				found = true
			}
		}
		if !found {
			t.Fatalf("GuliKit supported keys must include %d: %v", k, keys)
		}
	}
	for _, banned := range []int{AndroidButtonC, AndroidButtonZ, AndroidButtonL2, AndroidButtonR2} {
		for _, g := range keys {
			if g == banned {
				t.Fatalf("GuliKit hid-linear must not advertise leftover %d: %v", banned, keys)
			}
		}
	}
}

func TestGuliKitHIDLinearButtons(t *testing.T) {
	info, m := gulikitDevice()
	if !m.HIDLinearButtons {
		t.Fatal("GuliKit hid-microsoft 10-button node must use hid-linear remap")
	}
	r := NewReader(info.Abs)
	stream := [][]byte{
		EncodeInputEvent(EvKey, BtnC, 1),
		EncodeInputEvent(EvKey, BtnWest, 1),
		EncodeInputEvent(EvKey, BtnZ, 1),
		EncodeInputEvent(EvKey, BtnTL, 1),
		EncodeInputEvent(EvKey, BtnTR, 1),
		EncodeInputEvent(EvKey, BtnTL2, 1),
		EncodeInputEvent(EvKey, BtnTR2, 1),
		EncodeInputEvent(EvSyn, SynReport, 0),
	}
	f := feedStream(t, r, stream)
	af := MapFrame(f, 1, m, info.Abs)
	want := []int{
		AndroidButtonX, AndroidButtonL1, AndroidButtonR1,
		AndroidButtonSelect, AndroidButtonStart,
		AndroidButtonThumbL, AndroidButtonThumbR,
	}
	for _, k := range want {
		if !af.Buttons[k] {
			t.Fatalf("hid-linear must map to Android %d, got %+v", k, af.Buttons)
		}
	}
	if af.Buttons[AndroidButtonC] || af.Buttons[AndroidButtonZ] || af.Buttons[AndroidButtonL2] {
		t.Fatalf("hid-linear leftover C/Z/L2 must be absent, got %+v", af.Buttons)
	}
}
