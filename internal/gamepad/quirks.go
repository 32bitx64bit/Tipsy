// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"fmt"
	"strings"
)

// Mapping describes which physical evdev ABS codes carry the right stick
// and the analog triggers for one device, plus where the D-pad lives.
// Absent codes are NoAxis. Content-free: no input values, only topology.
const NoAxis = 0xffff

// Mapping is resolved once per device at connect by ResolveMapping.
type Mapping struct {
	// Name is the content-free mapping label (e.g. "xpad", "sony",
	// "generic-hat2x-triggers", "generic-zrz-sticks", "spec-default").
	Name string
	// RightX/RightY are the physical right-stick ABS codes.
	RightX uint16
	RightY uint16
	// TriggerL/TriggerR are the physical analog-trigger ABS codes, or
	// NoAxis when the pad only has digital BTN_TL2/TR2 edges.
	TriggerL uint16
	TriggerR uint16
	// DpadButtons/DpadHat report which D-pad doors the device offers.
	// Both may be true (duality: engine is fed both).
	DpadButtons bool
	DpadHat     bool
	// TriggersDigitalOnly is true when analog triggers are absent and
	// L2/R2 exist only as BTN_TL2/TR2 edges.
	TriggersDigitalOnly bool
	// HIDLinearButtons is true when EV_KEY is hid-input's unskipped
	// Button 1–10 → BTN_SOUTH..TR2 (C and Z occupied, no BTN_START/
	// SELECT/THUMB). hid-microsoft Xbox Bluetooth (GuliKit XW) looks
	// like this: X/L1/R1/Select/Start/L3/R3 sit on C/WEST/Z/TL/TR/TL2/TR2.
	HIDLinearButtons bool
}

// Quirk documents one vendor family's physical layout. Vendor may be zero
// to wildcard. NameSub matches an uppercased substring of the device name.
type Quirk struct {
	Vendor  uint16
	Product uint16
	NameSub string
	Label   string
	Note    string
}

// QuirkTable is the lean per-family table (simplified 2026-09-12): Xbox
// xpad and PlayStation sony (incl. the DS4-USB golden pin) plus
// capability-driven generic rules in ResolveMapping (RX/RY vs symmetric Z/RZ
// sticks, Z/RZ vs HAT2X vs digital-only triggers, BTN vs HAT0 D-pad). Axes
// always come from device capabilities, never this table.
//
// Deleted vs v1 (see gamepad-simplify-2026-09-12.md): Sony BT stamps,
// DualSense/DS3 rows, Switch/Joy-Con rows, 8BitDo DInput/Switch/BT rows,
// XInput-clone, Logitech/clone/Stadia/Valve rows, flight-stick rows. Those
// pads resolve via the generic rules under spec-default/generic-* labels;
// Bluetooth is paired in the OS, Tipsy rescans the same evdev node.
var QuirkTable = []Quirk{
	{Vendor: 0x045e, Label: "xpad", Note: "Xbox: RX/RY right stick, Z/RZ analog triggers, HAT0 D-pad"},
	{Vendor: 0x054c, Label: "sony", Note: "PlayStation: RX/RY right stick, Z/RZ analog triggers, HAT0 D-pad"},
}

// ResolveMapping picks the physical mapping for a device from its honest
// capabilities. Precedence:
//
//  1. Right stick: ABS_RX/RY when both present (spec + xpad + Sony);
//     else symmetric ABS_Z/RZ when both present (generic Z/RZ sticks);
//     else NoAxis.
//  2. Triggers: asymmetric ABS_Z/RZ when the right stick did NOT consume
//     them; else ABS_HAT2X/Y when present; else digital-only edges.
//  3. D-pad: buttons and/or ABS_HAT0X/Y, both fed when both present.
//
// HAT0X/Y and DPAD buttons are never exclusive: duality is always served
// downstream in android.go.
func ResolveMapping(name string, id DeviceID, hasAbs map[uint16]bool, infos map[uint16]AbsInfo) Mapping {
	m := Mapping{
		Name:        "spec-default",
		RightX:      NoAxis,
		RightY:      NoAxis,
		TriggerL:    NoAxis,
		TriggerR:    NoAxis,
		DpadButtons: hasAbs[BtnDpadUp] || hasAbs[BtnDpadDown] || hasAbs[BtnDpadLeft] || hasAbs[BtnDpadRight],
		DpadHat:     hasAbs[AbsHat0X] || hasAbs[AbsHat0Y],
	}
	upper := strings.ToUpper(name)
	for _, q := range QuirkTable {
		if q.Vendor != 0 && q.Vendor != id.Vendor {
			continue
		}
		if q.Product != 0 && q.Product != id.Product {
			continue
		}
		if q.NameSub != "" && !strings.Contains(upper, q.NameSub) {
			continue
		}
		m.Name = q.Label
		break
	}

	rxOK := hasAbs[AbsRX] && hasAbs[AbsRY]
	zrzOK := hasAbs[AbsZ] && hasAbs[AbsRZ]
	zrzSymmetric := false
	if zrzOK {
		zrzSymmetric = !IsTriggerAxis(infos[AbsZ]) && !IsTriggerAxis(infos[AbsRZ])
	}

	switch {
	case rxOK:
		m.RightX, m.RightY = AbsRX, AbsRY
		// Triggers default to asymmetric Z/RZ behind an RX/RY stick.
		if zrzOK && !zrzSymmetric {
			m.TriggerL, m.TriggerR = AbsZ, AbsRZ
		} else if hasAbs[AbsHat2X] {
			m.TriggerL, m.TriggerR = AbsHat2X, AbsHat2Y
			if hat2xRestamp(m.Name) {
				m.Name = "generic-hat2x-triggers"
			}
		}
	case zrzOK && zrzSymmetric:
		m.RightX, m.RightY = AbsZ, AbsRZ
		m.Name = "generic-zrz-sticks"
		if hasAbs[AbsHat2X] {
			m.TriggerL, m.TriggerR = AbsHat2X, AbsHat2Y
		}
	default:
		// No right stick axes at all; triggers may still exist.
		if zrzOK && !zrzSymmetric {
			m.TriggerL, m.TriggerR = AbsZ, AbsRZ
		} else if hasAbs[AbsHat2X] {
			m.TriggerL, m.TriggerR = AbsHat2X, AbsHat2Y
		}
	}

	// D-pad note: BTN_DPAD_* presence is checked by the caller via HasKey,
	// not HasAbs. ResolveMapping only sees ABS here, so DpadButtons above
	// covers ABS-mapped DPAD codes (rare). The HasKey half is merged by
	// ResolveMappingForDevice below.
	if m.TriggerL == NoAxis {
		m.TriggersDigitalOnly = true
	}
	return m
}

