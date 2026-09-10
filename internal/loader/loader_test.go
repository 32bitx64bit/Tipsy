// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

const (
	synthPage    = 0x1000
	synthRXEnd   = 0x2000
	synthRWVaddr = 0x4000
	synthRWFile  = 0x1000
	synthRWMem   = 0x2000

	vaInit     = 0x1000
	vaIFunc    = 0x1010
	vaCtor     = 0x1020 // guard-writing ctor; init_array entry via APS2 reloc
	vaRelSlot  = 0x4000
	vaGlobSlot = 0x4008
	vaAPS2Slot = 0x4010
	vaIRelSlot = 0x4018
	vaInitArr  = 0x4020
	vaGuard    = 0x4028 // byte set by vaCtor
	vaBSSCheck = 0x5000 // first BSS byte page (filesz ends at 0x5000)

	vaDynsym  = 0x4100
	vaDynstr  = 0x4200
	vaHash    = 0x4300
	vaRela    = 0x4340
	vaAPS2    = 0x4400
	vaDyn     = 0x4500
	vaVersym  = 0x4700
	vaVerdef  = 0x4800
	vaVerneed = 0x4900
)

type mapRes map[string]uintptr // key "lib|sym" or "|sym"

func (r mapRes) Lookup(lib, sym string) (uintptr, error) {
	if a, ok := r[lib+"|"+sym]; ok {
		return a, nil
	}
	if a, ok := r["|"+sym]; ok {
		return a, nil
	}
	return 0, errMissing{Name: sym}
}

