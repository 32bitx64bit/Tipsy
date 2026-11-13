// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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
	// Missing functions bind to the unsupported fallback by default; the
	// abort this test checks is the strict mode.
	t.Setenv("TIPSY_STRICT_SYMBOLS", "1")
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
	needed         []string
	imports        []string
	soname         string
	versionNeeds   []synthVersionNeed
	versionDefs    []synthVersionDef
	withAPS2       bool
	withIRel       bool
	withTextReloc  bool
	writableExec   bool
	ctorCallImport string // when non-empty, init_array ctor calls this imported function then increments vaGuard
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
	if opt.ctorCallImport != "" {
		opt.imports = append(append([]string(nil), opt.imports...), opt.ctorCallImport)
	}
	fileSize := synthRWVaddr + synthRWFile
	buf := make([]byte, fileSize)

	// RX code: DT_INIT = ret; IFUNC resolver returns 0xAABB; vaCtor sets
	// the guard byte and returns (movb $1, [rip+disp32]; ret).
	buf[vaInit] = 0xC3
	copy(buf[vaIFunc:], []byte{0xB8, 0xBB, 0xAA, 0x00, 0x00, 0xC3})
	if opt.ctorCallImport != "" {
		// vaCtor calls the function pointer in vaGlobSlot (GLOB_DAT), then
		// atomically increments the guard byte at vaGuard:
		//   sub $8, %rsp               ; 48 83 EC 08       (4B, next +4)
		//   mov (%rip+loadDisp), %rax  ; 48 8B 05 <disp32> (7B, next +11)
		//   call *%rax                 ; FF D0             (2B, next +13)
		//   add $8, %rsp               ; 48 83 C4 08       (4B, next +17)
		//   lock incb (%rip+guardDisp) ; F0 FE 05 <disp32> (7B, next +24)
		//   ret                        ; C3                (1B, next +25)
		// The sub/add keep RSP 16-byte aligned before call per SysV AMD64 ABI.
		loadDisp := int32(vaGlobSlot - (vaCtor + 11))
		guardDisp := int32(vaGuard - (vaCtor + 24))
		copy(buf[vaCtor:], []byte{
			0x48, 0x83, 0xEC, 0x08,
			0x48, 0x8B, 0x05, byte(loadDisp), byte(loadDisp >> 8), byte(loadDisp >> 16), byte(loadDisp >> 24),
			0xFF, 0xD0,
			0x48, 0x83, 0xC4, 0x08,
			0xF0, 0xFE, 0x05, byte(guardDisp), byte(guardDisp >> 8), byte(guardDisp >> 16), byte(guardDisp >> 24),
			0xC3,
		})
	} else {
		// C6 05 <rel32> 01  C3: movb $1, [rip+rel32]; ret. Instruction is 7
		// bytes, so the RIP-relative displacement is vaGuard - (vaCtor + 7).
		ctorDisp := int32(vaGuard - (vaCtor + 7))
		copy(buf[vaCtor:], []byte{
			0xC6, 0x05, byte(ctorDisp), byte(ctorDisp >> 8), byte(ctorDisp >> 16), byte(ctorDisp >> 24), 0x01,
			0xC3,
		})
	}

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

	relInfo := func(sym uint32, typ elf.R_X86_64) uint64 {
		return makeRelInfo(sym, uint32(typ))
	}
	initArrTarget := int64(vaInit)
	if opt.ctorCallImport != "" {
		initArrTarget = int64(vaCtor)
	}
	var relas []Reloc
	relas = append(relas, Reloc{Off: vaRelSlot, Info: relInfo(0, elf.R_X86_64_RELATIVE), Addend: 0x42})
	relas = append(relas, Reloc{Off: vaInitArr, Info: relInfo(0, elf.R_X86_64_RELATIVE), Addend: initArrTarget})
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
		// The init_array entry itself is relocated by the packed stream: the
		// ctor pointer exists only after APS2 application, before Init walks.
		aps2Relocs = append(aps2Relocs, Reloc{
			Off:    vaInitArr,
			Info:   relInfo(0, elf.R_X86_64_RELATIVE),
			Addend: int64(vaCtor),
		})
		aps2 = encodeAPS2(aps2Relocs)
		copy(buf[vaAPS2:], aps2)
	}

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

