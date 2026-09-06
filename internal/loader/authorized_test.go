// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/integrity"
	"golang.org/x/sys/unix"
)

func pinnedNative(t *testing.T, dir, soname string, raw []byte) integrity.NativeDescriptor {
	t.Helper()
	actual := filepath.Join(dir, soname)
	if err := os.WriteFile(actual, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(actual, 0o400); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(actual)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	digestText := hex.EncodeToString(digest[:])
	record := integrity.FileRecord{
		Path:       "lib/x86_64/" + soname,
		Size:       int64(len(raw)),
		SHA256:     digestText,
		APKEntry:   "lib/x86_64/" + soname,
		APKDigest:  strings.Repeat("a", sha256.Size*2),
		Executable: true,
	}
	pinned := &integrity.PinnedFile{
		File:   file,
		Record: record,
		Identity: integrity.FileIdentity{
			Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size, Mode: stat.Mode,
			UID: stat.Uid, Links: uint64(stat.Nlink), MTimeSec: stat.Mtim.Sec, MTimeNsec: stat.Mtim.Nsec,
			CTimeSec: stat.Ctim.Sec, CTimeNsec: stat.Ctim.Nsec,
		},
	}
	return integrity.NativeDescriptor{
		SONAME: soname, APKEntry: record.APKEntry, APKDigest: record.APKDigest,
		ExpectedSHA256: digestText, ExpectedSize: int64(len(raw)), File: pinned,
	}
}

func boundNativeSet(t *testing.T, descriptors ...integrity.NativeDescriptor) *integrity.NativeDescriptorSet {
	return boundNativeSetWithID(t, strings.Repeat("b", sha256.Size*2), descriptors...)
}

func boundNativeSetWithID(t *testing.T, id string, descriptors ...integrity.NativeDescriptor) *integrity.NativeDescriptorSet {
	t.Helper()
	generation := &integrity.Generation{
		ID: id, InventorySHA256: id,
		Files: make(map[string]*integrity.PinnedFile, len(descriptors)),
	}
	for _, descriptor := range descriptors {
		generation.Inventory.Files = append(generation.Inventory.Files, descriptor.File.Record)
		generation.Files[descriptor.File.Record.Path] = descriptor.File
	}
	set, err := generation.NativeDescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestOpenFDRequiresBoundDescriptorSet(t *testing.T) {
	if _, err := OpenFD(context.Background(), "libroblox.so", nil, mapRes{}); err == nil || !strings.Contains(err.Error(), "descriptor set is required") {
		t.Fatalf("nil descriptor-set error = %v", err)
	}
}

func TestOpenFDCallerCannotSpliceReturnedDescriptorCopy(t *testing.T) {
	requireSyntheticPage(t)
	root := pinnedNative(t, t.TempDir(), "libmain.so", buildSynthELF(synthOpts{}))
	set := boundNativeSet(t, root)
	foreign := pinnedNative(t, t.TempDir(), "libforeign.so", buildSynthELF(synthOpts{}))
	foreignSet := boundNativeSetWithID(t, strings.Repeat("c", sha256.Size*2), foreign)
	foreignDescriptors, err := foreignSet.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	copyOfSet, err := set.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	copyOfSet[0].GenerationID = foreignSet.GenerationID()
	copyOfSet = append(copyOfSet, foreignDescriptors[0])

	m, err := OpenFD(context.Background(), "libmain.so", set, mapRes{})
	if err != nil {
		t.Fatalf("caller mutation changed sealed descriptor set: %v", err)
	}
	defer m.Close()
	if len(m.authorized.ordered) != 1 || m.authorized.bySONAME["libforeign.so"] != nil {
		t.Fatal("caller spliced a foreign generation into Loader membership")
	}
}

func requireSyntheticPage(t *testing.T) {
	t.Helper()
	if syscall.Getpagesize() != synthPage {
		t.Skip("test ELF layout assumes 4KiB pages")
	}
}

func TestOpenFDMapsClosedAuthorizedDependencySet(t *testing.T) {
	requireSyntheticPage(t)
	dir := t.TempDir()
	dep := pinnedNative(t, dir, "libdep.so", buildSynthELF(synthOpts{}))
	root := pinnedNative(t, dir, "libmain.so", buildSynthELF(synthOpts{
		needed: []string{"libdep.so"}, imports: []string{"JNI_OnLoad"},
	}))

	set := boundNativeSet(t, dep, root)
	m, err := OpenFD(context.Background(), "libmain.so", set, mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(m.deps) != 1 || m.deps[0].Path != "libdep.so" {
		t.Fatalf("authorized dependencies = %#v", m.deps)
	}
	got, err := m.peek64(vaGlobSlot)
	if err != nil {
		t.Fatal(err)
	}
	want, err := m.deps[0].Lookup("JNI_OnLoad")
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(want) {
		t.Fatalf("resolved address %#x want authorized dependency %#x", got, want)
	}
	if m.authorized == nil || len(m.authorized.ordered) != 2 {
		t.Fatal("loader did not retain the closed authorized descriptor set")
	}
	if m.authorized.generationID != set.GenerationID() || m.authorized.inventorySHA256 != set.InventorySHA256() {
		t.Fatal("loader discarded the descriptor-set generation binding")
	}
	for _, file := range m.authorized.ordered {
		if file.duplicate == nil || file.duplicate.Fd() == file.descriptor.File.File.Fd() {
			t.Fatal("loader did not own a distinct live duplicate")
		}
	}
}

func TestOpenFDNeverFallsBackToAdjacentPath(t *testing.T) {
	requireSyntheticPage(t)
	dir := t.TempDir()
	_ = pinnedNative(t, dir, "libdep.so", buildSynthELF(synthOpts{}))
	root := pinnedNative(t, dir, "libmain.so", buildSynthELF(synthOpts{needed: []string{"libdep.so"}}))

	_, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, root), mapRes{})
	if err == nil || !strings.Contains(err.Error(), "not in the authorized descriptor set") {
		t.Fatalf("OpenFD error = %v, want closed-set dependency rejection", err)
	}
}

func TestOpenFDRejectsPathReplacement(t *testing.T) {
	requireSyntheticPage(t)
	dir := t.TempDir()
	raw := buildSynthELF(synthOpts{})
	descriptor := pinnedNative(t, dir, "libmain.so", raw)
	actual := filepath.Join(dir, "libmain.so")
	if err := os.Rename(actual, filepath.Join(dir, "original-renamed.so")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, []byte("path substitution"), 0o400); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), mapRes{}); err == nil {
		t.Fatal("OpenFD accepted a replaced authenticated path")
	}
}