func TestOpenSynthetic(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "libtipsy_test.so")
	raw := buildSynthELF(synthOpts{
		needed:   []string{"libc.so"},
		imports:  []string{"tipsy_test_sym"},
		withAPS2: true,
		withIRel: true,
	})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	const resolved = uintptr(0x1111222233334444)
	m, err := Open(path, mapRes{
		"libc.so|tipsy_test_sym": resolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	if m.Base == 0 {
		t.Fatal("zero load bias")
	}
	if len(m.Needed) != 1 || m.Needed[0] != "libc.so" {
		t.Fatalf("Needed=%v", m.Needed)
	}

	got, err := m.peek64(vaRelSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(m.bias)+0x42 {
		t.Fatalf("RELATIVE slot %#x want bias+0x42 (%#x)", got, uint64(m.bias)+0x42)
	}

	got, err = m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(resolved) {
		t.Fatalf("GLOB_DAT %#x want %#x", got, resolved)
	}

	got, err = m.peek64(vaAPS2Slot)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(m.bias)+0x100 {
		t.Fatalf("APS2 RELATIVE %#x want %#x", got, uint64(m.bias)+0x100)
	}

	// The init_array entry must be relocated by the APS2 stream BEFORE the
	// ctor walk reads it (Android-packed layout): it points at the ctor.
	got, err = m.peek64(vaInitArr)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(m.bias)+vaCtor {
		t.Fatalf("APS2 init_array entry %#x want bias+%#x", got, vaCtor)
	}
	if guard, err := m.peek64(vaGuard); err != nil || guard != 0 {
		t.Fatalf("guard before Init = %#x err %v, want 0", guard, err)
	}

	got, err = m.peek64(vaIRelSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0xAABB {
		t.Fatalf("IRELATIVE %#x want 0xAABB", got)
	}

	bss, err := m.peek64(vaBSSCheck)
	if err != nil {
		t.Fatal(err)
	}
	if bss != 0 {
		t.Fatalf("BSS not zero: %#x", bss)
	}

	jni, err := m.Lookup("JNI_OnLoad")
	if err != nil {
		t.Fatal(err)
	}
	if jni != m.bias+vaInit {
		t.Fatalf("JNI_OnLoad %#x want %#x", jni, m.bias+vaInit)
	}

	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	if err := m.Init(); err != nil {
		t.Fatal("second Init should be idempotent")
	}

	// The APS2-relocated init_array ctor must have run natively and set
	// its guard byte; a dropped packed reloc would leave the entry at 0
	// (skipped) and the guard unset.
	guard, err := m.peek64(vaGuard)
	if err != nil {
		t.Fatal(err)
	}
	if guard&0xff != 1 {
		t.Fatalf("guard after Init = %#x, want byte 1 (init_array ctor did not run)", guard)
	}
}

func TestOpenMissingSymbol(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	path := filepath.Join(t.TempDir(), "libmissing.so")
	raw := buildSynthELF(synthOpts{imports: []string{"tipsy_missing"}})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	_, err := Open(path, mapRes{})
	if err == nil {
		t.Fatal("expected unresolved error")
	}
	if !strings.Contains(err.Error(), "unresolved") && !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(logBuf.String(), "[loader] missing native symbol: tipsy_missing") {
		t.Fatalf("log missing prefix:\n%s", logBuf.String())
	}
}

func TestOpenLocalNeeded(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	dir := t.TempDir()
	depPath := filepath.Join(dir, "libdep.so")
	dep := buildSynthELF(synthOpts{})
	if err := os.WriteFile(depPath, dep, 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "libmain.so")
	main := buildSynthELF(synthOpts{
		needed:  []string{"libdep.so"},
		imports: []string{"JNI_OnLoad"},
	})
	if err := os.WriteFile(mainPath, main, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Open(mainPath, mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got, err := m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got == 0 {
		t.Fatal("dep symbol resolved to 0")
	}
	// JNI_OnLoad in the dep is at bias_dep+0x1000; main's GLOB_DAT should match dep Lookup.
	if len(m.deps) != 1 {
		t.Fatalf("deps=%d", len(m.deps))
	}
	want, err := m.deps[0].Lookup("JNI_OnLoad")
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(want) {
		t.Fatalf("GLOB_DAT %#x want %#x", got, want)
	}
}

func TestSystemSonameNotLoadedFromDisk(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	dir := t.TempDir()
	// A file named libc.so must not be mmap'd; bionic libc is Resolver-only.
	if err := os.WriteFile(filepath.Join(dir, "libc.so"), buildSynthELF(synthOpts{}), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "libapp.so")
	raw := buildSynthELF(synthOpts{needed: []string{"libc.so"}, imports: []string{"tipsy_test_sym"}})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, mapRes{"libc.so|tipsy_test_sym": 0x55})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(m.deps) != 0 {
		t.Fatalf("loaded system soname from disk: %d deps", len(m.deps))
	}
	got, err := m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x55 {
		t.Fatalf("resolved %#x", got)
	}
}

func TestOpenRejectsWrongArch(t *testing.T) {
	t.Parallel()
	var hdr elf.Header64
	hdr.Ident[0] = 0x7f
	hdr.Ident[1] = 'E'
	hdr.Ident[2] = 'L'
	hdr.Ident[3] = 'F'
	hdr.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	hdr.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	hdr.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	hdr.Type = uint16(elf.ET_DYN)
	hdr.Machine = uint16(elf.EM_AARCH64)
	hdr.Version = uint32(elf.EV_CURRENT)
	hdr.Ehsize = 64
	hdr.Phentsize = 56
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &hdr); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "arm.so")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, mapRes{}); err == nil {
		t.Fatal("expected arch error")
	}
}

func TestCallJNIOnLoadTrampoline(t *testing.T) {
	// Tiny SysV function: mov eax, 0x10006; ret   (JNI_VERSION_1_6)
	code := []byte{0xB8, 0x06, 0x00, 0x01, 0x00, 0xC3}
	page := syscall.Getpagesize()
	mem, err := syscall.Mmap(-1, 0, page, syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(mem)
	copy(mem, code)
	if err := syscall.Mprotect(mem, syscall.PROT_READ|syscall.PROT_EXEC); err != nil {
		t.Fatal(err)
	}
	fn := uintptr(unsafe.Pointer(&mem[0]))
	got := CallJNIOnLoad(fn, 0, 0)
	if got != 0x10006 {
		t.Fatalf("got %#x", got)
	}
}

type synthOpts struct {
	needed        []string
	imports       []string
	soname        string
	versionNeeds  []synthVersionNeed
	versionDefs   []synthVersionDef
	withAPS2      bool
	withIRel      bool
	withTextReloc bool
	writableExec  bool
}

type synthVersionNeed struct {
	library string
	version string
	imports []string
	flags   uint16
}

type synthVersionDef struct {
	version string
	exports []string
	flags   uint16
	hidden  bool
}

func buildSynthELF(opt synthOpts) []byte {
	fileSize := synthRWVaddr + synthRWFile
	buf := make([]byte, fileSize)

	// RX code: DT_INIT = ret; IFUNC resolver returns 0xAABB; vaCtor sets
	// the guard byte and returns (movb $1, [rip+disp32]; ret).
	buf[vaInit] = 0xC3
	copy(buf[vaIFunc:], []byte{0xB8, 0xBB, 0xAA, 0x00, 0x00, 0xC3})
	// C6 05 <rel32> 01  C3: movb $1, [rip+rel32]; ret. Instruction is 7
	// bytes, so the RIP-relative displacement is vaGuard - (vaCtor + 7).
	ctorDisp := int32(vaGuard - (vaCtor + 7))
	copy(buf[vaCtor:], []byte{
		0xC6, 0x05, byte(ctorDisp), byte(ctorDisp >> 8), byte(ctorDisp >> 16), byte(ctorDisp >> 24), 0x01,
		0xC3,
	})

	type ds struct {
		name  string
		value uint64
		info  byte
		shndx uint16
	}
	syms := []ds{{name: ""}} // STN_UNDEF
	dynstr := []byte{0}
	addStr := func(s string) uint32 {
		off := uint32(len(dynstr))
		dynstr = append(dynstr, s...)
		dynstr = append(dynstr, 0)
		return off
	}
	addStr("JNI_OnLoad")
	syms = append(syms, ds{name: "JNI_OnLoad", value: vaInit, info: (1 << 4) | 2, shndx: 1})
	importIdx := map[string]int{}
	for _, im := range opt.imports {
		addStr(im)
		importIdx[im] = len(syms)
		syms = append(syms, ds{name: im, info: (1 << 4) | 2, shndx: 0})
	}
	neededOff := make([]uint32, len(opt.needed))
	for i, n := range opt.needed {
		neededOff[i] = addStr(n)
	}
	sonameOff := uint32(0)
	if opt.soname != "" {
		sonameOff = addStr(opt.soname)
	}
	versionNeedNameOff := make([]uint32, len(opt.versionNeeds))
	versionNeedLibraryOff := make([]uint32, len(opt.versionNeeds))
	for i, need := range opt.versionNeeds {
		versionNeedLibraryOff[i] = addStr(need.library)
		versionNeedNameOff[i] = addStr(need.version)
	}
	versionDefNameOff := make([]uint32, len(opt.versionDefs))
	for i, def := range opt.versionDefs {
		versionDefNameOff[i] = addStr(def.version)
	}

	// Dynsym at vaDynsym
	symRaw := make([]byte, 24*len(syms))
	nameOff := func(name string) uint32 {
		if name == "" {
			return 0
		}
		o := 0
		for o < len(dynstr) {
			end := o
			for end < len(dynstr) && dynstr[end] != 0 {
				end++
			}
			if string(dynstr[o:end]) == name {
				return uint32(o)
			}
			o = end + 1
		}
		return 0
	}
	for i, s := range syms {
		b := symRaw[i*24:]
		binary.LittleEndian.PutUint32(b[0:4], nameOff(s.name))
		b[4] = s.info
		binary.LittleEndian.PutUint16(b[6:8], s.shndx)
		binary.LittleEndian.PutUint64(b[8:16], s.value)
	}
	copy(buf[vaDynsym:], symRaw)
	copy(buf[vaDynstr:], dynstr)

	if len(opt.versionNeeds) > 0 || len(opt.versionDefs) > 0 {
		versyms := make([]uint16, len(syms))
		for i := 1; i < len(versyms); i++ {
			versyms[i] = 1
		}
		nextVersionIndex := uint16(2)
		for i, def := range opt.versionDefs {
			idx := nextVersionIndex
			nextVersionIndex++
			for symIdx, sym := range syms {
				for _, name := range def.exports {
					if sym.name == name && sym.shndx != 0 {
						versyms[symIdx] = idx
						if def.hidden {
							versyms[symIdx] |= gnuVersionHidden
						}
					}
				}
			}
			record := vaVerdef + i*28
			binary.LittleEndian.PutUint16(buf[record:], gnuVersionCurrent)
			binary.LittleEndian.PutUint16(buf[record+2:], def.flags)
			binary.LittleEndian.PutUint16(buf[record+4:], idx)
			binary.LittleEndian.PutUint16(buf[record+6:], 1)
			binary.LittleEndian.PutUint32(buf[record+8:], uint32(0x1000+i))
			binary.LittleEndian.PutUint32(buf[record+12:], 20)
			if i+1 < len(opt.versionDefs) {
				binary.LittleEndian.PutUint32(buf[record+16:], 28)
			}
			binary.LittleEndian.PutUint32(buf[record+20:], versionDefNameOff[i])
		}
		for i, need := range opt.versionNeeds {
			idx := nextVersionIndex
			nextVersionIndex++
			for symIdx, sym := range syms {
				for _, name := range need.imports {
					if sym.name == name && sym.shndx == 0 {
						versyms[symIdx] = idx
					}
				}
			}
			record := vaVerneed + i*32
			binary.LittleEndian.PutUint16(buf[record:], gnuVersionCurrent)
			binary.LittleEndian.PutUint16(buf[record+2:], 1)
			binary.LittleEndian.PutUint32(buf[record+4:], versionNeedLibraryOff[i])
			binary.LittleEndian.PutUint32(buf[record+8:], 16)
			if i+1 < len(opt.versionNeeds) {
				binary.LittleEndian.PutUint32(buf[record+12:], 32)
			}
			binary.LittleEndian.PutUint32(buf[record+16:], uint32(0x2000+i))
			binary.LittleEndian.PutUint16(buf[record+20:], need.flags)
			binary.LittleEndian.PutUint16(buf[record+22:], idx)
			binary.LittleEndian.PutUint32(buf[record+24:], versionNeedNameOff[i])
		}
		for i, value := range versyms {
			binary.LittleEndian.PutUint16(buf[vaVersym+i*2:], value)
		}
	}

	// SysV hash: nchain = nsyms
	binary.LittleEndian.PutUint32(buf[vaHash:], 1)                   // nbucket
	binary.LittleEndian.PutUint32(buf[vaHash+4:], uint32(len(syms))) // nchain
	binary.LittleEndian.PutUint32(buf[vaHash+8:], 0)                 // bucket[0]
	// chains already zero

	relInfo := func(sym uint32, typ elf.R_X86_64) uint64 {
		return makeRelInfo(sym, uint32(typ))
	}
	var relas []Reloc
	relas = append(relas, Reloc{Off: vaRelSlot, Info: relInfo(0, elf.R_X86_64_RELATIVE), Addend: 0x42})
	relas = append(relas, Reloc{Off: vaInitArr, Info: relInfo(0, elf.R_X86_64_RELATIVE), Addend: int64(vaInit)})
	if opt.withIRel {
		relas = append(relas, Reloc{Off: vaIRelSlot, Info: relInfo(0, elf.R_X86_64_IRELATIVE), Addend: int64(vaIFunc)})
	}
	if opt.withTextReloc {
		relas = append(relas, Reloc{Off: vaInit + 8, Info: relInfo(0, elf.R_X86_64_RELATIVE), Addend: 0x55})
	}
	for _, im := range opt.imports {
		relas = append(relas, Reloc{
			Off:  vaGlobSlot,
			Info: relInfo(uint32(importIdx[im]), elf.R_X86_64_GLOB_DAT),
		})
	}
	relaRaw := make([]byte, 24*len(relas))
	for i, r := range relas {
		b := relaRaw[i*24:]
		binary.LittleEndian.PutUint64(b[0:8], r.Off)
		binary.LittleEndian.PutUint64(b[8:16], r.Info)
		binary.LittleEndian.PutUint64(b[16:24], uint64(r.Addend))
	}
	copy(buf[vaRela:], relaRaw)

	var aps2 []byte
	if opt.withAPS2 {
		aps2Relocs := []Reloc{{
			Off:    vaAPS2Slot,
			Info:   relInfo(0, elf.R_X86_64_RELATIVE),
			Addend: 0x100,
		}}
		// The init_array entry itself is relocated by the packed stream
		// (as in Android-packed libs like libroblox.so): the ctor pointer
		// exists only after APS2 application, before the Init walk.
		aps2Relocs = append(aps2Relocs, Reloc{
			Off:    vaInitArr,
			Info:   relInfo(0, elf.R_X86_64_RELATIVE),
			Addend: int64(vaCtor),
		})
		aps2 = encodeAPS2(aps2Relocs)
		copy(buf[vaAPS2:], aps2)
	}

	// Dynamic
	w := vaDyn
	put := func(tag elf.DynTag, val uint64) {
		binary.LittleEndian.PutUint64(buf[w:], uint64(tag))
		binary.LittleEndian.PutUint64(buf[w+8:], val)
		w += 16
	}
	for i := range opt.needed {
		put(elf.DT_NEEDED, uint64(neededOff[i]))
	}
	if opt.soname != "" {
		put(elf.DT_SONAME, uint64(sonameOff))
	}
	put(elf.DT_HASH, vaHash)
	put(elf.DT_STRTAB, vaDynstr)
	put(elf.DT_SYMTAB, vaDynsym)
	put(elf.DT_STRSZ, uint64(len(dynstr)))
	put(elf.DT_SYMENT, 24)
	if len(opt.versionNeeds) > 0 || len(opt.versionDefs) > 0 {
		put(elf.DT_VERSYM, vaVersym)
	}
	if len(opt.versionDefs) > 0 {
		put(elf.DT_VERDEF, vaVerdef)
		put(elf.DT_VERDEFNUM, uint64(len(opt.versionDefs)))
	}
	if len(opt.versionNeeds) > 0 {
		put(elf.DT_VERNEED, vaVerneed)
		put(elf.DT_VERNEEDNUM, uint64(len(opt.versionNeeds)))
	}
	put(elf.DT_RELA, vaRela)
	put(elf.DT_RELASZ, uint64(len(relaRaw)))
	put(elf.DT_RELAENT, 24)
	put(elf.DT_INIT, vaInit)
	put(elf.DT_INIT_ARRAY, vaInitArr)
	put(elf.DT_INIT_ARRAYSZ, 8)
	if opt.withAPS2 {
		put(dtAndroidRela, vaAPS2)
		put(dtAndroidRelasz, uint64(len(aps2)))
	}
	put(elf.DT_NULL, 0)
	dynSize := w - vaDyn

	// ELF header + 3 program headers
	var hdr elf.Header64
	hdr.Ident[0], hdr.Ident[1], hdr.Ident[2], hdr.Ident[3] = 0x7f, 'E', 'L', 'F'
	hdr.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	hdr.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	hdr.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	hdr.Ident[elf.EI_OSABI] = byte(elf.ELFOSABI_NONE)
	hdr.Type = uint16(elf.ET_DYN)
	hdr.Machine = uint16(elf.EM_X86_64)
	hdr.Version = uint32(elf.EV_CURRENT)
	hdr.Phoff = 64
	hdr.Ehsize = 64
	hdr.Phentsize = 56
	hdr.Phnum = 3
	hdr.Shentsize = 64
	hb := new(bytes.Buffer)
	_ = binary.Write(hb, binary.LittleEndian, &hdr)
	copy(buf[0:], hb.Bytes())

	writePhdr := func(off int, p elf.Prog64) {
		pb := new(bytes.Buffer)
		_ = binary.Write(pb, binary.LittleEndian, &p)
		copy(buf[off:], pb.Bytes())
	}
	rxFlags := elf.PF_R | elf.PF_X
	if opt.writableExec {
		rxFlags |= elf.PF_W
	}
	writePhdr(64, elf.Prog64{
		Type:   uint32(elf.PT_LOAD),
		Flags:  uint32(rxFlags),
		Off:    0,
		Vaddr:  0,
		Paddr:  0,
		Filesz: synthRXEnd,
		Memsz:  synthRXEnd,
		Align:  synthPage,
	})
	writePhdr(64+56, elf.Prog64{
		Type:   uint32(elf.PT_LOAD),
		Flags:  uint32(elf.PF_R | elf.PF_W),
		Off:    synthRWVaddr,
		Vaddr:  synthRWVaddr,
		Paddr:  synthRWVaddr,
		Filesz: synthRWFile,
		Memsz:  synthRWMem,
		Align:  synthPage,
	})
	writePhdr(64+112, elf.Prog64{
		Type:   uint32(elf.PT_DYNAMIC),
		Flags:  uint32(elf.PF_R | elf.PF_W),
		Off:    vaDyn,
		Vaddr:  vaDyn,
		Paddr:  vaDyn,
		Filesz: uint64(dynSize),
		Memsz:  uint64(dynSize),
		Align:  8,
	})
	return buf
}

// peek64 is a test-only read alias used by the synthetic-module assertions.
func (m *Module) peek64(vaddr uint64) (uint64, error) {
	return m.read64(vaddr)
}
