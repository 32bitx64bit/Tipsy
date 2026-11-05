// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

/*
#include <stdint.h>
unsigned int tipsy_unsupported_slot_count(void);
uintptr_t tipsy_unsupported_slot(unsigned int idx);
*/
import "C"

import (
	"debug/elf"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Unsupported-function fallback.
//
// A guest import that nothing provides used to abort the load. By default
// the loader now binds an unresolved *function* import to a per-name
// thunk (unsupported.c). Calling the thunk logs
// "unsupported native function called: <name>" once per name at ERROR
// level and returns 0. The load itself logs
// "native symbol bound to unsupported fallback: <name>" at WARN.
//
// What counts as a function import:
//   - STT_FUNC and STT_GNU_IFUNC symbols, whatever the relocation (a
//     GLOB_DAT or R_X86_64_64 slot holds the address of a function).
//   - STT_NOTYPE symbols only when the relocation is R_X86_64_JUMP_SLOT.
//     A PLT slot can only be called, so it is a function. A NOTYPE
//     symbol reached through GLOB_DAT or R_X86_64_64 may be data, and a
//     thunk address read as data would corrupt the guest silently, so it
//     still aborts.
//   - STT_OBJECT, STT_TLS and every other type are data and still abort.
//
// TIPSY_STRICT_SYMBOLS=1 turns the fallback off: every missing import
// aborts the load as before.
//
// The thunks live in the loader's own read-only text, so they are never
// writable and never need freeing. A name keeps one slot for the life of
// the process and every module that misses it shares that slot.

const strictSymbolsEnv = "TIPSY_STRICT_SYMBOLS"

// strictSymbols reports whether missing imports must abort the load.
func strictSymbols() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(strictSymbolsEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// fallbackEligible reports whether an unresolved import may be bound to
// the unsupported fallback. jumpSlot is true for R_X86_64_JUMP_SLOT.
func fallbackEligible(s dynSym, jumpSlot bool) bool {
	switch s.typ() {
	case byte(elf.STT_FUNC), sttGNUIFUNC:
		return true
	case byte(elf.STT_NOTYPE):
		return jumpSlot
	}
	return false
}

type unsupportedSlots struct {
	mu     sync.Mutex
	names  []string
	byName map[string]uint32
	logged []bool
}

var unsupportedTable unsupportedSlots

// thunk returns the address of name's thunk, allocating a slot on first
// use. ok is false when every slot is taken.
func (t *unsupportedSlots) thunk(name string) (addr uintptr, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx, found := t.byName[name]
	if !found {
		if uint(len(t.names)) >= uint(C.tipsy_unsupported_slot_count()) {
			return 0, false
		}
		if t.byName == nil {
			t.byName = make(map[string]uint32)
		}
		idx = uint32(len(t.names))
		t.names = append(t.names, name)
		t.logged = append(t.logged, false)
		t.byName[name] = idx
	}
	return uintptr(C.tipsy_unsupported_slot(C.uint(idx))), true
}

// hit records a call of slot idx. first is true on the slot's first call.
func (t *unsupportedSlots) hit(idx uint32) (name string, first bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if int(idx) >= len(t.names) {
		return "", false
	}
	first = !t.logged[idx]
	t.logged[idx] = true
	return t.names[idx], first
}

// GoLoader_UnsupportedCalled is the cgo entry for tipsy_unsupported_hit.
// It runs on whatever guest thread called the thunk.
//
//export GoLoader_UnsupportedCalled
func GoLoader_UnsupportedCalled(idx C.uint) {
	name, first := unsupportedTable.hit(uint32(idx))
	if first {
		slog.Error("unsupported native function called: " + name)
	}
}
