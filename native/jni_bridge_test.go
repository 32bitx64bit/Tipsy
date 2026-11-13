// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package native

import (
	"bufio"
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const mutateROEnv = "TIPSY_TEST_MUTATE_RO_JNI"

func TestJNITablesInProcessReadOnly(t *testing.T) {
	switch os.Getenv(mutateROEnv) {
	case "g_native":
		crashWriteNativeTable()
		t.Fatal("write to g_native succeeded; expected SIGSEGV")
	case "g_invoke":
		crashWriteInvokeTable()
		t.Fatal("write to g_invoke succeeded; expected SIGSEGV")
	}

	if rc := initBridge(); rc != 0 {
		t.Fatalf("tipsy_jni_init() = %d, want 0", rc)
	}

	nativeAddr := nativeTableAddr()
	invokeAddr := invokeTableAddr()
	vmAddr := javaVMAddr()
	if nativeAddr == 0 || invokeAddr == 0 || vmAddr == 0 {
		t.Fatalf("table addresses: g_native=%#x g_invoke=%#x g_vm=%#x", nativeAddr, invokeAddr, vmAddr)
	}

	// Verify all 229 JNINativeInterface_ slots (indices 4..232) and 5 JNIInvokeInterface_ slots (indices 3..7).
	nativeSlots := readPointerSlots(nativeAddr, nativeTableSize())
	if len(nativeSlots) != 233 {
		t.Fatalf("JNINativeInterface_ slot count = %d, want 233", len(nativeSlots))
	}
	for i := 0; i < 4; i++ {
		if nativeSlots[i] != 0 {
			t.Fatalf("g_native reserved%d = %#x, want 0", i, nativeSlots[i])
		}
	}
	for i := 4; i < len(nativeSlots); i++ {
		if nativeSlots[i] == 0 {
			t.Fatalf("g_native slot %d is NULL", i)
		}
	}

	invokeSlots := readPointerSlots(invokeAddr, invokeTableSize())
	if len(invokeSlots) != 8 {
		t.Fatalf("JNIInvokeInterface_ slot count = %d, want 8", len(invokeSlots))
	}
	for i := 0; i < 3; i++ {
		if invokeSlots[i] != 0 {
			t.Fatalf("g_invoke reserved%d = %#x, want 0", i, invokeSlots[i])
		}
	}
	for i := 3; i < len(invokeSlots); i++ {
		if invokeSlots[i] == 0 {
			t.Fatalf("g_invoke slot %d is NULL", i)
		}
	}

	// Verify /proc/self/maps marks the runtime mappings containing g_native and g_invoke read-only (r--p).
	for _, target := range []struct {
		name string
		addr uintptr
		size uintptr
	}{
		{name: "g_native", addr: nativeAddr, size: nativeTableSize()},
		{name: "g_invoke", addr: invokeAddr, size: invokeTableSize()},
	} {
		perms, err := lookupSelfMapPerms(target.addr, target.size)
		if err != nil {
			t.Fatalf("%s map lookup: %v", target.name, err)
		}
		if !strings.HasPrefix(perms, "r--") {
			t.Fatalf("%s mapping permissions = %q, want read-only r--*", target.name, perms)
		}
	}

	// Verify g_test_wrapped_native hook redirects env->functions without mutating g_native or g_invoke.
	beforeNative := append([]uintptr(nil), nativeSlots...)
	beforeInvoke := append([]uintptr(nil), invokeSlots...)
	beforeFn, wrappedFn, restoredFn, version, calls, ownerOK := exerciseWrappedNativeMain()
	if beforeFn != nativeAddr {
		t.Fatalf("initial env->functions = %#x, want g_native %#x", beforeFn, nativeAddr)
	}
	if wrappedFn == 0 || wrappedFn == nativeAddr {
		t.Fatalf("wrapped env->functions = %#x, want distinct g_test_wrapped_native", wrappedFn)
	}
	if restoredFn != nativeAddr {
		t.Fatalf("restored env->functions = %#x, want g_native %#x", restoredFn, nativeAddr)
	}
	if version != 0x00010006 {
		t.Fatalf("GetVersion() = %#x, want JNI_VERSION_1_6 (0x00010006)", version)
	}
	if calls != 1 || !ownerOK {
		t.Fatalf("wrapped FindClass calls=%d ownerOK=%v, want calls=1 ownerOK=true", calls, ownerOK)
	}
	if !reflect.DeepEqual(beforeNative, readPointerSlots(nativeAddr, nativeTableSize())) {
		t.Fatal("g_native contents changed after test wrapper toggle")
	}
	if !reflect.DeepEqual(beforeInvoke, readPointerSlots(invokeAddr, invokeTableSize())) {
		t.Fatal("g_invoke contents changed after test wrapper toggle")
	}

	// Verify hardware write protection faults with SIGSEGV when mutating g_native or g_invoke.
	for _, target := range []string{"g_native", "g_invoke"} {
		t.Run("write_fault_"+target, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestJNITablesInProcessReadOnly$")
			cmd.Env = append(os.Environ(), mutateROEnv+"="+target)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("child write to %s exited 0; output:\n%s", target, out)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("unexpected exec error for %s: %v", target, err)
			}
			ws, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGSEGV {
				t.Fatalf("child write to %s status=%v (signal=%v), want SIGSEGV; output:\n%s",
					target, exitErr, ws.Signal(), out)
			}
		})
	}
}

