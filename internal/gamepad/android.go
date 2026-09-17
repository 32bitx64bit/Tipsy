// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import "sort"

// Android KeyEvent keycodes (android.view.KeyEvent).
const (
	AndroidDpadUp     = 19
	AndroidDpadDown   = 20
	AndroidDpadLeft   = 21
	AndroidDpadRight  = 22
	AndroidDpadCenter = 23

	AndroidButtonA      = 96
	AndroidButtonB      = 97
	AndroidButtonC      = 98
	AndroidButtonX      = 99
	AndroidButtonY      = 100
	AndroidButtonZ      = 101
	AndroidButtonL1     = 102
	AndroidButtonR1     = 103
	AndroidButtonL2     = 104
	AndroidButtonR2     = 105
	AndroidButtonThumbL = 106
	AndroidButtonThumbR = 107
	AndroidButtonStart  = 108
	AndroidButtonSelect = 109
	AndroidButtonMode   = 110
)

// Android MotionEvent axes (AMOTION_EVENT_AXIS_*).
const (
	AndroidAxisX        = 0
	AndroidAxisY        = 1
	AndroidAxisZ        = 11
	AndroidAxisRX       = 12
	AndroidAxisRY       = 13
	AndroidAxisRZ       = 14
	AndroidAxisHatX     = 15
	AndroidAxisHatY     = 16
	AndroidAxisLTrigger = 17
	AndroidAxisRTrigger = 18
	AndroidAxisGas      = 22
	AndroidAxisBrake    = 23
)

// HatDpadThreshold is the normalized hat deflection that also fires the
// DPAD_* key door (±0.5, matching real Android drivers where a fully
// deflected discrete hat reads ±1 and the key edge follows the same motion).
const HatDpadThreshold = 0.5

// Android input sources.
const (
	AndroidSourceGamepad  = 0x401
	AndroidSourceDpad     = 0x201
	AndroidSourceJoystick = 0x1000010
)

// EvdevButtonToAndroid maps positional evdev diamond/shoulders/system
// buttons to Android keycodes: BTN_SOUTH is BUTTON_A (Xbox A), east is B,
// west is X, north is Y. BTN_TL/TR are L1/R1, BTN_TL2/TR2 are L2/R2.
func EvdevButtonToAndroid(code uint16) (int, bool) {
	return MapEvdevButton(code, false)
}

// MapEvdevButton is EvdevButtonToAndroid with the hid-linear 10-button
// overlay used by hid-microsoft Xbox Bluetooth (GuliKit XW): kernel
// Button 1–10 occupy SOUTH,EAST,C,NORTH,WEST,Z,TL,TR,TL2,TR2, so C is X,
// WEST/Z are L1/R1, TL/TR are Select/Start, and TL2/TR2 are stick clicks.
func MapEvdevButton(code uint16, hidLinear bool) (int, bool) {
	if hidLinear {
		switch code {
		case BtnC:
			return AndroidButtonX, true
		case BtnWest:
			return AndroidButtonL1, true
		case BtnZ:
			return AndroidButtonR1, true
		case BtnTL:
			return AndroidButtonSelect, true
		case BtnTR:
			return AndroidButtonStart, true
		case BtnTL2:
			return AndroidButtonThumbL, true
		case BtnTR2:
			return AndroidButtonThumbR, true
		}
	}
	switch code {
	case BtnSouth:
		return AndroidButtonA, true
	case BtnEast:
		return AndroidButtonB, true
	case BtnC:
		return AndroidButtonC, true
	case BtnNorth:
		return AndroidButtonY, true
	case BtnWest:
		return AndroidButtonX, true
	case BtnZ:
		return AndroidButtonZ, true
	case BtnTL:
		return AndroidButtonL1, true
	case BtnTR:
		return AndroidButtonR1, true
	case BtnTL2:
		return AndroidButtonL2, true
	case BtnTR2:
		return AndroidButtonR2, true
	case BtnThumbl:
		return AndroidButtonThumbL, true
	case BtnThumbr:
		return AndroidButtonThumbR, true
	case BtnStart:
		return AndroidButtonStart, true
	case BtnSelect:
		return AndroidButtonSelect, true
	case BtnMode:
		return AndroidButtonMode, true
	case BtnDpadUp:
		return AndroidDpadUp, true
	case BtnDpadDown:
		return AndroidDpadDown, true
	case BtnDpadLeft:
		return AndroidDpadLeft, true
	case BtnDpadRight:
		return AndroidDpadRight, true
	case KeyMenu:
		return AndroidButtonStart, true
	case KeyBack:
		return AndroidButtonSelect, true
	}
	return 0, false
}

