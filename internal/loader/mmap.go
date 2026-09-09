// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const mapFixedNoreplace = 0x100000 // linux MAP_FIXED_NOREPLACE

func pageSize() uintptr { return uintptr(syscall.Getpagesize()) }

func pageTrunc(a uintptr) uintptr {
	p := pageSize()
	return a &^ (p - 1)
}

func pageRound(a uintptr) uintptr {
	p := pageSize()
	return (a + p - 1) &^ (p - 1)
}

func protFromFlags(flags elf.ProgFlag) int {
	prot := 0
	if flags&elf.PF_R != 0 {
		prot |= syscall.PROT_READ
	}
	if flags&elf.PF_W != 0 {
		prot |= syscall.PROT_WRITE
	}
	if flags&elf.PF_X != 0 {
		prot |= syscall.PROT_EXEC
	}
	return prot
}

func stagedLoadProt(final int) int {
	// Relocations and BSS initialization may write to load segments, but code
	// is never executable until all loader writes have finished.
	return (final | syscall.PROT_WRITE) &^ syscall.PROT_EXEC
}

func rawMmap(addr, length uintptr, prot, flags, fd int, offset int64) (uintptr, error) {
	if length == 0 {
		return 0, fmt.Errorf("loader: mmap length 0")
	}
	r0, _, e := syscall.Syscall6(
		syscall.SYS_MMAP,
		addr,
		length,
		uintptr(prot),
		uintptr(flags),
		uintptr(fd),
		uintptr(offset),
	)
	if e != 0 {
		return 0, e
	}
	if r0 == 0 {
		return 0, fmt.Errorf("loader: mmap returned NULL")
	}
	return r0, nil
}

func rawMunmap(addr, length uintptr) error {
	if addr == 0 || length == 0 {
		return nil
	}
	_, _, e := syscall.Syscall(syscall.SYS_MUNMAP, addr, length, 0)
	if e != 0 {
		return e
	}
	return nil
}

func rawMprotect(addr, length uintptr, prot int) error {
	if length == 0 {
		return nil
	}
	page := pageSize()
	start := addr &^ (page - 1)
	end := (addr + length + page - 1) &^ (page - 1)
	_, _, e := syscall.Syscall(syscall.SYS_MPROTECT, start, end-start, uintptr(prot))
	if e != 0 {
		return e
	}
	return nil
}

func sliceAt(addr uintptr, n int) []byte {
	if n <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(ptrFromUintptr(addr)), n)
}

// ptrFromUintptr turns a mmap/reloc address into a pointer without a
// uintptr→unsafe.Pointer conversion that go vet rejects.
func ptrFromUintptr(u uintptr) unsafe.Pointer {
	var p unsafe.Pointer
	*(*uintptr)(unsafe.Pointer(&p)) = u
	return p
}

type loadSeg struct {
	vaddr  uint64
	memsz  uint64
	filesz uint64
	prot   int
}

func (m *Module) mapLoads(fd *os.File, ef *elf.File) error {
	var loads []*elf.Prog
	for _, p := range ef.Progs {
		if p.Type == elf.PT_LOAD {
			loads = append(loads, p)
		}
	}
	if len(loads) == 0 {
		return fmt.Errorf("loader: no PT_LOAD in %s", m.Path)
	}

	var minV, maxV uint64
	for i, p := range loads {
		if p.Flags&(elf.PF_W|elf.PF_X) == elf.PF_W|elf.PF_X {
			return fmt.Errorf("loader: writable executable PT_LOAD in %s", m.Path)
		}
		if i == 0 || p.Vaddr < minV {
			minV = p.Vaddr
		}
		end := p.Vaddr + p.Memsz
		if end < p.Vaddr {
			return fmt.Errorf("loader: PT_LOAD overflow in %s", m.Path)
		}
		if end > maxV {
			maxV = end
		}
	}

	pageMin := pageTrunc(uintptr(minV))
	pageMax := pageRound(uintptr(maxV))
	if pageMax <= pageMin {
		return fmt.Errorf("loader: empty load span in %s", m.Path)
	}
	span := pageMax - pageMin

	preferred := pageMin // ET_DYN preferred vaddr; 0 means kernel picks
	mapAddr, err := reserve(preferred, span)
	if err != nil {
		return fmt.Errorf("loader: reserve %s: %w", m.Path, err)
	}
	m.mapStart = mapAddr
	m.mapSize = span
	m.bias = mapAddr - pageMin
	m.Base = m.bias
	m.Size = uint64(span)
	m.minVaddr = uint64(pageMin)

	sysfd := int(fd.Fd())
	for _, p := range loads {
		if err := mapOneLoad(sysfd, m.bias, p); err != nil {
			return fmt.Errorf("loader: PT_LOAD %s: %w", m.Path, err)
		}
		m.segs = append(m.segs, loadSeg{
			vaddr:  p.Vaddr,
			memsz:  p.Memsz,
			filesz: p.Filesz,
			prot:   protFromFlags(p.Flags),
		})
	}
	m.hoistRelocSpans()

	for _, p := range ef.Progs {
		if p.Type == elf.PT_GNU_RELRO {
			m.relro = append(m.relro, loadSeg{vaddr: p.Vaddr, memsz: p.Memsz, prot: syscall.PROT_READ})
		}
		if p.Type == elf.PT_TLS {
			m.tlsPresent = true
		}
	}
	return nil
}

