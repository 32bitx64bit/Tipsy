// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"bufio"
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/integrity"
)

type fakeSym struct {
	name      string
	typ       elf.SymType
	bind      elf.SymBind
	undefined bool
	versioned bool // GNU verneed libc.so / LIBC
}

func importSym(name string, typ elf.SymType) fakeSym {
	return fakeSym{name: name, typ: typ, bind: elf.STB_GLOBAL, undefined: true}
}

// fakeReloc is one SHT_RELA entry. Symbol is the raw dynsym index, including
// the null entry the way r_info stores it.
type fakeReloc struct {
	symbol uint32
	typ    elf.R_X86_64
}

// buildFakeELF writes a minimal ELF64 x86-64 shared object that debug/elf can
// read: .dynsym, .dynstr, .dynamic (DT_NEEDED) and, when any symbol is
// versioned, .gnu.version and .gnu.version_r. It is data only and never run.
func buildFakeELF(needed []string, syms []fakeSym) []byte {
	return buildFakeELFWithRelocations(needed, syms, nil)
}

// buildFakeELFWithRelocations adds real .rela.plt/.rela.dyn sections, keyed by
// the same raw dynsym indexes an ELF r_info carries.
func buildFakeELFWithRelocations(needed []string, syms []fakeSym, relocs []fakeReloc) []byte {
	dynstr := []byte{0}
	str := func(s string) uint32 {
		off := uint32(len(dynstr))
		dynstr = append(dynstr, s...)
		dynstr = append(dynstr, 0)
		return off
	}
	le := binary.LittleEndian
	dynsym := make([]byte, 24)
	versym := []byte{0, 0}
	anyVersioned := false
	for _, s := range syms {
		ent := make([]byte, 24)
		le.PutUint32(ent[0:], str(s.name))
		ent[4] = byte(s.bind)<<4 | byte(s.typ)
		shndx := uint16(1)
		if s.undefined {
			shndx = 0
		}
		le.PutUint16(ent[6:], shndx)
		dynsym = append(dynsym, ent...)
		version := uint16(1)
		if s.versioned {
			version, anyVersioned = 2, true
		}
		versym = le.AppendUint16(versym, version)
	}
	var dynamic []byte
	for _, n := range needed {
		dynamic = le.AppendUint64(dynamic, uint64(elf.DT_NEEDED))
		dynamic = le.AppendUint64(dynamic, uint64(str(n)))
	}
	dynamic = append(dynamic, make([]byte, 16)...)
	rela := func(wantJumpSlot bool) []byte {
		var out []byte
		for _, r := range relocs {
			if (r.typ == elf.R_X86_64_JMP_SLOT) != wantJumpSlot {
				continue
			}
			out = le.AppendUint64(out, 0) // r_offset
			out = le.AppendUint64(out, uint64(r.symbol)<<32|uint64(r.typ))
			out = le.AppendUint64(out, 0) // r_addend
		}
		return out
	}
	var verneed []byte
	if anyVersioned {
		file, name := str("libc.so"), str("LIBC")
		verneed = le.AppendUint16(verneed, 1)    // vn_version
		verneed = le.AppendUint16(verneed, 1)    // vn_cnt
		verneed = le.AppendUint32(verneed, file) // vn_file
		verneed = le.AppendUint32(verneed, 16)   // vn_aux
		verneed = le.AppendUint32(verneed, 0)    // vn_next
		verneed = le.AppendUint32(verneed, 0)    // vna_hash
		verneed = le.AppendUint16(verneed, 0)    // vna_flags
		verneed = le.AppendUint16(verneed, 2)    // vna_other
		verneed = le.AppendUint32(verneed, name) // vna_name
		verneed = le.AppendUint32(verneed, 0)    // vna_next
	}

	type section struct {
		name       string
		typ        elf.SectionType
		data       []byte
		link, info uint32
		entsize    uint64
	}
	sections := []section{
		{},
		{name: ".dynsym", typ: elf.SHT_DYNSYM, data: dynsym, link: 2, info: 1, entsize: 24},
		{name: ".dynstr", typ: elf.SHT_STRTAB, data: dynstr},
		{name: ".dynamic", typ: elf.SHT_DYNAMIC, data: dynamic, link: 2, entsize: 16},
	}
	if data := rela(true); len(data) > 0 {
		sections = append(sections, section{name: ".rela.plt", typ: elf.SHT_RELA, data: data, link: 1, entsize: 24})
	}
	if data := rela(false); len(data) > 0 {
		sections = append(sections, section{name: ".rela.dyn", typ: elf.SHT_RELA, data: data, link: 1, entsize: 24})
	}
	if anyVersioned {
		sections = append(sections,
			section{name: ".gnu.version", typ: elf.SHT_GNU_VERSYM, data: versym, link: 1, entsize: 2},
			section{name: ".gnu.version_r", typ: elf.SHT_GNU_VERNEED, data: verneed, link: 2, info: 1})
	}
	shstrtab := []byte{0}
	nameOff := make([]uint32, len(sections)+1)
	for i := 1; i < len(sections); i++ {
		nameOff[i] = uint32(len(shstrtab))
		shstrtab = append(append(shstrtab, sections[i].name...), 0)
	}
	nameOff[len(sections)] = uint32(len(shstrtab))
	shstrtab = append(append(shstrtab, ".shstrtab"...), 0)
	sections = append(sections, section{name: ".shstrtab", typ: elf.SHT_STRTAB, data: shstrtab})

	out := make([]byte, 64)
	offsets := make([]uint64, len(sections))
	for i := 1; i < len(sections); i++ {
		for len(out)%8 != 0 {
			out = append(out, 0)
		}
		offsets[i] = uint64(len(out))
		out = append(out, sections[i].data...)
	}
	for len(out)%8 != 0 {
		out = append(out, 0)
	}
	shoff := uint64(len(out))
	for i, s := range sections {
		hdr := make([]byte, 64)
		le.PutUint32(hdr[0:], nameOff[i])
		le.PutUint32(hdr[4:], uint32(s.typ))
		le.PutUint64(hdr[24:], offsets[i])
		le.PutUint64(hdr[32:], uint64(len(s.data)))
		le.PutUint32(hdr[40:], s.link)
		le.PutUint32(hdr[44:], s.info)
		le.PutUint64(hdr[48:], 1)
		le.PutUint64(hdr[56:], s.entsize)
		out = append(out, hdr...)
	}
	copy(out, "\x7fELF")
	out[elf.EI_CLASS], out[elf.EI_DATA], out[elf.EI_VERSION] = byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)
	le.PutUint16(out[16:], uint16(elf.ET_DYN))
	le.PutUint16(out[18:], uint16(elf.EM_X86_64))
	le.PutUint32(out[20:], 1)
	le.PutUint64(out[40:], shoff)
	le.PutUint16(out[52:], 64)
	le.PutUint16(out[58:], 64)
	le.PutUint16(out[60:], uint16(len(sections)))
	le.PutUint16(out[62:], uint16(len(sections)-1))
	return out
}

