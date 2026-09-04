// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/loader"
)

// These tests exercise the exact production path the engine takes:
// Provider().Lookup (named GOT/PLT resolution, no version-pinned vaddrs)
// followed by a SysV call through loader.CallP8. Path arguments live in
// mmap'd memory (never Go heap) so no cgo pointer discipline is involved.

// atFDCWD is Linux AT_FDCWD (-100, fcntl.h). syscall exposes it on Linux
// only as unexported _AT_FDCWD, so the test names the value directly. It is
// a var (not const) so uintptr() conversion wraps instead of overflowing.
var atFDCWD = int64(-100)

// withCABundleOverride pins the shim bundle for one test and releases it.
func withCABundleOverride(t *testing.T, p string) {
	t.Helper()
	SetCABundlePath(p)
	t.Cleanup(ResetCABundlePath)
}

// shimAddr resolves one bionic file-open entry through the production table.
func shimAddr(t *testing.T, sym string) uintptr {
	t.Helper()
	a, err := Provider().Lookup("libc.so", sym)
	if err != nil || a == 0 {
		t.Fatalf("Lookup libc.so %s: p=%#x err=%v", sym, a, err)
	}
	return a
}

// mmapCString places a NUL-terminated copy of s in anonymous memory and
// returns its address. The memory is unmapped on test cleanup; every
// CallP8 invocation is synchronous, so no use-after-free is possible.
func mmapCString(t *testing.T, s string) uintptr {
	t.Helper()
	page := syscall.Getpagesize()
	if len(s)+1 > page {
		t.Fatalf("test path too long: %q", s)
	}
	mem, err := syscall.Mmap(-1, 0, page, syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(mem) })
	copy(mem, s)
	mem[len(s)] = 0
	return uintptr(unsafe.Pointer(&mem[0]))
}

func mmapBuffer(t *testing.T, n int) ([]byte, uintptr) {
	t.Helper()
	page := syscall.Getpagesize()
	size := (n + page - 1) &^ (page - 1)
	mem, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(mem) })
	return mem[:n], uintptr(unsafe.Pointer(&mem[0]))
}

// callShim invokes a shim entry through the production CallP8 trampoline.
// The trampoline returns int64, but the wrapped libc entries return int, so
// the low 32 bits must be sign-extended (a failing open yields RAX
// 0xFFFFFFFF, i.e. int64 4294967295 without the conversion).
func callShim(fn uintptr, args ...uintptr) int {
	tArgs := make([]uintptr, 8)
	copy(tArgs, args)
	return int(int32(loader.CallP8(fn, tArgs[0], tArgs[1], tArgs[2], tArgs[3], tArgs[4], tArgs[5], tArgs[6], tArgs[7])))
}

// requireNoTreeCACert skips if the test process CWD unexpectedly contains the
// relative engine path (redirect tests need it absent from host CWD).
func requireNoTreeCACert(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(cacertRelativePath); err == nil {
		t.Skipf("%s exists in test CWD; refusing to run redirect test here", cacertRelativePath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", cacertRelativePath, err)
	}
}

func checkCWDUnchanged(t *testing.T, before string) {
	t.Helper()
	after, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if after != before {
		t.Fatalf("CWD changed: before=%q after=%q", before, after)
	}
}

func readFD(t *testing.T, fd int) []byte {
	t.Helper()
	f := os.NewFile(uintptr(fd), "tipsy-test-fd")
	if f == nil {
		t.Fatal("os.NewFile returned nil")
	}
	b, err := io.ReadAll(f)
	if err != nil {
		_ = f.Close()
		t.Fatalf("read redirected fd: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close redirected fd: %v", err)
	}
	return b
}

// writeBundle creates a bundle file with known bytes and pins the override.
func writeBundle(t *testing.T, content string) {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "cacert.pem")
	if err := os.WriteFile(bundle, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	withCABundleOverride(t, bundle)
}

func TestCACertMappedInputIsExact(t *testing.T) {
	if cacertRelativePath != "./exe/cacert.pem" {
		t.Fatalf("mapped input=%q, want exactly ./exe/cacert.pem", cacertRelativePath)
	}
}

func TestDefaultCABundlePathDerivesFromConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	want := filepath.Join(root, "tipsy", "app-data", "com.roblox.client", "files", "exe", "cacert.pem")
	if got := defaultCABundlePath(); got != want {
		t.Fatalf("defaultCABundlePath()=%q want %q", got, want)
	}
}

func TestCACertBundlePathOverrideRoundTrip(t *testing.T) {
	withCABundleOverride(t, "/tmp/tipsy-test-override/cacert.pem")
	if got := cacertBundlePath(); got != "/tmp/tipsy-test-override/cacert.pem" {
		t.Fatalf("override path=%q", got)
	}
	ResetCABundlePath()
	if got, want := cacertBundlePath(), defaultCABundlePath(); got != want {
		t.Fatalf("after reset path=%q want default %q", got, want)
	}
}