// MapEvdevButtonWithFaceButtonLayout maps the positional evdev code and then
// applies the selected labelled diamond. Switch-labelled pads place B/A at
// south/east and Y/X at west/north, the inverse of Android's Xbox-positioned
// BUTTON_A/B/X/Y semantics. Shoulders, sticks, system, and D-pad controls are
// deliberately unchanged.
func MapEvdevButtonWithFaceButtonLayout(code uint16, hidLinear bool, layout FaceButtonLayout) (int, bool) {
	key, ok := MapEvdevButton(code, hidLinear)
	if !ok || NormalizeFaceButtonLayout(layout) != FaceButtonLayoutSwitch {
		return key, ok
	}
	switch key {
	case AndroidButtonA:
		return AndroidButtonB, true
	case AndroidButtonB:
		return AndroidButtonA, true
	case AndroidButtonX:
		return AndroidButtonY, true
	case AndroidButtonY:
		return AndroidButtonX, true
	default:
		return key, true
	}
}

// MotionRange is the honest per-axis range served for
// InputDevice.getMotionRange(axis): device min/max/flat, never zeros for
// unreported axes (unreported axes are simply absent from the map).
type MotionRange struct {
	Axis int32
	Min  float32
	Max  float32
	Flat float32
}

// AndroidFrame is the evdev→Android translation of one normalized Frame:
// pressed Android keycodes plus float axes plus honest ranges.
type AndroidFrame struct {
	DeviceID int
	// Buttons maps Android keyCode → pressed.
	Buttons map[int]bool
	// Axes maps Android axis → value: sticks -1..1, triggers 0..1,
	// hats -1..1.
	Axes map[int]float32
	// Ranges holds honest MotionRanges for reported axes only. It is a compact,
	// independently owned slice because Android has a small fixed axis topology.
	Ranges []MotionRange
	// Disconnect mirrors Frame.Disconnect.
	Disconnect bool
}

// MapFrame translates a normalized reader Frame to Android vocabulary
// and returns a freshly allocated AndroidFrame. Snapshot callers
// (DisconnectSnapshotFor, tests that keep first then next) may retain or
// mutate the result after the next SYN_REPORT; successive MapFrame
// results never alias.
//
// Hot-path consumers that copy edges before return must use MapFrameInto
// so Buttons/Axes/Ranges are cleared and refilled in place.
//
//   - ABS_X/Y → AXIS_X/Y (0/1).
//   - Physical right stick (mapping.RightX/RightY) → both Z(11)/RZ(14)
//     and the RX(12)/RY(13) mirror from the same sample. The engine reads
//     Z/RZ (Phase 0 §2); the mirror costs nothing and covers builds that
//     read RX/RY.
//   - Analog triggers (mapping.TriggerL/R) → AXIS_L/RTRIGGER (17/18).
//   - HAT0X/Y or DPAD buttons → both HAT_X/Y axes and DPAD_* keys
//     (duality, matching real Android drivers).
//   - BTN_TL2/TR2 edges → BUTTON_L2/R2 keys alongside the analog axes.
//   - Face buttons use f.FaceButtonLayout (Xbox by default; Switch swaps
//     only A/B and X/Y).
func MapFrame(f *Frame, deviceID int, m Mapping, infos map[uint16]AbsInfo) AndroidFrame {
	return MapFrameInto(nil, f, deviceID, m, infos)
}