func writeRuntimeLibs(t *testing.T, libs map[string][]byte) string {
	t.Helper()
	runtimeDir := t.TempDir()
	libDir := filepath.Join(runtimeDir, "lib", "x86_64")
	if err := os.MkdirAll(libDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range libs {
		if err := os.WriteFile(filepath.Join(libDir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return runtimeDir
}

// clientWithUnresolvableImports imports two names nothing can supply (a
// function and a datum) and a libc name Tipsy does not declare, next to
// imports every path must accept.
func clientWithUnresolvableImports() []byte {
	return buildFakeELF([]string{"liblog.so", "libbundled.so", "libc.so"}, []fakeSym{
		{name: "JNI_OnLoad", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL},
		importSym("__android_log_print", elf.STT_FUNC), // Tipsy compatibility table
		importSym("bundled_fn", elf.STT_FUNC),          // exported by a bundled DT_NEEDED library
		importSym("glFakeDriverEntry", elf.STT_FUNC),   // host graphics driver: not decidable statically
		{name: "AImageReader_tipsyMissing", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL, undefined: true},
		{name: "tipsy_missing_data", typ: elf.STT_OBJECT, bind: elf.STB_GLOBAL, undefined: true},
		{name: "tipsy_missing_weak", typ: elf.STT_FUNC, bind: elf.STB_WEAK, undefined: true}, // binds to 0
		{name: "memcpy", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL, undefined: true, versioned: true},
		{name: "tipsy_missing_libc", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL, undefined: true, versioned: true},
	})
}

func bundledLibrary() []byte {
	return buildFakeELF(nil, []fakeSym{{name: "bundled_fn", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL}})
}

func clientWithUnresolvableFunction() []byte {
	return buildFakeELF([]string{"liblog.so"}, []fakeSym{
		{name: "JNI_OnLoad", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL},
		importSym("AImageReader_tipsyMissing", elf.STT_FUNC),
	})
}

func clientWithUnresolvableData() []byte {
	return buildFakeELF(nil, []fakeSym{
		{name: "JNI_OnLoad", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL},
		importSym("tipsy_missing_data", elf.STT_OBJECT),
	})
}

func clientWithVersionedUnresolvableFunction() []byte {
	return buildFakeELF([]string{"liblog.so", "libc.so"}, []fakeSym{
		{name: "JNI_OnLoad", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL},
		{name: "tipsy_missing_libc", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL, undefined: true, versioned: true},
	})
}

func clientWithAbsentBundledLibrary() []byte {
	return buildFakeELF([]string{"liblog.so", "libbundledmissing.so"}, []fakeSym{
		{name: "JNI_OnLoad", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL},
	})
}

// clientWithNotypeRelocations has two undefined STT_NOTYPE imports: one
// reached only through a PLT JUMP_SLOT, one through a GLOB_DAT. Their r_info
// indexes are the raw dynsym positions (2 and 3), not the zero-based slice
// positions debug/elf reports.
func clientWithNotypeRelocations() []byte {
	return buildFakeELFWithRelocations(nil, []fakeSym{
		{name: "JNI_OnLoad", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL}, // raw index 1
		importSym("tipsy_notype_plt", elf.STT_NOTYPE),                 // raw index 2
		importSym("tipsy_notype_got", elf.STT_NOTYPE),                 // raw index 3
	}, []fakeReloc{
		{symbol: 2, typ: elf.R_X86_64_JMP_SLOT},
		{symbol: 3, typ: elf.R_X86_64_GLOB_DAT},
	})
}

func TestImportKindMatchesLoaderFallbackEligibility(t *testing.T) {
	sym := func(typ elf.SymType) elf.Symbol {
		return elf.Symbol{Info: byte(elf.STB_GLOBAL)<<4 | byte(typ)}
	}
	for _, tc := range []struct {
		name            string
		sym             elf.Symbol
		jumpSlot, other bool
		want            ImportKind
	}{
		{name: "function", sym: sym(elf.STT_FUNC), want: ImportFunction},
		{name: "ifunc", sym: sym(elf.STT_GNU_IFUNC), want: ImportFunction},
		{name: "object", sym: sym(elf.STT_OBJECT), want: ImportData},
		{name: "tls", sym: sym(elf.STT_TLS), want: ImportData},
		{name: "NOTYPE through PLT", sym: sym(elf.STT_NOTYPE), jumpSlot: true, want: ImportFunction},
		{name: "NOTYPE through GOT", sym: sym(elf.STT_NOTYPE), other: true, want: ImportUnknown},
		{name: "NOTYPE through both", sym: sym(elf.STT_NOTYPE), jumpSlot: true, other: true, want: ImportUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := importKind(tc.sym, tc.jumpSlot, tc.other); got != tc.want {
				t.Fatalf("importKind=%q want %q", got, tc.want)
			}
		})
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func mappedFiles(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		t.Skipf("no /proc/self/maps: %v", err)
	}
	defer f.Close()
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 6 && strings.HasPrefix(fields[5], "/") {
			seen[fields[5]] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for name := range seen {
		files = append(files, name)
	}
	slices.Sort(files)
	return files
}

func TestFindMissingClientImportsReportsEveryUnresolvableNonWeakImport(t *testing.T) {
	runtimeDir := writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so":  clientWithUnresolvableImports(),
		"libbundled.so": bundledLibrary(),
	})
	before := mappedFiles(t)
	missing, err := FindMissingClientImports(context.Background(), runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after := mappedFiles(t); !slices.Equal(before, after) {
		t.Fatalf("preflight changed the set of mapped files: before=%v after=%v", before, after)
	}
	want := []MissingImport{
		{Name: "AImageReader_tipsyMissing", Kind: ImportFunction},
		{Name: "tipsy_missing_data", Kind: ImportData},
		{Name: "tipsy_missing_libc", Kind: ImportFunction, Library: "libc.so", Version: "LIBC"},
	}
	if !slices.Equal(missing, want) {
		t.Fatalf("missing=%+v\nwant   %+v", missing, want)
	}
}

func TestFindMissingClientImportsClassifiesNotypeRelocations(t *testing.T) {
	runtimeDir := writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithNotypeRelocations(),
	})
	missing, err := FindMissingClientImports(context.Background(), runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []MissingImport{
		{Name: "tipsy_notype_got", Kind: ImportUnknown},
		{Name: "tipsy_notype_plt", Kind: ImportFunction},
	}
	if !slices.Equal(missing, want) {
		t.Fatalf("missing=%+v\nwant   %+v", missing, want)
	}
}

func TestValidateExtractedClientImportsRejectsNotypeDataRelocation(t *testing.T) {
	logs := captureLog(t)
	err := ValidateExtractedClientImports(context.Background(), writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithNotypeRelocations(),
	}))
	if ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "tipsy_notype_got") {
		t.Fatalf("error does not name the GLOB_DAT NOTYPE import: %v", err)
	}
	if strings.Contains(err.Error(), "tipsy_notype_plt") {
		t.Fatalf("error names the JUMP_SLOT-only NOTYPE function: %v", err)
	}
	if !strings.Contains(logs.String(), "tipsy_notype_plt") {
		t.Fatalf("warning does not name the JUMP_SLOT-only NOTYPE function:\n%s", logs)
	}
}

