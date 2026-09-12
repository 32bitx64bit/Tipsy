// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

// DefaultDeadzone is the normalized fallback deadzone applied when a
// device reports flat==0 for a stick axis. It is also the cap applied
// to an oversized EVIOCGABS flat: GuliKit 4095 on 0..65535 is ≈12.5%,
// which swallows firmware gyro-to-right-stick micro-aim.
const DefaultDeadzone = 0.08

// NormalizeStick maps a raw stick sample to -1..1 using the honest
// device min/max, with an evdev flat deadzone around centre. rest is the
// connect-time EVIOCGABS value: when it sits inside the idle band around
// geometric mid-range it becomes the deadzone origin (GuliKit unsigned
// sticks rest 1–2k off 32767; a mid-range origin made gyro pitch one-sided
// and ate yaw toward centre). A rest outside that band is a held stick
// at open and is ignored. rest==0 on an unsigned range is treated as unset.
//
// Gain is symmetric: n=(raw-centre)/half with half=(max-min)/2. Per-side
// min/max spans made the short side (GuliKit up / the biased yaw side)
// stronger and turned a rest-crossing overshoot into a bounce. Samples
// with |raw-centre|<=deadRaw report 0, where deadRaw=min(flat,
// DefaultDeadzone*half) when flat>0 else DefaultDeadzone*half.
//
// The second return reports whether the fallback deadzone was used.
func NormalizeStick(raw, minimum, maximum, flat, rest int32) (float64, bool) {
	return normalizeStick(raw, minimum, maximum, flat, rest, false)
}

func normalizeStick(raw, minimum, maximum, flat, rest int32, forceRest bool) (float64, bool) {
	if maximum <= minimum {
		return 0, false
	}
	half := float64(maximum-minimum) / 2
	if half <= 0 {
		return 0, false
	}
	var centre float64
	if forceRest && rest >= minimum && rest <= maximum && rest > minimum {
		centre = float64(rest)
	} else {
		centre = stickCentre(minimum, maximum, flat, rest)
	}
	deadRaw, fallback := stickDeadRaw(half, flat)
	if absF(float64(raw)-centre) <= deadRaw {
		return 0, fallback
	}
	n := (float64(raw) - centre) / half
	if raw == maximum {
		n = 1
	} else if raw == minimum {
		n = -1
	} else if n > 1 {
		n = 1
	} else if n < -1 {
		n = -1
	}
	return n, fallback
}

// stickCentre is geometric mid-range unless rest looks like an idle
// bias (inside the capped deadband of mid-range, and not pegged at min).
func stickCentre(minimum, maximum, flat, rest int32) float64 {
	geo := float64(minimum+maximum) / 2
	if rest <= minimum || rest > maximum {
		return geo
	}
	half := float64(maximum-minimum) / 2
	dead, _ := stickDeadRaw(half, flat)
	if absF(float64(rest)-geo) <= dead {
		return float64(rest)
	}
	return geo
}

// stickDeadRaw is the raw-unit stick deadband: device flat when it is
// positive and at most DefaultDeadzone of half-range, else the 0.08
// fallback. fallback is true only when flat==0.
func stickDeadRaw(half float64, flat int32) (float64, bool) {
	cap := DefaultDeadzone * half
	if flat > 0 {
		dead := float64(flat)
		if dead > cap {
			dead = cap
		}
		return dead, false
	}
	return cap, true
}

// StickFlatNormalized is the Android MotionRange.flat for a stick:
// device flat in axis units, capped at DefaultDeadzone so the engine's
// |v|<=flat gate cannot eat gyro-to-stick micro-aim.
func StickFlatNormalized(minimum, maximum, flat int32) float32 {
	if maximum <= minimum || flat <= 0 {
		return 0
	}
	half := float64(maximum-minimum) / 2
	if half <= 0 {
		return 0
	}
	n := float64(flat) / half
	if n > DefaultDeadzone {
		n = DefaultDeadzone
	}
	return float32(n)
}

// NormalizeTrigger maps a raw trigger sample to 0..1 using the honest
// device min/max. Samples with (raw-min)<=flat report 0. The fallback
// return mirrors NormalizeStick (true when flat==0 and raw is above the
// small default deadzone edge is still reported honestly; callers log once).
func NormalizeTrigger(raw, minimum, maximum, flat int32) (float64, bool) {
	if maximum <= minimum {
		return 0, false
	}
	span := float64(maximum - minimum)
	deadRaw := float64(flat)
	fallback := false
	if deadRaw <= 0 {
		deadRaw = DefaultDeadzone * span
		fallback = true
	}
	if float64(raw-minimum) <= deadRaw {
		return 0, fallback
	}
	t := float64(raw-minimum) / span
	if t > 1 {
		t = 1
	} else if t < 0 {
		t = 0
	}
	return t, fallback
}

