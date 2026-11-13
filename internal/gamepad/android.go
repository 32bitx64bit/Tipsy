// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package gamepad

import "sort"

// Android KeyEvent keycodes.
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

// Android MotionEvent axes.
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
// DPAD_* key door.
const HatDpadThreshold = 0.5

const (
	AndroidSourceGamepad  = 0x401
	AndroidSourceDpad     = 0x201
	AndroidSourceJoystick = 0x1000010
)

// EvdevButtonToAndroid maps positional evdev diamond/shoulder/system buttons
// to Android keycodes: BTN_SOUTH is BUTTON_A, east is B, west is X, north is
// Y, and BTN_TL/TR/TL2/TR2 are L1/R1/L2/R2.
func EvdevButtonToAndroid(code uint16) (int, bool) {
	return MapEvdevButton(code, false)
}

// MapEvdevButton is EvdevButtonToAndroid with the hid-linear 10-button
// overlay: C is X, WEST/Z are L1/R1, TL/TR are Select/Start, and TL2/TR2 are
// stick clicks.
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
// applies the selected labelled diamond. Switch-labelled pads swap A/B and
// X/Y; shoulders, sticks, system, and D-pad controls are unchanged.
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

// MotionRange is the per-axis range served for InputDevice.getMotionRange:
// device min/max/flat. Unreported axes are absent, never zeros.
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
	Buttons  map[int]bool
	// Axes maps Android axis → value: sticks -1..1, triggers 0..1,
	// hats -1..1.
	Axes map[int]float32
	// Ranges holds MotionRanges for reported axes only; it is independently
	// owned.
	Ranges     []MotionRange
	Disconnect bool

	// rangeMeta caches the stable range metadata for the mapping and device
	// ranges last used through this destination. MapFrameInto fills it
	// lazily; independent MapFrame results leave it nil.
	rangeMeta *rangeMeta
}

// MapFrame translates a normalized reader Frame to Android vocabulary.
// The result never aliases a later result; hot-path consumers must use
// MapFrameInto, which recycles its containers.
func MapFrame(f *Frame, deviceID int, m Mapping, infos map[uint16]AbsInfo) AndroidFrame {
	return MapFrameInto(nil, f, deviceID, m, infos)
}

