// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	gnuVersionCurrent = 1
	gnuVersionHidden  = 0x8000
	gnuVersionIndex   = 0x7fff

	maxVersionRecords = 1 << 16
	maxVersionAux     = 1 << 16
)

// VersionCompatibilityReason identifies why an authenticated versioned import
// could not be proven compatible. It is deliberately machine-readable so a
// staged install check can report a compatibility verdict without treating it
// as an authenticity failure.
type VersionCompatibilityReason string

const (
	VersionProviderUnavailable VersionCompatibilityReason = "version-aware provider unavailable"
	VersionProviderMissing     VersionCompatibilityReason = "compatible provider symbol unavailable"
)

// VersionCompatibilityError is returned instead of silently binding a
// versioned authenticated import to a same-name symbol with unknown ABI.
type VersionCompatibilityError struct {
	Module     string
	Dependency string
	Symbol     string
	Version    string
	Reason     VersionCompatibilityReason
}

func (e *VersionCompatibilityError) Error() string {
	if e == nil {
		return "loader: GNU symbol-version compatibility failure"
	}
	return fmt.Sprintf("loader: GNU symbol-version compatibility failure in %s: %s:%s@%s: %s",
		e.Module, e.Dependency, e.Symbol, e.Version, e.Reason)
}

type versionRequirement struct {
	index   uint16
	library string
	name    string
	hash    uint32
	flags   uint16
}

type versionDefinition struct {
	index   uint16
	name    string
	hash    uint32
	flags   uint16
	parents []string
}

type symbolVersion struct {
	index       uint16
	hidden      bool
	requirement *versionRequirement
	definition  *versionDefinition
}

type versionInfo struct {
	symbols      []symbolVersion
	requirements map[uint16]*versionRequirement
	definitions  map[uint16]*versionDefinition
}

func loadSymbolVersions(ef *elf.File, d *dynInfo, syms []dynSym, dynstr []byte) (*versionInfo, error) {
	info := &versionInfo{
		symbols:      make([]symbolVersion, len(syms)),
		requirements: make(map[uint16]*versionRequirement),
		definitions:  make(map[uint16]*versionDefinition),
	}
	if !d.hasVersym {
		if d.hasVerneed || d.hasVerneedNum || d.hasVerdef || d.hasVerdefNum {
			return nil, fmt.Errorf("version definition/need table exists without DT_VERSYM")
		}
		return info, nil
	}
	if d.versym == 0 {
		return nil, fmt.Errorf("DT_VERSYM has a zero address")
	}
	if len(syms) == 0 {
		return nil, fmt.Errorf("DT_VERSYM exists without dynamic symbols")
	}
	if d.strtab == 0 || d.strsz == 0 || len(dynstr) == 0 {
		return nil, fmt.Errorf("DT_VERSYM requires bounded DT_STRTAB/DT_STRSZ")
	}
	if d.hasVerneed != d.hasVerneedNum {
		return nil, fmt.Errorf("DT_VERNEED and DT_VERNEEDNUM must appear together")
	}
	if d.hasVerdef != d.hasVerdefNum {
		return nil, fmt.Errorf("DT_VERDEF and DT_VERDEFNUM must appear together")
	}
	if d.hasVerneed && (d.verneed == 0 || d.verneedNum == 0) {
		return nil, fmt.Errorf("DT_VERNEED and DT_VERNEEDNUM must be non-zero")
	}
	if d.hasVerdef && (d.verdef == 0 || d.verdefNum == 0) {
		return nil, fmt.Errorf("DT_VERDEF and DT_VERDEFNUM must be non-zero")
	}
	if d.verneedNum > maxVersionRecords || d.verdefNum > maxVersionRecords {
		return nil, fmt.Errorf("GNU version table count exceeds %d", maxVersionRecords)
	}

	needs, err := parseVersionNeeds(ef, d, dynstr)
	if err != nil {
		return nil, err
	}
	defs, err := parseVersionDefinitions(ef, d, dynstr)
	if err != nil {
		return nil, err
	}
	for idx, need := range needs {
		if _, conflict := defs[idx]; conflict {
			return nil, fmt.Errorf("GNU version index %d is both needed and defined", idx)
		}
		info.requirements[idx] = need
	}
	for idx, def := range defs {
		info.definitions[idx] = def
	}

	raw, err := versionFileBytes(ef, d.versym, uint64(len(syms))*2)
	if err != nil {
		return nil, fmt.Errorf("DT_VERSYM: %w", err)
	}
	for i, sym := range syms {
		value := binary.LittleEndian.Uint16(raw[i*2 : i*2+2])
		idx := value & gnuVersionIndex
		sv := symbolVersion{index: idx, hidden: value&gnuVersionHidden != 0}
		if idx <= 1 {
			if sv.hidden {
				return nil, fmt.Errorf("dynamic symbol %d has hidden reserved version index %d", i, idx)
			}
			info.symbols[i] = sv
			continue
		}
		if sym.defined() {
			sv.definition = defs[idx]
			if sv.definition == nil {
				return nil, fmt.Errorf("defined dynamic symbol %d %q refers to unknown version index %d", i, sym.name, idx)
			}
		} else {
			sv.requirement = needs[idx]
			if sv.requirement == nil {
				return nil, fmt.Errorf("undefined dynamic symbol %d %q refers to unknown version index %d", i, sym.name, idx)
			}
		}
		info.symbols[i] = sv
	}
	return info, nil
}

