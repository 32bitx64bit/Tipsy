// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"debug/elf"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Resolver looks up undefined native symbols. lib is a DT_NEEDED soname or "".
type Resolver interface {
	Lookup(lib, sym string) (uintptr, error)
}

// Module is one mapped Android x86-64 ET_DYN.
type Module struct {
	Path   string
	Base   uintptr // load bias: runtime address of vaddr 0
	Size   uint64
	Needed []string

	mu       sync.Mutex
	closed   bool
	inited   bool
	resolver Resolver
	ef       *elf.File // closed after Open; may be nil
	file     *os.File

	bias       uintptr
	mapStart   uintptr
	mapSize    uintptr
	minVaddr   uint64
	segs       []loadSeg
	relro      []loadSeg
	tlsPresent bool

	dyn    *dynInfo
	syms   []dynSym
	deps   []*Module
	soname string

	missing []string
	missSet map[string]struct{}
}

type loadSession struct {
	mu   sync.Mutex
	busy map[string]*Module
	done map[string]*Module
}

func newSession() *loadSession {
	return &loadSession{busy: map[string]*Module{}, done: map[string]*Module{}}
}

type errMissing struct{ Name string }

func (e errMissing) Error() string {
	return "[loader] missing native symbol: " + e.Name
}

func logMissing(name string) {
	slog.Error("[loader] missing native symbol: " + name)
}

func (m *Module) noteMissing(name string) {
	if name == "" {
		return
	}
	if m.missSet == nil {
		m.missSet = map[string]struct{}{}
	}
	if _, ok := m.missSet[name]; ok {
		return
	}
	m.missSet[name] = struct{}{}
	m.missing = append(m.missing, name)
}

// Open maps path, loads same-dir DT_NEEDED Android DSOs, and applies relocations.
// Constructors run in Init, not here.
func Open(path string, r Resolver) (*Module, error) {
	return open(path, r, newSession())
}

func open(path string, r Resolver, sess *loadSession) (*Module, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	if m := sess.done[abs]; m != nil {
		sess.mu.Unlock()
		return m, nil
	}
	if m := sess.busy[abs]; m != nil {
		sess.mu.Unlock()
		return m, nil
	}
	sess.mu.Unlock()

	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("loader: open %s: %w", path, err)
	}
	ef, err := elf.NewFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("loader: parse %s: %w", path, err)
	}

	if ef.Class != elf.ELFCLASS64 || ef.Machine != elf.EM_X86_64 {
		ef.Close()
		f.Close()
		return nil, fmt.Errorf("loader: %s is %s %s (need ELF64 EM_X86_64)", path, ef.Class, ef.Machine)
	}
	if ef.Type != elf.ET_DYN {
		ef.Close()
		f.Close()
		return nil, fmt.Errorf("loader: %s is %s (need ET_DYN)", path, ef.Type)
	}

	m := &Module{
		Path:     abs,
		resolver: r,
		file:     f,
		ef:       ef,
	}
	sess.mu.Lock()
	sess.busy[abs] = m
	sess.mu.Unlock()

	fail := func(err error) (*Module, error) {
		sess.mu.Lock()
		delete(sess.busy, abs)
		sess.mu.Unlock()
		m.Close()
		return nil, err
	}

	if err := m.mapLoads(f, ef); err != nil {
		return fail(err)
	}

	ents, err := readDynEntries(ef)
	if err != nil {
		return fail(err)
	}
	strtabVA, _ := dynVal(ents, elf.DT_STRTAB)
	strsz, _ := dynVal(ents, elf.DT_STRSZ)
	if strsz == 0 {
		strsz = 1 << 20
	}
	var strtab []byte
	if strtabVA != 0 {
		strtab, err = vaddrFileBytes(ef, strtabVA, strsz)
		if err != nil {
			return fail(fmt.Errorf("loader: strtab %s: %w", path, err))
		}
	}
	d, err := parseDyn(ents, strtab)
	if err != nil {
		return fail(err)
	}
	m.dyn = d
	m.Needed = append([]string(nil), d.needed...)
	m.soname = d.soname

	syms, _, err := loadDynsym(ef, d)
	if err != nil {
		return fail(err)
	}
	m.syms = syms

	dir := filepath.Dir(abs)
	for _, n := range m.Needed {
		base := neededBase(n)
		if _, sys := systemSonames[base]; sys {
			continue
		}
		cand := filepath.Join(dir, base)
		st, err := os.Stat(cand)
		if err != nil || st.IsDir() {
			continue
		}
		dep, err := open(cand, r, sess)
		if err != nil {
			return fail(fmt.Errorf("loader: needed %s: %w", base, err))
		}
		m.deps = append(m.deps, dep)
	}

	if err := m.relocate(); err != nil {
		return fail(err)
	}
	m.hookLLVMEmutls()
	m.protectFinal()

	// Relocs done: we can drop the elf.File parser (fd stays until Close
	// only if still needed; mappings are MAP_PRIVATE so fd can close).
	ef.Close()
	m.ef = nil
	f.Close()
	m.file = nil

	sess.mu.Lock()
	delete(sess.busy, abs)
	sess.done[abs] = m
	sess.mu.Unlock()
	return m, nil
}