// MapFrameInto is MapFrame for a reused destination. It clears and fills
// dst.Buttons, dst.Axes, and dst.Ranges. A retained copy of those containers
// aliases the next call; nil dst allocates like MapFrame.
func MapFrameInto(dst *AndroidFrame, f *Frame, deviceID int, m Mapping, infos map[uint16]AbsInfo) AndroidFrame {
	var fresh AndroidFrame
	reuse := dst != nil
	if !reuse {
		dst = &fresh
	}
	prepareAndroidFrame(dst, f, deviceID)
	if f == nil {
		return *dst
	}
	dst.Disconnect = f.Disconnect

	var meta *rangeMeta
	if reuse {
		meta = dst.rangeMeta
		if meta == nil {
			meta = new(rangeMeta)
			dst.rangeMeta = meta
		}
		meta.refresh(m, infos)
	}

	for code := range f.Buttons {
		if key, ok := MapEvdevButtonWithFaceButtonLayout(code, m.HIDLinearButtons, f.FaceButtonLayout); ok {
			dst.Buttons[key] = true
		}
	}

	axes := f.Axes
	if v, ok := axes[AbsX]; ok {
		dst.Axes[AndroidAxisX] = float32(v)
		dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisX, rangeSlotX, AbsX, infos, -1, 1)
	}
	if v, ok := axes[AbsY]; ok {
		dst.Axes[AndroidAxisY] = float32(v)
		dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisY, rangeSlotY, AbsY, infos, -1, 1)
	}

	if m.RightX != NoAxis && m.RightY != NoAxis {
		if vx, ok := axes[m.RightX]; ok {
			dst.Axes[AndroidAxisZ] = float32(vx)
			dst.Axes[AndroidAxisRX] = float32(vx)
			dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisZ, rangeSlotRightX, m.RightX, infos, -1, 1)
			dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisRX, rangeSlotRightX, m.RightX, infos, -1, 1)
		}
		if vy, ok := axes[m.RightY]; ok {
			dst.Axes[AndroidAxisRZ] = float32(vy)
			dst.Axes[AndroidAxisRY] = float32(vy)
			dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisRZ, rangeSlotRightY, m.RightY, infos, -1, 1)
			dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisRY, rangeSlotRightY, m.RightY, infos, -1, 1)
		}
	}

	if m.TriggerL != NoAxis {
		if v, ok := axes[m.TriggerL]; ok {
			dst.Axes[AndroidAxisLTrigger] = float32(v)
			dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisLTrigger, rangeSlotTriggerL, m.TriggerL, infos, 0, 1)
		}
	}
	if m.TriggerR != NoAxis {
		if v, ok := axes[m.TriggerR]; ok {
			dst.Axes[AndroidAxisRTrigger] = float32(v)
			dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisRTrigger, rangeSlotTriggerR, m.TriggerR, infos, 0, 1)
		}
	}

	// Hat axes and DPAD buttons each drive both the HAT_* axes and the
	// DPAD_* keys.
	hatX, hasHatX := axes[AbsHat0X]
	hatY, hasHatY := axes[AbsHat0Y]
	if hasHatX {
		dst.Axes[AndroidAxisHatX] = float32(hatX)
		dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisHatX, rangeSlotHatX, AbsHat0X, infos, -1, 1)
	}
	if hasHatY {
		dst.Axes[AndroidAxisHatY] = float32(hatY)
		dst.Ranges = appendFrameRange(dst.Ranges, meta, AndroidAxisHatY, rangeSlotHatY, AbsHat0Y, infos, -1, 1)
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
	// DPAD buttons drive the HAT axes only when the hat axes are absent.
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
		// +2 covers the hat-derived DPAD keys and the right-stick RX/RY
		// mirror.
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

// Range returns the MotionRange for an Android axis, if that physical axis
// was reported in this frame.
func (f AndroidFrame) Range(androidAxis int) (MotionRange, bool) {
	for _, r := range f.Ranges {
		if int(r.Axis) == androidAxis {
			return r, true
		}
	}
	return MotionRange{}, false
}

// Range metadata slots mirror the physical axes MapFrameInto reads. Ranges
// depend only on the mapping and the connect-time device ranges, so a reused
// destination keeps them across frames.
const (
	rangeSlotX = iota
	rangeSlotY
	rangeSlotRightX
	rangeSlotRightY
	rangeSlotTriggerL
	rangeSlotTriggerR
	rangeSlotHatX
	rangeSlotHatY
	rangeSlotCount
)

type rangeMeta struct {
	ready   bool
	mapping Mapping
	codes   [rangeSlotCount]uint16
	infos   [rangeSlotCount]AbsInfo
	flat    [rangeSlotCount]float32
	valid   [rangeSlotCount]bool
}

// refresh rebuilds the cached flat values when the mapping or any source
// AbsInfo changes; otherwise the cached metadata is reused as-is.
func (rm *rangeMeta) refresh(m Mapping, infos map[uint16]AbsInfo) {
	if rm.ready && rm.mapping == m {
		matches := true
		for i := 0; i < rangeSlotCount; i++ {
			info, ok := infos[rm.codes[i]]
			if ok != rm.valid[i] || (ok && info != rm.infos[i]) {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}
	rm.mapping = m
	rm.codes = [rangeSlotCount]uint16{
		AbsX, AbsY, m.RightX, m.RightY,
		m.TriggerL, m.TriggerR, AbsHat0X, AbsHat0Y,
	}
	for i, code := range rm.codes {
		info, ok := infos[code]
		if !ok || info.Maximum <= info.Minimum {
			rm.infos[i] = AbsInfo{}
			rm.flat[i] = 0
			rm.valid[i] = false
			continue
		}
		rm.infos[i] = info
		rm.flat[i] = rangeFlat(info)
		rm.valid[i] = true
	}
	rm.ready = true
}

// rangeFlat normalizes the device flat to the axis scale because evdev reports
// raw units. Stick flats are capped at DefaultDeadzone so an oversized device
// flat cannot swallow small deflections.
func rangeFlat(info AbsInfo) float32 {
	if info.Flat <= 0 {
		return 0
	}
	span := float64(info.Maximum - info.Minimum)
	if IsTriggerAxis(info) {
		return float32(float64(info.Flat) / span)
	}
	return StickFlatNormalized(info.Minimum, info.Maximum, info.Flat)
}

// appendRangeMeta records the cached device range for an Android axis,
// replacing an existing entry for the same axis.
func appendRangeMeta(ranges []MotionRange, androidAxis int, meta *rangeMeta, slot int, lo, hi float32) []MotionRange {
	if !meta.valid[slot] {
		return ranges
	}
	r := MotionRange{Axis: int32(androidAxis), Min: lo, Max: hi, Flat: meta.flat[slot]}
	for i := range ranges {
		if int(ranges[i].Axis) == androidAxis {
			ranges[i] = r
			return ranges
		}
	}
	return append(ranges, r)
}

// appendFrameRange uses cached metadata when the destination is reused, and
// falls back to direct computation for an independent MapFrame result.
func appendFrameRange(ranges []MotionRange, meta *rangeMeta, androidAxis, slot int, evdevCode uint16, infos map[uint16]AbsInfo, lo, hi float32) []MotionRange {
	if meta != nil {
		return appendRangeMeta(ranges, androidAxis, meta, slot, lo, hi)
	}
	return setRange(ranges, androidAxis, infos, evdevCode, lo, hi)
}

// setRange records the device range for an Android axis. Flat is normalized
// to the axis scale because evdev reports raw units. Stick flats are capped
// at DefaultDeadzone so an oversized device flat cannot swallow small
// deflections.
func setRange(ranges []MotionRange, androidAxis int, infos map[uint16]AbsInfo, evdevCode uint16, lo, hi float32) []MotionRange {
	info, ok := infos[evdevCode]
	if !ok || info.Maximum <= info.Minimum {
		return ranges
	}
	r := MotionRange{Axis: int32(androidAxis), Min: lo, Max: hi, Flat: rangeFlat(info)}
	for i := range ranges {
		if int(ranges[i].Axis) == androidAxis {
			ranges[i] = r
			return ranges
		}
	}
	return append(ranges, r)
}

// SupportedKeys returns the Android keycodes advertised for the capability
// setters (nativeSetGamepadSupportedKeyWithGamepadType), including L2/R2
// and MODE when the device physically offers them. KEY_MENU/KEY_BACK are
// treated as START/SELECT for pads that do not expose BTN_START.
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
// the mapping the device resolved to.
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
func KeySource(keyCode int) int {
	switch keyCode {
	case AndroidDpadUp, AndroidDpadDown, AndroidDpadLeft, AndroidDpadRight, AndroidDpadCenter:
		return AndroidSourceDpad
	}
	return AndroidSourceGamepad
}