func parseVersionNeeds(ef *elf.File, d *dynInfo, dynstr []byte) (map[uint16]*versionRequirement, error) {
	out := make(map[uint16]*versionRequirement)
	if d.verneedNum == 0 {
		return out, nil
	}
	needed := make(map[string]struct{}, len(d.needed))
	for _, name := range d.needed {
		needed[name] = struct{}{}
	}
	recordVA := d.verneed
	totalAux := uint64(0)
	for record := uint64(0); record < d.verneedNum; record++ {
		raw, err := versionFileBytes(ef, recordVA, 16)
		if err != nil {
			return nil, fmt.Errorf("DT_VERNEED record %d: %w", record, err)
		}
		version := binary.LittleEndian.Uint16(raw[0:2])
		count := binary.LittleEndian.Uint16(raw[2:4])
		fileOff := binary.LittleEndian.Uint32(raw[4:8])
		auxOff := binary.LittleEndian.Uint32(raw[8:12])
		next := binary.LittleEndian.Uint32(raw[12:16])
		if version != gnuVersionCurrent {
			return nil, fmt.Errorf("DT_VERNEED record %d has unsupported revision %d", record, version)
		}
		if count == 0 || totalAux+uint64(count) > maxVersionAux {
			return nil, fmt.Errorf("DT_VERNEED record %d has invalid auxiliary count %d", record, count)
		}
		totalAux += uint64(count)
		library, err := versionString(dynstr, fileOff)
		if err != nil {
			return nil, fmt.Errorf("DT_VERNEED record %d library: %w", record, err)
		}
		if _, ok := needed[library]; !ok {
			return nil, fmt.Errorf("DT_VERNEED record %d names %q, which is not an exact DT_NEEDED dependency", record, library)
		}
		if auxOff < 16 || auxOff%4 != 0 {
			return nil, fmt.Errorf("DT_VERNEED record %d has invalid auxiliary offset %#x", record, auxOff)
		}
		auxVA, ok := addVersionOffset(recordVA, auxOff)
		if !ok {
			return nil, fmt.Errorf("DT_VERNEED record %d auxiliary offset overflows", record)
		}
		for aux := uint16(0); aux < count; aux++ {
			a, err := versionFileBytes(ef, auxVA, 16)
			if err != nil {
				return nil, fmt.Errorf("DT_VERNEED record %d auxiliary %d: %w", record, aux, err)
			}
			hash := binary.LittleEndian.Uint32(a[0:4])
			flags := binary.LittleEndian.Uint16(a[4:6])
			other := binary.LittleEndian.Uint16(a[6:8])
			nameOff := binary.LittleEndian.Uint32(a[8:12])
			auxNext := binary.LittleEndian.Uint32(a[12:16])
			if other&gnuVersionHidden != 0 || other&gnuVersionIndex <= 1 {
				return nil, fmt.Errorf("DT_VERNEED record %d auxiliary %d has reserved version index %#x", record, aux, other)
			}
			idx := other & gnuVersionIndex
			name, err := versionString(dynstr, nameOff)
			if err != nil {
				return nil, fmt.Errorf("DT_VERNEED record %d auxiliary %d name: %w", record, aux, err)
			}
			if _, duplicate := out[idx]; duplicate {
				return nil, fmt.Errorf("duplicate DT_VERNEED version index %d", idx)
			}
			out[idx] = &versionRequirement{index: idx, library: library, name: name, hash: hash, flags: flags}
			last := aux+1 == count
			if last {
				if auxNext != 0 {
					return nil, fmt.Errorf("DT_VERNEED record %d final auxiliary has non-zero next offset", record)
				}
			} else {
				if auxNext < 16 || auxNext%4 != 0 {
					return nil, fmt.Errorf("DT_VERNEED record %d auxiliary %d has invalid next offset %#x", record, aux, auxNext)
				}
				auxVA, ok = addVersionOffset(auxVA, auxNext)
				if !ok {
					return nil, fmt.Errorf("DT_VERNEED auxiliary chain overflows")
				}
			}
		}
		last := record+1 == d.verneedNum
		if last {
			if next != 0 {
				return nil, fmt.Errorf("final DT_VERNEED record has non-zero next offset")
			}
		} else {
			if next < 16 || next%4 != 0 {
				return nil, fmt.Errorf("DT_VERNEED record %d has invalid next offset %#x", record, next)
			}
			recordVA, ok = addVersionOffset(recordVA, next)
			if !ok {
				return nil, fmt.Errorf("DT_VERNEED chain overflows")
			}
		}
	}
	return out, nil
}

