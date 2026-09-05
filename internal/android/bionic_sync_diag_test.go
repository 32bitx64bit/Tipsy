// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/loader"
)

func bionicSyncMappedFixture(t *testing.T) (*loader.Module, uintptr, uintptr) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "fixture.c")
	path := filepath.Join(dir, "libroblox.so")
	// An ordinary compiled caller in a privately mmap-loaded ELF, with no
	// tail call: the wrapper must see a return address in this image.
	if err := os.WriteFile(source, []byte("int fixture_call(int (*fn)(void)) { return fn(); }\nint fixture_data;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cc", "-shared", "-fPIC", "-nostdlib", "-O2", "-fno-optimize-sibling-calls", "-o", path, source)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	mod, err := loader.Open(path, Provider())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		UnregisterImage(mod.Base)
		_ = mod.Close()
	})
	entry, err := mod.Lookup("fixture_call")
	if err != nil {
		t.Fatal(err)
	}
	function, err := Provider().Lookup("libc.so", "sched_yield")
	if err != nil {
		t.Fatal(err)
	}
	return mod, entry, function
}

func TestBionicSyncDiagnosticsMappedImageAttribution(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	resetBionicSyncTest(t, true)
	mod, entry, function := bionicSyncMappedFixture(t)
	if testBionicSyncHostDladdr(entry) {
		t.Fatal("fixture unexpectedly visible to host dladdr")
	}
	checkCall := func(name string, threadClass, moduleClass int) {
		t.Helper()
		if rc := testBionicSyncNamedCall(entry, function, name); rc != 0 {
			t.Fatalf("mapped caller=%d", rc)
		}
		stats := BionicSyncSnapshot(true)
		if got := stats.Path(threadClass, moduleClass, BionicSyncSchedYield); got.Calls != 1 {
			t.Fatalf("mmap-loaded %s attribution=%+v; all-origin calls=%d", name, got, stats.Aggregate(BionicSyncSchedYield).Calls)
		}
		if got := stats.Aggregate(BionicSyncSchedYield).Calls; got != 1 {
			t.Fatalf("all-origin calls=%d want=1", got)
		}
	}
	// Keep the same OS thread and exact return address throughout: an earlier
	// unknown, positive, or other-module cache entry must never survive an
	// image registry change, including reuse of the very same load bias.
	checkCall("RBX Worker A", BionicSyncThreadRBXWorker, BionicSyncModuleUnknown)
	RegisterImage(mod.Base, mod.Path)
	checkCall("RBX Worker A", BionicSyncThreadRBXWorker, BionicSyncModuleRoblox)
	checkCall("RBX Worker", BionicSyncThreadRBXWorker, BionicSyncModuleRoblox)
	checkCall("RBX WorkerFake", BionicSyncThreadOther, BionicSyncModuleRoblox)
	checkCall("Main", BionicSyncThreadMain, BionicSyncModuleRoblox)
	RegisterImage(mod.Base, "/fixture/libroblox.so.extra")
	checkCall("RBX Worker B", BionicSyncThreadRBXWorker, BionicSyncModuleOther)
	RegisterImage(mod.Base, "/fixture/libroblox.so")
	checkCall("RBX Worker C", BionicSyncThreadRBXWorker, BionicSyncModuleRoblox)
	UnregisterImage(mod.Base)
	checkCall("RBX Worker C", BionicSyncThreadRBXWorker, BionicSyncModuleUnknown)
	RegisterImage(mod.Base, mod.Path)
	checkCall("RBX Worker C", BionicSyncThreadRBXWorker, BionicSyncModuleRoblox)
}

