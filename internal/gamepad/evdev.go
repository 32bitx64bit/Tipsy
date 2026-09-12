// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"encoding/binary"
	"sort"
)

// evdev event types (linux/input-event-codes.h).
const (
	EvSyn = 0x00
	EvKey = 0x01
	EvAbs = 0x03

	SynReport = 0
)

// evdev absolute axes used by gamepads.
const (
	AbsX     = 0x00
	AbsY     = 0x01
	AbsZ     = 0x02
	AbsRX    = 0x03
	AbsRY    = 0x04
	AbsRZ    = 0x05
	AbsHat0X = 0x10
	AbsHat0Y = 0x11
	AbsHat1X = 0x12
	AbsHat1Y = 0x13
	AbsHat2X = 0x14
	AbsHat2Y = 0x15
	AbsHat3X = 0x16
	AbsHat3Y = 0x17

	AbsMax = 0x3f
)

// evdev buttons used by gamepads (positional names; Xbox A == BtnSouth).
const (
	BtnSouth  = 0x130
	BtnEast   = 0x131
	BtnC      = 0x132
	BtnNorth  = 0x133
	BtnWest   = 0x134
	BtnZ      = 0x135
	BtnTL     = 0x136
	BtnTR     = 0x137
	BtnTL2    = 0x138
	BtnTR2    = 0x139
	BtnSelect = 0x13a
	BtnStart  = 0x13b
	BtnMode   = 0x13c
	BtnThumbl = 0x13d
	BtnThumbr = 0x13e

	BtnDpadUp    = 0x220
	BtnDpadDown  = 0x221
	BtnDpadLeft  = 0x222
	BtnDpadRight = 0x223

	// Bluetooth Xbox-compatible pads (GuliKit XW, some xpadneo nodes)
	// advertise Menu/View as keyboard keys instead of BTN_START/SELECT.
	KeyMenu = 139 // KEY_MENU
	KeyBack = 158 // KEY_BACK

	KeyMax = 0x2ff
)

// EventSize is the on-wire sizeof(struct input_event) on 64-bit LE Linux:
// 16-byte timeval + u16 type + u16 code + s32 value.
const EventSize = 24

// InputEvent is one decoded evdev record (time discarded).
type InputEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

// ParseInputEvents decodes a raw byte stream of input_event records.
// Trailing partial records are dropped.
func ParseInputEvents(buf []byte) []InputEvent {
	n := len(buf) / EventSize
	out := make([]InputEvent, 0, n)
	for i := 0; i < n; i++ {
		rec := buf[i*EventSize : (i+1)*EventSize]
		typ := binary.LittleEndian.Uint16(rec[16:18])
		code := binary.LittleEndian.Uint16(rec[18:20])
		val := int32(binary.LittleEndian.Uint32(rec[20:24]))
		out = append(out, InputEvent{Type: typ, Code: code, Value: val})
	}
	return out
}

// EncodeInputEvent builds one 24-byte input_event record (zero timestamp)
// for unit tests with recorded streams.
func EncodeInputEvent(typ, code uint16, value int32) []byte {
	buf := make([]byte, EventSize)
	binary.LittleEndian.PutUint16(buf[16:18], typ)
	binary.LittleEndian.PutUint16(buf[18:20], code)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(value))
	return buf
}

// AbsInfo mirrors struct input_absinfo (value/min/max/fuzz/flat/resolution).
type AbsInfo struct {
	Value      int32
	Minimum    int32
	Maximum    int32
	Fuzz       int32
	Flat       int32
	Resolution int32
}

// DeviceID mirrors struct input_id.
type DeviceID struct {
	BusType uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

// DeviceInfo is a connect-time snapshot: name/vendor/product/capabilities
// only, never input content.
type DeviceInfo struct {
	Path    string
	Name    string
	ID      DeviceID
	Abs     map[uint16]AbsInfo
	HasKey  map[uint16]bool
	HasAbs  map[uint16]bool
	Mapping Mapping
}

// SortedAbsCodes returns the present ABS codes in numeric order.
func (d DeviceInfo) SortedAbsCodes() []uint16 {
	out := make([]uint16, 0, len(d.HasAbs))
	for c, ok := range d.HasAbs {
		if ok {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// testBit reports bit n of a little-endian EVIOCGBIT buffer.
func testBit(buf []byte, n uint16) bool {
	i := int(n) / 8
	if i < 0 || i >= len(buf) {
		return false
	}
	return buf[i]&(1<<uint(n%8)) != 0
}

// IsGamepadKeyBits reports whether EV_KEY capability bits identify a
// gamepad. Presence of BTN_SOUTH (0x130) is the detector.
func IsGamepadKeyBits(keyBits []byte) bool {
	return testBit(keyBits, BtnSouth)
}