func parseVersionDefinitions(ef *elf.File, d *dynInfo, dynstr []byte) (map[uint16]*versionDefinition, error) {
	out := make(map[uint16]*versionDefinition)
	if d.verdefNum == 0 {
		return out, nil
	}
	recordVA := d.verdef
	totalAux := uint64(0)
	for record := uint64(0); record < d.verdefNum; record++ {
		raw, err := versionFileBytes(ef, recordVA, 20)
		if err != nil {
			return nil, fmt.Errorf("DT_VERDEF record %d: %w", record, err)
		}
		version := binary.LittleEndian.Uint16(raw[0:2])
		flags := binary.LittleEndian.Uint16(raw[2:4])
		idx := binary.LittleEndian.Uint16(raw[4:6])
		count := binary.LittleEndian.Uint16(raw[6:8])
		hash := binary.LittleEndian.Uint32(raw[8:12])
		auxOff := binary.LittleEndian.Uint32(raw[12:16])
		next := binary.LittleEndian.Uint32(raw[16:20])
		if version != gnuVersionCurrent {
			return nil, fmt.Errorf("DT_VERDEF record %d has unsupported revision %d", record, version)
		}
		if idx == 0 || idx&gnuVersionHidden != 0 {
			return nil, fmt.Errorf("DT_VERDEF record %d has invalid version index %#x", record, idx)
		}
		if count == 0 || totalAux+uint64(count) > maxVersionAux {
			return nil, fmt.Errorf("DT_VERDEF record %d has invalid auxiliary count %d", record, count)
		}
		totalAux += uint64(count)
		if _, duplicate := out[idx]; duplicate {
			return nil, fmt.Errorf("duplicate DT_VERDEF version index %d", idx)
		}
		if auxOff < 20 || auxOff%4 != 0 {
			return nil, fmt.Errorf("DT_VERDEF record %d has invalid auxiliary offset %#x", record, auxOff)
		}
		auxVA, ok := addVersionOffset(recordVA, auxOff)
		if !ok {
			return nil, fmt.Errorf("DT_VERDEF record %d auxiliary offset overflows", record)
		}
		def := &versionDefinition{index: idx, hash: hash, flags: flags}
		for aux := uint16(0); aux < count; aux++ {
			a, err := versionFileBytes(ef, auxVA, 8)
			if err != nil {
				return nil, fmt.Errorf("DT_VERDEF record %d auxiliary %d: %w", record, aux, err)
			}
			nameOff := binary.LittleEndian.Uint32(a[0:4])
			auxNext := binary.LittleEndian.Uint32(a[4:8])
			name, err := versionString(dynstr, nameOff)
			if err != nil {
				return nil, fmt.Errorf("DT_VERDEF record %d auxiliary %d name: %w", record, aux, err)
			}
			if aux == 0 {
				def.name = name
			} else {
				def.parents = append(def.parents, name)
			}
			last := aux+1 == count
			if last {
				if auxNext != 0 {
					return nil, fmt.Errorf("DT_VERDEF record %d final auxiliary has non-zero next offset", record)
				}
			} else {
				if auxNext < 8 || auxNext%4 != 0 {
					return nil, fmt.Errorf("DT_VERDEF record %d auxiliary %d has invalid next offset %#x", record, aux, auxNext)
				}
				auxVA, ok = addVersionOffset(auxVA, auxNext)
				if !ok {
					return nil, fmt.Errorf("DT_VERDEF auxiliary chain overflows")
				}
			}
		}
		out[idx] = def
		last := record+1 == d.verdefNum
		if last {
			if next != 0 {
				return nil, fmt.Errorf("final DT_VERDEF record has non-zero next offset")
			}
		} else {
			if next < 20 || next%4 != 0 {
				return nil, fmt.Errorf("DT_VERDEF record %d has invalid next offset %#x", record, next)
			}
			recordVA, ok = addVersionOffset(recordVA, next)
			if !ok {
				return nil, fmt.Errorf("DT_VERDEF chain overflows")
			}
		}
	}
	return out, nil
}