func reserve(preferred, span uintptr) (uintptr, error) {
	flags := syscall.MAP_PRIVATE | syscall.MAP_ANONYMOUS
	if preferred != 0 {
		addr, err := rawMmap(preferred, span, syscall.PROT_NONE, flags|mapFixedNoreplace, -1, 0)
		if err == nil {
			return addr, nil
		}
	}
	return rawMmap(0, span, syscall.PROT_NONE, flags, -1, 0)
}

func mapOneLoad(fd int, bias uintptr, p *elf.Prog) error {
	segStart := bias + uintptr(p.Vaddr)
	segEnd := segStart + uintptr(p.Memsz)
	segPageStart := pageTrunc(segStart)
	segPageEnd := pageRound(segEnd)
	fileEnd := segStart + uintptr(p.Filesz)
	prot := protFromFlags(p.Flags)
	// Keep segments writable while relocations and BSS initialization run,
	// but never writable and executable at the same time. Executable mappings
	// become RX only after relocation validation succeeds.
	mapProt := stagedLoadProt(prot)
	if mapProt&^syscall.PROT_WRITE == 0 && prot == 0 {
		mapProt = syscall.PROT_NONE
	}

	filePageStart := pageTrunc(uintptr(p.Off))
	if p.Filesz > 0 {
		fileLen := uintptr(p.Off) + uintptr(p.Filesz) - filePageStart
		_, err := rawMmap(segPageStart, fileLen, mapProt,
			syscall.MAP_PRIVATE|syscall.MAP_FIXED, fd, int64(filePageStart))
		if err != nil {
			return fmt.Errorf("file map vaddr=0x%x: %w", p.Vaddr, err)
		}
	}

	// Zero the tail of the last file page (file junk past p_filesz).
	zeroFrom := fileEnd
	zeroTo := pageRound(fileEnd)
	if zeroTo > segPageEnd {
		zeroTo = segPageEnd
	}
	if zeroTo > zeroFrom {
		z := sliceAt(zeroFrom, int(zeroTo-zeroFrom))
		for i := range z {
			z[i] = 0
		}
	}

	anonStart := pageRound(fileEnd)
	if segPageEnd > anonStart {
		_, err := rawMmap(anonStart, segPageEnd-anonStart, mapProt,
			syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS|syscall.MAP_FIXED, -1, 0)
		if err != nil {
			return fmt.Errorf("bss map vaddr=0x%x: %w", p.Vaddr, err)
		}
	}
	return nil
}