func TestModuleSegmentBoundsAndCloseInvalidation(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	t.Setenv("TIPSY_TRACE_CTORS", "1")
	path := filepath.Join(t.TempDir(), "libsegments.so")
	raw := buildSynthELF(synthOpts{withAPS2: true, withIRel: true})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prev)

	m, err := Open(path, mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = m.Close()
		}
	}()

	if !m.IsOpen() {
		t.Fatal("expected module IsOpen() == true")
	}
	if got := len(m.ReadableSegments()); got != 2 {
		t.Fatalf("ReadableSegments len = %d, want 2", got)
	}
	if got := len(m.ExecutableSegments()); got != 1 {
		t.Fatalf("ExecutableSegments len = %d, want 1", got)
	}
	if got := len(m.DataSegments()); got != 1 {
		t.Fatalf("DataSegments len = %d, want 1", got)
	}

	rxLo, rxHi := m.Base, m.Base+synthRXEnd
	rwLo, rwHi := m.Base+synthRWVaddr, m.Base+synthRWVaddr+synthRWMem

	if !m.ContainsExec(rxLo+vaInit, 16) || !m.ContainsReadable(rxLo+vaInit, 16) {
		t.Fatal("expected RX code range to be executable and readable")
	}
	if m.ContainsData(rxLo+vaInit, 16) {
		t.Fatal("RX segment must not satisfy ContainsData")
	}
	if !m.OverlapsExec(rxLo-8, 16) || !m.OverlapsExec(rxHi-4, 8) {
		t.Fatal("boundary spans crossing RX segment must report OverlapsExec")
	}

	if !m.ContainsData(rwLo, 16) || !m.ContainsData(m.Base+vaBSSCheck, 16) || !m.ContainsReadable(m.Base+vaBSSCheck, 16) {
		t.Fatal("expected RW data and BSS ranges to satisfy ContainsData and ContainsReadable")
	}
	if m.ContainsExec(rwLo, 16) || m.OverlapsExec(rwLo, synthRWMem) {
		t.Fatal("RW/BSS segment must not satisfy ContainsExec or OverlapsExec")
	}

	// Unmapped hole between RX and RW, zero, and overflow spans fail closed.
	if m.ContainsReadable(rxHi, 8) || m.ContainsExec(rxHi, 8) || m.ContainsData(rxHi, 8) || m.OverlapsExec(rxHi, synthRWVaddr-synthRXEnd) {
		t.Fatal("unmapped hole between PT_LOAD segments accepted")
	}
	if m.ContainsReadable(0, 8) || m.ContainsReadable(rxLo, 0) || m.ContainsReadable(^uintptr(0)-3, 8) || m.OverlapsExec(^uintptr(0)-3, 8) {
		t.Fatal("zero or wrapping address span accepted")
	}
	if m.ContainsReadable(rwHi-4, 8) || m.ContainsData(rwHi-4, 8) {
		t.Fatal("span crossing end of RW segment accepted")
	}

	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "[loader] constructor") || !strings.Contains(logs, "index=0") {
		t.Fatalf("expected constructor trace with index, got:\n%s", logs)
	}
	if strings.Contains(logs, "0x") || strings.Contains(logs, "fn=") {
		t.Fatalf("loader Info/Warn logs leaked raw ASLR pointer:\n%s", logs)
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	if m.IsOpen() {
		t.Fatal("expected IsOpen() == false after Close")
	}
	if len(m.ReadableSegments()) != 0 || len(m.ExecutableSegments()) != 0 || len(m.DataSegments()) != 0 {
		t.Fatal("expected segment slices cleared after Close")
	}
	if m.ContainsReadable(rxLo, 8) || m.ContainsExec(rxLo, 8) || m.ContainsData(rwLo, 8) || m.OverlapsExec(rxLo, 8) {
		t.Fatal("segment queries succeeded after Close")
	}
}

