// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// ImportKind says what a missing import is, from its ELF symbol type. A
// caller that can bind a stand-in for an absent function but never for absent
// data uses it to tell the two apart.
type ImportKind string

const (
	ImportFunction ImportKind = "function" // STT_FUNC or STT_GNU_IFUNC
	ImportData     ImportKind = "data"     // STT_OBJECT or STT_TLS
	ImportUnknown  ImportKind = "unknown"  // STT_NOTYPE and anything else
)

// MissingImport is a non-weak undefined dynamic symbol of libroblox.so that
// the loader could not bind.
type MissingImport struct {
	Name    string
	Kind    ImportKind
	Library string // GNU verneed SONAME, empty for an unversioned import
	Version string // GNU verneed version, empty for an unversioned import
}

// ValidateExtractedClientImports applies the loader's missing-symbol policy to
// libroblox.so before a build is activated. Unresolvable data imports abort the
// load, so they fail setup, listing every name. Unresolvable functions bind to
// the loader's unsupported fallback by default, so they are only warning-
// logged. A GNU-versioned import and a non-system DT_NEEDED library absent
// from lib/x86_64 have no fallback in the loader and always fail setup.
// TIPSY_STRICT_SYMBOLS=1 restores the loader's abort for every missing import
// and therefore fails setup for functions too.
func ValidateExtractedClientImports(ctx context.Context, runtimeDir string) error {
	missing, absent, err := findClientImports(ctx, runtimeDir)
	if err != nil {
		return setupError(ErrCompatibility, "check client imports", "the verified root client library's native imports could not be read", err)
	}
	fatal, unsupported := partitionMissingImports(missing)
	// A missing bundled library makes every symbol that might come from it
	// look unresolved, so a fallback warning would name functions the loader
	// never reaches. Report the absent library instead.
	if len(absent) == 0 && len(unsupported) > 0 {
		names := missingImportNames(unsupported)
		list := strings.Join(names, ", ")
		logging.Logger(logging.CatAPK).Warn(
			fmt.Sprintf("the verified client needs %d native function(s) that Tipsy cannot provide; the loader will bind them to its unsupported fallback: %s", len(names), list),
			"count", len(names), "symbols", list)
	}
	if len(fatal) == 0 && len(absent) == 0 {
		return nil
	}
	var details []string
	if len(absent) > 0 {
		details = append(details, fmt.Sprintf("the verified client needs %d bundled native library(ies) absent from lib/x86_64: %s", len(absent), strings.Join(absent, ", ")))
	}
	if len(fatal) > 0 {
		names := missingImportNames(fatal)
		details = append(details, fmt.Sprintf("the verified client needs %d native symbol(s) that Tipsy cannot provide: %s", len(names), strings.Join(names, ", ")))
	}
	return setupError(ErrCompatibility, "check client imports", strings.Join(details, "; "), nil)
}

// partitionMissingImports splits missing imports the way the loader treats
// them: an unversioned function may bind to the unsupported fallback unless
// TIPSY_STRICT_SYMBOLS is set, while data (and symbols whose type does not
// prove they are callable) abort the load. A GNU-versioned import also aborts:
// resolveVersionedUndef requires the exact version from its direct provider and
// never falls back. It mirrors loader fallbackEligible, which is not exported.
func partitionMissingImports(missing []MissingImport) (fatal, unsupported []MissingImport) {
	strict := strictNativeSymbols()
	for _, m := range missing {
		if !strict && m.Kind == ImportFunction && m.Version == "" {
			unsupported = append(unsupported, m)
			continue
		}
		fatal = append(fatal, m)
	}
	return fatal, unsupported
}

func missingImportNames(missing []MissingImport) []string {
	names := make([]string, len(missing))
	for i, m := range missing {
		names[i] = m.Name
	}
	return names
}

// strictNativeSymbols mirrors loader strictSymbols; TIPSY_STRICT_SYMBOLS=1 (or
// true/yes/on) restores the abort for every missing import.
const strictSymbolsEnv = "TIPSY_STRICT_SYMBOLS"

