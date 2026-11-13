// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"bufio"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *lockedBuf {
	t.Helper()
	buf := &lockedBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// openSynthImport opens a synthetic library importing name. info is the
// dynsym st_info of the import; relType replaces the import's relocation
// type unless it is zero.
func openSynthImport(t *testing.T, name string, info byte, relType elf.R_X86_64) (*Module, error) {
	t.Helper()
	if pageSize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
	raw := buildSynthELF(synthOpts{imports: []string{name}})
	const importSym = 2 // after STN_UNDEF and JNI_OnLoad
	raw[vaDynsym+importSym*24+4] = info
	if relType != 0 {
		patched := false
		for off := vaRela; off < vaRela+24*8; off += 24 {
			inf := binary.LittleEndian.Uint64(raw[off+8:])
			if elf.R_X86_64(relocType(inf)) == elf.R_X86_64_GLOB_DAT {
				binary.LittleEndian.PutUint64(raw[off+8:], makeRelInfo(relocSym(inf), uint32(relType)))
				patched = true
			}
		}
		if !patched {
			t.Fatal("import relocation not found in synthetic ELF")
		}
	}
	m, err := Open(writeSynth(t, "libimport.so", raw), mapRes{})
	if m != nil {
		t.Cleanup(func() { _ = m.Close() })
	}
	return m, err
}

const (
	infoFunc   = stbGlobal<<4 | byte(elf.STT_FUNC)
	infoObject = stbGlobal<<4 | byte(elf.STT_OBJECT)
	infoNoType = stbGlobal<<4 | byte(elf.STT_NOTYPE)
)

func TestMissingFunctionBindsToUnsupportedFallback(t *testing.T) {
	const name = "tipsy_unsupported_fn_bind"
	logs := captureLog(t)
	m, err := openSynthImport(t, name, infoFunc, 0)
	if err != nil {
		t.Fatalf("Open: %v\nlog:\n%s", err, logs)
	}
	if len(m.missing) != 0 {
		t.Fatalf("missing=%v, want none for a bound function", m.missing)
	}
	thunk, err := m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	if thunk == 0 {
		t.Fatal("GLOB_DAT slot is 0, want fallback thunk address")
	}
	if want := "native symbol bound to unsupported fallback: " + name; !strings.Contains(logs.String(), want) {
		t.Fatalf("log lacks %q:\n%s", want, logs)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("bind not logged at WARN:\n%s", logs)
	}
	if strings.Contains(logs.String(), "unsupported native function called") {
		t.Fatalf("called message logged before any call:\n%s", logs)
	}

	// Garbage arguments must not matter: the thunk returns 0.
	if got := CallP8(uintptr(thunk), 1, 2, 3, 4, 5, 6, 7, 8); got != 0 {
		t.Fatalf("thunk returned %#x, want 0", got)
	}
	called := "level=ERROR msg=\"unsupported native function called: " + name + "\""
	if !strings.Contains(logs.String(), called) {
		t.Fatalf("log lacks %q:\n%s", called, logs)
	}
	// Once per name.
	if got := CallP8(uintptr(thunk), 0, 0, 0, 0, 0, 0, 0, 0); got != 0 {
		t.Fatalf("second call returned %#x, want 0", got)
	}
	if n := strings.Count(logs.String(), "unsupported native function called: "+name); n != 1 {
		t.Fatalf("called message logged %d times, want 1:\n%s", n, logs)
	}
}

func TestUnsupportedThunkIsExecutableNotWritable(t *testing.T) {
	addr, ok := (&unsupportedSlots{}).thunk("tipsy_unsupported_fn_perm")
	if !ok || addr == 0 {
		t.Fatal("no thunk")
	}
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		lo, hi, _ := strings.Cut(fields[0], "-")
		start, e1 := strconv.ParseUint(lo, 16, 64)
		end, e2 := strconv.ParseUint(hi, 16, 64)
		if e1 != nil || e2 != nil || uint64(addr) < start || uint64(addr) >= end {
			continue
		}
		if perms := fields[1]; perms[0] != 'r' || perms[1] == 'w' || perms[2] != 'x' {
			t.Fatalf("thunk %#x in mapping with perms %s, want r-x", addr, perms)
		}
		return
	}
	t.Fatalf("thunk %#x not in /proc/self/maps", addr)
}