func TestJNITablesPIERELROPlacement(t *testing.T) {
	// Inspect /proc/self/exe using runtime addresses (go test strips .symtab on ephemeral test binaries).
	assertSelfELFReadOnlyTables(t)

	// Build an explicit -buildmode=pie test binary (-c preserves .symtab) and verify .data.rel.ro + PT_GNU_RELRO + BIND_NOW.
	pieBin := filepath.Join(t.TempDir(), "native_pie.test")
	buildCmd := exec.Command("go", "test", "-c", "-buildmode=pie", "-o", pieBin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go test -c -buildmode=pie failed: %v\n%s", err, out)
	}

	assertELFReadOnlyTables(t, pieBin, true)

	runCmd := exec.Command(pieBin, "-test.run=^TestJNITablesInProcessReadOnly$")
	if out, err := runCmd.CombinedOutput(); err != nil {
		t.Fatalf("PIE test binary failed: %v\n%s", err, out)
	}
}

func assertSelfELFReadOnlyTables(t *testing.T) {
	t.Helper()

	f, err := elf.Open("/proc/self/exe")
	if err != nil {
		t.Fatalf("elf.Open(/proc/self/exe): %v", err)
	}
	defer f.Close()

	slide, err := selfELFSlide(f)
	if err != nil {
		t.Fatalf("selfELFSlide: %v", err)
	}

	var relroProg *elf.Prog
	for _, p := range f.Progs {
		if p.Type == elf.PT_GNU_RELRO {
			relroProg = p
			break
		}
	}
	if f.Type == elf.ET_DYN && relroProg == nil {
		t.Fatal("/proc/self/exe: PIE binary missing PT_GNU_RELRO")
	}

	for _, target := range []struct {
		name string
		addr uintptr
		size uintptr
	}{
		{name: "g_native", addr: nativeTableAddr(), size: nativeTableSize()},
		{name: "g_invoke", addr: invokeTableAddr(), size: invokeTableSize()},
	} {
		vaddr := uint64(target.addr) - slide
		vend := vaddr + uint64(target.size)
		sec := sectionContaining(f, vaddr, uint64(target.size))
		if sec == nil {
			t.Fatalf("/proc/self/exe: no section covers %s [%#x, %#x)", target.name, vaddr, vend)
		}
		if sec.Name == ".bss" || sec.Name == ".data" {
			t.Fatalf("/proc/self/exe: %s in writable section %s", target.name, sec.Name)
		}
		if f.Type == elf.ET_DYN {
			if !strings.HasPrefix(sec.Name, ".data.rel.ro") {
				t.Fatalf("/proc/self/exe: PIE %s in section %q, want .data.rel.ro*", target.name, sec.Name)
			}
			if vaddr < relroProg.Vaddr || vend > relroProg.Vaddr+relroProg.Memsz {
				t.Fatalf("/proc/self/exe: %s [%#x, %#x) outside PT_GNU_RELRO [%#x, %#x)",
					target.name, vaddr, vend, relroProg.Vaddr, relroProg.Vaddr+relroProg.Memsz)
			}
		} else if sec.Name != ".rodata" && !strings.HasPrefix(sec.Name, ".data.rel.ro") {
			t.Fatalf("/proc/self/exe: %s in unexpected section %q", target.name, sec.Name)
		}
	}
	if f.Type == elf.ET_DYN && !hasFullRELROBindNow(t, f) {
		t.Fatal("/proc/self/exe: PIE binary missing BIND_NOW for full RELRO")
	}
}

func sectionContaining(f *elf.File, vaddr, size uint64) *elf.Section {
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NULL || (s.Flags&elf.SHF_ALLOC) == 0 {
			continue
		}
		if vaddr >= s.Addr && vaddr+size <= s.Addr+s.Size {
			return s
		}
	}
	return nil
}

func selfELFSlide(f *elf.File) (uint64, error) {
	if f.Type == elf.ET_EXEC {
		return 0, nil
	}
	exeReal, err := filepath.EvalSymlinks("/proc/self/exe")
	if err != nil {
		return 0, err
	}
	var firstLoad *elf.Prog
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD && p.Off == 0 {
			firstLoad = p
			break
		}
	}
	if firstLoad == nil {
		return 0, errors.New("no PT_LOAD segment at offset 0")
	}
	mapsFile, err := os.Open("/proc/self/maps")
	if err != nil {
		return 0, err
	}
	defer mapsFile.Close()
	scanner := bufio.NewScanner(mapsFile)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 || filepath.Clean(fields[5]) != filepath.Clean(exeReal) {
			continue
		}
		off, err := strconv.ParseUint(fields[2], 16, 64)
		if err != nil || off != 0 {
			continue
		}
		bounds := strings.SplitN(fields[0], "-", 2)
		lo, err := strconv.ParseUint(bounds[0], 16, 64)
		if err != nil {
			return 0, err
		}
		return lo - firstLoad.Vaddr, nil
	}
	return 0, errors.New("offset-0 mapping for /proc/self/exe not found")
}