func TestFindMissingClientImportsAcceptsResolvableClient(t *testing.T) {
	runtimeDir := writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": buildFakeELF([]string{"liblog.so", "libc.so"}, []fakeSym{
			importSym("__android_log_print", elf.STT_FUNC),
			{name: "memcpy", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL, undefined: true, versioned: true},
		}),
	})
	missing, err := FindMissingClientImports(context.Background(), runtimeDir)
	if err != nil || len(missing) != 0 {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	if err := ValidateExtractedClientImports(context.Background(), runtimeDir); err != nil {
		t.Fatal(err)
	}
}

func TestFindMissingClientImportsRequiresBundledProviderForUnresolvedName(t *testing.T) {
	// Without libbundled.so in the APK, bundled_fn has no provider.
	runtimeDir := writeRuntimeLibs(t, map[string][]byte{"libroblox.so": clientWithUnresolvableImports()})
	missing, err := FindMissingClientImports(context.Background(), runtimeDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(missing))
	for i, m := range missing {
		names[i] = m.Name
	}
	if !slices.Contains(names, "bundled_fn") {
		t.Fatalf("bundled_fn unexpectedly resolved: %v", names)
	}
}

func TestValidateExtractedClientImportsWarnsForFunctionsAndRejectsData(t *testing.T) {
	runtimeDir := writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so":  clientWithUnresolvableImports(),
		"libbundled.so": bundledLibrary(),
	})
	logs := captureLog(t)
	err := ValidateExtractedClientImports(context.Background(), runtimeDir)
	if ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "tipsy_missing_data") {
		t.Fatalf("error does not name missing data import: %v", err)
	}
	for _, function := range []string{"AImageReader_tipsyMissing"} {
		if strings.Contains(err.Error(), function) {
			t.Fatalf("error names fallback-eligible function %s: %v", function, err)
		}
		if !strings.Contains(logs.String(), function) {
			t.Fatalf("warning does not name %s:\n%s", function, logs)
		}
	}
	if !strings.Contains(err.Error(), "tipsy_missing_libc") {
		t.Fatalf("error does not name the versioned missing function: %v", err)
	}
	if strings.Contains(logs.String(), "tipsy_missing_libc") {
		t.Fatalf("versioned missing function was warned about instead of rejected:\n%s", logs)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("function fallback was not logged at WARN:\n%s", logs)
	}
	for _, resolved := range []string{"__android_log_print", "bundled_fn", "glFakeDriverEntry", "tipsy_missing_weak", "memcpy"} {
		if strings.Contains(logs.String(), resolved) {
			t.Fatalf("warning names resolvable import %s:\n%s", resolved, logs)
		}
	}
}