// Lookup returns the runtime address of an exported dynamic symbol.
func (m *Module) Lookup(sym string) (uintptr, error) {
	if m == nil {
		return 0, errors.New("loader: nil module")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, errors.New("loader: module closed")
	}
	s, ok := m.lookupDef(sym)
	if !ok {
		return 0, fmt.Errorf("loader: %s does not export %q", m.Path, sym)
	}
	return m.symbolAddr(s)
}

// Init runs DT_PREINIT_ARRAY, DT_INIT, and DT_INIT_ARRAY (after dependencies).
func (m *Module) Init() error {
	if m == nil {
		return errors.New("loader: nil module")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("loader: module closed")
	}
	if m.inited {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	for _, d := range m.deps {
		if err := d.Init(); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inited || m.closed {
		return nil
	}
	slog.Info("[loader] constructors",
		"preinit", m.dyn.preinitSz/8,
		"dt_init", m.dyn.init != 0,
		"init_array", m.dyn.initArraySz/8)
	if err := m.callArray("preinit_array", m.dyn.preinitArray, m.dyn.preinitSz); err != nil {
		return err
	}
	if m.dyn.init != 0 {
		call0(m.bias + uintptr(m.dyn.init))
	}
	if err := m.callArray("init_array", m.dyn.initArray, m.dyn.initArraySz); err != nil {
		return err
	}
	m.inited = true
	return nil
}

// callArray walks one PT_DYNAMIC function-pointer array (vaddr, size). The
// entries must already be relocated: Open applies DT_RELA / DT_JMPREL /
// DT_ANDROID_RELA (APS2) to the mapping before Init runs. Entry values 0 and
// ^0 are skipped (bionic skips them too); everything else is called in order.
func (m *Module) callArray(name string, vaddr, size uint64) error {
	if vaddr == 0 || size == 0 {
		return nil
	}
	n := size / 8
	var called, skipped int
	for i := uint64(0); i < n; i++ {
		fn, err := m.read64(vaddr + i*8)
		if err != nil {
			return err
		}
		if fn == 0 || fn == ^uint64(0) {
			skipped++
			continue
		}
		if i < 8 {
			slog.Info("[loader] constructor", "index", i, "fn", fmt.Sprintf("%#x", fn))
		}
		called++
		call0(uintptr(fn))
	}
	slog.Info("[loader] constructors walked",
		"array", name, "entries", n, "called", called, "skipped", skipped)
	return nil
}

// Close unmaps this image and any DT_NEEDED modules loaded with it.
func (m *Module) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	deps := m.deps
	if m.ef != nil {
		m.ef.Close()
		m.ef = nil
	}
	if m.file != nil {
		m.file.Close()
		m.file = nil
	}
	start, size := m.mapStart, m.mapSize
	m.mapStart = 0
	m.bias = 0
	m.Base = 0
	m.mu.Unlock()

	err := rawMunmap(start, size)
	for _, d := range deps {
		if e := d.Close(); e != nil && err == nil {
			err = e
		}
	}
	return err
}

func (m *Module) peek64(vaddr uint64) (uint64, error) {
	return m.read64(vaddr)
}
