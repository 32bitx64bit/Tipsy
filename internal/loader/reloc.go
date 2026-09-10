// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
)

func isTLSReloc(t elf.R_X86_64) bool {
	switch t {
	case elf.R_X86_64_DTPMOD64, elf.R_X86_64_DTPOFF64, elf.R_X86_64_TPOFF64,
		elf.R_X86_64_TLSGD, elf.R_X86_64_TLSLD, elf.R_X86_64_DTPOFF32,
		elf.R_X86_64_GOTTPOFF, elf.R_X86_64_TPOFF32, elf.R_X86_64_GOTPC32_TLSDESC,
		elf.R_X86_64_TLSDESC, elf.R_X86_64_TLSDESC_CALL:
		return true
	default:
		return false
	}
}

// applyReloc writes one x86-64 relocation. rela=true uses addend; RELATIVE
// with rela=false does *addr += bias (classic REL).
func applyReloc(m *Module, r Reloc, rela bool) error {
	typ := elf.R_X86_64(relocType(r.Info))
	switch typ {
	case elf.R_X86_64_NONE:
		return nil
	case elf.R_X86_64_RELATIVE, elf.R_X86_64_RELATIVE64:
		if rela {
			return m.write64(r.Off, uint64(m.bias)+uint64(r.Addend))
		}
		old, err := m.read64(r.Off)
		if err != nil {
			return err
		}
		return m.write64(r.Off, old+uint64(m.bias))
	case elf.R_X86_64_64:
		sym, err := m.symbolValue(relocSym(r.Info))
		if err != nil {
			return err
		}
		add := uint64(r.Addend)
		if !rela {
			old, err := m.read64(r.Off)
			if err != nil {
				return err
			}
			add = old
		}
		return m.write64(r.Off, sym+add)
	case elf.R_X86_64_GLOB_DAT, elf.R_X86_64_JMP_SLOT:
		sym, err := m.symbolValue(relocSym(r.Info))
		if err != nil {
			return err
		}
		return m.write64(r.Off, sym+uint64(r.Addend))
	case elf.R_X86_64_IRELATIVE:
		fn := m.bias + uintptr(r.Addend)
		val := callIFunc(fn)
		return m.write64(r.Off, uint64(val))
	case elf.R_X86_64_COPY:
		return fmt.Errorf("loader: R_X86_64_COPY not supported")
	default:
		if isTLSReloc(typ) {
			return fmt.Errorf("loader: TLS reloc %s not implemented", typ)
		}
		return fmt.Errorf("loader: unsupported reloc %s", typ)
	}
}

