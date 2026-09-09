// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestStagedLoadProtectionsNeverWritableExecutable(t *testing.T) {
	for final := 0; final < 8; final++ {
		got := stagedLoadProt(final)
		if got&syscall.PROT_WRITE != 0 && got&syscall.PROT_EXEC != 0 {
			t.Fatalf("final=%#x staged=%#x is W+X", final, got)
		}
		if final&syscall.PROT_EXEC != 0 && got&syscall.PROT_EXEC != 0 {
			t.Fatalf("final=%#x retained execute during staging: %#x", final, got)
		}
	}
}

func TestRelocationCannotWriteGuestExecutableSegment(t *testing.T) {
	mem, err := syscall.Mmap(-1, 0, syscall.Getpagesize(), syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(mem)
	copy(mem, []byte("signed-guest-code"))
	want := append([]byte(nil), mem[:32]...)

	m := &Module{
		Path: "guest.so",
		bias: uintptr(unsafe.Pointer(&mem[0])),
		segs: []loadSeg{{vaddr: 0, memsz: uint64(len(mem)), filesz: uint64(len(mem)), prot: syscall.PROT_READ | syscall.PROT_EXEC}},
	}
	if err := m.write64(8, 0x1122334455667788); err == nil || !strings.Contains(err.Error(), "text relocation") {
		t.Fatalf("write64 error=%v, want prohibited text relocation", err)
	}
	if !m.spansReady || len(m.execSpans) != 1 || len(m.writeSpans) != 0 {
		t.Fatalf("lazy hoist: ready=%v exec=%d write=%d", m.spansReady, len(m.execSpans), len(m.writeSpans))
	}
	if !bytes.Equal(mem[:32], want) {
		t.Fatalf("guest executable bytes changed: %x want %x", mem[:32], want)
	}
}

func TestRelocationCanWriteGuestWritableSegment(t *testing.T) {
	mem, err := syscall.Mmap(-1, 0, syscall.Getpagesize(), syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(mem)

	const want uint64 = 0x1122334455667788
	m := &Module{
		Path: "guest.so",
		bias: uintptr(unsafe.Pointer(&mem[0])),
		segs: []loadSeg{{vaddr: 0, memsz: uint64(len(mem)), filesz: uint64(len(mem)), prot: syscall.PROT_READ | syscall.PROT_WRITE}},
	}
	if err := m.write64(8, want); err != nil {
		t.Fatal(err)
	}
	if !m.spansReady || len(m.writeSpans) != 1 || len(m.execSpans) != 0 {
		t.Fatalf("lazy hoist: ready=%v write=%d exec=%d", m.spansReady, len(m.writeSpans), len(m.execSpans))
	}
	got := binary.LittleEndian.Uint64(mem[8:])
	if got != want {
		t.Fatalf("writable store %#x want %#x", got, want)
	}
}

func TestOpenRejectsTextRelocation(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	path := filepath.Join(t.TempDir(), "libtextrel.so")
	if err := os.WriteFile(path, buildSynthELF(synthOpts{withTextReloc: true}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, mapRes{}); err == nil || !strings.Contains(err.Error(), "text relocation") {
		t.Fatalf("Open error=%v, want prohibited text relocation", err)
	}
}

func TestOpenRejectsWritableExecutableSegment(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	path := filepath.Join(t.TempDir(), "librwx.so")
	if err := os.WriteFile(path, buildSynthELF(synthOpts{writableExec: true}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, mapRes{}); err == nil || !strings.Contains(err.Error(), "writable executable") {
		t.Fatalf("Open error=%v, want writable executable rejection", err)
	}
}

func TestOpenLeavesNoWritableExecutableMapping(t *testing.T) {
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	path := filepath.Join(t.TempDir(), "libwxcheck.so")
	if err := os.WriteFile(path, buildSynthELF(synthOpts{}), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(path, mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if got, want := sliceAt(m.bias, synthRXEnd), buildSynthELF(synthOpts{})[:synthRXEnd]; !bytes.Equal(got, want) {
		t.Fatal("loader changed authenticated executable file bytes")
	}

	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := m.mapStart, m.mapStart+m.mapSize
	seen := false
	for _, line := range strings.Split(string(maps), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		parts := strings.SplitN(fields[0], "-", 2)
		if len(parts) != 2 {
			continue
		}
		start, e1 := strconv.ParseUint(parts[0], 16, 64)
		end, e2 := strconv.ParseUint(parts[1], 16, 64)
		if e1 != nil || e2 != nil || uintptr(end) <= lo || uintptr(start) >= hi {
			continue
		}
		seen = true
		if strings.Contains(fields[1], "w") && strings.Contains(fields[1], "x") {
			t.Fatalf("loader mapping is W+X: %s", line)
		}
	}
	if !seen {
		t.Fatal(fmt.Sprintf("no /proc/self/maps entries for module [%#x,%#x)", lo, hi))
	}
}
