// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package diagnostics

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

type diagnosticImport struct {
	name string
	typ  elf.SymType
}

// buildDiagnosticELF is the same data-only fake ELF shape used by setupsvc's
// import-preflight tests: DT_NEEDED, .dynsym and .dynstr, with no executable
// code. Every listed import is a non-weak undefined dynamic symbol.
func buildDiagnosticELF(needed []string, imports []diagnosticImport) []byte {
	dynstr := []byte{0}
	str := func(value string) uint32 {
		offset := uint32(len(dynstr))
		dynstr = append(dynstr, value...)
		dynstr = append(dynstr, 0)
		return offset
	}
	le := binary.LittleEndian
	dynsym := make([]byte, 24)
	for _, symbol := range imports {
		entry := make([]byte, 24)
		le.PutUint32(entry[0:], str(symbol.name))
		entry[4] = byte(elf.STB_GLOBAL)<<4 | byte(symbol.typ)
		dynsym = append(dynsym, entry...)
	}
	var dynamic []byte
	for _, name := range needed {
		dynamic = le.AppendUint64(dynamic, uint64(elf.DT_NEEDED))
		dynamic = le.AppendUint64(dynamic, uint64(str(name)))
	}
	dynamic = append(dynamic, make([]byte, 16)...)

	type section struct {
		name    string
		typ     elf.SectionType
		data    []byte
		link    uint32
		entsize uint64
	}
	sections := []section{
		{},
		{name: ".dynsym", typ: elf.SHT_DYNSYM, data: dynsym, link: 2, entsize: 24},
		{name: ".dynstr", typ: elf.SHT_STRTAB, data: dynstr},
		{name: ".dynamic", typ: elf.SHT_DYNAMIC, data: dynamic, link: 2, entsize: 16},
	}
	shstrtab := []byte{0}
	nameOffsets := make([]uint32, len(sections)+1)
	for i := 1; i < len(sections); i++ {
		nameOffsets[i] = uint32(len(shstrtab))
		shstrtab = append(append(shstrtab, sections[i].name...), 0)
	}
	nameOffsets[len(sections)] = uint32(len(shstrtab))
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
	sectionOffset := uint64(len(out))
	for i, sec := range sections {
		header := make([]byte, 64)
		le.PutUint32(header[0:], nameOffsets[i])
		le.PutUint32(header[4:], uint32(sec.typ))
		le.PutUint64(header[24:], offsets[i])
		le.PutUint64(header[32:], uint64(len(sec.data)))
		le.PutUint32(header[40:], sec.link)
		le.PutUint64(header[48:], 1)
		le.PutUint64(header[56:], sec.entsize)
		out = append(out, header...)
	}
	copy(out, "\x7fELF")
	out[elf.EI_CLASS], out[elf.EI_DATA], out[elf.EI_VERSION] = byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)
	le.PutUint16(out[16:], uint16(elf.ET_DYN))
	le.PutUint16(out[18:], uint16(elf.EM_X86_64))
	le.PutUint32(out[20:], 1)
	le.PutUint64(out[40:], sectionOffset)
	le.PutUint16(out[52:], 64)
	le.PutUint16(out[58:], 64)
	le.PutUint16(out[60:], uint16(len(sections)))
	le.PutUint16(out[62:], uint16(len(sections)-1))
	return out
}

func writeDiagnosticGeneration(t *testing.T, libroblox []byte) (setupsvc.TrustPolicy, string) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, filepath.Join(root, strings.ToLower(key)))
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	paths := config.Paths()
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.DataDir, "present"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	digestBytes := sha256.Sum256(libroblox)
	digest := hex.EncodeToString(digestBytes[:])
	inventory := integrity.Inventory{
		Schema:            integrity.InventorySchema,
		PackageName:       "com.roblox.client",
		VersionName:       "2.746.1266",
		VersionCode:       7461266,
		AuthorizationMode: string(setupsvc.DevelopmentUnrestricted),
		SignerLineageID:   "compiled-development",
		SignerLineage:     []string{digest},
		Splits:            []string{"base"},
		Files: []integrity.FileRecord{{
			Path: "lib/x86_64/libroblox.so", Size: int64(len(libroblox)), SHA256: digest,
			Origin: integrity.OriginAPK, APKEntry: "lib/x86_64/libroblox.so", APKDigest: digest, Executable: true,
		}},
	}
	rawInventory, generation, err := integrity.CanonicalInventory(inventory)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := setupsvc.GenerationStoreDir(filepath.Join(paths.DataDir, "runtime"))
	generationRoot := filepath.Join(storeRoot, "generations", generation)
	if err := os.MkdirAll(filepath.Join(generationRoot, "lib", "x86_64"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generationRoot, "lib", "x86_64", "libroblox.so"), libroblox, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generationRoot, "inventory.json"), rawInventory, 0o600); err != nil {
		t.Fatal(err)
	}
	active, err := json.Marshal(integrity.ActiveRecord{Schema: integrity.ActiveSchema, Generation: generation, InventorySHA256: generation})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeRoot, "active-development.json"), active, 0o600); err != nil {
		t.Fatal(err)
	}
	return setupsvc.DevelopmentTrustPolicy(), generation
}

