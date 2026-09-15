// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "unicode/utf16"

func hasUnpairedSurrogate(units []uint16) bool {
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u >= 0xd800 && u <= 0xdbff {
			if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return true
			}
			i++
		} else if u >= 0xdc00 && u <= 0xdfff {
			return true
		}
	}
	return false
}

// Go range and []rune have the same replacement policy for malformed UTF-8.
// Java's raw, unpaired UTF-16 surrogates are handled separately by the caller.
func modifiedUTF8StringLength(s string) (int, bool) {
	const maxInt = int(^uint(0) >> 1)
	n := 0
	for _, r := range s {
		width := 3
		switch {
		case r != 0 && r <= 0x7f:
			width = 1
		case r <= 0x7ff:
			width = 2
		case r > 0xffff:
			width = 6 // two independently encoded surrogate units
		}
		if n > maxInt-width {
			return 0, false
		}
		n += width
	}
	return n, true
}

func encodeModifiedUTF8Unit(dst []byte, u uint16) int {
	switch {
	case u != 0 && u <= 0x7f:
		dst[0] = byte(u)
		return 1
	case u <= 0x7ff:
		dst[0] = 0xc0 | byte(u>>6)
		dst[1] = 0x80 | byte(u&0x3f)
		return 2
	default:
		dst[0] = 0xe0 | byte(u>>12)
		dst[1] = 0x80 | byte((u>>6)&0x3f)
		dst[2] = 0x80 | byte(u&0x3f)
		return 3
	}
}

// dst must have modifiedUTF8StringLength(s) bytes; no C terminator is written.
func encodeModifiedUTF8StringTo(dst []byte, s string) int {
	n := 0
	for _, r := range s {
		if r <= 0xffff {
			n += encodeModifiedUTF8Unit(dst[n:], uint16(r))
			continue
		}
		hi, lo := utf16.EncodeRune(r)
		n += encodeModifiedUTF8Unit(dst[n:], uint16(hi))
		n += encodeModifiedUTF8Unit(dst[n:], uint16(lo))
	}
	return n
}