// hat2xRestamp reports whether a quirk label belongs to a dual-stick family
// whose HAT2X triggers take the shared generic-hat2x-triggers stamp.
func hat2xRestamp(label string) bool {
	switch label {
	case "spec-default", "sony", "xpad":
		return true
	}
	return false
}

// ResolveMappingForDevice is the connect-time entry point: it merges the
// ABS topology with the BTN_DPAD_* key presence, stamps the mapping name
// from the quirk table, and returns the content-free log line.
func ResolveMappingForDevice(info DeviceInfo) (Mapping, string) {
	m := ResolveMapping(info.Name, info.ID, info.HasAbs, info.Abs)
	if info.HasKey[BtnDpadUp] || info.HasKey[BtnDpadDown] || info.HasKey[BtnDpadLeft] || info.HasKey[BtnDpadRight] {
		m.DpadButtons = true
	}
	m.HIDLinearButtons = hidLinearButtons(info.HasKey)
	return m, FormatMappingLog(info, m)
}

// hidLinearButtons reports hid-input's unskipped 10-button gamepad map:
// Button1..10 land on SOUTH,EAST,C,NORTH,WEST,Z,TL,TR,TL2,TR2 and the
// xpad codes (START/SELECT/THUMB) are absent. xpad USB nodes have the
// skipped layout (WEST=X, THUMB present) and must not match.
func hidLinearButtons(hasKey map[uint16]bool) bool {
	if hasKey == nil {
		return false
	}
	return hasKey[BtnC] && hasKey[BtnZ] && hasKey[BtnTL2] && hasKey[BtnTR2] &&
		!hasKey[BtnStart] && !hasKey[BtnSelect] &&
		!hasKey[BtnThumbl] && !hasKey[BtnThumbr]
}

// FormatMappingLog renders the once-per-connect content-free mapping line:
// name/vendor/product/capabilities only, never input content.
func FormatMappingLog(info DeviceInfo, m Mapping) string {
	return fmt.Sprintf("gamepad: connect %s vendor=%04x product=%04x mapping=%s right=%s triggers=%s dpad=%s buttons=%s abs=%s",
		sanitizeName(info.Name), info.ID.Vendor, info.ID.Product, m.Name,
		axisPair(m.RightX, m.RightY), axisPair(m.TriggerL, m.TriggerR),
		dpadDoors(m), buttonLayout(m), absList(info))
}

func buttonLayout(m Mapping) string {
	if m.HIDLinearButtons {
		return "hid-linear"
	}
	return "positional"
}

func axisPair(x, y uint16) string {
	return fmt.Sprintf("%s/%s", absName(x), absName(y))
}

func absName(c uint16) string {
	switch c {
	case NoAxis:
		return "-"
	case AbsX:
		return "X"
	case AbsY:
		return "Y"
	case AbsZ:
		return "Z"
	case AbsRX:
		return "RX"
	case AbsRY:
		return "RY"
	case AbsRZ:
		return "RZ"
	case AbsHat0X:
		return "HAT0X"
	case AbsHat0Y:
		return "HAT0Y"
	case AbsHat2X:
		return "HAT2X"
	case AbsHat2Y:
		return "HAT2Y"
	}
	return fmt.Sprintf("ABS_%d", c)
}

func dpadDoors(m Mapping) string {
	switch {
	case m.DpadButtons && m.DpadHat:
		return "btn+hat"
	case m.DpadButtons:
		return "btn"
	case m.DpadHat:
		return "hat"
	}
	return "none"
}

func absList(info DeviceInfo) string {
	codes := info.SortedAbsCodes()
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		parts = append(parts, absName(c))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}
