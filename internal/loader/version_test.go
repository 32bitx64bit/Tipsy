// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

type legacyRecordingResolver struct {
	values mapRes
	calls  []string
}

func (r *legacyRecordingResolver) Lookup(lib, sym string) (uintptr, error) {
	r.calls = append(r.calls, lib+"|"+sym)
	return r.values.Lookup(lib, sym)
}

type versionRecordingResolver struct {
	legacyRecordingResolver
	versions     map[string]uintptr
	versionCalls []string
}

func (r *versionRecordingResolver) LookupVersion(lib, sym, version string) (uintptr, error) {
	key := lib + "|" + sym + "@" + version
	r.versionCalls = append(r.versionCalls, key)
	if addr := r.versions[key]; addr != 0 {
		return addr, nil
	}
	return 0, fmt.Errorf("unsupported versioned symbol %s", key)
}

func TestOpenFDVersionedImportRejectsNameOnlyResolver(t *testing.T) {
	requireSyntheticPage(t)
	raw := buildSynthELF(synthOpts{
		needed: []string{"libc.so"}, imports: []string{"tipsy_versioned"},
		versionNeeds: []synthVersionNeed{{
			library: "libc.so", version: "LIBC_TIPSY_1", imports: []string{"tipsy_versioned"},
		}},
	})
	descriptor := pinnedNative(t, t.TempDir(), "libmain.so", raw)
	resolver := &legacyRecordingResolver{values: mapRes{
		"|tipsy_versioned":        0x1111,
		"libc.so|tipsy_versioned": 0x2222,
	}}

	_, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), resolver)
	var compatErr *VersionCompatibilityError
	if !errors.As(err, &compatErr) {
		t.Fatalf("OpenFD error = %v, want typed VersionCompatibilityError", err)
	}
	if compatErr.Reason != VersionProviderUnavailable || compatErr.Dependency != "libc.so" ||
		compatErr.Symbol != "tipsy_versioned" || compatErr.Version != "LIBC_TIPSY_1" {
		t.Fatalf("compatibility error = %#v", compatErr)
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("authenticated versioned import reached name-only resolver: %v", resolver.calls)
	}
}

func TestOpenFDVersionedResolverGetsExactOwnerAndVersion(t *testing.T) {
	requireSyntheticPage(t)
	raw := buildSynthELF(synthOpts{
		needed: []string{"libc.so", "libm.so"}, imports: []string{"tipsy_versioned"},
		versionNeeds: []synthVersionNeed{{
			library: "libm.so", version: "LIBM_TIPSY_2", imports: []string{"tipsy_versioned"},
		}},
	})
	descriptor := pinnedNative(t, t.TempDir(), "libmain.so", raw)
	resolver := &versionRecordingResolver{
		legacyRecordingResolver: legacyRecordingResolver{values: mapRes{
			"|tipsy_versioned":        0x1111,
			"libc.so|tipsy_versioned": 0x2222,
		}},
		versions: map[string]uintptr{"libm.so|tipsy_versioned@LIBM_TIPSY_2": 0x3333},
	}

	m, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got, err := m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x3333 {
		t.Fatalf("versioned relocation = %#x, want exact version provider %#x", got, uint64(0x3333))
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("versioned import fell back to name-only lookup: %v", resolver.calls)
	}
	if want := []string{"libm.so|tipsy_versioned@LIBM_TIPSY_2"}; fmt.Sprint(resolver.versionCalls) != fmt.Sprint(want) {
		t.Fatalf("version calls = %v, want %v", resolver.versionCalls, want)
	}
	if guard, err := m.peek64(vaGuard); err != nil || guard != 0 {
		t.Fatalf("constructor guard before Init = %#x, err %v", guard, err)
	}
	requirement := m.versions.requirements[2]
	if requirement == nil || requirement.library != "libm.so" || requirement.name != "LIBM_TIPSY_2" {
		t.Fatalf("retained version requirement = %#v", requirement)
	}
}