func TestTipsyOpenRedirectsExactCACertPath(t *testing.T) {
	requireNoTreeCACert(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, "official APK CA bytes")

	fd := callShim(shimAddr(t, "open"), mmapCString(t, cacertRelativePath),
		uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatalf("shim open(%q) failed", cacertRelativePath)
	}
	if got := string(readFD(t, fd)); got != "official APK CA bytes" {
		t.Fatalf("redirected content=%q", got)
	}
	checkCWDUnchanged(t, cwd)
}

func TestTipsyFortifyOpen2Redirects(t *testing.T) {
	requireNoTreeCACert(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, "fortify bundle")

	fd := callShim(shimAddr(t, "__open_2"), mmapCString(t, cacertRelativePath),
		uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatalf("shim __open_2(%q) failed", cacertRelativePath)
	}
	if got := string(readFD(t, fd)); got != "fortify bundle" {
		t.Fatalf("redirected content=%q", got)
	}
	checkCWDUnchanged(t, cwd)
}

func TestTipsyOpenLeavesOtherPathsAlone(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, "unused bundle")

	// Absolute non-matching path passes through with content intact.
	plain := filepath.Join(t.TempDir(), "other.pem")
	if err := os.WriteFile(plain, []byte("plain bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd := callShim(shimAddr(t, "open"), mmapCString(t, plain),
		uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatalf("passthrough open(%q) failed", plain)
	}
	if got := string(readFD(t, fd)); got != "plain bytes" {
		t.Fatalf("passthrough content=%q", got)
	}

	// Near-miss relative inputs are NOT redirected: the shim reports
	// failure, and the Go side proves the same input is genuinely ENOENT.
	for _, miss := range []string{
		"exe/cacert.pem",    // bare form never observed; must stay untouched
		"./exe/cacert.pem/", // trailing slash
		"./exe/cacert.pem.bak",
		"./EXE/cacert.pem",   // case differs
		"./exe/./cacert.pem", // unnormalized
	} {
		got := callShim(shimAddr(t, "open"), mmapCString(t, miss),
			uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0, 0)
		if got >= 0 {
			_ = syscall.Close(got)
			t.Fatalf("near-miss %q was redirected (fd=%d)", miss, got)
		}
		if _, gerr := os.ReadFile(miss); !errors.Is(gerr, os.ErrNotExist) {
			t.Fatalf("near-miss %q test precondition: %v", miss, gerr)
		}
	}
	checkCWDUnchanged(t, cwd)
}

func TestTipsyOpenAtDirfdSemantics(t *testing.T) {
	requireNoTreeCACert(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, "openat bundle")
	openat := shimAddr(t, "openat")
	atFDCWD := uintptr(atFDCWD)

	// AT_FDCWD + exact input redirects.
	fd := callShim(openat, atFDCWD, mmapCString(t, cacertRelativePath),
		uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatalf("openat(AT_FDCWD, %q) failed", cacertRelativePath)
	}
	if got := string(readFD(t, fd)); got != "openat bundle" {
		t.Fatalf("openat redirected content=%q", got)
	}

	// Explicit dirfd + exact input does NOT redirect: dirfd wins, and the
	// entry is absent under that directory.
	emptydir := t.TempDir()
	df, err := os.Open(emptydir)
	if err != nil {
		t.Fatal(err)
	}
	defer df.Close()
	got := callShim(openat, df.Fd(), mmapCString(t, cacertRelativePath),
		uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0)
	if got >= 0 {
		_ = syscall.Close(got)
		t.Fatal("openat with explicit dirfd was redirected; dirfd must be preserved")
	}

	// Explicit dirfd + dirfd-relative input resolves against the fd (decoy).
	decoydir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(decoydir, "exe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoydir, "exe", "cacert.pem"), []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}
	ddf, err := os.Open(decoydir)
	if err != nil {
		t.Fatal(err)
	}
	defer ddf.Close()
	fd = callShim(openat, ddf.Fd(), mmapCString(t, "exe/cacert.pem"),
		uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatal("dirfd-relative openat failed")
	}
	if got := string(readFD(t, fd)); got != "decoy" {
		t.Fatalf("dirfd-relative content=%q want decoy", got)
	}
	checkCWDUnchanged(t, cwd)
}

func TestTipsyOpenAt2FortifyRedirects(t *testing.T) {
	requireNoTreeCACert(t)
	writeBundle(t, "openat2 bundle")

	fd := callShim(shimAddr(t, "__openat_2"), uintptr(atFDCWD),
		mmapCString(t, cacertRelativePath), uintptr(syscall.O_RDONLY), 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatalf("shim __openat_2(AT_FDCWD, %q) failed", cacertRelativePath)
	}
	if got := string(readFD(t, fd)); got != "openat2 bundle" {
		t.Fatalf("redirected content=%q", got)
	}
}

func TestTipsyOpenPreservesFlagsAndMode(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, "flag bundle")

	// O_CLOEXEC survives the redirect.
	fd := callShim(shimAddr(t, "open"), mmapCString(t, cacertRelativePath),
		uintptr(syscall.O_RDONLY|syscall.O_CLOEXEC), 0, 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatal("cloexec redirect open failed")
	}
	r1, _, errno := syscall.RawSyscall(syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0)
	_ = syscall.Close(fd)
	if errno != 0 {
		t.Fatalf("fcntl F_GETFD errno=%d", errno)
	}
	if r1&syscall.FD_CLOEXEC == 0 {
		t.Fatal("O_CLOEXEC lost across the redirect")
	}

	// Variadic mode forwards exactly on the passthrough path (O_CREAT).
	mk := filepath.Join(t.TempDir(), "created.pem")
	fd = callShim(shimAddr(t, "open"), mmapCString(t, mk),
		uintptr(syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL), uintptr(0o600), 0, 0, 0, 0, 0)
	if fd < 0 {
		t.Fatal("O_CREAT passthrough open failed")
	}
	_ = syscall.Close(fd)
	st, err := os.Stat(mk)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("created mode=%o want 600", st.Mode().Perm())
	}
	checkCWDUnchanged(t, cwd)
}