func TestMissingDataImportStillFails(t *testing.T) {
	const name = "tipsy_unsupported_data_import"
	logs := captureLog(t)
	_, err := openSynthImport(t, name, infoObject, 0)
	if err == nil {
		t.Fatal("Open succeeded, want failure for a missing data import")
	}
	if !strings.Contains(err.Error(), "unresolved native symbol") {
		t.Fatalf("error %v", err)
	}
	if want := "[loader] missing native symbol: " + name; !strings.Contains(logs.String(), want) {
		t.Fatalf("log lacks %q:\n%s", want, logs)
	}
	if strings.Contains(logs.String(), "bound to unsupported fallback") {
		t.Fatalf("data import was bound to the fallback:\n%s", logs)
	}
}

func TestStrictSymbolsRestoresAbortForMissingFunction(t *testing.T) {
	const name = "tipsy_unsupported_fn_strict"
	t.Setenv("TIPSY_STRICT_SYMBOLS", "1")
	logs := captureLog(t)
	_, err := openSynthImport(t, name, infoFunc, 0)
	if err == nil {
		t.Fatal("Open succeeded under TIPSY_STRICT_SYMBOLS=1, want abort")
	}
	if want := "[loader] missing native symbol: " + name; !strings.Contains(logs.String(), want) {
		t.Fatalf("log lacks %q:\n%s", want, logs)
	}
	if strings.Contains(logs.String(), "bound to unsupported fallback") {
		t.Fatalf("function was bound under strict mode:\n%s", logs)
	}
}

func TestNoTypeImportBindsOnlyThroughJumpSlot(t *testing.T) {
	t.Run("JMP_SLOT binds", func(t *testing.T) {
		const name = "tipsy_unsupported_notype_plt"
		logs := captureLog(t)
		m, err := openSynthImport(t, name, infoNoType, elf.R_X86_64_JMP_SLOT)
		if err != nil {
			t.Fatalf("Open: %v\n%s", err, logs)
		}
		if got, _ := m.peek64(vaGlobSlot); got == 0 {
			t.Fatal("JUMP_SLOT slot not bound")
		}
	})
	t.Run("GLOB_DAT aborts", func(t *testing.T) {
		const name = "tipsy_unsupported_notype_got"
		logs := captureLog(t)
		if _, err := openSynthImport(t, name, infoNoType, 0); err == nil {
			t.Fatalf("Open succeeded, want abort for NOTYPE via GLOB_DAT\n%s", logs)
		}
	})
}

func TestFunctionImportBindsViaJumpSlot(t *testing.T) {
	const name = "tipsy_unsupported_fn_plt"
	logs := captureLog(t)
	m, err := openSynthImport(t, name, infoFunc, elf.R_X86_64_JMP_SLOT)
	if err != nil {
		t.Fatalf("Open: %v\n%s", err, logs)
	}
	thunk, _ := m.peek64(vaGlobSlot)
	if thunk == 0 || CallP8(uintptr(thunk), 0, 0, 0, 0, 0, 0, 0, 0) != 0 {
		t.Fatalf("thunk %#x not callable returning 0", thunk)
	}
}

func TestUnsupportedSlotsAreStablePerNameAndBounded(t *testing.T) {
	var tab unsupportedSlots
	a, ok := tab.thunk("a")
	if !ok {
		t.Fatal("no slot for a")
	}
	b, _ := tab.thunk("b")
	a2, _ := tab.thunk("a")
	if a != a2 || a == b {
		t.Fatalf("slots a=%#x a again=%#x b=%#x", a, a2, b)
	}
	for i := 2; i < 1024; i++ {
		if _, ok := tab.thunk(fmt.Sprintf("fill%d", i)); !ok {
			t.Fatalf("slot %d refused before the table was full", i)
		}
	}
	if _, ok := tab.thunk("one-too-many"); ok {
		t.Fatal("slot handed out beyond the table")
	}
	if _, ok := tab.thunk("a"); !ok {
		t.Fatal("existing name lost its slot when the table filled")
	}
}
