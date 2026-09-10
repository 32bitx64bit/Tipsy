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
	name    string
	value   uint64
	size    uint64
	info    byte
	other   byte
	shndx   uint16
	version symbolVersion
}

type undefResult struct {
	value uint64
	err   error
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
	var strtab []byte
	var err error
	if strsz != 0 {
		strtab, err = vaddrFileBytesExact(ef, d.strtab, strsz)
	} else {
		// No DT_STRSZ: names are still bounds-checked against the clamped
		// file bytes below.
		strtab, err = vaddrFileBytes(ef, d.strtab, 1<<20)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("loader: dynstr: %w", err)
	}
	nsym, err := dynsymCount(ef, d)
	if err != nil {
		return nil, nil, err
	}
	// ELF64 dynsym entries are exactly 24 bytes. Anything else (notably 0,
	// which previously defaulted) makes the range arithmetic below ambiguous.
	if d.syment != 24 {
		return nil, nil, fmt.Errorf("loader: unsupported DT_SYMENT %d (ELF64 requires 24)", d.syment)
	}
	if nsym <= 0 || nsym > 1<<24 {
		return nil, nil, fmt.Errorf("loader: implausible dynsym count %d", nsym)
	}
	size := d.syment * uint64(nsym)
	if size/d.syment != uint64(nsym) {
		return nil, nil, fmt.Errorf("loader: dynsym size overflow (%d x %d)", d.syment, nsym)
	}
	raw, err := vaddrFileBytesExact(ef, d.symtab, size)
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
		other := b[5]
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
		out[i] = dynSym{name: name, value: value, size: size, info: info, other: other, shndx: shndx}
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

// buildSymbolIndex records, per exported name, the dynsym indices that could
// satisfy lookupDef, in ascending index order. Version hiding is checked at
// lookup time because versions are attached after loadDynsym.
func buildSymbolIndex(syms []dynSym) map[string][]int32 {
	index := make(map[string][]int32)
	for i := 1; i < len(syms); i++ {
		s := syms[i]
		if s.name == "" || !s.defined() || s.bind() == stbLocal {
			continue
		}
		index[s.name] = append(index[s.name], int32(i))
	}
	return index
}

func (m *Module) lookupDef(name string) (dynSym, bool) {
	if name == "" {
		return dynSym{}, false
	}
	if m.symIndex != nil {
		for _, i := range m.symIndex[name] {
			s := m.syms[i]
			if !s.version.hidden {
				return s, true
			}
		}
		return dynSym{}, false
	}
	for i, s := range m.syms {
		if i == 0 {
			continue
		}
		if s.name == name && s.defined() && s.bind() != stbLocal && !s.version.hidden {
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
	return m.resolveUndefIndexed(idx, s)
}

// resolveUndefIndexed memoizes resolveUndef per undefined dynsym index, so a
// symbol referenced by many relocations resolves once. Resolution is a pure
// function of already-loaded modules; the lock is never held across resolver
// callbacks to avoid re-entrant deadlock.
func (m *Module) resolveUndefIndexed(idx uint32, s dynSym) (uint64, error) {
	m.undefMu.Lock()
	if r, ok := m.resolved[idx]; ok {
		m.undefMu.Unlock()
		return r.value, r.err
	}
	m.undefMu.Unlock()

	value, err := m.resolveUndef(s)

	m.undefMu.Lock()
	if m.resolved == nil {
		m.resolved = make(map[uint32]undefResult)
	}
	m.resolved[idx] = undefResult{value: value, err: err}
	m.undefMu.Unlock()
	return value, err
}

func (m *Module) resolveUndef(s dynSym) (uint64, error) {
	if s.version.requirement != nil {
		return m.resolveVersionedUndef(s, *s.version.requirement)
	}
	if addr, ok := m.lookupLoaded(s.name); ok {
		return uint64(addr), nil
	}
	if m.resolver != nil {
		for _, lib := range m.Needed {
			addr, err := m.resolver.Lookup(neededBase(lib), s.name)
			if err == nil && addr != 0 {
				return uint64(addr), nil
			}
		}
		addr, err := m.resolver.Lookup("", s.name)
		if err == nil && addr != 0 {
			return uint64(addr), nil
		}
	}
	if s.bind() == stbWeak {
		return 0, nil
	}
	m.noteMissing(s.name)
	return 0, errMissing{Name: s.name}
}

func (m *Module) lookupLoaded(name string) (uintptr, bool) {
	// A dependency DAG can be re-entered through diamonds; each module only
	// needs to be searched once per lookup.
	visited := make(map[*Module]struct{})
	var walk func(mod *Module, depth int) (uintptr, bool)
	walk = func(mod *Module, depth int) (uintptr, bool) {
		if mod == nil || depth > 64 {
			return 0, false
		}
		if _, seen := visited[mod]; seen {
			return 0, false
		}
		visited[mod] = struct{}{}
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