func TestTipsyOpenMissingBundleIsHonestENOENT(t *testing.T) {
	requireNoTreeCACert(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(t.TempDir(), "absent-dir", "cacert.pem")
	withCABundleOverride(t, absent)
	if _, serr := os.Stat(absent); !os.IsNotExist(serr) {
		t.Fatalf("test precondition: %v", serr)
	}
	// The same absent absolute path fails with ENOENT on the Go side,
	// proving the condition the shim returns honestly is ENOENT.
	if _, gerr := os.Open(absent); !errors.Is(gerr, os.ErrNotExist) {
		t.Fatalf("absent bundle precondition: %v", gerr)
	}

	atFDCWD := uintptr(atFDCWD)
	calls := []struct {
		sym  string
		args []uintptr
	}{
		{"open", []uintptr{mmapCString(t, cacertRelativePath), uintptr(syscall.O_RDONLY)}},
		{"__open_2", []uintptr{mmapCString(t, cacertRelativePath), uintptr(syscall.O_RDONLY)}},
		{"openat", []uintptr{atFDCWD, mmapCString(t, cacertRelativePath), uintptr(syscall.O_RDONLY)}},
		{"__openat_2", []uintptr{atFDCWD, mmapCString(t, cacertRelativePath), uintptr(syscall.O_RDONLY)}},
	}
	for _, c := range calls {
		a := c.args
		for len(a) < 8 {
			a = append(a, 0)
		}
		got := callShim(shimAddr(t, c.sym), a[0], a[1], a[2], a[3], a[4], a[5], a[6], a[7])
		if got >= 0 {
			_ = syscall.Close(got)
			t.Fatalf("%s with missing bundle succeeded (fd=%d); want honest failure", c.sym, got)
		}
	}

	// fopen with a missing bundle returns NULL.
	stream := loader.CallP8(shimAddr(t, "fopen"), mmapCString(t, cacertRelativePath),
		mmapCString(t, "r"), 0, 0, 0, 0, 0, 0)
	if stream != 0 {
		t.Fatalf("fopen with missing bundle returned %x; want NULL", stream)
	}
	checkCWDUnchanged(t, cwd)
}

func TestTipsyFopenRedirectsExactPath(t *testing.T) {
	requireNoTreeCACert(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	writeBundle(t, "fopen bundle bytes")

	stream := loader.CallP8(shimAddr(t, "fopen"), mmapCString(t, cacertRelativePath),
		mmapCString(t, "r"), 0, 0, 0, 0, 0, 0)
	if stream == 0 {
		t.Fatal("shim fopen redirect returned NULL")
	}
	buf, bufPtr := mmapBuffer(t, 1<<16)
	n := callShim(shimAddr(t, "fread"), bufPtr, 1, uintptr(len(buf)), uintptr(stream), 0, 0, 0, 0)
	if rc := callShim(shimAddr(t, "fclose"), uintptr(stream), 0, 0, 0, 0, 0, 0, 0); rc != 0 {
		t.Fatalf("fclose rc=%d", rc)
	}
	got := string(buf[:n])
	if got != "fopen bundle bytes" {
		t.Fatalf("fopen redirected content=%q", got)
	}
	checkCWDUnchanged(t, cwd)
}

func TestFileOpenSymbolsResolveToTipsyShim(t *testing.T) {
	// Every entry must resolve through the Tipsy table. Behavior (redirect
	// works, near-misses fail) proves these are the shim, not host glibc:
	// a host fallback could never remap the exact relative input.
	for _, name := range []string{"open", "openat", "fopen", "__open_2", "__openat_2"} {
		shimAddr(t, name)
	}
}