// IsTriggerAxis reports whether an ABS axis is trigger-shaped rather than
// a stick or hat.
//
// Signed ranges (min < 0) are sticks or hats. Unsigned 16-bit sticks
// (span > 4096, e.g. GuliKit / xpadneo Bluetooth Xbox pads at 0..65535)
// rest near centre and must use NormalizeStick — treating them as
// triggers maps rest to ~0.5 and makes the pad look stuck/glitchy.
// Remaining small unsigned ranges (0..255, 0..1023) are triggers when
// they rest at minimum, and DualShock-style 0..255 sticks when they
// rest near centre. An unset Value (0) on a 0..255 axis matches trigger
// idle, which keeps the historical 0..255-is-trigger classification.
func IsTriggerAxis(info AbsInfo) bool {
	if info.Maximum <= info.Minimum {
		return false
	}
	if info.Minimum < 0 {
		return false
	}
	span := info.Maximum - info.Minimum
	if span > 4096 {
		return false
	}
	slack := info.Flat
	if slack <= 0 {
		slack = int32(float64(span)*DefaultDeadzone + 0.5)
		if slack < 1 {
			slack = 1
		}
	}
	rest := info.Value - info.Minimum
	if rest < 0 {
		rest = -rest
	}
	return rest <= slack*2
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Reader holds per-device raw state and normalizes ABS samples with the
// honest EVIOCGABS ranges. It is pure Go: Feed consumes decoded events and
// emits a Frame on each SYN_REPORT. No syscalls here.
type Reader struct {
	Abs map[uint16]AbsInfo

	held map[uint16]bool
	raw  map[uint16]int32

	// mapping selects aim-assist hold (L1 / LT). Zero value means xpad L1
	// (BTN_TL) only; hid-linear pads must SetMapping so WEST is L1.
	mapping Mapping
	// aimRest is the right-stick origin captured on the rising edge of
	// L1/LT (GuliKit motion assist) when that axis is still at rest.
	// A stick already aimed is left on connect-time rest so ADS/ZL
	// cannot invert right→left or down→up. Cleared on release.
	aimRest map[uint16]int32
	aimHeld bool

	// Warn, when non-nil, receives at most one fallback-deadzone notice.
	// The live hotplug manager wires this to a content-free log line.
	Warn           func(msg string)
	warnedFallback bool
}

// NewReader returns a Reader backed by the device's ABS ranges.
func NewReader(abs map[uint16]AbsInfo) *Reader {
	cp := make(map[uint16]AbsInfo, len(abs))
	for c, i := range abs {
		cp[c] = i
	}
	return &Reader{Abs: cp, held: make(map[uint16]bool), raw: make(map[uint16]int32)}
}

// SetMapping installs the connect-time topology (aim-assist L1/LT and
// right-stick codes). The hotplug manager calls this; tests may too.
func (r *Reader) SetMapping(m Mapping) {
	if r == nil {
		return
	}
	r.mapping = m
}

// Frame is one normalized pad state at a SYN_REPORT boundary.
type Frame struct {
	// Buttons holds pressed evdev BTN_* codes.
	Buttons map[uint16]bool
	// Axes holds normalized values per ABS code: sticks/hats in -1..1,
	// trigger-shaped axes in 0..1.
	Axes map[uint16]float64
	// Disconnect marks a synthesized disconnect frame: Buttons is empty
	// and Axes is zeroed in the same frame.
	Disconnect bool
}

// Held returns a copy of the currently pressed buttons.
func (r *Reader) Held() map[uint16]bool {
	out := make(map[uint16]bool, len(r.held))
	for c, p := range r.held {
		if p {
			out[c] = true
		}
	}
	return out
}

// Feed consumes one event. It returns a Frame (non-nil) at each
// SYN_REPORT boundary, or nil for state events between reports.
func (r *Reader) Feed(ev InputEvent) *Frame {
	switch ev.Type {
	case EvKey:
		if isGamepadButton(ev.Code) {
			if ev.Value != 0 {
				r.held[ev.Code] = true
			} else {
				delete(r.held, ev.Code)
			}
		}
	case EvAbs:
		if _, ok := r.Abs[ev.Code]; ok {
			r.raw[ev.Code] = ev.Value
		} else {
			// Record unknown ABS codes anyway so a device that
			// reports caps late still moves; normalization uses a
			// symmetric fallback range only for these unknowns.
			r.raw[ev.Code] = ev.Value
		}
	case EvSyn:
		if ev.Code != SynReport {
			return nil
		}
		return r.snapshot()
	}
	return nil
}

// FeedAll consumes a decoded stream and returns one Frame per SYN_REPORT.
func (r *Reader) FeedAll(evs []InputEvent) []*Frame {
	var out []*Frame
	for _, ev := range evs {
		if f := r.Feed(ev); f != nil {
			out = append(out, f)
		}
	}
	return out
}

func (r *Reader) snapshot() *Frame {
	r.updateAimRest()
	f := &Frame{
		Buttons: make(map[uint16]bool, len(r.held)),
		Axes:    make(map[uint16]float64, len(r.raw)),
	}
	for c := range r.held {
		f.Buttons[c] = true
	}
	for code, raw := range r.raw {
		info, ok := r.Abs[code]
		if !ok {
			continue
		}
		var v float64
		var fb bool
		if IsTriggerAxis(info) {
			v, fb = NormalizeTrigger(raw, info.Minimum, info.Maximum, info.Flat)
		} else {
			rest := info.Value
			force := false
			if ar, ok := r.aimRest[code]; ok {
				rest = ar
				force = true
			}
			v, fb = normalizeStick(raw, info.Minimum, info.Maximum, info.Flat, rest, force)
		}
		if fb && !r.warnedFallback {
			r.warnedFallback = true
			if r.Warn != nil {
				r.Warn("gamepad: device reports flat=0, using small default deadzone")
			}
		}
		f.Axes[code] = v
	}
	return f
}

// updateAimRest captures the right-stick raw origin when motion-aim hold
// starts (L1 or LT) so gyro-to-stick is measured from the idle pose at
// press, not the connect-time Value. An axis already outside the idle
// band is left alone: capturing a held right/down pose made ZL invert
// it (firmware spring toward HID centre then read as left/up).
// Release clears it.
func (r *Reader) updateAimRest() {
	held := r.aimAssistHeld()
	if held && !r.aimHeld {
		if r.aimRest == nil {
			r.aimRest = make(map[uint16]int32)
		}
		for _, c := range []uint16{r.mapping.RightX, r.mapping.RightY} {
			if c == 0 || c == NoAxis {
				continue
			}
			raw, ok := r.raw[c]
			if !ok || !r.stickNearIdle(c, raw) {
				continue
			}
			r.aimRest[c] = raw
		}
	}
	if !held && r.aimHeld {
		r.aimRest = nil
	}
	r.aimHeld = held
}

// stickNearIdle reports whether raw sits inside the stick deadband around
// the connect-time rest (or geo centre when rest is a held-stick Value).
func (r *Reader) stickNearIdle(code uint16, raw int32) bool {
	info, ok := r.Abs[code]
	if !ok || info.Maximum <= info.Minimum {
		return false
	}
	half := float64(info.Maximum-info.Minimum) / 2
	if half <= 0 {
		return false
	}
	centre := stickCentre(info.Minimum, info.Maximum, info.Flat, info.Value)
	deadRaw, _ := stickDeadRaw(half, info.Flat)
	return absF(float64(raw)-centre) <= deadRaw
}

func (r *Reader) aimAssistHeld() bool {
	if r.mapping.HIDLinearButtons {
		if r.held[BtnWest] {
			return true
		}
	} else if r.held[BtnTL] {
		return true
	}
	tl := r.mapping.TriggerL
	if tl == 0 || tl == NoAxis {
		return false
	}
	info, ok := r.Abs[tl]
	if !ok || !IsTriggerAxis(info) {
		return false
	}
	raw, ok := r.raw[tl]
	if !ok {
		return false
	}
	v, _ := NormalizeTrigger(raw, info.Minimum, info.Maximum, info.Flat)
	return v >= 0.15
}

// DisconnectFrame synthesizes the Phase 1 disconnect state: UP for every
// held button plus zeroed axes in the same frame.
func DisconnectFrame(held map[uint16]bool, axes []uint16) *Frame {
	f := &Frame{
		Buttons:    make(map[uint16]bool),
		Axes:       make(map[uint16]float64, len(axes)),
		Disconnect: true,
	}
	for _, c := range axes {
		f.Axes[c] = 0
	}
	_ = held
	return f
}

// isGamepadButton reports whether an EV_KEY code is pad input the reader
// tracks (diamond/shoulders/start/select/mode/sticks-clicks/DPAD).
func isGamepadButton(code uint16) bool {
	switch code {
	case BtnSouth, BtnEast, BtnC, BtnNorth, BtnWest, BtnZ,
		BtnTL, BtnTR, BtnTL2, BtnTR2,
		BtnSelect, BtnStart, BtnMode, BtnThumbl, BtnThumbr,
		BtnDpadUp, BtnDpadDown, BtnDpadLeft, BtnDpadRight,
		KeyMenu, KeyBack:
		return true
	}
	return false
}