func (m *Module) protectFinal() error {
	for _, s := range m.segs {
		if s.prot&syscall.PROT_WRITE != 0 && s.prot&syscall.PROT_EXEC != 0 {
			return fmt.Errorf("loader: refusing writable executable final mapping in %s", m.Path)
		}
		addr := m.bias + uintptr(s.vaddr)
		sz := uintptr(s.memsz)
		if err := rawMprotect(pageTrunc(addr), pageRound(addr+sz)-pageTrunc(addr), s.prot); err != nil {
			return fmt.Errorf("loader: final PT_LOAD protection in %s: %w", m.Path, err)
		}
	}
	for _, s := range m.relro {
		addr := m.bias + uintptr(s.vaddr)
		sz := uintptr(s.memsz)
		if sz == 0 {
			continue
		}
		if err := rawMprotect(pageTrunc(addr), pageRound(addr+sz)-pageTrunc(addr), syscall.PROT_READ); err != nil {
			return fmt.Errorf("loader: GNU_RELRO protection in %s: %w", m.Path, err)
		}
	}
	return nil
}

func (m *Module) protectExecutableLoads() error {
	for _, s := range m.segs {
		if s.prot&syscall.PROT_EXEC == 0 {
			continue
		}
		if s.prot&syscall.PROT_WRITE != 0 {
			return fmt.Errorf("loader: refusing writable executable PT_LOAD in %s", m.Path)
		}
		addr := m.bias + uintptr(s.vaddr)
		sz := uintptr(s.memsz)
		if err := rawMprotect(pageTrunc(addr), pageRound(addr+sz)-pageTrunc(addr), s.prot); err != nil {
			return fmt.Errorf("loader: executable PT_LOAD protection in %s: %w", m.Path, err)
		}
	}
	return nil
}

func vaddrFileBytes(ef *elf.File, vaddr, size uint64) ([]byte, error) {
	if size == 0 {
		return []byte{}, nil
	}
	for _, p := range ef.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		if vaddr < p.Vaddr || vaddr >= p.Vaddr+p.Filesz {
			continue
		}
		off := vaddr - p.Vaddr
		n := size
		if off+n > p.Filesz {
			n = p.Filesz - off
		}
		buf := make([]byte, size)
		sr := io.NewSectionReader(p.ReaderAt, int64(off), int64(n))
		if _, err := io.ReadFull(sr, buf[:n]); err != nil {
			return nil, err
		}
		return buf, nil
	}
	return nil, fmt.Errorf("loader: vaddr 0x%x not in PT_LOAD filesz", vaddr)
}

type vaddrSpan struct {
	lo, hi uint64 // [lo, hi)
}

func inSpans(spans []vaddrSpan, vaddr, n uint64) bool {
	end := vaddr + n
	for i := range spans {
		if vaddr >= spans[i].lo && end <= spans[i].hi {
			return true
		}
	}
	return false
}

func (m *Module) hoistRelocSpans() {
	m.execSpans = m.execSpans[:0]
	m.writeSpans = m.writeSpans[:0]
	for _, s := range m.segs {
		if s.memsz == 0 {
			continue
		}
		sp := vaddrSpan{lo: s.vaddr, hi: s.vaddr + s.memsz}
		if s.prot&syscall.PROT_EXEC != 0 {
			m.execSpans = append(m.execSpans, sp)
			continue
		}
		m.writeSpans = append(m.writeSpans, sp)
	}
	m.spansReady = true
}

func (m *Module) ensureRelocSpans() {
	if !m.spansReady {
		m.hoistRelocSpans()
	}
}

func (m *Module) write64(vaddr uint64, val uint64) error {
	if !m.contains(vaddr, 8) {
		return fmt.Errorf("loader: reloc store 0x%x out of range", vaddr)
	}
	m.ensureRelocSpans()
	if inSpans(m.execSpans, vaddr, 8) {
		return fmt.Errorf("loader: text relocation store 0x%x prohibited in %s", vaddr, m.Path)
	}
	addr := m.bias + uintptr(vaddr)
	binary.LittleEndian.PutUint64(sliceAt(addr, 8), val)
	return nil
}

func (m *Module) read64(vaddr uint64) (uint64, error) {
	if !m.contains(vaddr, 8) {
		return 0, fmt.Errorf("loader: reloc load 0x%x out of range", vaddr)
	}
	addr := m.bias + uintptr(vaddr)
	return binary.LittleEndian.Uint64(sliceAt(addr, 8)), nil
}

func (m *Module) contains(vaddr, n uint64) bool {
	m.ensureRelocSpans()
	return inSpans(m.execSpans, vaddr, n) || inSpans(m.writeSpans, vaddr, n)
}
