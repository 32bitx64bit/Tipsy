// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
)

const (
	sttGNUIFUNC = 10
	stbLocal    = 0
	stbGlobal   = 1
	stbWeak     = 2
)

type dynSym struct {
	name  string
	value uint64
	size  uint64
	info  byte
	shndx uint16
}

func (s dynSym) bind() byte { return s.info >> 4 }
func (s dynSym) typ() byte  { return s.info & 0xf }

func (s dynSym) defined() bool {
	return s.shndx != uint16(elf.SHN_UNDEF) && s.shndx != uint16(elf.SHN_COMMON)
}

func loadDynsym(ef *elf.File, d *dynInfo) ([]dynSym, []byte, error) {
	if d.strtab == 0 {
		return nil, nil, fmt.Errorf("loader: no DT_STRTAB")
	}
	strsz := d.strsz
	if strsz == 0 {
		strsz = 1 << 20
	}
	strtab, err := vaddrFileBytes(ef, d.strtab, strsz)
	if err != nil {
		return nil, nil, fmt.Errorf("loader: dynstr: %w", err)
	}
	nsym, err := dynsymCount(ef, d)
	if err != nil {
		return nil, nil, err
	}
	if d.syment == 0 {
		d.syment = 24
	}
	raw, err := vaddrFileBytes(ef, d.symtab, d.syment*uint64(nsym))
	if err != nil {
		return nil, nil, fmt.Errorf("loader: dynsym: %w", err)
	}
	out := make([]dynSym, nsym)
	for i := 0; i < nsym; i++ {
		b := raw[i*int(d.syment):]
		if len(b) < 24 {
			break
		}
		nameOff := binary.LittleEndian.Uint32(b[0:4])
		info := b[4]
		shndx := binary.LittleEndian.Uint16(b[6:8])
		value := binary.LittleEndian.Uint64(b[8:16])
		size := binary.LittleEndian.Uint64(b[16:24])
		name := ""
		if uint64(nameOff) < uint64(len(strtab)) {
			j := int(nameOff)
			for j < len(strtab) && strtab[j] != 0 {
				j++
			}
			name = string(strtab[nameOff:j])
		}
		out[i] = dynSym{name: name, value: value, size: size, info: info, shndx: shndx}
	}
	return out, strtab, nil
}

func dynsymCount(ef *elf.File, d *dynInfo) (int, error) {
	if d.hash != 0 {
		b, err := vaddrFileBytes(ef, d.hash, 8)
		if err == nil && len(b) >= 8 {
			nchain := binary.LittleEndian.Uint32(b[4:8])
			if nchain > 0 && nchain < 1<<24 {
				return int(nchain), nil
			}
		}
	}
	if d.gnuHash != 0 {
		if n, err := gnuHashCount(ef, d.gnuHash); err == nil && n > 0 {
			return n, nil
		}
	}
	if sec := ef.SectionByType(elf.SHT_DYNSYM); sec != nil && sec.Entsize > 0 {
		return int(sec.Size / sec.Entsize), nil
	}
	return 0, fmt.Errorf("loader: cannot determine dynsym count")
}

func gnuHashCount(ef *elf.File, vaddr uint64) (int, error) {
	hdr, err := vaddrFileBytes(ef, vaddr, 16)
	if err != nil || len(hdr) < 16 {
		return 0, fmt.Errorf("gnu hash header")
	}
	nbuckets := binary.LittleEndian.Uint32(hdr[0:4])
	symoffset := binary.LittleEndian.Uint32(hdr[4:8])
	bloomSize := binary.LittleEndian.Uint32(hdr[8:12])
	if nbuckets == 0 || bloomSize > 1<<20 || nbuckets > 1<<20 {
		return 0, fmt.Errorf("gnu hash bounds")
	}
	// bloom is u64 on ELF64, then buckets[nbuckets] u32, then chains.
	off := 16 + uint64(bloomSize)*8 + uint64(nbuckets)*4
	// Read a generous chain prefix; last symbol has LSB of chain word set.
	const maxGuess = 1 << 18
	raw, err := vaddrFileBytes(ef, vaddr, off+maxGuess*4)
	if err != nil {
		return 0, err
	}
	if uint64(len(raw)) < off {
		return 0, fmt.Errorf("gnu hash truncated")
	}
	buckets := raw[16+uint64(bloomSize)*8 : off]
	maxIdx := uint32(0)
	for i := uint32(0); i < nbuckets; i++ {
		b := binary.LittleEndian.Uint32(buckets[i*4 : i*4+4])
		if b == 0 {
			continue
		}
		idx := b
		for {
			ci := idx - symoffset
			co := off + uint64(ci)*4
			if co+4 > uint64(len(raw)) {
				break
			}
			chain := binary.LittleEndian.Uint32(raw[co : co+4])
			if idx > maxIdx {
				maxIdx = idx
			}
			if chain&1 != 0 {
				break
			}
			idx++
		}
	}
	if maxIdx+1 < symoffset {
		return int(symoffset), nil
	}
	return int(maxIdx + 1), nil
}

func (m *Module) lookupDef(name string) (dynSym, bool) {
	if name == "" {
		return dynSym{}, false
	}
	for i, s := range m.syms {
		if i == 0 {
			continue
		}
		if s.name == name && s.defined() && s.bind() != stbLocal {
			return s, true
		}
	}
	return dynSym{}, false
}

func (m *Module) symbolAddr(s dynSym) (uintptr, error) {
	addr := m.bias + uintptr(s.value)
	if s.typ() == sttGNUIFUNC {
		addr = callIFunc(addr)
	}
	return addr, nil
}

func (m *Module) symbolValue(idx uint32) (uint64, error) {
	if int(idx) >= len(m.syms) {
		return 0, fmt.Errorf("loader: dynsym index %d out of range", idx)
	}
	s := m.syms[idx]
	if idx == 0 || s.name == "" && !s.defined() {
		return 0, nil
	}
	if s.defined() {
		a, err := m.symbolAddr(s)
		return uint64(a), err
	}
	return m.resolveUndef(s)
}

func (m *Module) resolveUndef(s dynSym) (uint64, error) {
	if addr, ok := m.lookupLoaded(s.name); ok {
		return uint64(addr), nil
	}
	if m.resolver != nil {
		addr, err := m.resolver.Lookup("", s.name)
		if err == nil && addr != 0 {
			return uint64(addr), nil
		}
		for _, lib := range m.Needed {
			addr, err := m.resolver.Lookup(lib, s.name)
			if err == nil && addr != 0 {
				return uint64(addr), nil
			}
		}
	}
	if s.bind() == stbWeak {
		return 0, nil
	}
	m.noteMissing(s.name)
	return 0, errMissing{Name: s.name}
}

func (m *Module) lookupLoaded(name string) (uintptr, bool) {
	var walk func(mod *Module, depth int) (uintptr, bool)
	walk = func(mod *Module, depth int) (uintptr, bool) {
		if mod == nil || depth > 64 {
			return 0, false
		}
		if s, ok := mod.lookupDef(name); ok {
			a, err := mod.symbolAddr(s)
			if err != nil {
				return 0, false
			}
			return a, true
		}
		for _, d := range mod.deps {
			if a, ok := walk(d, depth+1); ok {
				return a, true
			}
		}
		return 0, false
	}
	for _, d := range m.deps {
		if a, ok := walk(d, 0); ok {
			return a, true
		}
	}
	return 0, false
}