func (m *Module) relocate() error {
	d := m.dyn
	if d == nil {
		return fmt.Errorf("loader: no dynamic section")
	}

	applyOne := func(r Reloc, rela bool) error {
		if err := applyReloc(m, r, rela); err != nil {
			if _, ok := err.(errMissing); ok {
				return nil
			}
			return err
		}
		return nil
	}
	applyList := func(relocs []Reloc, rela bool) error {
		for _, r := range relocs {
			if err := applyOne(r, rela); err != nil {
				return err
			}
		}
		return nil
	}
	applyAPS2 := func(blob []byte, rela bool) error {
		return walkAPS2(blob, func(r Reloc) error {
			return applyOne(r, rela)
		})
	}

	if d.rela != 0 && d.relasz > 0 {
		rs, err := m.readRela(d.rela, d.relasz, d.relaent)
		if err != nil {
			return err
		}
		if err := applyList(rs, true); err != nil {
			return err
		}
	}
	if d.jmprel != 0 && d.pltrelsz > 0 {
		if d.pltrel == uint64(elf.DT_REL) {
			rs, err := m.readRel(d.jmprel, d.pltrelsz, d.relent)
			if err != nil {
				return err
			}
			if err := applyList(rs, false); err != nil {
				return err
			}
		} else {
			rs, err := m.readRela(d.jmprel, d.pltrelsz, d.relaent)
			if err != nil {
				return err
			}
			if err := applyList(rs, true); err != nil {
				return err
			}
		}
	}
	if d.rel != 0 && d.relsz > 0 {
		rs, err := m.readRel(d.rel, d.relsz, d.relent)
		if err != nil {
			return err
		}
		if err := applyList(rs, false); err != nil {
			return err
		}
	}

	packed := false
	if d.androidRela != 0 {
		blob, err := m.bytesAt(d.androidRela, d.androidRelasz)
		if err != nil {
			return fmt.Errorf("loader: DT_ANDROID_RELA: %w", err)
		}
		if err := applyAPS2(blob, true); err != nil {
			return err
		}
		packed = true
	}
	if d.androidRel != 0 && d.androidRela == 0 {
		blob, err := m.bytesAt(d.androidRel, d.androidRelsz)
		if err != nil {
			return fmt.Errorf("loader: DT_ANDROID_REL: %w", err)
		}
		if err := applyAPS2(blob, false); err != nil {
			return err
		}
		packed = true
	}
	if !packed {
		if err := m.applyPackedSections(); err != nil {
			return err
		}
	}

	if len(m.missing) > 0 {
		for _, name := range m.missing {
			logMissing(name)
		}
		return fmt.Errorf("loader: %d unresolved native symbol(s) in %s", len(m.missing), m.Path)
	}
	return nil
}

func (m *Module) applyPackedSections() error {
	if m.ef == nil {
		return nil
	}
	for _, sec := range m.ef.Sections {
		if sec == nil {
			continue
		}
		var rela bool
		switch sec.Type {
		case shtAndroidRela:
			rela = true
		case shtAndroidRel:
			rela = false
		default:
			continue
		}
		data, err := sec.Data()
		if err != nil {
			return err
		}
		if err := walkAPS2(data, func(r Reloc) error {
			if err := applyReloc(m, r, rela); err != nil {
				if _, ok := err.(errMissing); ok {
					return nil
				}
				return err
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) readRela(vaddr, size, ent uint64) ([]Reloc, error) {
	if ent == 0 {
		ent = 24
	}
	b, err := m.bytesAt(vaddr, size)
	if err != nil {
		return nil, err
	}
	var out []Reloc
	for i := 0; i+int(ent) <= len(b); i += int(ent) {
		off := binary.LittleEndian.Uint64(b[i : i+8])
		info := binary.LittleEndian.Uint64(b[i+8 : i+16])
		add := int64(binary.LittleEndian.Uint64(b[i+16 : i+24]))
		out = append(out, Reloc{Off: off, Info: info, Addend: add})
	}
	return out, nil
}

func (m *Module) readRel(vaddr, size, ent uint64) ([]Reloc, error) {
	if ent == 0 {
		ent = 16
	}
	b, err := m.bytesAt(vaddr, size)
	if err != nil {
		return nil, err
	}
	var out []Reloc
	for i := 0; i+int(ent) <= len(b); i += int(ent) {
		off := binary.LittleEndian.Uint64(b[i : i+8])
		info := binary.LittleEndian.Uint64(b[i+8 : i+16])
		out = append(out, Reloc{Off: off, Info: info})
	}
	return out, nil
}

func (m *Module) bytesAt(vaddr, size uint64) ([]byte, error) {
	if size == 0 {
		// Try a reasonable cap for APS2 if size tag missing.
		size = 1 << 20
		if m.mapSize != 0 && uint64(m.mapSize) < size {
			size = uint64(m.mapSize)
		}
	}
	if !m.contains(vaddr, 1) {
		return nil, fmt.Errorf("vaddr 0x%x not mapped", vaddr)
	}
	addr := m.bias + uintptr(vaddr)
	// Clamp to mapping end.
	end := m.mapStart + m.mapSize
	max := uint64(end - addr)
	if size > max {
		size = max
	}
	return append([]byte(nil), sliceAt(addr, int(size))...), nil
}
