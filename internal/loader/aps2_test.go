// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"testing"
)

func TestSLEB128Roundtrip(t *testing.T) {
	t.Parallel()
	vals := []int64{0, 1, -1, 63, 64, -64, -65, 127, 128, -128, 0x1000, -0x1000, 1 << 32, -(1 << 32), 0x7fffffffffffffff, -(1 << 62)}
	for _, v := range vals {
		b := appendSLEB128(nil, v)
		got, n, ok := readSLEB128(b)
		if !ok || n != len(b) || got != v {
			t.Errorf("sleb %d: got %d n=%d ok=%v bytes=%x", v, got, n, ok, b)
		}
	}
}

func TestDecodeAPS2Header(t *testing.T) {
	t.Parallel()
	if _, err := decodeAPS2([]byte("XXXX")); err == nil {
		t.Fatal("expected error")
	}
}

func TestAPS2RelativeRoundtrip(t *testing.T) {
	t.Parallel()
	info := makeRelInfo(0, uint32(elf.R_X86_64_RELATIVE))
	in := []Reloc{
		{Off: 0x1000, Info: info, Addend: 0x42},
		{Off: 0x1008, Info: info, Addend: 0x100},
		{Off: 0x2000, Info: info, Addend: -8},
	}
	blob := encodeAPS2(in)
	if string(blob[:4]) != "APS2" {
		t.Fatalf("magic %q", blob[:4])
	}
	out, err := decodeAPS2(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("len %d want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("[%d] got %+v want %+v", i, out[i], in[i])
		}
	}
}

func TestAPS2InitialOffsetAndGroupDelta(t *testing.T) {
	t.Parallel()
	info := int64(elf.R_X86_64_RELATIVE)
	// initial r_offset = 0x1000, then OFFSET_DELTA=8 yields 0x1008, 0x1010, 0x1018.
	b := append([]byte(nil), "APS2"...)
	b = appendSLEB128(b, 3)      // count
	b = appendSLEB128(b, 0x1000) // initial r_offset
	b = appendSLEB128(b, 3)      // group size
	flags := relocGroupedByOffsetDelta | relocGroupedByInfo | relocGroupHasAddend
	b = appendSLEB128(b, int64(flags))
	b = appendSLEB128(b, 8)    // offset delta
	b = appendSLEB128(b, info) // group r_info
	// HAS_ADDEND without GROUPED_BY_ADDEND: per-reloc addend deltas (cumulative)
	b = appendSLEB128(b, 1) // addend 1
	b = appendSLEB128(b, 2) // addend 3
	b = appendSLEB128(b, 4) // addend 7

	out, err := decodeAPS2(b)
	if err != nil {
		t.Fatal(err)
	}
	wantOff := []uint64{0x1008, 0x1010, 0x1018}
	wantAdd := []int64{1, 3, 7}
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	for i := range out {
		if out[i].Off != wantOff[i] || out[i].Addend != wantAdd[i] {
			t.Errorf("[%d] off=0x%x addend=%d want 0x%x %d", i, out[i].Off, out[i].Addend, wantOff[i], wantAdd[i])
		}
		if elf.R_X86_64(relocType(out[i].Info)) != elf.R_X86_64_RELATIVE {
			t.Errorf("[%d] info=%#x", i, out[i].Info)
		}
	}
}

func TestAPS2GroupedByAddend(t *testing.T) {
	t.Parallel()
	info := int64(elf.R_X86_64_RELATIVE)
	b := append([]byte(nil), "APS2"...)
	b = appendSLEB128(b, 2)
	b = appendSLEB128(b, 0) // initial
	b = appendSLEB128(b, 2) // group size
	flags := relocGroupedByOffsetDelta | relocGroupedByInfo | relocGroupedByAddend | relocGroupHasAddend
	b = appendSLEB128(b, int64(flags))
	b = appendSLEB128(b, 8)    // delta
	b = appendSLEB128(b, info) // r_info
	b = appendSLEB128(b, 5)    // group addend += 5
	// no per-reloc addends or offsets
	out, err := decodeAPS2(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].Off != 8 || out[1].Off != 16 {
		t.Fatalf("offs 0x%x 0x%x", out[0].Off, out[1].Off)
	}
	if out[0].Addend != 5 || out[1].Addend != 5 {
		t.Fatalf("addends %d %d", out[0].Addend, out[1].Addend)
	}
}