func TestBionicSyncDiagnosticsUsesOnlyExecutableLoadRanges(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	resetBionicSyncTest(t, true)
	mod, entry, _ := bionicSyncMappedFixture(t)
	RegisterImage(mod.Base, mod.Path)
	if got := testBionicSyncModuleClass(entry); got != BionicSyncModuleRoblox {
		t.Fatalf("code module=%d", got)
	}
	data, err := mod.Lookup("fixture_data")
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []uintptr{0, mod.Base, data} {
		if got := testBionicSyncModuleClass(address); got != BionicSyncModuleUnknown {
			t.Fatalf("non-executable module=%d", got)
		}
	}
	ef, err := elf.Open(mod.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer ef.Close()
	for _, prog := range ef.Progs {
		if prog.Type != elf.PT_LOAD || prog.Flags&elf.PF_X == 0 {
			continue
		}
		start := mod.Base + uintptr(prog.Vaddr)
		end := start + uintptr(prog.Memsz)
		for _, address := range []uintptr{start, end - 1} {
			if got := testBionicSyncModuleClass(address); got != BionicSyncModuleRoblox {
				t.Fatalf("executable boundary module=%d", got)
			}
		}
		if got := testBionicSyncModuleClass(end); got != BionicSyncModuleUnknown {
			t.Fatalf("exclusive executable boundary module=%d", got)
		}
	}
}

func TestBionicSyncDiagnosticsInvalidatesOtherThreadCache(t *testing.T) {
	resetBionicSyncTest(t, true)
	mod, entry, _ := bionicSyncMappedFixture(t)
	requests := make(chan struct{})
	results := make(chan int)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		for range requests {
			results <- testBionicSyncModuleClass(entry)
		}
	}()
	defer func() {
		close(requests)
		<-done
	}()
	check := func(want int) {
		t.Helper()
		requests <- struct{}{}
		if got := <-results; got != want {
			t.Fatalf("other OS thread module=%d want=%d", got, want)
		}
	}
	check(BionicSyncModuleUnknown)
	RegisterImage(mod.Base, mod.Path)
	check(BionicSyncModuleRoblox)
	RegisterImage(mod.Base, "/fixture/other.so")
	check(BionicSyncModuleOther)
	UnregisterImage(mod.Base)
	check(BionicSyncModuleUnknown)
}

func TestBionicSyncDiagnosticsRefreshesRenamedThread(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	resetBionicSyncTest(t, true)
	mod, entry, function := bionicSyncMappedFixture(t)
	RegisterImage(mod.Base, mod.Path)
	if rc := testBionicSyncRenameCall(entry, function); rc != 0 {
		t.Fatalf("rename caller=%d", rc)
	}
	stats := BionicSyncSnapshot(true)
	if got := stats.Aggregate(BionicSyncSchedYield).Calls; got != 129 {
		t.Fatalf("all-origin calls=%d want=129", got)
	}
	if got := stats.Path(BionicSyncThreadRBXWorker, BionicSyncModuleRoblox, BionicSyncSchedYield).Calls; got < 64 {
		t.Fatalf("renamed worker calls=%d; name cache failed to refresh", got)
	}
}

func resetBionicSyncTest(t *testing.T, enabled bool) {
	t.Helper()
	SetBionicSyncDiagnostics(false)
	_ = BionicSyncSnapshot(true)
	testBionicSyncResetTLS()
	SetBionicSyncDiagnostics(enabled)
	t.Cleanup(func() {
		SetBionicSyncDiagnostics(false)
		_ = BionicSyncSnapshot(true)
	})
}

func TestBionicSyncDiagnosticsDefaultOffSkipsClockAndCounters(t *testing.T) {
	resetBionicSyncTest(t, false)
	before := testBionicSyncClockCalls()
	if rc := testBionicSyncMutex(); rc != 0 {
		t.Fatalf("mutex helper=%d", rc)
	}
	if after := testBionicSyncClockCalls(); after != before {
		t.Fatalf("disabled clock calls changed: before=%d after=%d", before, after)
	}
	if got := BionicSyncSnapshot(false).Aggregate(BionicSyncMutexLock); got != (BionicSyncPathStats{}) {
		t.Fatalf("disabled mutex aggregate=%+v", got)
	}
}