func TestOpenFDRejectsMutationAndTruncation(t *testing.T) {
	requireSyntheticPage(t)
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "write", mutate: func(t *testing.T, name string) {
			if err := os.Chmod(name, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(name, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteAt([]byte{0}, vaInit); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			_ = file.Close()
		}},
		{name: "truncate", mutate: func(t *testing.T, name string) {
			if err := os.Chmod(name, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(name, 128); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			descriptor := pinnedNative(t, dir, "libmain.so", buildSynthELF(synthOpts{}))
			test.mutate(t, filepath.Join(dir, "libmain.so"))
			if _, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), mapRes{}); err == nil {
				t.Fatal("OpenFD accepted changed authenticated content")
			}
		})
	}
}

func TestOpenFDRejectsClosedOrReusedSourceDescriptor(t *testing.T) {
	requireSyntheticPage(t)
	dir := t.TempDir()
	descriptor := pinnedNative(t, dir, "libmain.so", buildSynthELF(synthOpts{}))
	set := boundNativeSet(t, descriptor)
	if err := descriptor.File.File.Close(); err != nil {
		t.Fatal(err)
	}
	// Opening another file commonly reuses the numeric descriptor. The os.File
	// state still marks the authenticated authority closed, so reuse cannot
	// redirect Dup to an unrelated file.
	replacement, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, err := OpenFD(context.Background(), "libmain.so", set, mapRes{}); err == nil {
		t.Fatal("OpenFD accepted a closed/reused authenticated descriptor")
	}
}