func TestAPS2NoAddendResets(t *testing.T) {
	t.Parallel()
	info := int64(elf.R_X86_64_RELATIVE)
	b := append([]byte(nil), "APS2"...)
	b = appendSLEB128(b, 2)
	b = appendSLEB128(b, 0)
	// group 1: HAS_ADDEND, one reloc addend 9
	b = appendSLEB128(b, 1)
	b = appendSLEB128(b, int64(relocGroupHasAddend))
	b = appendSLEB128(b, 0x20) // offset delta
	b = appendSLEB128(b, info)
	b = appendSLEB128(b, 9)
	// group 2: no HAS_ADDEND → addend 0
	b = appendSLEB128(b, 1)
	b = appendSLEB128(b, int64(relocGroupedByInfo))
	b = appendSLEB128(b, info)
	b = appendSLEB128(b, 0x8)
	out, err := decodeAPS2(b)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Addend != 9 || out[1].Addend != 0 {
		t.Fatalf("addends %d %d", out[0].Addend, out[1].Addend)
	}
	if out[0].Off != 0x20 || out[1].Off != 0x28 {
		t.Fatalf("offs 0x%x 0x%x", out[0].Off, out[1].Off)
	}
}

func TestApplyRelativeFake(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 32)
	info := makeRelInfo(0, uint32(elf.R_X86_64_RELATIVE))
	relocs := []Reloc{
		{Off: 0, Info: info, Addend: 0x10},
		{Off: 8, Info: info, Addend: 0x20},
		{Off: 16, Info: info, Addend: -1},
	}
	const bias uintptr = 0x7f0000000000
	if err := applyRelativeFake(buf, bias, relocs); err != nil {
		t.Fatal(err)
	}
	if g := binary.LittleEndian.Uint64(buf[0:]); g != uint64(bias)+0x10 {
		t.Fatalf("slot0=%#x", g)
	}
	if g := binary.LittleEndian.Uint64(buf[8:]); g != uint64(bias)+0x20 {
		t.Fatalf("slot1=%#x", g)
	}
	if g := binary.LittleEndian.Uint64(buf[16:]); g != uint64(bias)-1 {
		t.Fatalf("slot2=%#x", g)
	}
}

func TestApplyGlobDatFake(t *testing.T) {
	t.Parallel()
	buf := make([]byte, 16)
	relocs := []Reloc{
		{Off: 0, Info: makeRelInfo(1, uint32(elf.R_X86_64_GLOB_DAT)), Addend: 0},
		{Off: 8, Info: makeRelInfo(2, uint32(elf.R_X86_64_JMP_SLOT)), Addend: 0},
	}
	syms := map[uint32]uint64{1: 0x1000, 2: 0x2000}
	if err := applyNamedFake(buf, 0, relocs, syms); err != nil {
		t.Fatal(err)
	}
	if g := binary.LittleEndian.Uint64(buf[0:]); g != 0x1000 {
		t.Fatalf("glob=%#x", g)
	}
	if g := binary.LittleEndian.Uint64(buf[8:]); g != 0x2000 {
		t.Fatalf("jump=%#x", g)
	}
}

func TestAPS2ThenRelativeApply(t *testing.T) {
	t.Parallel()
	info := makeRelInfo(0, uint32(elf.R_X86_64_RELATIVE))
	blob := encodeAPS2([]Reloc{
		{Off: 0, Info: info, Addend: 5},
		{Off: 8, Info: info, Addend: 9},
	})
	rs, err := decodeAPS2(blob)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	const bias uintptr = 0x10000
	if err := applyRelativeFake(buf, bias, rs); err != nil {
		t.Fatal(err)
	}
	if g := binary.LittleEndian.Uint64(buf[0:]); g != 0x10005 {
		t.Fatalf("got %#x", g)
	}
	if g := binary.LittleEndian.Uint64(buf[8:]); g != 0x10009 {
		t.Fatalf("got %#x", g)
	}
}
