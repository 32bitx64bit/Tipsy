package elfinspect

import (
	"bytes"
	"cmp"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// Non-JNI defined dynsym listings are capped so huge Android .so files stay readable.
// JNI_OnLoad, JNI_OnUnload, and Java_* exports are always kept.
const maxListedNonJNIExports = 256

// Analyze opens path and returns a static ELF report.
func Analyze(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return AnalyzeBytes(path, data)
}

// AnalyzeBytes parses an in-memory ELF object. name is a display path (file or zip member).
func AnalyzeBytes(name string, data []byte) (*Report, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("elfinspect: empty file %s", name)
	}
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("elfinspect: parse %s: %w", name, err)
	}
	defer f.Close()
	return analyzeFile(name, f)
}

func analyzeFile(name string, f *elf.File) (*Report, error) {
	rep := &Report{
		Name:     name,
		Class:    className(f.Class),
		Machine:  f.Machine.String(),
		Type:     f.Type.String(),
		DTNeeded: []string{},
		Imports:  []Symbol{},
		Exports:  []Symbol{},
	}

	rep.Interpreter = readInterp(f)
	if needed, err := f.ImportedLibraries(); err == nil && len(needed) > 0 {
		rep.DTNeeded = needed
	}
	rep.TLS = readTLS(f)

	dyn, err := f.DynamicSymbols()
	if err != nil && err != elf.ErrNoSymbols {
		return nil, fmt.Errorf("elfinspect: dynsym %s: %w", name, err)
	}
	imports, exports := splitDynsym(dyn)
	slices.SortFunc(imports, cmpSymbol)
	rep.Imports = imports
	rep.JNIEntryPoints = jniEntryPoints(exports)
	listed, total, trunc := selectExports(exports)
	rep.Exports = listed
	rep.ExportTotal = total
	rep.ExportsTruncated = trunc

	rep.Relocations = summarizeRelocs(f)
	rep.AndroidNotes = collectAndroidNotes(f, rep)
	return rep, nil
}

func className(c elf.Class) string {
	switch c {
	case elf.ELFCLASS64:
		return "ELF64"
	case elf.ELFCLASS32:
		return "ELF32"
	default:
		return c.String()
	}
}

func readInterp(f *elf.File) string {
	for _, p := range f.Progs {
		if p.Type != elf.PT_INTERP {
			continue
		}
		b, err := io.ReadAll(p.Open())
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(b), "\x00")
	}
	return ""
}

func readTLS(f *elf.File) TLSInfo {
	var tls TLSInfo
	for _, p := range f.Progs {
		if p.Type != elf.PT_TLS {
			continue
		}
		tls.Present = true
		tls.Size = p.Memsz
		tls.Align = p.Align
		tls.FileSize = p.Filesz
		break
	}
	var sections []string
	var sectionSize uint64
	for _, s := range f.Sections {
		if s == nil {
			continue
		}
		switch s.Name {
		case ".tdata", ".tbss":
			tls.Present = true
			sections = append(sections, s.Name)
			sectionSize += s.Size
		}
	}
	if len(sections) > 0 {
		tls.SectionNames = sections
	}
	if tls.Size == 0 && sectionSize > 0 {
		tls.Size = sectionSize
	}
	return tls
}

func splitDynsym(syms []elf.Symbol) (imports, exports []Symbol) {
	for _, s := range syms {
		if s.Name == "" {
			continue
		}
		typ := elf.ST_TYPE(s.Info)
		if typ == elf.STT_SECTION || typ == elf.STT_FILE {
			continue
		}
		bind := elf.ST_BIND(s.Info)
		if bind != elf.STB_GLOBAL && bind != elf.STB_WEAK {
			continue
		}
		sym := Symbol{
			Name:    s.Name,
			Version: s.Version,
			Library: s.Library,
			Binding: bind.String(),
			Type:    typ.String(),
		}
		if s.Section == elf.SHN_UNDEF {
			imports = append(imports, sym)
			continue
		}
		exports = append(exports, sym)
	}
	return imports, exports
}

func isJNIExportName(name string) bool {
	return name == "JNI_OnLoad" || name == "JNI_OnUnload" || strings.HasPrefix(name, "Java_")
}

