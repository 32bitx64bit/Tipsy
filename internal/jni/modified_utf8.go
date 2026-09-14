// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"unicode/utf16"
	"unsafe"
)

// rawUTF16 exists only for a String that contains an unpaired surrogate.
// Keeping it behind one pointer avoids charging every Object a slice header.
type rawUTF16 struct {
	units []uint16
}

// newStringUTF16On preserves Java's UTF-16 code-unit model. Go strings cannot
// retain an unpaired surrogate, so retain a raw unit copy only in that lossy
// case. This is representation correctness, not a UTF-16 conversion cache for
// ordinary strings.
//
// vm.mu must be held by the caller.
func (vm *VM) newStringUTF16On(env unsafe.Pointer, units []uint16) *Object {
	text := string(utf16.Decode(units))
	o := vm.newStringOn(env, text)
	if !sameUTF16(units, utf16.Encode([]rune(text))) {
		o.utf16 = &rawUTF16{units: append([]uint16(nil), units...)}
	}
	return o
}

func sameUTF16(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func objectUTF16Units(o *Object) []uint16 {
	if o == nil {
		return nil
	}
	if o.utf16 != nil {
		return o.utf16.units
	}
	return utf16.Encode([]rune(o.str))
}

func objectUTF16Length(o *Object) int {
	if o == nil {
		return 0
	}
	if o.utf16 != nil {
		return len(o.utf16.units)
	}
	return utf16UnitCount(o.str)
}

// decodeModifiedUTF8 decodes JNI's Modified UTF-8 as UTF-16 code units. MUTF-8
// represents NUL as C0 80 and supplementary characters as their two surrogate
// units, each encoded independently. Four-byte UTF-8 and overlong encodings
// are invalid MUTF-8. Callers reject malformed guest input with NULL instead
// of silently applying Go's replacement-rune policy.
func decodeModifiedUTF8(in []byte) ([]uint16, bool) {
	capacity := len(in)
	if capacity > maxGuestStringUnits {
		capacity = maxGuestStringUnits
	}
	units := make([]uint16, 0, capacity)
	appendUnit := func(unit uint16) bool {
		if len(units) >= maxGuestStringUnits {
			return false
		}
		units = append(units, unit)
		return true
	}
	for i := 0; i < len(in); {
		b0 := in[i]
		switch {
		case b0 >= 0x01 && b0 <= 0x7f:
			if !appendUnit(uint16(b0)) {
				return nil, false
			}
			i++
		case b0 == 0xc0:
			if i+1 >= len(in) || in[i+1] != 0x80 || !appendUnit(0) {
				return nil, false
			}
			i += 2
		case b0 >= 0xc2 && b0 <= 0xdf:
			if i+1 >= len(in) || in[i+1]&0xc0 != 0x80 {
				return nil, false
			}
			if !appendUnit(uint16(b0&0x1f)<<6 | uint16(in[i+1]&0x3f)) {
				return nil, false
			}
			i += 2
		case b0 >= 0xe0 && b0 <= 0xef:
			if i+2 >= len(in) || in[i+1]&0xc0 != 0x80 || in[i+2]&0xc0 != 0x80 {
				return nil, false
			}
			// E0 80..9F is an overlong encoding. ED surrogate ranges are
			// deliberately accepted: MUTF-8 operates on UTF-16 units.
			if b0 == 0xe0 && in[i+1] < 0xa0 {
				return nil, false
			}
			if !appendUnit(uint16(b0&0x0f)<<12 | uint16(in[i+1]&0x3f)<<6 | uint16(in[i+2]&0x3f)) {
				return nil, false
			}
			i += 3
		default:
			return nil, false
		}
	}
	return units, true
}

func modifiedUTF8Length(units []uint16) (int, bool) {
	n := 0
	const maxInt = int(^uint(0) >> 1)
	for _, unit := range units {
		width := 3
		if unit != 0 && unit <= 0x7f {
			width = 1
		} else if unit <= 0x7ff {
			width = 2
		}
		if n > maxInt-width {
			return 0, false
		}
		n += width
	}
	return n, true
}

// encodeModifiedUTF8To writes exactly the Modified UTF-8 payload length for
// units; it never appends a C terminator.
func encodeModifiedUTF8To(dst []byte, units []uint16) int {
	n := 0
	for _, unit := range units {
		switch {
		case unit != 0 && unit <= 0x7f:
			dst[n] = byte(unit)
			n++
		case unit <= 0x7ff:
			dst[n] = 0xc0 | byte(unit>>6)
			dst[n+1] = 0x80 | byte(unit&0x3f)
			n += 2
		default:
			dst[n] = 0xe0 | byte(unit>>12)
			dst[n+1] = 0x80 | byte((unit>>6)&0x3f)
			dst[n+2] = 0x80 | byte(unit&0x3f)
			n += 3
		}
	}
	return n
}
