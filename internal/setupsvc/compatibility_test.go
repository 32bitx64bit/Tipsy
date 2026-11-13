// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package setupsvc

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateOpenedClientCompatibilityUsesReaderAt(t *testing.T) {
	var syms []fakeSym
	seen := map[string]bool{}
	for _, capability := range requiredClientCapabilities {
		for _, name := range capability.exports {
			if seen[name] {
				continue
			}
			seen[name] = true
			syms = append(syms, fakeSym{name: name, typ: elf.STT_FUNC, bind: elf.STB_GLOBAL})
		}
	}
	raw := buildFakeELF(nil, syms)
	path := filepath.Join(t.TempDir(), "libroblox.so")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOpenedClientCompatibility(context.Background(), file, info.Size()); err != nil {
		t.Fatalf("compatible ELF rejected: %v", err)
	}
	// Analysis reads through a section reader, so the caller's descriptor must
	// still be usable afterwards.
	if _, err := file.ReadAt(make([]byte, 1), 0); err != nil {
		t.Fatalf("descriptor unusable after analysis: %v", err)
	}
}