// MapFrameInto is MapFrame for a reused destination. It clears and fills
// dst.Buttons, dst.Axes, and dst.Ranges (growing slice cap when needed).
// dst's containers are recycled: a retained copy of those maps or the
// range slice aliases the next call. Nil dst allocates like MapFrame.
//
// The JNI pump keeps one scratch AndroidFrame per pump goroutine and
// copies edges in handleGamepadFrame before return.
func MapFrameInto(dst *AndroidFrame, f *Frame, deviceID int, m Mapping, infos map[uint16]AbsInfo) AndroidFrame {
	var fresh AndroidFrame
	if dst == nil {
		dst = &fresh
	}
	prepareAndroidFrame(dst, f, deviceID)
	if f == nil {
		return *dst
	}
	dst.Disconnect = f.Disconnect

	for code := range f.Buttons {
		if key, ok := MapEvdevButtonWithFaceButtonLayout(code, m.HIDLinearButtons, f.FaceButtonLayout); ok {
			dst.Buttons[key] = true
		}
	}

	axes := f.Axes
	if v, ok := axes[AbsX]; ok {
		dst.Axes[AndroidAxisX] = float32(v)
		dst.Ranges = setRange(dst.Ranges, AndroidAxisX, infos, AbsX, -1, 1)
	}
	if v, ok := axes[AbsY]; ok {
		dst.Axes[AndroidAxisY] = float32(v)
		dst.Ranges = setRange(dst.Ranges, AndroidAxisY, infos, AbsY, -1, 1)
	}

	// Right stick → Z/RZ + RX/RY mirror.
	if m.RightX != NoAxis && m.RightY != NoAxis {
		if vx, ok := axes[m.RightX]; ok {
			dst.Axes[AndroidAxisZ] = float32(vx)
			dst.Axes[AndroidAxisRX] = float32(vx)
			dst.Ranges = setRange(dst.Ranges, AndroidAxisZ, infos, m.RightX, -1, 1)
			dst.Ranges = setRange(dst.Ranges, AndroidAxisRX, infos, m.RightX, -1, 1)
		}
		if vy, ok := axes[m.RightY]; ok {
			dst.Axes[AndroidAxisRZ] = float32(vy)
			dst.Axes[AndroidAxisRY] = float32(vy)
			dst.Ranges = setRange(dst.Ranges, AndroidAxisRZ, infos, m.RightY, -1, 1)
			dst.Ranges = setRange(dst.Ranges, AndroidAxisRY, infos, m.RightY, -1, 1)
		}
	}

	// Analog triggers → L/RTRIGGER.
	if m.TriggerL != NoAxis {
		if v, ok := axes[m.TriggerL]; ok {
			dst.Axes[AndroidAxisLTrigger] = float32(v)
			dst.Ranges = setRange(dst.Ranges, AndroidAxisLTrigger, infos, m.TriggerL, 0, 1)
		}
	}
	if m.TriggerR != NoAxis {
		if v, ok := axes[m.TriggerR]; ok {
			dst.Axes[AndroidAxisRTrigger] = float32(v)
			dst.Ranges = setRange(dst.Ranges, AndroidAxisRTrigger, infos, m.TriggerR, 0, 1)
		}
	}

	// Hat/DPAD duality: hat axes drive HAT_X/Y plus DPAD keys at ±0.5;
	// DPAD button keys (already in Buttons) additionally drive the HAT
	// axes so both doors fire from one physical motion.
	hatX, hasHatX := axes[AbsHat0X]
	hatY, hasHatY := axes[AbsHat0Y]
	if hasHatX {
		dst.Axes[AndroidAxisHatX] = float32(hatX)
		dst.Ranges = setRange(dst.Ranges, AndroidAxisHatX, infos, AbsHat0X, -1, 1)
	}
	if hasHatY {
		dst.Axes[AndroidAxisHatY] = float32(hatY)
		dst.Ranges = setRange(dst.Ranges, AndroidAxisHatY, infos, AbsHat0Y, -1, 1)
	}
	if hasHatX || hasHatY {
		if hatX < -HatDpadThreshold {
			dst.Buttons[AndroidDpadLeft] = true
		} else if hatX > HatDpadThreshold {
			dst.Buttons[AndroidDpadRight] = true
		}
		if hatY < -HatDpadThreshold {
			dst.Buttons[AndroidDpadUp] = true
		} else if hatY > HatDpadThreshold {
			dst.Buttons[AndroidDpadDown] = true
		}
	}
	// DPAD buttons → HAT axes (only when the hat axes are absent, so a
	// real hat keeps its analog value).
	if !hasHatX {
		hx := 0.0
		if dst.Buttons[AndroidDpadLeft] {
			hx = -1
		} else if dst.Buttons[AndroidDpadRight] {
			hx = 1
		}
		if dst.Buttons[AndroidDpadLeft] || dst.Buttons[AndroidDpadRight] {
			dst.Axes[AndroidAxisHatX] = float32(hx)
		}
	}
	if !hasHatY {
		hy := 0.0
		if dst.Buttons[AndroidDpadUp] {
			hy = -1
		} else if dst.Buttons[AndroidDpadDown] {
			hy = 1
		}
		if dst.Buttons[AndroidDpadUp] || dst.Buttons[AndroidDpadDown] {
			dst.Axes[AndroidAxisHatY] = float32(hy)
		}
	}
	return *dst
}