func versionFileBytes(ef *elf.File, vaddr, size uint64) ([]byte, error) {
	if size == 0 {
		return []byte{}, nil
	}
	end := vaddr + size
	if end < vaddr {
		return nil, fmt.Errorf("address range overflows")
	}
	for _, p := range ef.Progs {
		if p.Type != elf.PT_LOAD || vaddr < p.Vaddr {
			continue
		}
		fileEnd := p.Vaddr + p.Filesz
		if fileEnd < p.Vaddr || end > fileEnd {
			continue
		}
		off := vaddr - p.Vaddr
		buf := make([]byte, size)
		if _, err := io.ReadFull(io.NewSectionReader(p.ReaderAt, int64(off), int64(size)), buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	return nil, fmt.Errorf("vaddr range 0x%x..0x%x is not wholly backed by a PT_LOAD file range", vaddr, end)
}

func versionString(dynstr []byte, off uint32) (string, error) {
	if off == 0 || uint64(off) >= uint64(len(dynstr)) {
		return "", fmt.Errorf("invalid dynamic-string offset %#x", off)
	}
	end := int(off)
	for end < len(dynstr) && dynstr[end] != 0 {
		end++
	}
	if end == len(dynstr) {
		return "", fmt.Errorf("unterminated dynamic string at offset %#x", off)
	}
	if end == int(off) {
		return "", fmt.Errorf("empty dynamic string at offset %#x", off)
	}
	return string(dynstr[off:end]), nil
}

func addVersionOffset(base uint64, off uint32) (uint64, bool) {
	next := base + uint64(off)
	return next, next >= base
}

func (m *Module) resolveVersionedUndef(s dynSym, req versionRequirement) (uint64, error) {
	if dep := m.directDependency(req.library); dep != nil {
		if provider, ok := dep.lookupDefVersion(s.name, req.name); ok {
			addr, err := dep.symbolAddr(provider)
			return uint64(addr), err
		}
		if s.bind() == stbWeak {
			return 0, nil
		}
		return 0, m.versionCompatibilityError(s.name, req, VersionProviderMissing)
	}

	if resolver, ok := m.resolver.(VersionedResolver); ok {
		addr, err := resolver.LookupVersion(req.library, s.name, req.name)
		if err == nil && addr != 0 {
			return uint64(addr), nil
		}
		if s.bind() == stbWeak {
			return 0, nil
		}
		return 0, m.versionCompatibilityError(s.name, req, VersionProviderMissing)
	}
	if m.authenticated {
		if s.bind() == stbWeak {
			return 0, nil
		}
		return 0, m.versionCompatibilityError(s.name, req, VersionProviderUnavailable)
	}
	if m.resolver != nil {
		addr, err := m.resolver.Lookup(req.library, s.name)
		if err == nil && addr != 0 {
			return uint64(addr), nil
		}
	}
	if s.bind() == stbWeak {
		return 0, nil
	}
	return 0, m.versionCompatibilityError(s.name, req, VersionProviderMissing)
}

func (m *Module) directDependency(soname string) *Module {
	for _, dep := range m.deps {
		if dep == nil {
			continue
		}
		name := dep.soname
		if name == "" {
			name = neededBase(dep.Path)
		}
		if name == soname {
			return dep
		}
	}
	return nil
}

func (m *Module) lookupDefVersion(name, version string) (dynSym, bool) {
	if name == "" || version == "" {
		return dynSym{}, false
	}
	for i, sym := range m.syms {
		if i == 0 || sym.name != name || !sym.defined() || sym.bind() == stbLocal {
			continue
		}
		if sym.version.definition != nil && sym.version.definition.name == version {
			return sym, true
		}
	}
	return dynSym{}, false
}

func (m *Module) versionCompatibilityError(symbol string, req versionRequirement, reason VersionCompatibilityReason) error {
	return &VersionCompatibilityError{
		Module: m.Path, Dependency: req.library, Symbol: symbol, Version: req.name, Reason: reason,
	}
}