func assertELFReadOnlyTables(t *testing.T, path string, requirePIE bool) {
	t.Helper()

	f, err := elf.Open(path)
	if err != nil {
		t.Fatalf("elf.Open(%s): %v", path, err)
	}
	defer f.Close()

	if requirePIE && f.Type != elf.ET_DYN {
		t.Fatalf("%s ELF type = %v, want ET_DYN (PIE)", path, f.Type)
	}

	syms, err := f.Symbols()
	if err != nil {
		t.Fatalf("f.Symbols(%s): %v", path, err)
	}

	wantSizes := map[string]uint64{
		"g_native": uint64(nativeTableSize()),
		"g_invoke": uint64(invokeTableSize()),
	}
	found := make(map[string]elf.Symbol)
	for _, sym := range syms {
		wantSize, ok := wantSizes[sym.Name]
		if !ok || sym.Size != wantSize {
			continue
		}
		found[sym.Name] = sym
	}
	for name, wantSize := range wantSizes {
		if _, ok := found[name]; !ok {
			t.Fatalf("%s: symbol %s (size %d) not found in .symtab", path, name, wantSize)
		}
	}

	var relroProg *elf.Prog
	for _, p := range f.Progs {
		if p.Type == elf.PT_GNU_RELRO {
			relroProg = p
			break
		}
	}
	if f.Type == elf.ET_DYN && relroProg == nil {
		t.Fatalf("%s: missing PT_GNU_RELRO program header", path)
	}

	for name, sym := range found {
		if int(sym.Section) < 0 || int(sym.Section) >= len(f.Sections) {
			t.Fatalf("%s: symbol %s has invalid section index %v", path, name, sym.Section)
		}
		sec := f.Sections[sym.Section]
		if sec.Name == ".bss" || sec.Name == ".data" {
			t.Fatalf("%s: %s placed in writable section %s", path, name, sec.Name)
		}
		if f.Type == elf.ET_DYN {
			if !strings.HasPrefix(sec.Name, ".data.rel.ro") {
				t.Fatalf("%s: PIE symbol %s placed in section %q, want .data.rel.ro*", path, name, sec.Name)
			}
			relroEnd := relroProg.Vaddr + relroProg.Memsz
			symEnd := sym.Value + sym.Size
			if sym.Value < relroProg.Vaddr || symEnd > relroEnd {
				t.Fatalf("%s: %s [%#x, %#x) outside PT_GNU_RELRO [%#x, %#x)",
					path, name, sym.Value, symEnd, relroProg.Vaddr, relroEnd)
			}
		} else if sec.Name != ".rodata" && !strings.HasPrefix(sec.Name, ".data.rel.ro") {
			t.Fatalf("%s: symbol %s placed in unexpected section %q", path, name, sec.Name)
		}
	}

	if f.Type == elf.ET_DYN {
		if !hasFullRELROBindNow(t, f) {
			t.Fatalf("%s: missing BIND_NOW / DF_BIND_NOW / DF_1_NOW for full RELRO", path)
		}
	}
}

func hasFullRELROBindNow(t *testing.T, f *elf.File) bool {
	t.Helper()
	dynSec := f.SectionByType(elf.SHT_DYNAMIC)
	if dynSec == nil {
		return false
	}
	data, err := dynSec.Data()
	if err != nil {
		t.Fatalf("read .dynamic: %v", err)
	}
	for len(data) >= 16 {
		tag := elf.DynTag(f.ByteOrder.Uint64(data[0:8]))
		val := f.ByteOrder.Uint64(data[8:16])
		data = data[16:]
		if tag == elf.DT_NULL {
			break
		}
		if tag == elf.DT_BIND_NOW {
			return true
		}
		if tag == elf.DT_FLAGS && (elf.DynFlag(val)&elf.DF_BIND_NOW) != 0 {
			return true
		}
		if tag == elf.DT_FLAGS_1 && (elf.DynFlag1(val)&elf.DF_1_NOW) != 0 {
			return true
		}
	}
	return false
}

func lookupSelfMapPerms(addr, size uintptr) (string, error) {
	file, err := os.Open("/proc/self/maps")
	if err != nil {
		return "", err
	}
	defer file.Close()

	end := uint64(addr + size)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		bounds := strings.SplitN(fields[0], "-", 2)
		if len(bounds) != 2 {
			continue
		}
		lo, err1 := strconv.ParseUint(bounds[0], 16, 64)
		hi, err2 := strconv.ParseUint(bounds[1], 16, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if uint64(addr) >= lo && end <= hi {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("address range [%#x, %#x) not found in /proc/self/maps", addr, end)
}