func prepareAndroidFrame(dst *AndroidFrame, f *Frame, deviceID int) {
	buttonCap, axisCap := 0, 0
	if f != nil {
		// Android translation can add at most the two hat-derived DPAD keys
		// beyond the normalized button snapshot, and only the physical right
		// stick expands its two source axes into the Z/RZ + RX/RY mirrors.
		// Reserve the bounded topology up front so a first fill (or a
		// capacity-short reuse) does not grow maps or the compact range
		// slice while it is being populated.
		buttonCap = len(f.Buttons) + 2
		axisCap = len(f.Axes) + 2
	}
	dst.DeviceID = deviceID
	dst.Disconnect = false
	if dst.Buttons == nil {
		dst.Buttons = make(map[int]bool, buttonCap)
	} else {
		clear(dst.Buttons)
	}
	if dst.Axes == nil {
		dst.Axes = make(map[int]float32, axisCap)
	} else {
		clear(dst.Axes)
	}
	if cap(dst.Ranges) < axisCap {
		dst.Ranges = make([]MotionRange, 0, axisCap)
	} else {
		dst.Ranges = dst.Ranges[:0]
	}
}

// Range returns the honest MotionRange for an Android axis, if that physical
// axis was reported in this frame.
func (f AndroidFrame) Range(androidAxis int) (MotionRange, bool) {
	for _, r := range f.Ranges {
		if int(r.Axis) == androidAxis {
			return r, true
		}
	}
	return MotionRange{}, false
}

// setRange records the honest device range for an Android axis. Flat is
// normalized to the axis scale (stick half-range, trigger span) because
// Android reports flat in axis units while evdev reports raw units.
// Stick flats are capped at DefaultDeadzone so an oversized EVIOCGABS
// flat cannot make the engine's |v|<=flat gate swallow gyro-to-stick.
func setRange(ranges []MotionRange, androidAxis int, infos map[uint16]AbsInfo, evdevCode uint16, lo, hi float32) []MotionRange {
	info, ok := infos[evdevCode]
	if !ok || info.Maximum <= info.Minimum {
		return ranges
	}
	var flat float32
	span := float64(info.Maximum - info.Minimum)
	if span > 0 && info.Flat > 0 {
		if IsTriggerAxis(info) {
			flat = float32(float64(info.Flat) / span)
		} else {
			flat = StickFlatNormalized(info.Minimum, info.Maximum, info.Flat)
		}
	}
	r := MotionRange{Axis: int32(androidAxis), Min: lo, Max: hi, Flat: flat}
	for i := range ranges {
		if int(ranges[i].Axis) == androidAxis {
			ranges[i] = r
			return ranges
		}
	}
	return append(ranges, r)
}

