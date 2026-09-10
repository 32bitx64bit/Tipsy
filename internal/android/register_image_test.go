// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"encoding/binary"
	"syscall"
	"testing"
	"unsafe"
)

// fakeELF writes a minimal ELF64 header whose program header table is
// controlled by the caller. The buffer must be page sized.
func fakeELF(t *testing.T, phoff uint64, phnum uint16) []byte {
	t.Helper()
	mem, err := syscall.Mmap(-1, 0, 4096, syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(mem) })
	mem[0], mem[1], mem[2], mem[3] = 0x7f, 'E', 'L', 'F'
	mem[4] = 2                                  // ELFCLASS64
	mem[5] = 1                                  // ELFDATA2LSB
	binary.LittleEndian.PutUint16(mem[54:], 56) // e_phentsize
	binary.LittleEndian.PutUint16(mem[56:], phnum)
	binary.LittleEndian.PutUint64(mem[32:], phoff)
	return mem
}

func fakeBase(mem []byte) uintptr {
	return uintptr(unsafe.Pointer(&mem[0]))
}

func TestRegisterImageRejectsOutOfMappingPhdrs(t *testing.T) {
	base := fakeBase(fakeELF(t, 1<<40, 4))
	before := testImageGeneration()
	RegisterImage(base, "/fake/overflow.so")
	if after := testImageGeneration(); after != before {
		t.Fatalf("registration accepted out-of-mapping phdrs (generation %d -> %d)", before, after)
	}
	if start, _, _ := testImageCodeRange(base + 0x100); start != 0 {
		t.Fatalf("out-of-mapping image contributed a code range at %#x", start)
	}
}

func TestRegisterImageExtractsMappedCodeRanges(t *testing.T) {
	mem := fakeELF(t, 64, 1)
	// One PT_LOAD at vaddr 0x100, PF_R|PF_X, memsz 0x50.
	binary.LittleEndian.PutUint32(mem[64:], 1)
	binary.LittleEndian.PutUint32(mem[64+4:], 5)
	binary.LittleEndian.PutUint64(mem[64+16:], 0x100)
	binary.LittleEndian.PutUint64(mem[64+40:], 0x50)
	base := fakeBase(mem)

	before := testImageGeneration()
	RegisterImage(base, "/fake/mapped.so")
	t.Cleanup(func() { UnregisterImage(base) })
	if after := testImageGeneration(); after == before {
		t.Fatal("registration rejected a program header table inside the mapping")
	}
	start, end, moduleClass := testImageCodeRange(base + 0x100)
	if start != base+0x100 || end != base+0x150 || moduleClass != 1 {
		t.Fatalf("code range = [%#x,%#x) class %d, want [%#x,%#x) class 1",
			start, end, moduleClass, base+0x100, base+0x150)
	}
}