func TestDoctorAndDiagnoseActiveGenerationImports(t *testing.T) {
	tests := []struct {
		name       string
		libroblox  []byte
		strict     bool
		wantStatus string
		wantNames  []string
	}{
		{
			name: "clean",
			libroblox: buildDiagnosticELF([]string{"liblog.so"}, []diagnosticImport{
				{name: "__android_log_print", typ: elf.STT_FUNC},
			}),
			wantStatus: ImportsOK,
		},
		{
			name: "fallback functions",
			libroblox: buildDiagnosticELF(nil, []diagnosticImport{
				{name: "AHardwareBuffer_tipsyMissing", typ: elf.STT_FUNC},
				{name: "AImageReader_tipsyMissing", typ: elf.STT_FUNC},
			}),
			wantStatus: ImportsWarning,
			wantNames:  []string{"AHardwareBuffer_tipsyMissing", "AImageReader_tipsyMissing"},
		},
		{
			name: "data import",
			libroblox: buildDiagnosticELF(nil, []diagnosticImport{
				{name: "tipsy_missing_data", typ: elf.STT_OBJECT},
			}),
			wantStatus: ImportsBlocked,
			wantNames:  []string{"tipsy_missing_data"},
		},
		{
			name: "strict function",
			libroblox: buildDiagnosticELF(nil, []diagnosticImport{
				{name: "AImageReader_tipsyMissing", typ: elf.STT_FUNC},
			}),
			strict:     true,
			wantStatus: ImportsBlocked,
			wantNames:  []string{"AImageReader_tipsyMissing"},
		},
		{
			name:       "absent bundled library",
			libroblox:  buildDiagnosticELF([]string{"libtipsy_absent.so"}, nil),
			wantStatus: ImportsBlocked,
			wantNames:  []string{"libtipsy_absent.so"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TIPSY_STRICT_SYMBOLS", "")
			if test.strict {
				t.Setenv("TIPSY_STRICT_SYMBOLS", "1")
			}
			trust, generation := writeDiagnosticGeneration(t, test.libroblox)
			report := DoctorForTrust(context.Background(), trust)
			if report.Roblox.Generation != generation || report.Roblox.Version != "2.746.1266" || report.Roblox.ImportStatus != test.wantStatus {
				t.Fatalf("Roblox report=%+v, want generation=%q import status=%q", report.Roblox, generation, test.wantStatus)
			}
			var generationIssues []string
			for _, issue := range report.Issues {
				if strings.Contains(issue, generation) {
					generationIssues = append(generationIssues, issue)
				}
			}
			if test.wantStatus == ImportsOK && len(generationIssues) != 0 {
				t.Fatalf("clean generation added issues: %v", generationIssues)
			}
			if test.wantStatus != ImportsOK {
				if len(generationIssues) != 1 {
					t.Fatalf("generation issues=%v, all issues=%v", generationIssues, report.Issues)
				}
				wantPrefix := "Warning: "
				if test.wantStatus == ImportsBlocked {
					wantPrefix = "Blocking: "
				}
				if !strings.HasPrefix(generationIssues[0], wantPrefix) || !strings.Contains(generationIssues[0], "2.746.1266") {
					t.Fatalf("issue lacks severity/version: %q", generationIssues[0])
				}
			}
			for _, name := range test.wantNames {
				if !strings.Contains(report.Roblox.ImportDetail, name) || len(generationIssues) == 0 || !strings.Contains(generationIssues[0], name) {
					t.Fatalf("import %q missing from detail/issues: %q / %v", name, report.Roblox.ImportDetail, generationIssues)
				}
			}

			for _, subsystem := range []string{"roblox", "loader"} {
				subsystemReport := DiagnoseForTrust(context.Background(), subsystem, trust)
				wantSubsystemStatus := "active"
				if test.wantStatus != ImportsOK {
					wantSubsystemStatus = test.wantStatus
				}
				if subsystemReport.Status != wantSubsystemStatus {
					t.Fatalf("%s status=%q, want %q", subsystem, subsystemReport.Status, wantSubsystemStatus)
				}
				formatted := FormatSubsystem(subsystemReport)
				if !strings.Contains(formatted, generation) {
					t.Fatalf("%s output omits generation:\n%s", subsystem, formatted)
				}
				for _, name := range test.wantNames {
					if !strings.Contains(formatted, name) {
						t.Fatalf("%s output omits %q:\n%s", subsystem, name, formatted)
					}
				}
			}
		})
	}
}