func TestBionicSyncDiagnosticsAggregatesActualExports(t *testing.T) {
	resetBionicSyncTest(t, true)
	if rc := testBionicSyncMutex(); rc != 0 {
		t.Fatalf("mutex helper=%d", rc)
	}
	if rc := testBionicSyncCondition(); rc != 0 {
		t.Fatalf("condition helper=%d", rc)
	}
	stats := BionicSyncSnapshot(true)
	if got := stats.Aggregate(BionicSyncMutexLock); got.Calls != 1 || got.Samples != 1 {
		t.Fatalf("mutex lock aggregate=%+v", got)
	}
	if got := stats.Aggregate(BionicSyncMutexTryLock); got.Calls != 1 || got.Contention != 1 || got.Errors != 1 {
		t.Fatalf("mutex try aggregate=%+v", got)
	}
	if got := stats.Aggregate(BionicSyncMutexUnlock); got.Calls != 1 {
		t.Fatalf("mutex unlock aggregate=%+v", got)
	}
	if got := stats.Aggregate(BionicSyncCondSignal); got.Calls != 1 {
		t.Fatalf("condition signal aggregate=%+v", got)
	}
	if got := stats.Aggregate(BionicSyncCondBroadcast); got.Calls != 1 {
		t.Fatalf("condition broadcast aggregate=%+v", got)
	}
	if got := BionicSyncSnapshot(false).Aggregate(BionicSyncMutexLock); got != (BionicSyncPathStats{}) {
		t.Fatalf("reset retained mutex aggregate=%+v", got)
	}
}

func TestBionicSyncSnapshotRaceDoesNotLoseMutexCalls(t *testing.T) {
	resetBionicSyncTest(t, true)
	const workers = 4
	const perWorker = 300
	var done atomic.Bool
	var observed atomic.Uint64
	var snapshotWG sync.WaitGroup
	snapshotWG.Add(1)
	go func() {
		defer snapshotWG.Done()
		for !done.Load() {
			observed.Add(BionicSyncSnapshot(true).Aggregate(BionicSyncMutexLock).Calls)
			runtime.Gosched()
		}
	}()
	var workersWG sync.WaitGroup
	workersWG.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer workersWG.Done()
			for n := 0; n < perWorker; n++ {
				if rc := testBionicSyncMutex(); rc != 0 {
					t.Errorf("mutex helper=%d", rc)
					return
				}
			}
		}()
	}
	workersWG.Wait()
	done.Store(true)
	snapshotWG.Wait()
	observed.Add(BionicSyncSnapshot(true).Aggregate(BionicSyncMutexLock).Calls)
	if got, want := observed.Load(), uint64(workers*perWorker); got != want {
		t.Fatalf("observed mutex calls=%d want=%d", got, want)
	}
}

func TestBionicSyncResolverBindsOnlyWhenOptedIn(t *testing.T) {
	names := []string{
		"pthread_mutex_lock", "pthread_mutex_trylock", "pthread_mutex_timedlock", "pthread_mutex_unlock",
		"pthread_cond_signal", "pthread_cond_broadcast", "pthread_setaffinity_np", "sched_yield",
		"sched_getaffinity", "sched_setaffinity", "nice",
	}
	SetBionicSyncDiagnostics(false)
	for _, name := range names {
		got, err := Provider().Lookup("libc.so", name)
		if err != nil || got == 0 {
			t.Fatalf("disabled Lookup(%s): got=%#x err=%v", name, got, err)
		}
		host := hostDlsym(name)
		if host != 0 && got != host {
			t.Fatalf("disabled Lookup(%s)=%#x want host %#x", name, got, host)
		}
	}
	SetBionicSyncDiagnostics(true)
	t.Cleanup(func() { SetBionicSyncDiagnostics(false) })
	for _, name := range names {
		got, err := Provider().Lookup("libc.so", name)
		if err != nil || got == 0 {
			t.Fatalf("enabled Lookup(%s): got=%#x err=%v", name, got, err)
		}
		host := hostDlsym(name)
		if host != 0 && got == host {
			t.Fatalf("enabled Lookup(%s) remained host %#x", name, got)
		}
	}
}