// ctorInterceptHandler intercepts the unsupported-function fallback log record
// emitted synchronously on the native constructor pthread when a synthetic
// constructor calls an unresolved STT_FUNC import through vaGlobSlot.
type ctorInterceptHandler struct {
	next   slog.Handler
	target string
	onHit  func()
}

func (h *ctorInterceptHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *ctorInterceptHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.target && h.onHit != nil {
		h.onHit()
	}
	return h.next.Handle(ctx, r)
}

func (h *ctorInterceptHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ctorInterceptHandler{next: h.next.WithAttrs(attrs), target: h.target, onHit: h.onHit}
}

func (h *ctorInterceptHandler) WithGroup(name string) slog.Handler {
	return &ctorInterceptHandler{next: h.next.WithGroup(name), target: h.target, onHit: h.onHit}
}

var ctorProbeSeq atomic.Uint64

func installCtorProbe(t *testing.T, prefix string, onHit func()) string {
	t.Helper()
	t.Setenv("TIPSY_STRICT_SYMBOLS", "")
	sym := fmt.Sprintf("%s_%d", prefix, ctorProbeSeq.Add(1))
	prev := slog.Default()
	slog.SetDefault(slog.New(&ctorInterceptHandler{
		next:   slog.NewTextHandler(io.Discard, nil),
		target: "unsupported native function called: " + sym,
		onHit:  onHit,
	}))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return sym
}

// TestInitConstructorLookupReentrant pins the dlsym self-deadlock fix: Init
// must not hold m.mu while constructors run, because a constructor resolving
// a symbol re-enters Lookup on the same module.
func TestInitConstructorLookupReentrant(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}

	var (
		mu           sync.Mutex
		mod          *Module
		calls        int
		lookupAddr   uintptr
		lookupErr    error
		containsExec bool
	)
	sym := installCtorProbe(t, "tipsy_ctor_reentrant_lookup", func() {
		mu.Lock()
		m := mod
		calls++
		mu.Unlock()
		addr, err := m.Lookup("JNI_OnLoad")
		execOK := m.ContainsExec(addr, 1)
		mu.Lock()
		lookupAddr, lookupErr, containsExec = addr, err, execOK
		mu.Unlock()
	})

	path := filepath.Join(t.TempDir(), "libreentrant.so")
	raw := buildSynthELF(synthOpts{ctorCallImport: sym})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	needClose := true
	defer func() {
		if needClose {
			_ = m.Close()
		}
	}()
	mu.Lock()
	mod = m
	mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- m.Init() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
	case <-time.After(10 * time.Second):
		needClose = false
		t.Fatal("Init deadlocked: constructor Lookup re-entered the module mutex")
	}

	mu.Lock()
	gotCalls, gotAddr, gotErr, gotExec := calls, lookupAddr, lookupErr, containsExec
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("constructor callback count = %d, want 1", gotCalls)
	}
	if gotErr != nil {
		t.Fatalf("Lookup inside constructor: %v", gotErr)
	}
	if want := m.bias + vaInit; gotAddr != want {
		t.Fatalf("Lookup inside constructor = %#x, want %#x", gotAddr, want)
	}
	if !gotExec {
		t.Fatal("ContainsExec inside constructor returned false for JNI_OnLoad")
	}
	guard, err := m.peek64(vaGuard)
	if err != nil {
		t.Fatal(err)
	}
	if guard&0xff != 1 {
		t.Fatalf("guard after Init = %#x, want byte 1 (constructor did not finish)", guard)
	}
}

