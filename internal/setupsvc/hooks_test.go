// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"testing"
)

func resolvableClientDir(t *testing.T) string {
	t.Helper()
	return writeRuntimeLibs(t, map[string][]byte{
		"libroblox.so": buildFakeELF([]string{"liblog.so"}, []fakeSym{importSym("__android_log_print", elf.STT_FUNC)}),
	})
}

func TestMissingImportSupportHookFailsClosed(t *testing.T) {
	runtimeDir := resolvableClientDir(t)
	if err := ValidateExtractedClientImports(context.Background(), runtimeDir, testHooks().SupportsImport); err != nil {
		t.Fatalf("control client rejected with the hook: %v", err)
	}
	if err := ValidateExtractedClientImports(context.Background(), runtimeDir, nil); ErrorKindOf(err) != ErrCompatibility {
		t.Fatalf("nil hook validate kind=%q err=%v", ErrorKindOf(err), err)
	}
	if _, err := InspectExtractedClientImports(context.Background(), runtimeDir, nil); err == nil {
		t.Fatal("nil hook inspect passed")
	}
	root := filepath.Join(t.TempDir(), "generations")
	if _, err := InspectActiveClientImports(context.Background(), root, DevelopmentTrustPolicy(), nil); err == nil {
		t.Fatal("nil hook active inspect passed")
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("diagnostic created store root: %v", err)
	}
	// The Service default must pass its own (nil) hook through, not skip it.
	if err := New(Hooks{}).importPreflightFn()(context.Background(), runtimeDir); err == nil {
		t.Fatal("service without an import hook passed the preflight")
	}
	if err := New(testHooks()).importPreflightFn()(context.Background(), runtimeDir); err != nil {
		t.Fatalf("service with the import hook: %v", err)
	}
}

func TestUnconfiguredServiceHooksFailClosed(t *testing.T) {
	s := New(Hooks{})
	if root := s.generationStoreRoot(); root != "" {
		t.Fatalf("store root derived without a runtime directory: %q", root)
	}
	snapshot, err := s.Snapshot(context.Background())
	if ErrorKindOf(err) != ErrInstall || snapshot.Installed || snapshot.Readiness != ReadinessRejected {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	input := filepath.Join(t.TempDir(), "base.apk")
	if err := os.WriteFile(input, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Install(context.Background(), InstallRequest{Mode: InstallLocal, LocalPaths: []string{input}}, nil); ErrorKindOf(err) != ErrInstall {
		t.Fatalf("install without a runtime directory err=%v", err)
	}
	if err := s.prepareFn()(); err == nil {
		t.Fatal("missing app-storage hook prepared storage")
	}
	derived := New(Hooks{RuntimeDir: filepath.Join("data", "runtime")})
	if got, want := derived.generationStoreRoot(), GenerationStoreDir(filepath.Join("data", "runtime")); got != want {
		t.Fatalf("store root=%q want %q", got, want)
	}
}