// SupportedKeys returns the Android keycodes advertised for the capability
// setters (nativeSetGamepadSupportedKeyWithGamepadType): the engine probe
// set H[] = A/B/X/Y, DPAD 19-22, R1/L1, THUMBL/R, SELECT/START, plus L2/R2
// and MODE when the device physically offers them. KEY_MENU/KEY_BACK are
// treated as START/SELECT for Bluetooth pads that do not expose BTN_START.
func SupportedKeys(hasKey map[uint16]bool) []int {
	return supportedKeys(hasKey, false, false)
}

// SupportedKeysForDevice is the connect-time set: SupportedKeys plus DPAD
// 19–22 when HAT0 is present, so hat-synthesized DPAD keys are advertised.
func SupportedKeysForDevice(info DeviceInfo) []int {
	m, _ := ResolveMappingForDevice(info)
	return supportedKeys(info.HasKey, info.HasAbs[AbsHat0X] || info.HasAbs[AbsHat0Y], m.HIDLinearButtons)
}

func supportedKeys(hasKey map[uint16]bool, hatDPAD, hidLinear bool) []int {
	set := make(map[int]bool)
	for _, ev := range []uint16{BtnSouth, BtnEast, BtnWest, BtnNorth, BtnDpadUp, BtnDpadDown, BtnDpadLeft, BtnDpadRight, BtnTR, BtnTL, BtnThumbl, BtnThumbr, BtnSelect, BtnStart, KeyMenu, KeyBack, BtnTL2, BtnTR2, BtnMode, BtnC, BtnZ} {
		if !hasKey[ev] {
			continue
		}
		if key, ok := MapEvdevButton(ev, hidLinear); ok {
			set[key] = true
		}
	}
	if hatDPAD {
		for _, k := range []int{AndroidDpadUp, AndroidDpadDown, AndroidDpadLeft, AndroidDpadRight} {
			set[k] = true
		}
	}
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// SupportedMotions returns the Android axes advertised for the capability
// setters (nativeSetGamepadSupportedMotionWithGamepadType), derived from
// the mapping the device resolved to: X/Y always when present, right-stick
// Z/RZ (+RX/RY mirror), triggers, hats.
func SupportedMotions(m Mapping, hasAbs map[uint16]bool) []int {
	set := make(map[int]bool)
	if hasAbs[AbsX] {
		set[AndroidAxisX] = true
	}
	if hasAbs[AbsY] {
		set[AndroidAxisY] = true
	}
	if m.RightX != NoAxis && m.RightY != NoAxis {
		set[AndroidAxisZ] = true
		set[AndroidAxisRZ] = true
		set[AndroidAxisRX] = true
		set[AndroidAxisRY] = true
	}
	if m.TriggerL != NoAxis {
		set[AndroidAxisLTrigger] = true
	}
	if m.TriggerR != NoAxis {
		set[AndroidAxisRTrigger] = true
	}
	if hasAbs[AbsHat0X] {
		set[AndroidAxisHatX] = true
	}
	if hasAbs[AbsHat0Y] {
		set[AndroidAxisHatY] = true
	}
	out := make([]int, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Ints(out)
	return out
}

// KeySource returns the truthful Android source for a button keycode:
// DPAD keys use SOURCE_DPAD, gamepad buttons use SOURCE_GAMEPAD.
// It reports truthful sources and never combines them.
func KeySource(keyCode int) int {
	switch keyCode {
	case AndroidDpadUp, AndroidDpadDown, AndroidDpadLeft, AndroidDpadRight, AndroidDpadCenter:
		return AndroidSourceDpad
	}
	return AndroidSourceGamepad
}
