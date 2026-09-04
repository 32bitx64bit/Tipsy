package elfinspect

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAnalyzeBytesMinimalArch(t *testing.T) {
	t.Parallel()
	data := minimalELF64(elf.EM_AARCH64, elf.ET_DYN)
	rep, err := AnalyzeBytes("fake-aarch64.so", data)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Class != "ELF64" {
		t.Fatalf("class=%q", rep.Class)
	}
	if rep.Machine != "EM_AARCH64" {
		t.Fatalf("machine=%q", rep.Machine)
	}
	if rep.Type != "ET_DYN" {
		t.Fatalf("type=%q", rep.Type)
	}
	if rep.IsX86_64() {
		t.Fatal("aarch64 reported as x86-64")
	}
	text := FormatText(rep)
	if !strings.Contains(text, "UNSUPPORTED_ARCH") {
		t.Fatalf("FormatText missing arch warning:\n%s", text)
	}
	if !containsNote(rep.AndroidNotes, "UNSUPPORTED_ARCH") {
		t.Fatalf("AndroidNotes missing arch warning: %v", rep.AndroidNotes)
	}
}

func TestAnalyzeBytesELF32(t *testing.T) {
	t.Parallel()
	data := minimalELF32(elf.EM_386, elf.ET_DYN)
	rep, err := AnalyzeBytes("fake-i386.so", data)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Class != "ELF32" || rep.Machine != "EM_386" {
		t.Fatalf("got %s %s", rep.Class, rep.Machine)
	}
	if !strings.Contains(FormatText(rep), "UNSUPPORTED_ARCH") {
		t.Fatal("32-bit ELF not flagged")
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	t.Parallel()
	if _, err := AnalyzeBytes("empty", nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestAnalyzeBytesHostShared(t *testing.T) {
	cc := requireCC(t)
	src := `
#include <stdio.h>
#include <stdlib.h>
void tipsy_hello(void) {
	void *p = malloc(8);
	printf("tipsy %p\n", p);
	free(p);
}
`
	path := compileShared(t, cc, "tiny.c", src)
	rep, err := Analyze(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.IsX86_64() {
		t.Fatalf("expected x86-64 host .so, got %s %s", rep.Class, rep.Machine)
	}
	if rep.Type != "ET_DYN" {
		t.Fatalf("type=%q", rep.Type)
	}
	if !slices.Contains(rep.DTNeeded, "libc.so.6") {
		t.Fatalf("DT_NEEDED=%v, want libc.so.6", rep.DTNeeded)
	}
	if len(rep.Imports) == 0 {
		t.Fatal("expected imported symbols")
	}
	if !containsNote(rep.AndroidNotes, "libc.so.6") {
		t.Fatalf("expected glibc note, got %v", rep.AndroidNotes)
	}
	names := symbolNames(rep.Imports)
	if !containsAny(names, "printf", "malloc", "free", "puts") {
		t.Fatalf("imports=%v, expected libc function", names)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rep2, err := AnalyzeBytes("tiny.so", data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.DTNeeded, rep2.DTNeeded) {
		t.Fatalf("AnalyzeBytes DT_NEEDED=%v, Analyze=%v", rep2.DTNeeded, rep.DTNeeded)
	}
	if len(rep2.Imports) != len(rep.Imports) {
		t.Fatalf("import count AnalyzeBytes=%d Analyze=%d", len(rep2.Imports), len(rep.Imports))
	}

	js, err := FormatJSON(rep)
	if err != nil {
		t.Fatal(err)
	}
	var round Report
	if err := json.Unmarshal(js, &round); err != nil {
		t.Fatal(err)
	}
	if round.Name != rep.Name || round.Machine != "EM_X86_64" {
		t.Fatalf("json roundtrip: %+v", round)
	}
}

func TestAnalyzeJNIAndTLS(t *testing.T) {
	cc := requireCC(t)
	src := `
__thread int tipsy_tls = 42;
int JNI_OnLoad(void *vm, void *reserved) { (void)vm; (void)reserved; return 0x00010006; }
void JNI_OnUnload(void *vm, void *reserved) { (void)vm; (void)reserved; }
void Java_com_example_Foo_bar(void *env, void *obj) { (void)env; (void)obj; }
int tipsy_tls_get(void) { return tipsy_tls; }
`
	path := compileShared(t, cc, "jni.c", src)
	rep, err := Analyze(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.TLS.Present {
		t.Fatalf("expected TLS, got %+v", rep.TLS)
	}
	if !slices.Contains(rep.JNIEntryPoints, "JNI_OnLoad") {
		t.Fatalf("JNI_OnLoad missing: %v", rep.JNIEntryPoints)
	}
	if !slices.Contains(rep.JNIEntryPoints, "JNI_OnUnload") {
		t.Fatalf("JNI_OnUnload missing: %v", rep.JNIEntryPoints)
	}
	if !slices.Contains(rep.JNIEntryPoints, "Java_com_example_Foo_bar") {
		t.Fatalf("Java_* missing: %v", rep.JNIEntryPoints)
	}
	text := FormatText(rep)
	if !strings.Contains(text, "JNI_OnLoad") || !strings.Contains(text, "Java_com_example_Foo_bar") {
		t.Fatalf("FormatText missing JNI:\n%s", text)
	}
}

func TestAnalyzeUndefinedAndroidSymbol(t *testing.T) {
	cc := requireCC(t)
	src := `
extern int ALooper_pollOnce(int timeoutMillis, int *outFd, int *outEvents, void **outData);
int tipsy_poll(void) { return ALooper_pollOnce(-1, 0, 0, 0); }
`
	path := compileShared(t, cc, "alooper.c", src, "-Wl,--unresolved-symbols=ignore-all")
	rep, err := Analyze(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range rep.Imports {
		if s.Name == "ALooper_pollOnce" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ALooper_pollOnce not imported: %v", symbolNames(rep.Imports))
	}
}

func TestParseAPS2Count(t *testing.T) {
	t.Parallel()
	n, ok := parseAPS2Count([]byte{'A', 'P', 'S', '2', 3, 0})
	if !ok || n != 3 {
		t.Fatalf("got n=%d ok=%v", n, ok)
	}
	if _, ok := parseAPS2Count([]byte("XXXX")); ok {
		t.Fatal("expected failure")
	}
}

func TestRelocTypeName(t *testing.T) {
	t.Parallel()
	if g := relocTypeName(elf.EM_X86_64, uint32(elf.R_X86_64_JMP_SLOT)); g != "R_X86_64_JMP_SLOT" {
		t.Fatalf("got %q", g)
	}
}

func requireCC(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"gcc", "cc"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("gcc not found")
	return ""
}

func compileShared(t *testing.T, cc, filename, src string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	cpath := filepath.Join(dir, filename)
	if err := os.WriteFile(cpath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, strings.TrimSuffix(filename, ".c")+".so")
	args := append([]string{"-shared", "-fPIC", "-O0", "-o", out, cpath}, extra...)
	cmd := exec.Command(cc, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile %s: %v\n%s", filename, err, b)
	}
	return out
}

func containsNote(notes []string, substr string) bool {
	for _, n := range notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func containsAny(have []string, want ...string) bool {
	set := map[string]struct{}{}
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; ok {
			return true
		}
	}
	return false
}

func minimalELF64(machine elf.Machine, typ elf.Type) []byte {
	var hdr elf.Header64
	hdr.Ident[0] = 0x7f
	hdr.Ident[1] = 'E'
	hdr.Ident[2] = 'L'
	hdr.Ident[3] = 'F'
	hdr.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	hdr.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	hdr.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	hdr.Type = uint16(typ)
	hdr.Machine = uint16(machine)
	hdr.Version = uint32(elf.EV_CURRENT)
	hdr.Ehsize = 64
	hdr.Phentsize = 56
	hdr.Shentsize = 64
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &hdr); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func minimalELF32(machine elf.Machine, typ elf.Type) []byte {
	var hdr elf.Header32
	hdr.Ident[0] = 0x7f
	hdr.Ident[1] = 'E'
	hdr.Ident[2] = 'L'
	hdr.Ident[3] = 'F'
	hdr.Ident[elf.EI_CLASS] = byte(elf.ELFCLASS32)
	hdr.Ident[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	hdr.Ident[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	hdr.Type = uint16(typ)
	hdr.Machine = uint16(machine)
	hdr.Version = uint32(elf.EV_CURRENT)
	hdr.Ehsize = 52
	hdr.Phentsize = 32
	hdr.Shentsize = 40
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &hdr); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestParseNotesTruncatedDoesNotPanic(t *testing.T) {
	t.Parallel()
	// namesz=3 so nameEnd=15; align4(15)=16 which is past a 15-byte buffer.
	data := make([]byte, 15)
	binary.LittleEndian.PutUint32(data[0:4], 3) // namesz
	binary.LittleEndian.PutUint32(data[4:8], 0) // descsz
	binary.LittleEndian.PutUint32(data[8:12], 1)
	data[12] = 'A'
	data[13] = 'B'
	data[14] = 0
	var notes []elfNote
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("panic: %v", rec)
			}
		}()
		notes = parseNotes(data, binary.LittleEndian)
	}()
	if len(notes) != 0 {
		t.Fatalf("expected no notes, got %+v", notes)
	}
}

func TestFirstNULFields(t *testing.T) {
	t.Parallel()
	in := []byte("r27-beta1\x00\x00\x00\x0011718014\x00")
	got := firstNULFields(in)
	if got != "r27-beta1 11718014" {
		t.Fatalf("got %q", got)
	}
}
