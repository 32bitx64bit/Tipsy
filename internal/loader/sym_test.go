// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"debug/elf"
)

func corruptDynValue(t *testing.T, raw []byte, tag elf.DynTag, value uint64) {
	t.Helper()
	for off := vaDyn; off+16 <= len(raw); off += 16 {
		if elf.DynTag(binary.LittleEndian.Uint64(raw[off:])) == elf.DT_NULL {
			break
		}
		if elf.DynTag(binary.LittleEndian.Uint64(raw[off:])) == tag {
			binary.LittleEndian.PutUint64(raw[off+8:], value)
			return
		}
	}
	t.Fatalf("dynamic tag %v not present", tag)
}

func writeSynth(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenRejectsBogusSyment(t *testing.T) {
	if pageSize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	for _, syment := range []uint64{0, 48, 1 << 62} {
		raw := buildSynthELF(synthOpts{})
		corruptDynValue(t, raw, elf.DT_SYMENT, syment)
		path := writeSynth(t, "libbadsyment.so", raw)
		if _, err := Open(path, mapRes{}); err == nil || !strings.Contains(err.Error(), "DT_SYMENT") {
			t.Fatalf("syment=%d Open error=%v, want DT_SYMENT rejection", syment, err)
		}
	}
}

func TestOpenRejectsTruncatedDynamicRead(t *testing.T) {
	if pageSize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	raw := buildSynthELF(synthOpts{})
	// PT_DYNAMIC is the third program header. Claim more bytes than the file
	// holds so the dynamic read ends short instead of silently truncating.
	const dynPhdr = 64 + 2*56
	binary.LittleEndian.PutUint64(raw[dynPhdr+32:], 1<<20)
	path := writeSynth(t, "libshortdyn.so", raw)
	if _, err := Open(path, mapRes{}); err == nil || !strings.Contains(err.Error(), "PT_DYNAMIC") {
		t.Fatalf("Open error=%v, want short PT_DYNAMIC read rejection", err)
	}
}

type countingResolver struct {
	mu    sync.Mutex
	calls int
}

func (r *countingResolver) Lookup(lib, sym string) (uintptr, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if lib == "" && sym == "memo_need" {
		return 0xabc, nil
	}
	return 0, errMissing{Name: sym}
}

func (r *countingResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestSymbolValueMemoizesUndefined(t *testing.T) {
	r := &countingResolver{}
	m := &Module{
		Path:     "memo.so",
		resolver: r,
		syms: []dynSym{
			{},
			{name: "memo_need", info: stbGlobal << 4},
		},
	}
	m.symIndex = buildSymbolIndex(m.syms)
	for i := 0; i < 3; i++ {
		got, err := m.symbolValue(1)
		if err != nil || got != 0xabc {
			t.Fatalf("symbolValue #%d = %#x, %v", i, got, err)
		}
	}
	if got := r.count(); got != 1 {
		t.Fatalf("undefined symbol resolved %d times, want 1", got)
	}
}

func TestLookupLoadedDiamondFindsSharedDependency(t *testing.T) {
	dep := &Module{
		Path: "dep.so",
		bias: 0x1000,
		syms: []dynSym{
			{},
			{name: "shared", value: 0x40, info: (stbGlobal << 4) | 2, shndx: 1},
		},
	}
	dep.symIndex = buildSymbolIndex(dep.syms)
	a := &Module{Path: "a.so", deps: []*Module{dep}}
	b := &Module{Path: "b.so", deps: []*Module{dep}}
	root := &Module{Path: "root.so", deps: []*Module{a, b}}
	got, ok := root.lookupLoaded("shared")
	if !ok || got != 0x1040 {
		t.Fatalf("lookupLoaded(shared) = %#x, %v; want 0x1040, true", got, ok)
	}
}

func TestBuildSymbolIndexSkipsLocalAndUndefined(t *testing.T) {
	syms := []dynSym{
		{},
		{name: "global", value: 1, info: (stbGlobal << 4) | 2, shndx: 1},
		{name: "local", value: 2, info: (stbLocal << 4) | 2, shndx: 1},
		{name: "undef", info: (stbGlobal << 4) | 2},
		{name: "global", value: 3, info: (stbWeak << 4) | 2, shndx: 1},
	}
	index := buildSymbolIndex(syms)
	if got := index["global"]; len(got) != 2 || got[0] != 1 || got[1] != 4 {
		t.Fatalf("global index = %v, want [1 4]", got)
	}
	if _, ok := index["local"]; ok {
		t.Fatal("local symbol indexed")
	}
	if _, ok := index["undef"]; ok {
		t.Fatal("undefined symbol indexed")
	}
}