func TestGNUVersionDefinitionAndSymbolVisibilityRetained(t *testing.T) {
	requireSyntheticPage(t)
	raw := buildSynthELF(synthOpts{versionDefs: []synthVersionDef{{
		version: "LIBTIPSY_PRIVATE_1", exports: []string{"JNI_OnLoad"}, hidden: true,
	}}})
	// st_other is independent from the GNU versym hidden bit and must survive
	// dynamic-symbol parsing for future ABI/visibility policy checks.
	raw[vaDynsym+24+5] = byte(elf.STV_PROTECTED)
	m, err := Open(writeSyntheticELF(t, "libdefinition.so", raw), mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if got := elf.SymVis(m.syms[1].other); got != elf.STV_PROTECTED {
		t.Fatalf("retained st_other visibility = %v", got)
	}
	definition := m.versions.definitions[2]
	if definition == nil || definition.name != "LIBTIPSY_PRIVATE_1" || !m.syms[1].version.hidden {
		t.Fatalf("retained version definition = %#v symbol=%#v", definition, m.syms[1].version)
	}
	if _, err := m.Lookup("JNI_OnLoad"); err == nil {
		t.Fatal("unversioned Lookup accepted a hidden non-default version")
	}
	if _, ok := m.lookupDefVersion("JNI_OnLoad", "LIBTIPSY_PRIVATE_1"); !ok {
		t.Fatal("exact-version lookup did not find hidden definition")
	}
}

func TestOpenFDVersionedImportUsesMatchingGuestDefinitionOnly(t *testing.T) {
	requireSyntheticPage(t)
	for _, test := range []struct {
		name        string
		providerVer string
		wantErr     bool
	}{
		{name: "matching", providerVer: "LIBDEP_1"},
		{name: "wrong-version", providerVer: "LIBDEP_2", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dep := pinnedNative(t, t.TempDir(), "libdep.so", buildSynthELF(synthOpts{
				soname: "libdep.so",
				versionDefs: []synthVersionDef{{
					version: test.providerVer, exports: []string{"JNI_OnLoad"},
				}},
			}))
			root := pinnedNative(t, t.TempDir(), "libmain.so", buildSynthELF(synthOpts{
				soname: "libmain.so", needed: []string{"libdep.so"}, imports: []string{"JNI_OnLoad"},
				versionNeeds: []synthVersionNeed{{
					library: "libdep.so", version: "LIBDEP_1", imports: []string{"JNI_OnLoad"},
				}},
			}))
			resolver := &versionRecordingResolver{
				versions: map[string]uintptr{"libdep.so|JNI_OnLoad@LIBDEP_1": 0x9999},
			}
			m, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, dep, root), resolver)
			if test.wantErr {
				var compatErr *VersionCompatibilityError
				if !errors.As(err, &compatErr) || compatErr.Reason != VersionProviderMissing {
					t.Fatalf("OpenFD error = %v, want provider-missing VersionCompatibilityError", err)
				}
				if len(resolver.versionCalls) != 0 {
					t.Fatalf("wrong-version guest dependency fell through to resolver: %v", resolver.versionCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			got, err := m.peek64(vaGlobSlot)
			if err != nil {
				t.Fatal(err)
			}
			want, err := m.deps[0].Lookup("JNI_OnLoad")
			if err != nil {
				t.Fatal(err)
			}
			if got != uint64(want) {
				t.Fatalf("versioned guest relocation = %#x, want matching dependency %#x", got, want)
			}
			if len(resolver.versionCalls) != 0 {
				t.Fatalf("matching guest dependency unexpectedly reached resolver: %v", resolver.versionCalls)
			}
		})
	}
}

func TestOpenVersionedImportKeepsDevelopmentFallbackDependencyScoped(t *testing.T) {
	requireSyntheticPage(t)
	raw := buildSynthELF(synthOpts{
		needed: []string{"libc.so", "libm.so"}, imports: []string{"tipsy_versioned"},
		versionNeeds: []synthVersionNeed{{
			library: "libm.so", version: "LIBM_TIPSY_2", imports: []string{"tipsy_versioned"},
		}},
	})
	path := writeSyntheticELF(t, "libdevelopment.so", raw)
	resolver := &legacyRecordingResolver{values: mapRes{
		"|tipsy_versioned":        0x1111,
		"libc.so|tipsy_versioned": 0x2222,
		"libm.so|tipsy_versioned": 0x3333,
	}}
	m, err := Open(path, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	got, err := m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x3333 {
		t.Fatalf("development versioned relocation = %#x", got)
	}
	if want := []string{"libm.so|tipsy_versioned"}; fmt.Sprint(resolver.calls) != fmt.Sprint(want) {
		t.Fatalf("development lookup calls = %v, want exact dependency only %v", resolver.calls, want)
	}
}

func TestGNUVersionMetadataRejectsMalformedTables(t *testing.T) {
	requireSyntheticPage(t)
	valid := func() []byte {
		return buildSynthELF(synthOpts{
			needed: []string{"libc.so"}, imports: []string{"tipsy_versioned"},
			versionNeeds: []synthVersionNeed{{
				library: "libc.so", version: "LIBC_TIPSY_1", imports: []string{"tipsy_versioned"},
			}},
		})
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte)
		want   string
	}{
		{
			name: "unsupported-revision",
			mutate: func(raw []byte) {
				binary.LittleEndian.PutUint16(raw[vaVerneed:], gnuVersionCurrent+1)
			},
			want: "unsupported revision",
		},
		{
			name: "unknown-versym-index",
			mutate: func(raw []byte) {
				binary.LittleEndian.PutUint16(raw[vaVersym+2*2:], 99)
			},
			want: "unknown version index",
		},
		{
			name: "unterminated-aux-chain",
			mutate: func(raw []byte) {
				binary.LittleEndian.PutUint16(raw[vaVerneed+2:], 2)
			},
			want: "invalid next offset",
		},
		{
			name: "unbounded-versym-range",
			mutate: func(raw []byte) {
				setSyntheticDynValue(t, raw, elf.DT_VERSYM, synthRWVaddr+synthRWFile-2)
			},
			want: "not wholly backed",
		},
		{
			name: "excessive-record-count",
			mutate: func(raw []byte) {
				setSyntheticDynValue(t, raw, elf.DT_VERNEEDNUM, maxVersionRecords+1)
			},
			want: "table count exceeds",
		},
		{
			name: "duplicate-dynamic-tag",
			mutate: func(raw []byte) {
				setSyntheticDynTag(t, raw, elf.DT_INIT, elf.DT_VERSYM)
			},
			want: "duplicate GNU version dynamic tag",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := valid()
			test.mutate(raw)
			_, err := Open(writeSyntheticELF(t, "libmalformed.so", raw), mapRes{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func setSyntheticDynTag(t *testing.T, raw []byte, oldTag, newTag elf.DynTag) {
	t.Helper()
	for off := vaDyn; off+16 <= len(raw); off += 16 {
		got := elf.DynTag(binary.LittleEndian.Uint64(raw[off : off+8]))
		if got == elf.DT_NULL {
			break
		}
		if got == oldTag {
			binary.LittleEndian.PutUint64(raw[off:off+8], uint64(newTag))
			return
		}
	}
	t.Fatalf("synthetic ELF has no dynamic tag %s", oldTag)
}

func TestGNUVersionNeedMustNameExactDTNeeded(t *testing.T) {
	requireSyntheticPage(t)
	raw := buildSynthELF(synthOpts{
		needed: []string{"libc.so"}, imports: []string{"tipsy_versioned"},
		versionNeeds: []synthVersionNeed{{
			library: "libother.so", version: "LIB_OTHER", imports: []string{"tipsy_versioned"},
		}},
	})
	_, err := Open(writeSyntheticELF(t, "libwrongowner.so", raw), mapRes{})
	if err == nil || !strings.Contains(err.Error(), "not an exact DT_NEEDED dependency") {
		t.Fatalf("Open error = %v, want exact dependency rejection", err)
	}
}

func writeSyntheticELF(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setSyntheticDynValue(t *testing.T, raw []byte, tag elf.DynTag, value uint64) {
	t.Helper()
	for off := vaDyn; off+16 <= len(raw); off += 16 {
		got := elf.DynTag(binary.LittleEndian.Uint64(raw[off : off+8]))
		if got == elf.DT_NULL {
			break
		}
		if got == tag {
			binary.LittleEndian.PutUint64(raw[off+8:off+16], value)
			return
		}
	}
	t.Fatalf("synthetic ELF has no dynamic tag %s", tag)
}
