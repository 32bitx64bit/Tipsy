// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"math"
	"testing"
)

func stickAbs() map[uint16]AbsInfo {
	return map[uint16]AbsInfo{
		AbsX:     {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsY:     {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRX:    {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRY:    {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsZ:     {Minimum: 0, Maximum: 255},
		AbsRZ:    {Minimum: 0, Maximum: 255},
		AbsHat0X: {Minimum: -1, Maximum: 1},
		AbsHat0Y: {Minimum: -1, Maximum: 1},
	}
}

func TestParseEncodeRoundTrip(t *testing.T) {
	evs := []InputEvent{
		{Type: EvKey, Code: BtnSouth, Value: 1},
		{Type: EvAbs, Code: AbsX, Value: 16383},
		{Type: EvSyn, Code: SynReport, Value: 0},
	}
	var buf []byte
	for _, e := range evs {
		buf = append(buf, EncodeInputEvent(e.Type, e.Code, e.Value)...)
	}
	got := ParseInputEvents(buf)
	if len(got) != len(evs) {
		t.Fatalf("want %d events, got %d", len(evs), len(got))
	}
	for i, e := range evs {
		if got[i] != e {
			t.Fatalf("event %d: want %+v got %+v", i, e, got[i])
		}
	}
	// Trailing partial record is dropped.
	if g := ParseInputEvents(append(buf, 0x01, 0x02)); len(g) != len(evs) {
		t.Fatalf("partial tail must be dropped, got %d", len(g))
	}
}

func TestNormalizeStickCentreAndFlat(t *testing.T) {
	if v, _ := NormalizeStick(0, -32768, 32767, 255, 0); v != 0 {
		t.Fatalf("centre must be 0, got %v", v)
	}
	// Within flat (±255 of centre ~0) → 0.
	if v, _ := NormalizeStick(200, -32768, 32767, 255, 0); v != 0 {
		t.Fatalf("within flat must be 0, got %v", v)
	}
	// Half deflection: 16383/32767 ≈ 0.5 from rest 0 to max.
	v, fb := NormalizeStick(16383, -32768, 32767, 255, 0)
	if fb {
		t.Fatal("flat>0 must not use fallback")
	}
	if math.Abs(v-0.5) > 0.002 {
		t.Fatalf("half deflection ≈0.5, got %v", v)
	}
	if v, _ := NormalizeStick(32767, -32768, 32767, 255, 0); v != 1 {
		t.Fatalf("max must be 1, got %v", v)
	}
	if v, _ := NormalizeStick(-32768, -32768, 32767, 255, 0); v != -1 {
		t.Fatalf("min must be -1, got %v", v)
	}
	if v, _ := NormalizeStick(0, 5, 5, 0, 0); v != 0 {
		t.Fatalf("degenerate range must be 0, got %v", v)
	}
}

func TestNormalizeStickCapsOversizedFlat(t *testing.T) {
	// GuliKit unsigned 16-bit sticks: flat 4095 ≈ 12.5% would eat
	// firmware gyro-to-right-stick micro-aim. Cap at 0.08 (≈2621 raw).
	const min, max, flat int32 = 0, 65535, 4095
	centre := int32((min + max) / 2) // 32767
	if v, fb := NormalizeStick(centre, min, max, flat, 0); v != 0 || fb {
		t.Fatalf("rest must stay 0 without fallback, got %v,%v", v, fb)
	}
	// 3000 counts is inside device flat 4095 but outside the 0.08 cap.
	v, fb := NormalizeStick(centre+3000, min, max, flat, 0)
	if fb {
		t.Fatal("flat>0 must not use fallback")
	}
	if math.Abs(v) < 0.05 {
		t.Fatalf("gyro-scale deflection must survive the cap, got %v", v)
	}
	if f := StickFlatNormalized(min, max, flat); math.Abs(float64(f)-DefaultDeadzone) > 0.001 {
		t.Fatalf("advertised stick flat = %v, want capped %v", f, DefaultDeadzone)
	}
	if f := StickFlatNormalized(-32768, 32767, 255); f > 0.01 {
		t.Fatalf("small xpad flat must not be inflated, got %v", f)
	}
}

func TestNormalizeStickIdleRestBias(t *testing.T) {
	// Live GuliKit RY rests at 34497, not 32767. A mid-range deadzone
	// ate gyro toward centre (look down / look right) while the easy
	// side (look up) passed — "mostly up", side-to-side mushy.
	const min, max, flat, rest int32 = 0, 65535, 4095, 34497
	if v, _ := NormalizeStick(rest, min, max, flat, rest); v != 0 {
		t.Fatalf("idle rest must be 0, got %v", v)
	}
	down, _ := NormalizeStick(rest-3000, min, max, flat, rest)
	up, _ := NormalizeStick(rest+3000, min, max, flat, rest)
	if down >= 0 || math.Abs(down) < 0.05 {
		t.Fatalf("gyro toward geo centre must be negative, got %v", down)
	}
	if up <= 0 || math.Abs(up) < 0.05 {
		t.Fatalf("gyro away from geo centre must be positive, got %v", up)
	}
	if math.Abs(math.Abs(up)-math.Abs(down)) > 0.002 {
		t.Fatalf("equal-count gyro must have equal |n|, up=%v down=%v", up, down)
	}
	// Held stick at open must not become the origin.
	if v, _ := NormalizeStick(32767, min, max, flat, 50000); v != 0 {
		t.Fatalf("held-stick Value must not recenter, geo rest got %v", v)
	}
}

func TestNormalizeStickFallbackDeadzone(t *testing.T) {
	// flat==0 → small default deadzone (≈0.08 of half range ≈ 2621 raw).
	if v, fb := NormalizeStick(1000, -32768, 32767, 0, 0); v != 0 || !fb {
		t.Fatalf("small raw must be deadzoned with fallback, got %v,%v", v, fb)
	}
	if v, fb := NormalizeStick(16383, -32768, 32767, 0, 0); math.Abs(v-0.5) > 0.002 || !fb {
		t.Fatalf("half deflection must pass with fallback flag, got %v,%v", v, fb)
	}
}

func TestNormalizeTrigger(t *testing.T) {
	if v, _ := NormalizeTrigger(0, 0, 255, 0); v != 0 {
		t.Fatalf("released trigger must be 0, got %v", v)
	}
	v, _ := NormalizeTrigger(255, 0, 255, 0)
	if math.Abs(v-1) > 1e-9 {
		t.Fatalf("full pull must be 1, got %v", v)
	}
	v, _ = NormalizeTrigger(128, 0, 255, 0)
	if math.Abs(v-128.0/255) > 0.02 {
		t.Fatalf("half pull ≈0.5, got %v", v)
	}
	// flat edge: within flat → 0.
	if v, _ := NormalizeTrigger(3, 0, 255, 8); v != 0 {
		t.Fatalf("within flat must be 0, got %v", v)
	}
}

func TestIsTriggerAxis(t *testing.T) {
	if !IsTriggerAxis(AbsInfo{Minimum: 0, Maximum: 255}) {
		t.Fatal("0..255 must classify as trigger")
	}
	if IsTriggerAxis(AbsInfo{Minimum: -32768, Maximum: 32767}) {
		t.Fatal("symmetric range must classify as stick")
	}
	if IsTriggerAxis(AbsInfo{Minimum: -1, Maximum: 1}) {
		t.Fatal("hat -1..1 must not classify as trigger")
	}
	if IsTriggerAxis(AbsInfo{Minimum: 5, Maximum: 5}) {
		t.Fatal("degenerate range must not classify as trigger")
	}
	// GuliKit / xpadneo Bluetooth Xbox: unsigned 16-bit sticks, analog 10-bit triggers.
	if IsTriggerAxis(AbsInfo{Minimum: 0, Maximum: 65535, Value: 32768, Flat: 4095}) {
		t.Fatal("unsigned 16-bit stick must not classify as trigger")
	}
	if IsTriggerAxis(AbsInfo{Minimum: 0, Maximum: 65535}) {
		t.Fatal("unsigned 16-bit stick with unset value must still not classify as trigger")
	}
	if !IsTriggerAxis(AbsInfo{Minimum: 0, Maximum: 1023, Value: 0, Flat: 63}) {
		t.Fatal("0..1023 idle-at-min must classify as trigger")
	}
	if IsTriggerAxis(AbsInfo{Minimum: 0, Maximum: 255, Value: 128}) {
		t.Fatal("0..255 DualShock stick at centre must not classify as trigger")
	}
}

func TestNormalizeUnsignedStick(t *testing.T) {
	// GuliKit rest ~32768 on 0..65535 with flat 4095 → 0, not ~0.5.
	if v, _ := NormalizeStick(32768, 0, 65535, 4095, 0); v != 0 {
		t.Fatalf("unsigned stick centre must be 0, got %v", v)
	}
	if v, _ := NormalizeStick(31533, 0, 65535, 4095, 0); v != 0 {
		t.Fatalf("unsigned stick rest (~1.2k, inside 0.08 cap) must be 0, got %v", v)
	}
	if v, _ := NormalizeStick(0, 0, 65535, 4095, 0); v != -1 {
		t.Fatalf("unsigned stick min must be -1, got %v", v)
	}
	if v, _ := NormalizeStick(65535, 0, 65535, 4095, 0); v != 1 {
		t.Fatalf("unsigned stick max must be 1, got %v", v)
	}
}

func TestReaderButtonAndSynBoundary(t *testing.T) {
	r := NewReader(stickAbs())
	if f := r.Feed(InputEvent{Type: EvKey, Code: BtnSouth, Value: 1}); f != nil {
		t.Fatal("key press alone must not emit (no SYN yet)")
	}
	f := r.Feed(InputEvent{Type: EvSyn, Code: SynReport})
	if f == nil {
		t.Fatal("SYN_REPORT must emit a frame")
	}
	if !f.Buttons[BtnSouth] {
		t.Fatalf("BTN_SOUTH must be held, got %v", f.Buttons)
	}
	// Release + SYN → empty.
	r.Feed(InputEvent{Type: EvKey, Code: BtnSouth, Value: 0})
	f = r.Feed(InputEvent{Type: EvSyn, Code: SynReport})
	if f == nil || len(f.Buttons) != 0 {
		t.Fatalf("release must clear buttons, got %+v", f)
	}
}

func TestReaderIgnoresNonPadKeys(t *testing.T) {
	r := NewReader(stickAbs())
	r.Feed(InputEvent{Type: EvKey, Code: 0x1e, Value: 1}) // KEY_A, not a pad button
	f := r.Feed(InputEvent{Type: EvSyn, Code: SynReport})
	if f == nil {
		t.Fatal("SYN must still emit")
	}
	if len(f.Buttons) != 0 {
		t.Fatalf("keyboard keys must not enter pad buttons, got %v", f.Buttons)
	}
}

func TestReaderFallbackWarnedOnce(t *testing.T) {
	abs := map[uint16]AbsInfo{AbsX: {Minimum: -32768, Maximum: 32767, Flat: 0}}
	r := NewReader(abs)
	calls := 0
	r.Warn = func(string) { calls++ }
	r.Feed(InputEvent{Type: EvAbs, Code: AbsX, Value: 16383})
	for i := 0; i < 3; i++ {
		r.Feed(InputEvent{Type: EvSyn, Code: SynReport})
		r.Feed(InputEvent{Type: EvAbs, Code: AbsX, Value: 20000})
	}
	if calls != 1 {
		t.Fatalf("fallback must be logged exactly once, got %d", calls)
	}
}

func TestReaderAimAssistRecenter(t *testing.T) {
	info, m := gulikitDevice()
	r := NewReader(info.Abs)
	r.SetMapping(m)
	// Pose is 1263 counts off connect-time RX rest (30737). Without
	// recenter, 32000-3000 stays inside the 0.08 band of 30737.
	press := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 32000),
		EncodeInputEvent(EvAbs, AbsRY, 34497),
		EncodeInputEvent(EvKey, BtnWest, 1),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if press.Axes[AbsRX] != 0 {
		t.Fatalf("L1 press pose must be the new origin, got RX %v", press.Axes[AbsRX])
	}
	left := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 32000-3000),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if left.Axes[AbsRX] >= 0 || math.Abs(left.Axes[AbsRX]) < 0.05 {
		t.Fatalf("gyro left of L1-press origin must be negative, got %v", left.Axes[AbsRX])
	}
	right := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 32000+3000),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if right.Axes[AbsRX] <= 0 || math.Abs(right.Axes[AbsRX]) < 0.05 {
		t.Fatalf("gyro right of L1-press origin must be positive, got %v", right.Axes[AbsRX])
	}
	if math.Abs(math.Abs(left.Axes[AbsRX])-math.Abs(right.Axes[AbsRX])) > 0.002 {
		t.Fatalf("equal-count yaw must match, left=%v right=%v", left.Axes[AbsRX], right.Axes[AbsRX])
	}
}

func TestReaderAimAssistDoesNotInvertHeldStick(t *testing.T) {
	info, m := gulikitDevice()
	r := NewReader(info.Abs)
	r.SetMapping(m)
	// Physical aim right (RX 50000) then analog ZL: capturing that
	// pose as origin made firmware spring toward HID 32767 read as left.
	right := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 50000),
		EncodeInputEvent(EvAbs, AbsRY, 34497),
		EncodeInputEvent(EvAbs, AbsZ, 400),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if right.Axes[AbsRX] <= 0 {
		t.Fatalf("aim right + ZL must stay positive, got RX %v", right.Axes[AbsRX])
	}
	sprung := feedStream(t, r, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 32767),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if sprung.Axes[AbsRX] < 0 {
		t.Fatalf("spring toward HID centre must not invert right→left, got %v", sprung.Axes[AbsRX])
	}

	r2 := NewReader(info.Abs)
	r2.SetMapping(m)
	// Physical aim down (RY toward min) then ZL must not become look-up.
	down := feedStream(t, r2, [][]byte{
		EncodeInputEvent(EvAbs, AbsRX, 30737),
		EncodeInputEvent(EvAbs, AbsRY, 15000),
		EncodeInputEvent(EvAbs, AbsZ, 400),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if down.Axes[AbsRY] >= 0 {
		t.Fatalf("aim down + ZL must stay negative, got RY %v", down.Axes[AbsRY])
	}
	sprungY := feedStream(t, r2, [][]byte{
		EncodeInputEvent(EvAbs, AbsRY, 32767),
		EncodeInputEvent(EvSyn, SynReport, 0),
	})
	if sprungY.Axes[AbsRY] > 0 {
		t.Fatalf("spring toward HID centre must not invert down→up, got %v", sprungY.Axes[AbsRY])
	}
}

func TestDisconnectFrameZeroes(t *testing.T) {
	held := map[uint16]bool{BtnSouth: true, BtnStart: true}
	f := DisconnectFrame(held, []uint16{AbsX, AbsZ})
	if !f.Disconnect {
		t.Fatal("disconnect frame must be marked")
	}
	if len(f.Buttons) != 0 {
		t.Fatalf("disconnect must release all buttons, got %v", f.Buttons)
	}
	for c, v := range f.Axes {
		if v != 0 {
			t.Fatalf("axis %d must be zero, got %v", c, v)
		}
	}
	if len(f.Axes) != 2 {
		t.Fatalf("want 2 zeroed axes, got %v", f.Axes)
	}
}