func TestValidateExtractedClientImportsAcceptsMissingFunctionWithWarning(t *testing.T) {
	logs := captureLog(t)
	err := ValidateExtractedClientImports(context.Background(), writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithUnresolvableFunction(),
	}))
	if err != nil {
		t.Fatalf("missing function rejected by default: %v", err)
	}
	if !strings.Contains(logs.String(), "AImageReader_tipsyMissing") {
		t.Fatalf("warning does not name the missing function:\n%s", logs)
	}
}

func TestValidateExtractedClientImportsStrictRejectsMissingFunction(t *testing.T) {
	t.Setenv("TIPSY_STRICT_SYMBOLS", "1")
	logs := captureLog(t)
	err := ValidateExtractedClientImports(context.Background(), writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithUnresolvableFunction(),
	}))
	if ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "AImageReader_tipsyMissing") {
		t.Fatalf("error does not name missing function: %v", err)
	}
	if strings.Contains(logs.String(), "unsupported fallback") {
		t.Fatalf("strict mode still warned about a fallback:\n%s", logs)
	}
}

func TestValidateExtractedClientImportsRejectsMissingData(t *testing.T) {
	logs := captureLog(t)
	err := ValidateExtractedClientImports(context.Background(), writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithUnresolvableData(),
	}))
	if ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "tipsy_missing_data") {
		t.Fatalf("error does not name missing data import: %v", err)
	}
	if strings.Contains(logs.String(), "unsupported fallback") {
		t.Fatalf("data import warned instead of failing:\n%s", logs)
	}
}