// TestInitConcurrentSingleFlight pins that concurrent Init callers wait for
// the in-flight constructor walk rather than returning early or running
// constructors more than once, while concurrent Lookup callers proceed.
func TestInitConcurrentSingleFlight(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var probeCalls atomic.Int32

	sym := installCtorProbe(t, "tipsy_ctor_singleflight", func() {
		probeCalls.Add(1)
		enteredOnce.Do(func() { close(entered) })
		<-release
	})

	path := filepath.Join(t.TempDir(), "libsingleflight.so")
	raw := buildSynthELF(synthOpts{ctorCallImport: sym})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	needClose := true
	defer func() {
		if needClose {
			_ = m.Close()
		}
	}()

	type initResult struct {
		err   error
		guard uint64
	}
	var wg sync.WaitGroup
	var finished atomic.Int32
	results := make(chan initResult, 3)
	startInit := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := m.Init()
			g, peekErr := m.peek64(vaGuard)
			if err == nil && peekErr != nil {
				err = peekErr
			}
			finished.Add(1)
			results <- initResult{err: err, guard: g}
		}()
	}

	startInit() // claims the flight, then pauses inside the native constructor
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		needClose = false
		close(release)
		t.Fatal("constructor never entered")
	}

	// Concurrent Lookup on another goroutine must not block while constructors run.
	lookupDone := make(chan error, 1)
	go func() {
		addr, err := m.Lookup("JNI_OnLoad")
		if err == nil && addr != m.bias+vaInit {
			err = fmt.Errorf("Lookup = %#x, want %#x", addr, m.bias+vaInit)
		}
		lookupDone <- err
	}()
	select {
	case err := <-lookupDone:
		if err != nil {
			needClose = false
			close(release)
			t.Fatalf("concurrent Lookup during Init: %v", err)
		}
	case <-time.After(2 * time.Second):
		needClose = false
		close(release)
		t.Fatal("concurrent Lookup blocked while constructor was in flight")
	}

	startInit() // must wait for the in-flight attempt, not return early
	startInit()
	time.Sleep(50 * time.Millisecond)
	if n := finished.Load(); n != 0 {
		needClose = false
		close(release)
		t.Fatalf("%d Init calls returned while constructors were still in flight", n)
	}

	close(release)
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		needClose = false
		t.Fatal("concurrent Init deadlocked")
	}
	close(results)
	for res := range results {
		if res.err != nil {
			t.Fatalf("concurrent Init: %v", res.err)
		}
		if res.guard&0xff != 1 {
			t.Fatalf("caller observed guard=%#x upon Init return, want 1", res.guard)
		}
	}

	if got := probeCalls.Load(); got != 1 {
		t.Fatalf("constructor probe ran %d times, want 1", got)
	}
	guard, err := m.peek64(vaGuard)
	if err != nil {
		t.Fatal(err)
	}
	if guard&0xff != 1 {
		t.Fatalf("guard after Init = %#x, want 1 (constructor ran %d times)", guard, guard&0xff)
	}
	if err := m.Init(); err != nil {
		t.Fatalf("Init after completion: %v", err)
	}
	guard, err = m.peek64(vaGuard)
	if err != nil {
		t.Fatal(err)
	}
	if guard&0xff != 1 {
		t.Fatalf("constructors re-ran on idempotent Init: guard=%#x", guard)
	}
}

// TestCloseWaitsForInFlightInit verifies Close does not unmap module segments
// while a constructor is still executing native instructions.
func TestCloseWaitsForInFlightInit(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	sym := installCtorProbe(t, "tipsy_ctor_close_wait", func() {
		enteredOnce.Do(func() { close(entered) })
		<-release
	})

	path := filepath.Join(t.TempDir(), "libclosewait.so")
	raw := buildSynthELF(synthOpts{ctorCallImport: sym})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, mapRes{})
	if err != nil {
		t.Fatal(err)
	}

	initDone := make(chan error, 1)
	go func() { initDone <- m.Init() }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("constructor never entered")
	}

	var closeFinished atomic.Bool
	closeDone := make(chan error, 1)
	go func() {
		err := m.Close()
		closeFinished.Store(true)
		closeDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if closeFinished.Load() {
		close(release)
		t.Fatal("Close returned while constructor was still executing")
	}

	close(release)
	select {
	case err := <-initDone:
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Init hung while Close was waiting")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung after Init finished")
	}
	if m.IsOpen() {
		t.Fatal("expected IsOpen() == false after Close")
	}
	if _, err := m.Lookup("JNI_OnLoad"); err == nil {
		t.Fatal("expected Lookup error after Close")
	}
}
