// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

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

// walkAPS2 decodes Android packed relocations and visits each record without
// retaining the full []Reloc; grouping/addend semantics match AOSP
// for_all_packed_relocs (linker_reloc_iterators.h).
func walkAPS2(data []byte, visit func(Reloc) error) error {
	if len(data) < 4 || string(data[:4]) != "APS2" {
		return fmt.Errorf("loader: packed reloc header is not APS2")
	}
	p := data[4:]
	count, n, ok := readSLEB128(p)
	if !ok {
		return fmt.Errorf("loader: APS2 count SLEB128")
	}
	p = p[n:]
	if count < 0 {
		return fmt.Errorf("loader: APS2 negative count %d", count)
	}
	if count > 1<<28 {
		return fmt.Errorf("loader: APS2 count %d too large", count)
	}

	// Initial r_offset is a dedicated SLEB after count (AOSP for_all_packed_relocs).
	initOff, n, ok := readSLEB128(p)
	if !ok {
		return fmt.Errorf("loader: APS2 initial r_offset SLEB128")
	}
	p = p[n:]

	rOff := uint64(initOff)
	var rInfo uint64
	var addend int64
	have := 0

	for have < int(count) {
		if len(p) == 0 {
			return fmt.Errorf("loader: APS2 truncated (have %d of %d)", have, count)
		}
		gs, n, ok := readSLEB128(p)
		if !ok {
			return fmt.Errorf("loader: APS2 group_size SLEB128")
		}
		p = p[n:]
		if gs <= 0 {
			return fmt.Errorf("loader: APS2 group_size %d", gs)
		}
		remain := int(count) - have
		if gs > int64(remain) {
			return fmt.Errorf("loader: APS2 group_size %d exceeds remaining %d", gs, remain)
		}

		flags, n, ok := readSLEB128(p)
		if !ok {
			return fmt.Errorf("loader: APS2 group flags SLEB128")
		}
		p = p[n:]
		gflags := uint64(flags)

		var groupDelta uint64
		if gflags&relocGroupedByOffsetDelta != 0 {
			d, n, ok := readSLEB128(p)
			if !ok {
				return fmt.Errorf("loader: APS2 group offset delta SLEB128")
			}
			p = p[n:]
			groupDelta = uint64(d)
		}
		if gflags&relocGroupedByInfo != 0 {
			info, n, ok := readSLEB128(p)
			if !ok {
				return fmt.Errorf("loader: APS2 group r_info SLEB128")
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
				return fmt.Errorf("loader: APS2 group addend SLEB128")
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
					return fmt.Errorf("loader: APS2 r_offset delta SLEB128")
				}
				p = p[n:]
				rOff += uint64(d)
			}
			if gflags&relocGroupedByInfo == 0 {
				info, n, ok := readSLEB128(p)
				if !ok {
					return fmt.Errorf("loader: APS2 r_info SLEB128")
				}
				p = p[n:]
				rInfo = uint64(info)
			}
			if hasAddend && !groupedAddend {
				d, n, ok := readSLEB128(p)
				if !ok {
					return fmt.Errorf("loader: APS2 r_addend SLEB128")
				}
				p = p[n:]
				addend += d
			}
			if err := visit(Reloc{Off: rOff, Info: rInfo, Addend: addend}); err != nil {
				return err
			}
			have++
		}
	}
	return nil
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