func TestValidateExtractedClientImportsRejectsVersionedMissingFunction(t *testing.T) {
	logs := captureLog(t)
	err := ValidateExtractedClientImports(context.Background(), writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithVersionedUnresolvableFunction(),
	}))
	if ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "tipsy_missing_libc") {
		t.Fatalf("error does not name versioned missing function: %v", err)
	}
	if strings.Contains(logs.String(), "unsupported fallback") {
		t.Fatalf("versioned missing function warned instead of failing:\n%s", logs)
	}
}

func TestValidateExtractedClientImportsRejectsAbsentBundledLibrary(t *testing.T) {
	err := ValidateExtractedClientImports(context.Background(), writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": clientWithAbsentBundledLibrary(),
	}))
	if ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(err), err)
	}
	if !strings.Contains(err.Error(), "libbundledmissing.so") {
		t.Fatalf("error does not name the absent bundled library: %v", err)
	}
}

func TestValidateExtractedClientImportsRejectsUnreadableRoot(t *testing.T) {
	for name, libs := range map[string]map[string][]byte{
		"missing":   {},
		"not elf":   {"libroblox.so": []byte("native")},
		"cancelled": {"libroblox.so": clientWithUnresolvableImports()},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if name == "cancelled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
				libs["libbundled.so"] = bundledLibrary()
			}
			err := ValidateExtractedClientImports(ctx, writeRuntimeLibs(t, libs))
			want := ErrCompatibility
			if name == "cancelled" {
				want = ErrCanceled
			}
			if ErrorKindOf(err) != want {
				t.Fatalf("kind=%q err=%v want %q", ErrorKindOf(err), err, want)
			}
		})
	}
}

type installOutcome struct {
	result      *InstallResult
	err         error
	authorized  bool
	prepared    bool
	activeStore bool
}