func strictNativeSymbols() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(strictSymbolsEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// FindMissingClientImports lists, sorted by name, the non-weak undefined
// dynamic imports of <runtimeDir>/lib/x86_64/libroblox.so that nothing can
// supply. Resolution mirrors the loader's resolveUndef: exports of bundled
// DT_NEEDED libraries, then the Tipsy Android provider (android.SupportsImport).
// Weak imports bind to zero, as in the loader, and are never reported.
//
// A bundled DT_NEEDED library absent from lib/x86_64 is not an import, so it is
// not named here; ValidateExtractedClientImports reports it as fatal.
//
// It is safe to run before any launch: the library is parsed as data with
// debug/elf and never mapped, relocated or executed, no JNI_OnLoad runs, and
// SupportsImport loads no host library.
func FindMissingClientImports(ctx context.Context, runtimeDir string) ([]MissingImport, error) {
	missing, _, err := findClientImports(ctx, runtimeDir)
	return missing, err
}

// findClientImports returns the missing imports plus the non-system DT_NEEDED
// libraries absent from lib/x86_64. The loader's authenticated open aborts on
// an absent dependency, so a caller that gates install must treat that list as
// fatal.
func findClientImports(ctx context.Context, runtimeDir string) ([]MissingImport, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	libDir := filepath.Join(runtimeDir, "lib", "x86_64")
	root, closeRoot, err := openELFRegular(filepath.Join(libDir, "libroblox.so"))
	if err != nil {
		return nil, nil, err
	}
	defer closeRoot()
	needed, err := root.ImportedLibraries()
	if err != nil {
		return nil, nil, fmt.Errorf("read DT_NEEDED: %w", err)
	}
	syms, err := root.DynamicSymbols()
	if err != nil && err != elf.ErrNoSymbols {
		return nil, nil, fmt.Errorf("read dynamic symbols: %w", err)
	}
	direct, all, absent, err := bundledExports(ctx, libDir, needed)
	if err != nil {
		return nil, nil, err
	}
	jumpSlots, otherRelocs, err := undefinedNoTypeRelocations(root, syms)
	if err != nil {
		return nil, nil, err
	}

	var missing []MissingImport
	seen := map[string]struct{}{}
	for i, s := range syms {
		if s.Name == "" || s.Section != elf.SHN_UNDEF {
			continue
		}
		if bind := elf.ST_BIND(s.Info); bind == elf.STB_WEAK || bind == elf.STB_LOCAL {
			continue
		}
		if importResolves(s, direct, all) {
			continue
		}
		key := s.Name + "\x00" + s.Library + "\x00" + s.Version
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		// DynamicSymbols omits the null entry at dynsym index 0, while an
		// r_info symbol index is the raw section index, so shift by one.
		idx := uint32(i + 1)
		missing = append(missing, MissingImport{Name: s.Name, Kind: importKind(s, jumpSlots[idx], otherRelocs[idx]), Library: s.Library, Version: s.Version})
	}
	slices.SortFunc(missing, func(a, b MissingImport) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Version, b.Version)
	})
	return missing, absent, nil
}

// importResolves follows loader.Module.resolveUndef. A versioned import is
// bound to exactly the dependency named by its verneed entry; an unversioned
// one is searched through every loaded dependency, then the provider.
func importResolves(s elf.Symbol, direct map[string]map[string]struct{}, all []map[string]struct{}) bool {
	if s.Library != "" && s.Version != "" {
		if dep, ok := direct[s.Library]; ok {
			_, has := dep[s.Name]
			return has
		}
		return android.SupportsImport(s.Library, s.Name, s.Version)
	}
	for _, exports := range all {
		if _, ok := exports[s.Name]; ok {
			return true
		}
	}
	return android.SupportsImport("", s.Name, "")
}

// importKind classifies an unresolved import the way the loader's
// fallbackEligible does. STT_FUNC and STT_GNU_IFUNC are functions whatever the
// relocation. STT_NOTYPE is a function only when its sole relocation is
// R_X86_64_JUMP_SLOT: a PLT slot can only be called, while a GLOB_DAT or
// R_X86_64_64 reference may read it as data, so the loader still aborts.
// Everything else is data-shaped and fatal for the loader.
func importKind(s elf.Symbol, jumpSlot, otherReloc bool) ImportKind {
	switch elf.ST_TYPE(s.Info) {
	case elf.STT_FUNC, elf.STT_GNU_IFUNC:
		return ImportFunction
	case elf.STT_OBJECT, elf.STT_TLS:
		return ImportData
	case elf.STT_NOTYPE:
		if jumpSlot && !otherReloc {
			return ImportFunction
		}
		return ImportUnknown
	default:
		return ImportUnknown
	}
}

