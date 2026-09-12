// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"strings"
	"testing"
)

func xpadCaps() (map[uint16]bool, map[uint16]AbsInfo) {
	has := map[uint16]bool{AbsX: true, AbsY: true, AbsRX: true, AbsRY: true, AbsZ: true, AbsRZ: true, AbsHat0X: true, AbsHat0Y: true}
	infos := map[uint16]AbsInfo{
		AbsX:     {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsY:     {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRX:    {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRY:    {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsZ:     {Minimum: 0, Maximum: 255},
		AbsRZ:    {Minimum: 0, Maximum: 255},
		AbsHat0X: {Minimum: -1, Maximum: 1},
		AbsHat0Y: {Minimum: -1, Maximum: 1},
	}
	return has, infos
}

func TestResolveXboxMapping(t *testing.T) {
	has, infos := xpadCaps()
	m := ResolveMapping("Microsoft X-Box 360 pad", DeviceID{Vendor: 0x045e, Product: 0x028e}, has, infos)
	if m.Name != "xpad" {
		t.Fatalf("want xpad label, got %q", m.Name)
	}
	if m.RightX != AbsRX || m.RightY != AbsRY {
		t.Fatalf("xpad right stick must be RX/RY, got %v/%v", m.RightX, m.RightY)
	}
	if m.TriggerL != AbsZ || m.TriggerR != AbsRZ {
		t.Fatalf("xpad triggers must be Z/RZ, got %v/%v", m.TriggerL, m.TriggerR)
	}
	if m.TriggersDigitalOnly {
		t.Fatal("xpad must not be digital-only triggers")
	}
}

func TestResolveSonyMapping(t *testing.T) {
	has, infos := xpadCaps()
	m := ResolveMapping("Sony Interactive Entertainment DualShock 4", DeviceID{Vendor: 0x054c, Product: 0x09cc}, has, infos)
	if m.Name != "sony" {
		t.Fatalf("want sony label, got %q", m.Name)
	}
	if m.RightX != AbsRX || m.RightY != AbsRY {
		t.Fatalf("sony right stick must be RX/RY, got %v/%v", m.RightX, m.RightY)
	}
	if m.TriggerL != AbsZ || m.TriggerR != AbsRZ {
		t.Fatalf("sony triggers must be Z/RZ, got %v/%v", m.TriggerL, m.TriggerR)
	}
}

func TestResolveGenericZrzSticks(t *testing.T) {
	has := map[uint16]bool{AbsX: true, AbsY: true, AbsZ: true, AbsRZ: true}
	infos := map[uint16]AbsInfo{
		AbsX:  {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsY:  {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsZ:  {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRZ: {Minimum: -32768, Maximum: 32767, Flat: 255},
	}
	m := ResolveMapping("Generic USB Joystick", DeviceID{Vendor: 0x1234, Product: 0x5678}, has, infos)
	if m.Name != "generic-zrz-sticks" {
		t.Fatalf("want generic-zrz-sticks, got %q", m.Name)
	}
	if m.RightX != AbsZ || m.RightY != AbsRZ {
		t.Fatalf("generic right stick must be Z/RZ, got %v/%v", m.RightX, m.RightY)
	}
	if !m.TriggersDigitalOnly {
		t.Fatal("no analog trigger axes must mean digital-only")
	}
}

func TestResolveHat2xTriggers(t *testing.T) {
	has := map[uint16]bool{AbsX: true, AbsY: true, AbsRX: true, AbsRY: true, AbsHat2X: true, AbsHat2Y: true}
	infos := map[uint16]AbsInfo{
		AbsX:     {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsY:     {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRX:    {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsRY:    {Minimum: -32768, Maximum: 32767, Flat: 255},
		AbsHat2X: {Minimum: 0, Maximum: 255},
		AbsHat2Y: {Minimum: 0, Maximum: 255},
	}
	m := ResolveMapping("Sony-like HAT2X pad", DeviceID{Vendor: 0x054c, Product: 0x0ce6}, has, infos)
	if m.TriggerL != AbsHat2X || m.TriggerR != AbsHat2Y {
		t.Fatalf("triggers must be HAT2X/Y, got %v/%v", m.TriggerL, m.TriggerR)
	}
}

func TestResolveDpadMerging(t *testing.T) {
	has, infos := xpadCaps()
	info := DeviceInfo{
		Name: "Pad", ID: DeviceID{Vendor: 0x045e},
		Abs: infos, HasAbs: has,
		HasKey: map[uint16]bool{BtnDpadUp: true, BtnSouth: true},
	}
	m, _ := ResolveMappingForDevice(info)
	if !m.DpadButtons {
		t.Fatal("BTN_DPAD_* presence must set DpadButtons")
	}
	if !m.DpadHat {
		t.Fatal("HAT0 presence must set DpadHat")
	}
}

func TestMappingLogIsContentFree(t *testing.T) {
	has, infos := xpadCaps()
	info := DeviceInfo{
		Name: "Microsoft X-Box 360 pad", ID: DeviceID{Vendor: 0x045e, Product: 0x028e},
		Abs: infos, HasAbs: has, HasKey: map[uint16]bool{BtnSouth: true},
	}
	m, line := ResolveMappingForDevice(info)
	if !strings.Contains(line, "vendor=045e") || !strings.Contains(line, "product=028e") {
		t.Fatalf("log must carry vendor/product, got %q", line)
	}
	if !strings.Contains(line, m.Name) {
		t.Fatalf("log must carry mapping name, got %q", line)
	}
	for _, banned := range []string{"password", "token", "cookie"} {
		if strings.Contains(strings.ToLower(line), banned) {
			t.Fatalf("log must never carry secrets, got %q", line)
		}
	}
}

func TestResolveGuliKitUnsignedXboxSticks(t *testing.T) {
	has := map[uint16]bool{AbsX: true, AbsY: true, AbsRX: true, AbsRY: true, AbsZ: true, AbsRZ: true, AbsHat0X: true, AbsHat0Y: true}
	infos := map[uint16]AbsInfo{
		AbsX:     {Value: 31533, Minimum: 0, Maximum: 65535, Flat: 4095},
		AbsY:     {Value: 35079, Minimum: 0, Maximum: 65535, Flat: 4095},
		AbsRX:    {Value: 30737, Minimum: 0, Maximum: 65535, Flat: 4095},
		AbsRY:    {Value: 34497, Minimum: 0, Maximum: 65535, Flat: 4095},
		AbsZ:     {Value: 0, Minimum: 0, Maximum: 1023, Flat: 63},
		AbsRZ:    {Value: 0, Minimum: 0, Maximum: 1023, Flat: 63},
		AbsHat0X: {Minimum: -1, Maximum: 1},
		AbsHat0Y: {Minimum: -1, Maximum: 1},
	}
	m := ResolveMapping("GuliKit Controller XW", DeviceID{Vendor: 0x045e, Product: 0x02e0}, has, infos)
	if m.Name != "xpad" {
		t.Fatalf("GuliKit VID 045e must keep xpad label, got %q", m.Name)
	}
	if m.RightX != AbsRX || m.RightY != AbsRY {
		t.Fatalf("GuliKit right stick must be RX/RY, got %v/%v", m.RightX, m.RightY)
	}
	if m.TriggerL != AbsZ || m.TriggerR != AbsRZ {
		t.Fatalf("GuliKit triggers must stay Z/RZ, got %v/%v", m.TriggerL, m.TriggerR)
	}
	info := DeviceInfo{
		Name: "GuliKit Controller XW", ID: DeviceID{Vendor: 0x045e, Product: 0x02e0},
		Abs: infos, HasAbs: has,
		HasKey: map[uint16]bool{
			BtnSouth: true, BtnEast: true, BtnC: true, BtnNorth: true, BtnWest: true, BtnZ: true,
			BtnTL: true, BtnTR: true, BtnTL2: true, BtnTR2: true, KeyMenu: true,
		},
	}
	m2, line := ResolveMappingForDevice(info)
	if !m2.HIDLinearButtons {
		t.Fatal("GuliKit must stamp HIDLinearButtons")
	}
	if !strings.Contains(line, "buttons=hid-linear") {
		t.Fatalf("log must name hid-linear buttons, got %q", line)
	}
}