// installClient runs Service.Install over a stubbed package whose extracted
// libroblox.so is libroblox, with the real import preflight and every later
// stage observable.
func installClient(t *testing.T, libroblox []byte, bundled map[string][]byte) installOutcome {
	t.Helper()
	root := t.TempDir()
	input := writeTestZIP(t, filepath.Join(root, "base.apk"), map[string][]byte{
		"AndroidManifest.xml":     []byte("manifest"),
		"lib/x86_64/libroblox.so": []byte("native"),
	})
	runtimeDir := filepath.Join(root, "data", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const cert = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var out installOutcome
	s := New()
	s.RuntimeDir = runtimeDir
	s.GenerationStoreRoot = filepath.Join(root, "data", "runtime-generations")
	t.Cleanup(func() { makeGenerationTreeWritable(s.GenerationStoreRoot) })
	s.Trust = testTrust(cert)
	s.inspect = func(_ context.Context, selected []string) (*apk.Report, error) {
		report := validReport(cert)
		digest, size, err := hashGenerationFile(context.Background(), selected[0])
		if err != nil {
			return nil, err
		}
		report.Packages[0].Path, report.Packages[0].FileSHA256, report.Packages[0].Size = selected[0], digest, size
		report.Packages[0].NativeLibraries[0].APKPath = selected[0]
		report.Packages[0].NativeLibraries[0].SHA256 = testSHA256(libroblox)
		report.Packages[0].NativeLibraries[0].Size = int64(len(libroblox))
		report.Merged.NativeLibraries = append([]apk.NativeLib(nil), report.Packages[0].NativeLibraries...)
		return report, nil
	}
	s.verify = func(context.Context, *apk.Report) error { return nil }
	s.prepare = func() error { out.prepared = true; return nil }
	s.compatibility = func(context.Context, string) error { return nil } // ELF contract is covered elsewhere
	s.authorizeStaged = func(context.Context, integrity.Store, string, TrustPolicy) error {
		out.authorized = true
		return nil
	}
	s.snapshot = func(_ context.Context, storeRoot string, _ TrustPolicy) (InstallSnapshot, error) {
		return InstallSnapshot{
			Installed: true, Readiness: ReadinessLaunchInputs,
			RuntimeDir:  filepath.Join(storeRoot, "generations", "synthetic"),
			PackageName: "com.roblox.client", VersionName: "2.734.917", VersionCode: 2908,
			Architectures: []string{"x86_64"},
		}, nil
	}
	s.extract = func(_ context.Context, selected []string, dest string) (*apk.ExtractResult, error) {
		if err := os.MkdirAll(filepath.Join(dest, "apk"), 0o700); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(dest, "lib", "x86_64"), 0o700); err != nil {
			return nil, err
		}
		rawAPK, err := os.ReadFile(selected[0])
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, "apk", "base.apk"), rawAPK, 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dest, "lib", "x86_64", "libroblox.so"), libroblox, 0o600); err != nil {
			return nil, err
		}
		for name, raw := range bundled {
			if err := os.WriteFile(filepath.Join(dest, "lib", "x86_64", name), raw, 0o600); err != nil {
				return nil, err
			}
		}
		meta := apk.Meta{PackageName: "com.roblox.client", VersionName: "2.734.917", VersionCode: 2908,
			Packages: []apk.MetaFile{{Dest: "apk/base.apk", SHA256: testSHA256(rawAPK), Size: int64(len(rawAPK))}}}
		raw, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dest, "meta.json"), raw, 0o600); err != nil {
			return nil, err
		}
		return &apk.ExtractResult{DestDir: dest}, nil
	}
	out.result, out.err = s.Install(context.Background(), InstallRequest{Mode: InstallLocal, LocalPaths: []string{input}}, nil)
	if active, err := StoreForTrust(s.GenerationStoreRoot, s.Trust).Active(context.Background()); err == nil {
		out.activeStore = true
		active.Close()
	}
	return out
}

func TestInstallRejectsUnresolvableDataImportsBeforeAuthorizationAndActivation(t *testing.T) {
	logs := captureLog(t)
	out := installClient(t, clientWithUnresolvableData(), nil)
	if out.err == nil || out.result != nil {
		t.Fatalf("install accepted a client with an unresolvable data import: result=%+v err=%v", out.result, out.err)
	}
	if ErrorKindOf(out.err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(out.err), out.err)
	}
	if !strings.Contains(out.err.Error(), "tipsy_missing_data") {
		t.Fatalf("error does not name the data import: %v", out.err)
	}
	if strings.Contains(logs.String(), "unsupported fallback") {
		t.Fatalf("data import was warned about instead of rejected:\n%s", logs)
	}
	if out.authorized || out.prepared || out.activeStore {
		t.Fatalf("install progressed past the import preflight: authorized=%v prepared=%v active=%v", out.authorized, out.prepared, out.activeStore)
	}
}