func jniEntryPoints(exports []Symbol) []string {
	var load, unload bool
	var java []string
	for _, s := range exports {
		switch {
		case s.Name == "JNI_OnLoad":
			load = true
		case s.Name == "JNI_OnUnload":
			unload = true
		case strings.HasPrefix(s.Name, "Java_"):
			java = append(java, s.Name)
		}
	}
	slices.Sort(java)
	java = slices.Compact(java)
	out := make([]string, 0, len(java)+2)
	if load {
		out = append(out, "JNI_OnLoad")
	}
	if unload {
		out = append(out, "JNI_OnUnload")
	}
	return append(out, java...)
}

func selectExports(all []Symbol) (listed []Symbol, total int, truncated bool) {
	total = len(all)
	jni := make([]Symbol, 0)
	rest := make([]Symbol, 0)
	for _, s := range all {
		if isJNIExportName(s.Name) {
			jni = append(jni, s)
		} else {
			rest = append(rest, s)
		}
	}
	slices.SortFunc(jni, cmpSymbol)
	slices.SortFunc(rest, cmpSymbol)
	if len(rest) <= maxListedNonJNIExports {
		return append(jni, rest...), total, false
	}
	listed = append(jni, rest[:maxListedNonJNIExports]...)
	return listed, total, true
}

func cmpSymbol(a, b Symbol) int {
	if c := cmp.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Library, b.Library); c != 0 {
		return c
	}
	return cmp.Compare(a.Version, b.Version)
}

func dynEntries(f *elf.File) []dynEnt {
	ds := f.SectionByType(elf.SHT_DYNAMIC)
	if ds == nil {
		return nil
	}
	d, err := ds.Data()
	if err != nil {
		return nil
	}
	entSize := 8
	if f.Class == elf.ELFCLASS64 {
		entSize = 16
	}
	if entSize == 0 || len(d)%entSize != 0 {
		return nil
	}
	out := make([]dynEnt, 0, len(d)/entSize)
	for len(d) >= entSize {
		var tag elf.DynTag
		var val uint64
		if f.Class == elf.ELFCLASS64 {
			tag = elf.DynTag(f.ByteOrder.Uint64(d[0:8]))
			val = f.ByteOrder.Uint64(d[8:16])
		} else {
			tag = elf.DynTag(f.ByteOrder.Uint32(d[0:4]))
			val = uint64(f.ByteOrder.Uint32(d[4:8]))
		}
		d = d[entSize:]
		if tag == elf.DT_NULL {
			break
		}
		out = append(out, dynEnt{Tag: tag, Val: val})
	}
	return out
}

type dynEnt struct {
	Tag elf.DynTag
	Val uint64
}

func hasDynTag(ents []dynEnt, tag elf.DynTag) bool {
	for _, e := range ents {
		if e.Tag == tag {
			return true
		}
	}
	return false
}

func dynVal(ents []dynEnt, tag elf.DynTag) (uint64, bool) {
	for _, e := range ents {
		if e.Tag == tag {
			return e.Val, true
		}
	}
	return 0, false
}

func parseNotes(data []byte, order binary.ByteOrder) []elfNote {
	var notes []elfNote
	for len(data) >= 12 {
		namesz := order.Uint32(data[0:4])
		descsz := order.Uint32(data[4:8])
		typ := order.Uint32(data[8:12])
		off := 12
		if uint64(namesz) > uint64(len(data)-off) {
			break
		}
		nameEnd := off + int(namesz)
		name := strings.TrimRight(string(data[off:nameEnd]), "\x00")
		off = align4(nameEnd)
		if off < 0 || off > len(data) {
			break
		}
		remain := len(data) - off
		if uint64(descsz) > uint64(remain) {
			break
		}
		descEnd := off + int(descsz)
		next := align4(descEnd)
		if next < descEnd || next > len(data) {
			break
		}
		desc := data[off:descEnd]
		notes = append(notes, elfNote{Name: name, Type: typ, Desc: append([]byte(nil), desc...)})
		data = data[next:]
	}
	return notes
}

type elfNote struct {
	Name string
	Type uint32
	Desc []byte
}

func align4(n int) int {
	return (n + 3) &^ 3
}

func neededBase(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