// undefinedNoTypeRelocations reports the dynsym indexes the loader reaches
// through R_X86_64_JUMP_SLOT and those also reached through any other
// relocation. Only libraries with an undefined STT_NOTYPE symbol need it;
// every other symbol type classifies from its ELF type alone, so the common
// case reads no relocation section.
func undefinedNoTypeRelocations(root *elf.File, syms []elf.Symbol) (map[uint32]bool, map[uint32]bool, error) {
	needed := false
	for _, s := range syms {
		if s.Name != "" && s.Section == elf.SHN_UNDEF && elf.ST_TYPE(s.Info) == elf.STT_NOTYPE {
			needed = true
			break
		}
	}
	if !needed {
		return nil, nil, nil
	}
	sections := root.Sections
	dynsymIndex := -1
	for i, sec := range sections {
		if sec.Type == elf.SHT_DYNSYM {
			dynsymIndex = i
			break
		}
	}
	if dynsymIndex < 0 {
		return nil, nil, nil
	}
	jumpSlots := map[uint32]bool{}
	otherRelocs := map[uint32]bool{}
	for _, sec := range sections {
		if sec.Type != elf.SHT_RELA && sec.Type != elf.SHT_REL {
			continue
		}
		if int(sec.Link) != dynsymIndex {
			continue
		}
		data, err := sec.Data()
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", sec.Name, err)
		}
		entrySize := int(sec.Entsize)
		if entrySize < 16 || entrySize > 32 || len(data)%entrySize != 0 {
			return nil, nil, fmt.Errorf("read %s: invalid relocation entry size %d", sec.Name, entrySize)
		}
		for off := 0; off+entrySize <= len(data); off += entrySize {
			info := binary.LittleEndian.Uint64(data[off+8:])
			idx := uint32(info >> 32)
			if idx == 0 {
				continue
			}
			if elf.R_X86_64(uint32(info)) == elf.R_X86_64_JMP_SLOT {
				jumpSlots[idx] = true
				continue
			}
			otherRelocs[idx] = true
		}
	}
	return jumpSlots, otherRelocs, nil
}

// bundledExports returns the defined, non-local exports of the bundled
// (non-system) DT_NEEDED libraries of the root: direct keyed by SONAME for
// versioned binding, and every transitive dependency for unversioned search.
// It also returns, sorted, the non-system DT_NEEDED libraries whose file is
// absent from lib/x86_64: the loader's authenticated open aborts on one, so the
// preflight must not ignore it.
func bundledExports(ctx context.Context, libDir string, needed []string) (map[string]map[string]struct{}, []map[string]struct{}, []string, error) {
	direct := map[string]map[string]struct{}{}
	var all []map[string]struct{}
	var absent []string
	// known holds each bundled library's exports once parsed; a nil entry
	// marks a name that is visited but absent or still being read.
	known := map[string]map[string]struct{}{"libroblox.so": nil}
	type pending struct {
		name   string
		direct bool
	}
	var queue []pending
	for _, n := range needed {
		queue = append(queue, pending{n, true})
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		item := queue[0]
		queue = queue[1:]
		base := filepath.Base(item.name)
		if base == "." || base == ".." || base == string(filepath.Separator) || loader.IsSystemSoname(base) {
			continue
		}
		if exports, dup := known[base]; dup {
			if item.direct && exports != nil {
				direct[base] = exports
			}
			continue
		}
		known[base] = nil
		lib, closeLib, err := openELFRegular(filepath.Join(libDir, base))
		if err != nil {
			if os.IsNotExist(err) {
				absent = append(absent, base)
				continue
			}
			return nil, nil, nil, fmt.Errorf("bundled %s: %w", base, err)
		}
		syms, symErr := lib.DynamicSymbols()
		deps, depErr := lib.ImportedLibraries()
		closeLib()
		if symErr != nil && symErr != elf.ErrNoSymbols {
			return nil, nil, nil, fmt.Errorf("bundled %s: read dynamic symbols: %w", base, symErr)
		}
		if depErr != nil {
			return nil, nil, nil, fmt.Errorf("bundled %s: read DT_NEEDED: %w", base, depErr)
		}
		exports := map[string]struct{}{}
		for _, s := range syms {
			if s.Name == "" || s.Section == elf.SHN_UNDEF || s.Section == elf.SHN_COMMON || elf.ST_BIND(s.Info) == elf.STB_LOCAL {
				continue
			}
			exports[s.Name] = struct{}{}
		}
		known[base] = exports
		all = append(all, exports)
		if item.direct {
			direct[base] = exports
		}
		for _, d := range deps {
			queue = append(queue, pending{d, false})
		}
	}
	slices.Sort(absent)
	return direct, all, absent, nil
}

// openELFRegular opens path only if it is a regular, non-symlink file and
// parses it with debug/elf. The file is read on demand, never mapped.
func openELFRegular(path string) (*elf.File, func(), error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return nil, nil, fmt.Errorf("%s is not a regular non-empty file", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s changed before inspection", filepath.Base(path))
	}
	parsed, err := elf.NewFile(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s is not a readable ELF object: %w", filepath.Base(path), err)
	}
	return parsed, func() { _ = parsed.Close(); _ = file.Close() }, nil
}