func TestOpenFDRejectsBoundDependencySubstitution(t *testing.T) {
	requireSyntheticPage(t)
	dir := t.TempDir()
	root := pinnedNative(t, dir, "libmain.so", buildSynthELF(synthOpts{needed: []string{"libdep.so"}}))
	substitute := pinnedNative(t, dir, "libother.so", buildSynthELF(synthOpts{}))
	if _, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, root, substitute), mapRes{}); err == nil || !strings.Contains(err.Error(), "libdep.so is not in the authorized descriptor set") {
		t.Fatalf("dependency substitution error = %v", err)
	}
}

func TestOpenFDRejectsSameDescriptorUnderDifferentSONAMEs(t *testing.T) {
	requireSyntheticPage(t)
	root := pinnedNative(t, t.TempDir(), "libmain.so", buildSynthELF(synthOpts{}))
	alias := root
	alias.SONAME = "libalias.so"
	alias.APKEntry = "lib/x86_64/libalias.so"
	alias.File = &integrity.PinnedFile{
		File:     root.File.File,
		Identity: root.File.Identity,
		Record:   root.File.Record,
	}
	alias.File.Record.Path = "lib/x86_64/libalias.so"
	alias.File.Record.APKEntry = alias.APKEntry
	if _, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, root, alias), mapRes{}); err == nil || !strings.Contains(err.Error(), "name the same file") {
		t.Fatalf("duplicate file identity error = %v", err)
	}
}

func TestOpenFDRechecksAfterMapAndBeforeConstructors(t *testing.T) {
	requireSyntheticPage(t)
	dir := t.TempDir()
	descriptor := pinnedNative(t, dir, "libmain.so", buildSynthELF(synthOpts{withAPS2: true}))
	m, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	actual := filepath.Join(dir, "libmain.so")
	if err := os.Chmod(actual, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(actual, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0}, vaInit); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	_ = file.Close()
	if err := m.Init(); err == nil || !strings.Contains(err.Error(), "changed before constructors") {
		t.Fatalf("Init error = %v, want post-map mutation rejection", err)
	}
	guard, err := m.peek64(vaGuard)
	if err != nil {
		t.Fatal(err)
	}
	if guard != 0 {
		t.Fatalf("constructor ran before verification; guard=%#x", guard)
	}
}

func TestOpenFDRequiresGenerationLifetimeThroughConstructors(t *testing.T) {
	requireSyntheticPage(t)
	descriptor := pinnedNative(t, t.TempDir(), "libmain.so", buildSynthELF(synthOpts{withAPS2: true}))
	m, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := descriptor.File.File.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Init(); err == nil || !strings.Contains(err.Error(), "changed before constructors") {
		t.Fatalf("Init error = %v, want closed generation rejection", err)
	}
	guard, err := m.peek64(vaGuard)
	if err != nil {
		t.Fatal(err)
	}
	if guard != 0 {
		t.Fatalf("constructor ran after generation owner closed; guard=%#x", guard)
	}
}

func TestOpenFDOwnsDuplicatesUntilModuleClose(t *testing.T) {
	requireSyntheticPage(t)
	descriptor := pinnedNative(t, t.TempDir(), "libmain.so", buildSynthELF(synthOpts{}))
	m, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), mapRes{})
	if err != nil {
		t.Fatal(err)
	}
	fd := m.authorized.ordered[0].duplicate.Fd()
	if _, err := unix.FcntlInt(fd, unix.F_GETFD, 0); err != nil {
		t.Fatalf("loader duplicate not live: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(fd, unix.F_GETFD, 0); err == nil {
		t.Fatal("loader duplicate remained open after Module.Close")
	}
	if _, err := unix.FcntlInt(descriptor.File.File.Fd(), unix.F_GETFD, 0); err != nil {
		t.Fatalf("Module.Close closed caller-owned descriptor: %v", err)
	}
}

func TestOpenFDRejectsMissingRootAndCanceledContext(t *testing.T) {
	requireSyntheticPage(t)
	descriptor := pinnedNative(t, t.TempDir(), "libdep.so", buildSynthELF(synthOpts{}))
	if _, err := OpenFD(context.Background(), "libmain.so", boundNativeSet(t, descriptor), mapRes{}); err == nil || !strings.Contains(err.Error(), "root libmain.so is missing") {
		t.Fatalf("missing root error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenFD(ctx, "libdep.so", boundNativeSet(t, descriptor), mapRes{}); err == nil {
		t.Fatal("OpenFD ignored canceled context")
	}
}
