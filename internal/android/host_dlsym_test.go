// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const preloadHelperEnv = "TIPSY_TEST_HOST_DLSYM_PRELOAD_CHILD"

func TestHostDlsymResolvesExplicitHostSONAMEsOnly(t *testing.T) {
	for _, tc := range []struct {
		lib        string
		sym        string
		wantSubstr string
	}{
		{lib: "libc.so", sym: "getentropy", wantSubstr: "libc.so.6"},
		{lib: "libc.so", sym: "memcpy", wantSubstr: "libc.so.6"},
		{lib: "libc.so", sym: "pthread_create", wantSubstr: ".so"},
		{lib: "libm.so", sym: "sinf", wantSubstr: "libm.so.6"},
		{lib: "libz.so", sym: "deflate", wantSubstr: "libz.so.1"},
		{lib: "libz.so", sym: "inflate", wantSubstr: "libz.so.1"},
		{lib: "libz.so", sym: "zError", wantSubstr: "libz.so.1"},
	} {
		t.Run(tc.lib+"/"+tc.sym, func(t *testing.T) {
			byLib := hostDlsymLibraryForTest(tc.lib, tc.sym)
			if byLib == 0 {
				t.Fatalf("tipsy_host_dlsym_library(%q, %q) = 0", tc.lib, tc.sym)
			}
			byHost := hostDlsymForTest(tc.sym)
			if byHost == 0 {
				t.Fatalf("tipsy_host_dlsym(%q) = 0", tc.sym)
			}
			if byHost != byLib {
				t.Fatalf("tipsy_host_dlsym(%q) = %#x, want exact SONAME address %#x", tc.sym, byHost, byLib)
			}
			fname, _, ok := dladdrOwnerForTest(byHost)
			if !ok || !strings.Contains(filepath.Base(fname), tc.wantSubstr) {
				t.Fatalf("dladdr(%q) = %q (ok=%v), want basename containing %q", tc.sym, fname, ok, tc.wantSubstr)
			}
		})
	}
}

func TestHostDlsymRejectsUnownedGlobalAndCrossSONAMESymbols(t *testing.T) {
	// Symbols present in the process global scope (main binary cgo exports and
	// linked non-ABI libraries such as libpulse-simple) must NOT resolve once
	// dlsym(RTLD_DEFAULT, ...) is removed.
	for _, sym := range []string{
		"GoAndroid_LogWrite",
		"GoAndroid_LogMissing",
		"pa_simple_new",
		"tipsy_definitely_nonexistent_symbol",
	} {
		if addr := hostDlsymForTest(sym); addr != 0 {
			fname, _, _ := dladdrOwnerForTest(addr)
			t.Fatalf("tipsy_host_dlsym(%q) = %#x (%s), want 0", sym, addr, fname)
		}
	}

	// Cross-SONAME dependency bleed (e.g. libc symbols reachable via libm/libz DT_NEEDED)
	// must be rejected by dlinfo(RTLD_DI_LINKMAP) + dladdr ownership checks.
	for _, tc := range []struct {
		lib string
		sym string
	}{
		{lib: "libm.so", sym: "memcpy"},
		{lib: "libz.so", sym: "memcpy"},
		{lib: "libc.so", sym: "deflate"},
		{lib: "libc.so", sym: "sinf"},
		{lib: "libpulse.so", sym: "pa_simple_new"},
	} {
		if addr := hostDlsymLibraryForTest(tc.lib, tc.sym); addr != 0 {
			fname, _, _ := dladdrOwnerForTest(addr)
			t.Fatalf("tipsy_host_dlsym_library(%q, %q) = %#x (%s), want 0", tc.lib, tc.sym, addr, fname)
		}
	}
}

func TestHostDlsymIgnoresLDPreloadInterpositionWithoutScrubbingEnv(t *testing.T) {
	if preloadSO := os.Getenv(preloadHelperEnv); preloadSO != "" {
		// LD_PRELOAD must remain intact in process environment (never scrubbed).
		if gotEnv := os.Getenv("LD_PRELOAD"); !strings.Contains(gotEnv, preloadSO) {
			t.Fatalf("LD_PRELOAD was scrubbed or modified: got %q, want %q", gotEnv, preloadSO)
		}

		// Confirm the preloaded library is genuinely active in RTLD_DEFAULT.
		injectedDefault := rtldDefaultLookupForTest("tipsy_preload_injected_symbol")
		if injectedDefault == 0 {
			t.Fatal("preloaded symbol missing from RTLD_DEFAULT; LD_PRELOAD did not load")
		}
		defaultGetentropy := rtldDefaultLookupForTest("getentropy")
		defaultOwner, _, ok := dladdrOwnerForTest(defaultGetentropy)
		if !ok || filepath.Clean(defaultOwner) != filepath.Clean(preloadSO) {
			t.Fatalf("RTLD_DEFAULT getentropy owner = %q, want preload %q", defaultOwner, preloadSO)
		}

		// tipsy_host_dlsym and Provider().Lookup must NOT return symbols from LD_PRELOAD.
		if addr := hostDlsymForTest("tipsy_preload_injected_symbol"); addr != 0 {
			t.Fatalf("tipsy_host_dlsym resolved LD_PRELOAD-only symbol at %#x", addr)
		}
		if addr, err := Provider().Lookup("libc.so", "tipsy_preload_injected_symbol"); err == nil || addr != 0 {
			t.Fatalf("Provider().Lookup resolved LD_PRELOAD-only symbol: %#x, %v", addr, err)
		}

		hostGetentropy := hostDlsymForTest("getentropy")
		if hostGetentropy == 0 {
			t.Fatal("tipsy_host_dlsym(\"getentropy\") = 0")
		}
		if hostGetentropy == defaultGetentropy {
			t.Fatalf("tipsy_host_dlsym(\"getentropy\") returned LD_PRELOAD interposer %#x", hostGetentropy)
		}
		owner, _, ok := dladdrOwnerForTest(hostGetentropy)
		if !ok || !strings.Contains(filepath.Base(owner), "libc.so.6") {
			t.Fatalf("tipsy_host_dlsym(\"getentropy\") owner = %q, want libc.so.6", owner)
		}

		providerAddr, err := Provider().Lookup("libc.so", "getentropy")
		if err != nil || providerAddr != hostGetentropy {
			t.Fatalf("Provider().Lookup(\"libc.so\", \"getentropy\") = %#x, %v; want %#x", providerAddr, err, hostGetentropy)
		}
		return
	}

	requireCCompiler(t)

	dir := t.TempDir()
	preloadSrc := filepath.Join(dir, "interpose.c")
	preloadSO := filepath.Join(dir, "libtipsy_test_interpose.so")
	writeFixtureFile(t, preloadSrc, `
#include <stddef.h>

int tipsy_preload_injected_symbol(void) {
	return 42;
}

int getentropy(void *buffer, size_t length) {
	(void)buffer;
	(void)length;
	return -1;
}
`)
	runFixtureCommand(t, "cc", "-shared", "-fPIC", preloadSrc, "-o", preloadSO)

	existingPreload := os.Getenv("LD_PRELOAD")
	preloadVal := preloadSO
	if existingPreload != "" {
		preloadVal = preloadSO + ":" + existingPreload
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHostDlsymIgnoresLDPreloadInterpositionWithoutScrubbingEnv$")
	cmd.Env = append(os.Environ(),
		preloadHelperEnv+"="+preloadSO,
		"LD_PRELOAD="+preloadVal,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("LD_PRELOAD child test failed: %v\n%s", err, out)
	}
}