func TestInstallRejectsVersionedMissingFunctionBeforeAuthorizationAndActivation(t *testing.T) {
	logs := captureLog(t)
	out := installClient(t, clientWithVersionedUnresolvableFunction(), nil)
	if out.err == nil || out.result != nil {
		t.Fatalf("install accepted a client with a versioned missing function: result=%+v err=%v", out.result, out.err)
	}
	if ErrorKindOf(out.err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(out.err), out.err)
	}
	if !strings.Contains(out.err.Error(), "tipsy_missing_libc") {
		t.Fatalf("error does not name the versioned missing function: %v", out.err)
	}
	if strings.Contains(logs.String(), "unsupported fallback") {
		t.Fatalf("versioned missing function was warned about instead of rejected:\n%s", logs)
	}
	if out.authorized || out.prepared || out.activeStore {
		t.Fatalf("install progressed past the import preflight: authorized=%v prepared=%v active=%v", out.authorized, out.prepared, out.activeStore)
	}
}

func TestInstallRejectsAbsentBundledLibraryBeforeAuthorizationAndActivation(t *testing.T) {
	out := installClient(t, clientWithAbsentBundledLibrary(), nil)
	if out.err == nil || out.result != nil {
		t.Fatalf("install accepted a client with an absent bundled library: result=%+v err=%v", out.result, out.err)
	}
	if ErrorKindOf(out.err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(out.err), out.err)
	}
	if !strings.Contains(out.err.Error(), "libbundledmissing.so") {
		t.Fatalf("error does not name the absent bundled library: %v", out.err)
	}
	if out.authorized || out.prepared || out.activeStore {
		t.Fatalf("install progressed past the import preflight: authorized=%v prepared=%v active=%v", out.authorized, out.prepared, out.activeStore)
	}
}

func TestInstallAcceptsUnresolvableFunctionWithWarning(t *testing.T) {
	logs := captureLog(t)
	out := installClient(t, clientWithUnresolvableFunction(), nil)
	if out.err != nil || out.result == nil || !out.result.Snapshot.Installed {
		t.Fatalf("result=%+v err=%v", out.result, out.err)
	}
	if !out.authorized || !out.prepared || !out.activeStore {
		t.Fatalf("install did not complete: authorized=%v prepared=%v active=%v", out.authorized, out.prepared, out.activeStore)
	}
	if !strings.Contains(logs.String(), "AImageReader_tipsyMissing") {
		t.Fatalf("warning does not name the fallback function:\n%s", logs)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("fallback warning was not logged at WARN:\n%s", logs)
	}
}

func TestInstallStrictRejectsUnresolvableFunction(t *testing.T) {
	t.Setenv("TIPSY_STRICT_SYMBOLS", "1")
	out := installClient(t, clientWithUnresolvableFunction(), nil)
	if out.err == nil || out.result != nil {
		t.Fatalf("strict install accepted a missing function: result=%+v err=%v", out.result, out.err)
	}
	if ErrorKindOf(out.err) != ErrCompatibility {
		t.Fatalf("kind=%q err=%v", ErrorKindOf(out.err), out.err)
	}
	if !strings.Contains(out.err.Error(), "AImageReader_tipsyMissing") {
		t.Fatalf("error does not name the missing function: %v", out.err)
	}
	if out.authorized || out.prepared || out.activeStore {
		t.Fatalf("strict install progressed past the import preflight: authorized=%v prepared=%v active=%v", out.authorized, out.prepared, out.activeStore)
	}
}

func TestInstallAcceptsClientWhoseImportsAllResolve(t *testing.T) {
	libroblox := buildFakeELF([]string{"liblog.so", "libc.so"}, []fakeSym{
		importSym("__android_log_print", elf.STT_FUNC),
		{name: "memcpy", typ: elf.STT_FUNC, bind: elf.STB_GLOBAL, undefined: true, versioned: true},
		{name: "tipsy_missing_weak", typ: elf.STT_FUNC, bind: elf.STB_WEAK, undefined: true},
	})
	out := installClient(t, libroblox, nil)
	if out.err != nil || out.result == nil || !out.result.Snapshot.Installed {
		t.Fatalf("result=%+v err=%v", out.result, out.err)
	}
	if !out.authorized || !out.prepared || !out.activeStore {
		t.Fatalf("install did not complete: authorized=%v prepared=%v active=%v", out.authorized, out.prepared, out.activeStore)
	}
}
