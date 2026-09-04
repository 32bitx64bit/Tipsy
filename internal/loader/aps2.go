// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"fmt"
)

// APS2 group flags (AOSP bionic linker_reloc_iterators.h).
const (
	relocGroupedByInfo        = 1
	relocGroupedByOffsetDelta = 2
	relocGroupedByAddend      = 4
	relocGroupHasAddend       = 8
)

// Reloc is one decoded ELF64 RELA (standard or APS2).
type Reloc struct {
	Off    uint64
	Info   uint64
	Addend int64
}

func relocType(info uint64) uint32 { return uint32(info) }
func relocSym(info uint64) uint32  { return uint32(info >> 32) }

func makeRelInfo(sym uint32, typ uint32) uint64 {
	return uint64(sym)<<32 | uint64(typ)
}

func decodeAPS2(data []byte) ([]Reloc, error) {
	if len(data) < 4 || string(data[:4]) != "APS2" {
		return nil, fmt.Errorf("loader: packed reloc header is not APS2")
	}
	p := data[4:]
	count, n, ok := readSLEB128(p)
	if !ok {
		return nil, fmt.Errorf("loader: APS2 count SLEB128")
	}
	p = p[n:]
	if count < 0 {
		return nil, fmt.Errorf("loader: APS2 negative count %d", count)
	}
	if count > 1<<28 {
		return nil, fmt.Errorf("loader: APS2 count %d too large", count)
	}

	// Initial r_offset is a dedicated SLEB after count (AOSP for_all_packed_relocs).
	initOff, n, ok := readSLEB128(p)
	if !ok {
		return nil, fmt.Errorf("loader: APS2 initial r_offset SLEB128")
	}
	p = p[n:]

	rOff := uint64(initOff)
	var rInfo uint64
	var addend int64
	out := make([]Reloc, 0, int(count))

	for len(out) < int(count) {
		if len(p) == 0 {
			return nil, fmt.Errorf("loader: APS2 truncated (have %d of %d)", len(out), count)
		}
		gs, n, ok := readSLEB128(p)
		if !ok {
			return nil, fmt.Errorf("loader: APS2 group_size SLEB128")
		}
		p = p[n:]
		if gs <= 0 {
			return nil, fmt.Errorf("loader: APS2 group_size %d", gs)
		}
		remain := int(count) - len(out)
		if gs > int64(remain) {
			return nil, fmt.Errorf("loader: APS2 group_size %d exceeds remaining %d", gs, remain)
		}

		flags, n, ok := readSLEB128(p)
		if !ok {
			return nil, fmt.Errorf("loader: APS2 group flags SLEB128")
		}
		p = p[n:]
		gflags := uint64(flags)

		var groupDelta uint64
		if gflags&relocGroupedByOffsetDelta != 0 {
			d, n, ok := readSLEB128(p)
			if !ok {
				return nil, fmt.Errorf("loader: APS2 group offset delta SLEB128")
			}
			p = p[n:]
			groupDelta = uint64(d)
		}
		if gflags&relocGroupedByInfo != 0 {
			info, n, ok := readSLEB128(p)
			if !ok {
				return nil, fmt.Errorf("loader: APS2 group r_info SLEB128")
			}
			p = p[n:]
			rInfo = uint64(info)
		}

		hasAddend := gflags&relocGroupHasAddend != 0
		groupedAddend := gflags&relocGroupedByAddend != 0
		switch {
		case hasAddend && groupedAddend:
			d, n, ok := readSLEB128(p)
			if !ok {
				return nil, fmt.Errorf("loader: APS2 group addend SLEB128")
			}
			p = p[n:]
			addend += d
		case !hasAddend:
			addend = 0
		}

		for i := int64(0); i < gs; i++ {
			if gflags&relocGroupedByOffsetDelta != 0 {
				rOff += groupDelta
			} else {
				d, n, ok := readSLEB128(p)
				if !ok {
					return nil, fmt.Errorf("loader: APS2 r_offset delta SLEB128")
				}
				p = p[n:]
				rOff += uint64(d)
			}
			if gflags&relocGroupedByInfo == 0 {
				info, n, ok := readSLEB128(p)
				if !ok {
					return nil, fmt.Errorf("loader: APS2 r_info SLEB128")
				}
				p = p[n:]
				rInfo = uint64(info)
			}
			if hasAddend && !groupedAddend {
				d, n, ok := readSLEB128(p)
				if !ok {
					return nil, fmt.Errorf("loader: APS2 r_addend SLEB128")
				}
				p = p[n:]
				addend += d
			}
			out = append(out, Reloc{Off: rOff, Info: rInfo, Addend: addend})
		}
	}
	return out, nil
}

func encodeAPS2(relocs []Reloc) []byte {
	b := append([]byte(nil), "APS2"...)
	b = appendSLEB128(b, int64(len(relocs)))
	b = appendSLEB128(b, 0) // initial r_offset; first reloc encodes absolute offset as delta
	var off uint64
	var addend int64
	for _, r := range relocs {
		// One reloc per group: HAS_ADDEND, unique offset/info/addend deltas.
		b = appendSLEB128(b, 1)
		b = appendSLEB128(b, int64(relocGroupHasAddend))
		b = appendSLEB128(b, int64(r.Off-off))
		off = r.Off
		b = appendSLEB128(b, int64(r.Info))
		b = appendSLEB128(b, r.Addend-addend)
		addend = r.Addend
	}
	return b
}

func readSLEB128(b []byte) (val int64, n int, ok bool) {
	var shift uint
	for i := 0; i < len(b); i++ {
		c := b[i]
		val |= int64(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			if shift < 64 && c&0x40 != 0 {
				val |= int64(-1) << shift
			}
			return val, i + 1, true
		}
		if shift >= 64 {
			return 0, 0, false
		}
	}
	return 0, 0, false
}

func appendSLEB128(b []byte, v int64) []byte {
	for {
		c := byte(v) & 0x7f
		v >>= 7
		sign := c&0x40 != 0
		done := (v == 0 && !sign) || (v == -1 && sign)
		if !done {
			c |= 0x80
		}
		b = append(b, c)
		if done {
			return b
		}
	}
}
