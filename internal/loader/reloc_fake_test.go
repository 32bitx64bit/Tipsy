// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
)

// applyRelativeFake applies R_X86_64_RELATIVE entries onto buf, treating
// vaddr 0 as buf[0]. bias is added to each addend.
func applyRelativeFake(buf []byte, bias uintptr, relocs []Reloc) error {
	for _, r := range relocs {
		if elf.R_X86_64(relocType(r.Info)) != elf.R_X86_64_RELATIVE {
			return fmt.Errorf("test: expected RELATIVE, got type %d", relocType(r.Info))
		}
		if r.Off+8 > uint64(len(buf)) {
			return fmt.Errorf("test: offset 0x%x out of fake mapping", r.Off)
		}
		binary.LittleEndian.PutUint64(buf[r.Off:], uint64(bias)+uint64(r.Addend))
	}
	return nil
}

func applyNamedFake(buf []byte, bias uintptr, relocs []Reloc, symbols map[uint32]uint64) error {
	for _, r := range relocs {
		typ := elf.R_X86_64(relocType(r.Info))
		off := r.Off
		if off+8 > uint64(len(buf)) {
			return fmt.Errorf("test: offset 0x%x out of fake mapping", off)
		}
		var val uint64
		switch typ {
		case elf.R_X86_64_RELATIVE:
			val = uint64(bias) + uint64(r.Addend)
		case elf.R_X86_64_64, elf.R_X86_64_GLOB_DAT, elf.R_X86_64_JMP_SLOT:
			sym := symbols[relocSym(r.Info)]
			val = sym + uint64(r.Addend)
		default:
			return fmt.Errorf("test: unsupported type %s", typ)
		}
		binary.LittleEndian.PutUint64(buf[off:], val)
	}
	return nil
}
