// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/loader"
)

func TestVersionedProviderAcceptsExactOwnedCapabilities(t *testing.T) {
	resolver, ok := Provider().(loader.VersionedResolver)
	if !ok {
		t.Fatal("Android Provider does not implement loader.VersionedResolver")
	}
	for _, test := range []struct {
		library string
		symbol  string
		version string
	}{
		{library: "libc.so", symbol: "memcpy", version: "LIBC"},
		{library: "libc.so", symbol: "__fread_chk", version: "LIBC_N"},
		{library: "libc.so", symbol: "__fwrite_chk", version: "LIBC_N"},
		{library: "libc.so", symbol: "__write_chk", version: "LIBC_N"},
		{library: "libc.so", symbol: "in6addr_any", version: "LIBC_N"},
		{library: "libc.so", symbol: "in6addr_loopback", version: "LIBC_N"},
		{library: "libc.so", symbol: "__sendto_chk", version: "LIBC_O"},
		{library: "libm.so", symbol: "sinf", version: "LIBC"},
		{library: "libdl.so", symbol: "dlopen", version: "LIBC"},
	} {
		t.Run(test.library+"/"+test.symbol+"@"+test.version, func(t *testing.T) {
			addr, err := resolver.LookupVersion(test.library, test.symbol, test.version)
			if err != nil || addr == 0 {
				t.Fatalf("LookupVersion() = %#x, %v", addr, err)
			}
		})
	}
}

func TestVersionedProviderRejectsWrongOwnerVersionAndPath(t *testing.T) {
	resolver := Provider().(loader.VersionedResolver)
	for _, test := range []struct {
		name    string
		library string
		symbol  string
		version string
		reason  VersionCapabilityReason
	}{
		{name: "wrong owner libc as libm", library: "libm.so", symbol: "memcpy", version: "LIBC", reason: VersionCapabilityUnknownSymbol},
		{name: "wrong owner libm as libc", library: "libc.so", symbol: "sinf", version: "LIBC", reason: VersionCapabilityUnknownSymbol},
		{name: "wrong base version", library: "libc.so", symbol: "memcpy", version: "LIBC_N", reason: VersionCapabilityWrongVersion},
		{name: "wrong introduction version", library: "libc.so", symbol: "__fread_chk", version: "LIBC", reason: VersionCapabilityWrongVersion},
		{name: "future version", library: "libc.so", symbol: "memcpy", version: "LIBC_FUTURE", reason: VersionCapabilityWrongVersion},
		{name: "normalized path forbidden", library: "/system/lib64/libc.so", symbol: "memcpy", version: "LIBC", reason: VersionCapabilityUnknownSymbol},
		{name: "unknown owner", library: "libother.so", symbol: "memcpy", version: "LIBC", reason: VersionCapabilityUnknownSymbol},
	} {
		t.Run(test.name, func(t *testing.T) {
			addr, err := resolver.LookupVersion(test.library, test.symbol, test.version)
			if addr != 0 || err == nil {
				t.Fatalf("LookupVersion() = %#x, %v; want typed refusal", addr, err)
			}
			var capabilityErr *VersionedSymbolError
			if !errors.As(err, &capabilityErr) || capabilityErr.Reason != test.reason {
				t.Fatalf("LookupVersion() error = %T %v, want reason %q", err, err, test.reason)
			}
		})
	}
}

func TestVersionCapabilityTableResolvesEveryAdvertisedTuple(t *testing.T) {
	resolver := Provider().(loader.VersionedResolver)
	for library, symbols := range versionedSymbolCapabilities {
		for symbol, version := range symbols {
			t.Run(library+"/"+symbol+"@"+version, func(t *testing.T) {
				addr, err := resolver.LookupVersion(library, symbol, version)
				if err != nil || addr == 0 {
					t.Fatalf("advertised tuple did not resolve: addr=%#x err=%v", addr, err)
				}
			})
		}
	}
}

func TestLoaderUsesAndroidVersionedProviderExactly(t *testing.T) {
	requireCCompiler(t)

	t.Run("exact", func(t *testing.T) {
		modulePath := buildVersionedLoaderFixture(t, "libc.so", "LIBC")
		module, err := loader.Open(modulePath, Provider())
		if err != nil {
			t.Fatal(err)
		}
		if err := module.Close(); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name    string
		library string
		version string
	}{
		{name: "wrong owner", library: "libm.so", version: "LIBC"},
		{name: "wrong version", library: "libc.so", version: "LIBC_N"},
	} {
		t.Run(test.name, func(t *testing.T) {
			modulePath := buildVersionedLoaderFixture(t, test.library, test.version)
			_, err := loader.Open(modulePath, Provider())
			var compatibilityErr *loader.VersionCompatibilityError
			if !errors.As(err, &compatibilityErr) {
				t.Fatalf("loader.Open() error = %T %v, want VersionCompatibilityError", err, err)
			}
			if compatibilityErr.Dependency != test.library || compatibilityErr.Symbol != "memcpy" ||
				compatibilityErr.Version != test.version || compatibilityErr.Reason != loader.VersionProviderMissing {
				t.Fatalf("loader.Open() compatibility error = %#v", compatibilityErr)
			}
		})
	}
}

func requireCCompiler(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cc"); err != nil {
		t.Skip("cc is required for ELF provider integration fixture")
	}
}

func buildVersionedLoaderFixture(t *testing.T, library, version string) string {
	t.Helper()
	dir := t.TempDir()
	providerSource := filepath.Join(dir, "provider.c")
	providerMap := filepath.Join(dir, "provider.map")
	providerSO := filepath.Join(dir, library)
	moduleSource := filepath.Join(dir, "module.c")
	moduleSO := filepath.Join(dir, "module.so")

	writeFixtureFile(t, providerSource, `
#include <stddef.h>
void *memcpy(void *destination, const void *source, size_t length) {
	(void)source;
	(void)length;
	return destination;
}
`)
	writeFixtureFile(t, providerMap, fmt.Sprintf("%s { global: memcpy; local: *; };\n", version))
	writeFixtureFile(t, moduleSource, `
#include <stddef.h>
extern void *memcpy(void *, const void *, size_t);
void *tipsy_test_versioned_relocation(void *destination, const void *source, size_t length) {
	return memcpy(destination, source, length);
}
`)
	runFixtureCommand(t, "cc", "-shared", "-fPIC", "-nostdlib", "-fno-builtin", providerSource,
		"-Wl,-soname,"+library, "-Wl,--version-script,"+providerMap, "-o", providerSO)
	runFixtureCommand(t, "cc", "-shared", "-fPIC", "-nostdlib", "-fno-builtin", moduleSource,
		"-Wl,--no-as-needed", "-L"+dir, "-Wl,-rpath,$ORIGIN", "-Wl,-l:"+library, "-o", moduleSO)
	return moduleSO
}

func writeFixtureFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runFixtureCommand(t *testing.T, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}
