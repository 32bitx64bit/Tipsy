// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProviderLookupMemcpy(t *testing.T) {
	r := Provider()
	p, err := r.Lookup("libc.so", "memcpy")
	if err != nil {
		t.Fatalf("Lookup memcpy: %v", err)
	}
	if p == 0 {
		t.Fatal("memcpy address is nil")
	}
}

func TestProviderLookupMissingLogs(t *testing.T) {
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	r := Provider()
	_, err := r.Lookup("libc.so", "this_symbol_does_not_exist_tipsy")
	if err == nil {
		t.Fatal("expected error for missing symbol")
	}
	out := buf.String()
	if !strings.Contains(out, "[android] missing native symbol: this_symbol_does_not_exist_tipsy") {
		t.Fatalf("missing-symbol log not found: %s", out)
	}
}

func TestLibcTableContainsMemcpy(t *testing.T) {
	found := false
	for _, s := range libcSymbols {
		if s == "memcpy" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("libcSymbols should include memcpy")
	}
}

func TestAndroidLookupALooper(t *testing.T) {
	r := Provider()
	p, err := r.Lookup("libandroid.so", "ALooper_pollOnce")
	if err != nil || p == 0 {
		t.Fatalf("ALooper_pollOnce: p=%v err=%v", p, err)
	}
}

func TestEGLPresentationLookupsUseCompatibilityWrappers(t *testing.T) {
	for _, name := range []string{"eglSwapInterval", "eglSwapBuffers"} {
		ours, err := Provider().Lookup("libEGL.so", name)
		if err != nil || ours == 0 {
			t.Fatalf("%s: p=%#x err=%v", name, ours, err)
		}
		host := hostDlsym(name)
		if host != 0 && ours == host {
			t.Fatalf("%s Lookup=%#x is host EGL, want Tipsy compatibility wrapper", name, ours)
		}
		if !testEGLProcIsWrapped(name) {
			t.Fatalf("eglGetProcAddress(%q) did not return Tipsy compatibility wrapper", name)
		}
	}
}

func TestEGLSwapStatsReportsSuccessfulPresentRate(t *testing.T) {
	SetEGLVSync(false)
	testEGLRecordSwap(1_000_000_000)
	testEGLRecordSwap(1_004_000_000)
	testEGLRecordSwap(1_008_000_000)
	got := EGLSwapStats()
	if got.SuccessfulSwaps != 3 || got.Elapsed != 8*time.Millisecond || got.RateFPS != 250 {
		t.Fatalf("EGLSwapStats() = %+v", got)
	}
}

func TestEGLSwapStatsDisabledSkipsHotPath(t *testing.T) {
	SetEGLPresentStats(true)
	resetEGLSwapStats()
	t.Cleanup(func() {
		SetEGLPresentStats(false)
		resetEGLSwapStats()
	})

	SetEGLPresentStats(false)
	if EGLPresentStatsEnabled() {
		t.Fatal("SetEGLPresentStats(false) left stats enabled")
	}
	testEGLNoteSuccessfulSwap()
	got := EGLSwapStats()
	if got.SuccessfulSwaps != 0 {
		t.Fatalf("disabled hot path recorded %+v", got)
	}

	testEGLRecordSwap(1_000_000_000)
	if EGLSwapStats().SuccessfulSwaps != 1 {
		t.Fatal("tipsy_test_egl_record_swap must record while stats are disabled")
	}

	resetEGLSwapStats()
	SetEGLPresentStats(true)
	testEGLNoteSuccessfulSwap()
	testEGLNoteSuccessfulSwap()
	got = EGLSwapStats()
	if got.SuccessfulSwaps != 2 {
		t.Fatalf("enabled swap stats = %+v", got)
	}
}

func TestEGLPresentStatsFollowGraphicsInfoLogger(t *testing.T) {
	t.Cleanup(func() {
		SetEGLPresentStats(false)
		resetEGLSwapStats()
	})
	SetEGLPresentStats(false)
	SetEGLVSync(false)
	if got, want := EGLPresentStatsEnabled(), presentStatsLoggerEnabled(); got != want {
		t.Fatalf("SetEGLVSync present stats enabled=%v, want logger Info gate %v", got, want)
	}
}

func BenchmarkEGLSwapStats(b *testing.B) {
	b.Cleanup(func() {
		SetEGLPresentStats(false)
		resetEGLSwapStats()
	})
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			SetEGLPresentStats(enabled)
			resetEGLSwapStats()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				testEGLNoteSuccessfulSwap()
			}
		})
	}
}

func TestEGLSwapIntervalPolicyVSyncOffForcesZero(t *testing.T) {
	t.Cleanup(func() { SetEGLVSync(false) })
	testEGLRecordSwap(1_000_000_000)
	testEGLRecordSwap(1_004_000_000)
	result, first, second, calls, _ := testEGLSwapIntervalPolicy(false, 1, 1, 0, 1, 0)
	if result != 1 || first != 0 || second != 0 || calls != 1 {
		t.Fatalf("VSync-off result=%d intervals=(%d,%d) calls=%d", result, first, second, calls)
	}
	if got := EGLSwapStats(); got.SuccessfulSwaps != 0 {
		t.Fatalf("accepted interval did not start a fresh presentation epoch: %+v", got)
	}
	if eglVSyncEnabled() {
		t.Fatal("VSync-off test left VSync enabled")
	}
}

func TestEGLSwapIntervalPolicyVSyncOnForcesOne(t *testing.T) {
	t.Cleanup(func() { SetEGLVSync(false) })
	result, first, second, calls, _ := testEGLSwapIntervalPolicy(true, 0, 1, 0, 1, 0)
	if result != 1 || first != 1 || second != 0 || calls != 1 {
		t.Fatalf("VSync-on result=%d intervals=(%d,%d) calls=%d", result, first, second, calls)
	}
}

func TestEGLSwapIntervalPolicyOffFallsBackHonestly(t *testing.T) {
	t.Cleanup(func() { SetEGLVSync(false) })
	const badParameter = 0x300c
	result, first, second, calls, reportedError := testEGLSwapIntervalPolicy(false, 1, 0, badParameter, 1, 0)
	if result != 1 || first != 0 || second != 1 || calls != 2 || reportedError != 0 {
		t.Fatalf("VSync-off fallback result=%d intervals=(%d,%d) calls=%d finalError=%#x", result, first, second, calls, reportedError)
	}
}

func TestEGLSwapIntervalPolicyOnFallsBackHonestly(t *testing.T) {
	t.Cleanup(func() { SetEGLVSync(false) })
	const badParameter = 0x300c
	result, first, second, calls, reportedError := testEGLSwapIntervalPolicy(true, 0, 0, badParameter, 1, 0)
	if result != 1 || first != 1 || second != 0 || calls != 2 || reportedError != 0 {
		t.Fatalf("VSync-on fallback result=%d intervals=(%d,%d) calls=%d finalError=%#x", result, first, second, calls, reportedError)
	}
}

func TestEGLSwapIntervalPolicyLeavesFallbackErrorForClient(t *testing.T) {
	t.Cleanup(func() { SetEGLVSync(false) })
	const (
		badParameter = 0x300c
		badDisplay   = 0x3008
	)
	result, first, second, calls, reportedError := testEGLSwapIntervalPolicy(false, 1, 0, badParameter, 0, badDisplay)
	if result != 0 || first != 0 || second != 1 || calls != 2 || reportedError != badDisplay {
		t.Fatalf("failure result=%d intervals=(%d,%d) calls=%d callerError=%#x", result, first, second, calls, reportedError)
	}
}

func TestEGLSwapIntervalPolicyLeavesDirectErrorForClient(t *testing.T) {
	t.Cleanup(func() { SetEGLVSync(false) })
	const badParameter = 0x300c
	result, first, second, calls, reportedError := testEGLSwapIntervalPolicy(true, 1, 0, badParameter, 1, 0)
	if result != 0 || first != 1 || second != 0 || calls != 1 || reportedError != badParameter {
		t.Fatalf("direct failure result=%d intervals=(%d,%d) calls=%d callerError=%#x", result, first, second, calls, reportedError)
	}
}

func TestBionicCompatSymbols(t *testing.T) {
	r := Provider()
	for _, name := range []string{
		"__strlen_chk", "__strncpy_chk2", "__fwrite_chk", "__strchr_chk",
		"__FD_SET_chk", "__FD_ISSET_chk", "__FD_CLR_chk",
		"__assert2", "__sendto_chk", "__write_chk",
		"__gnu_strerror_r", "__sF", "__stack_chk_guard",
		"__open_2", "__gcov_dump", "sysconf", "fflush", "fwrite",
		"fread", "fprintf", "vfprintf", "fclose", "fileno", "fputc", "fputs",
		"fgets", "fseek", "ftell",
		"getaddrinfo", "freeaddrinfo", "getnameinfo", "gai_strerror",
		"sigaction", "sigaction64", "rt_sigaction", "__rt_sigaction",
	} {
		p, err := r.Lookup("libc.so", name)
		if err != nil || p == 0 {
			t.Fatalf("Lookup libc.so %s: p=%v err=%v", name, p, err)
		}
	}
}

func TestFflushIsWrapper(t *testing.T) {
	ours, err := Provider().Lookup("libc.so", "fflush")
	if err != nil || ours == 0 {
		t.Fatalf("Lookup fflush: p=%v err=%v", ours, err)
	}
	host := hostDlsym("fflush")
	if host != 0 && ours == host {
		t.Fatalf("fflush Lookup=%#x is glibc, want tipsy wrapper", ours)
	}
}

func TestBionicSFFlush(t *testing.T) {
	// Roblox uses bionic FILE size 152: &__sF[1] is not glibc &FILE[1].
	if rc := fflushBionicIndex(1); rc != 0 {
		t.Fatalf("fflush(&__sF[1])=%d", rc)
	}
	if rc := fflushBionicIndex(2); rc != 0 {
		t.Fatalf("fflush(&__sF[2])=%d", rc)
	}
}

func TestBionicSysconfPagesize(t *testing.T) {
	// Bionic _SC_PAGESIZE is 0x27; glibc's is 30. Passing 0x27 through
	// host sysconf returns DELAYTIMER_MAX, not the page size.
	got := Sysconf(0x27)
	if got < 4096 || got&(got-1) != 0 {
		t.Fatalf("bionic sysconf(_SC_PAGESIZE=0x27)=%d, want power-of-two page size", got)
	}
	if Sysconf(0x28) != got {
		t.Fatalf("bionic sysconf(_SC_PAGE_SIZE=0x28)=%d, want %d", Sysconf(0x28), got)
	}
	if Sysconf(0x61) < 1 {
		t.Fatalf("bionic sysconf(_SC_NPROCESSORS_ONLN=0x61)=%d", Sysconf(0x61))
	}
	if Sysconf(0x62) < 1 {
		t.Fatalf("bionic sysconf(_SC_PHYS_PAGES=0x62)=%d", Sysconf(0x62))
	}
	// Unmapped name must not hit a plausible glibc query.
	if Sysconf(0x7fff) != -1 {
		t.Fatalf("unmapped sysconf name should return -1")
	}
}

func TestRegisterDlsym(t *testing.T) {
	want := uintptr(0x1234)
	Register("libfake.so", func(sym string) (uintptr, error) {
		if sym == "foo" {
			return want, nil
		}
		return 0, errMissing
	})
	r := Provider()
	p, err := r.Lookup("libfake.so", "foo")
	if err != nil || p != want {
		t.Fatalf("registered lookup: p=%v err=%v", p, err)
	}
}

func TestALooperForThreadPrepares(t *testing.T) {
	p := looperForThread()
	if p == 0 {
		t.Fatal("ALooper_forThread returned nil; GameActivity initializeNativeCode needs a looper")
	}
	if looperForThread() != p {
		t.Fatal("ALooper_forThread should be stable for the calling thread")
	}
}

func TestALooperPollOnceTimeoutZero(t *testing.T) {
	if looperForThread() == 0 {
		t.Fatal("looper")
	}
	start := time.Now()
	rc := pollOnce(0)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("pollOnce(0) blocked for %s", time.Since(start))
	}
	if rc != -3 && rc != -1 {
		t.Fatalf("pollOnce(0)=%d want TIMEOUT(-3) or WAKE(-1)", rc)
	}
}

func TestPthreadCondWaitIsWrapper(t *testing.T) {
	for _, name := range []string{"pthread_cond_wait", "pthread_cond_timedwait"} {
		ours, err := Provider().Lookup("libc.so", name)
		if err != nil || ours == 0 {
			t.Fatalf("Lookup %s: p=%v err=%v", name, ours, err)
		}
		host := hostDlsym(name)
		if host != 0 && ours == host {
			t.Fatalf("%s Lookup=%#x is glibc, want nested-looper wrapper", name, ours)
		}
	}
}

func TestALooperNestedPollFromCondWait(t *testing.T) {
	if rc := condWaitPollsLooper(); rc != 1 {
		t.Fatalf("cond_wait nested ALooper_pollOnce did not run the fd callback, rc=%d", rc)
	}
}

func TestALooperWakeUnblocksCondWait(t *testing.T) {
	if rc := condWaitWakeUnblocks(); rc != 1 {
		t.Fatalf("ALooper_wake did not unblock tipsy_pthread_cond_wait, rc=%d", rc)
	}
}

func TestALooperLostWakeupCondWait(t *testing.T) {
	if rc := condWaitLostWakeup(); rc != 1 {
		t.Fatalf("lost-wakeup pending_wake did not run the fd callback, rc=%d", rc)
	}
}

func TestALooperWatcherFailureFallsBackToSlices(t *testing.T) {
	if rc := condWaitFallbackWithoutWatcher(); rc != 1 {
		t.Fatalf("cond_wait without watcher did not nest-poll via 16ms slices, rc=%d", rc)
	}
}

func TestALooperWatcherShutdownJoins(t *testing.T) {
	if rc := looperWatcherShutdown(); rc != 1 {
		t.Fatalf("watcher shutdown did not join, rc=%d", rc)
	}
}

func TestNativeMainIdleUnblocksOnWake(t *testing.T) {
	if rc := idleUnblocksOnWake(); rc != 1 {
		t.Fatalf("tipsy_native_main_idle(-1) did not return after ALooper_wake, rc=%d", rc)
	}
}

func TestALooperFutexRealWakeVsLooperWake(t *testing.T) {
	if rc := futexRealWakeVsLooperWake(); rc != 1 {
		t.Fatalf("futex real-wake vs looper-wake, rc=%d", rc)
	}
}

func TestGetaddrinfoIsWrapper(t *testing.T) {
	for _, name := range []string{"getaddrinfo", "freeaddrinfo", "getnameinfo", "gai_strerror"} {
		ours, err := Provider().Lookup("libc.so", name)
		if err != nil || ours == 0 {
			t.Fatalf("Lookup libc.so %s: p=%v err=%v", name, ours, err)
		}
		host := hostDlsym(name)
		if host != 0 && ours == host {
			t.Fatalf("%s Lookup=%#x is glibc, want bionic ABI wrapper", name, ours)
		}
	}
}

func TestBionicGetaddrinfoNumericLoopback(t *testing.T) {
	if rc := getaddrinfoNumericLoopback(); rc != 0 {
		t.Fatalf("bionic getaddrinfo 127.0.0.1:443 rc=%d (want 0; ai_addr must be sockaddr not canonname)", rc)
	}
}

func TestBionicGetaddrinfoAddrconfig(t *testing.T) {
	glibcRC := glibcUntranslatedAddrconfig()
	if glibcRC == 0 {
		t.Fatal("glibc getaddrinfo with untranslated bionic AI_ADDRCONFIG (0x400) + service http unexpectedly succeeded")
	}
	if rc := getaddrinfoBionicAddrconfig(); rc != 0 {
		t.Fatalf("bionic getaddrinfo AI_ADDRCONFIG 127.0.0.1:http rc=%d (glibc untranslated rc=%d)", rc, glibcRC)
	}
}

func TestBionicGetaddrinfoEAINoname(t *testing.T) {
	rc := getaddrinfoEAINoname()
	if rc != 8 {
		t.Fatalf("bionic EAI_NONAME want 8, got %d (glibc uses negative EAI codes)", rc)
	}
}

func TestBionicGetnameinfoNumeric(t *testing.T) {
	if rc := getnameinfoNumeric(); rc != 0 {
		t.Fatalf("bionic getnameinfo NI_NUMERICHOST|NI_NUMERICSERV rc=%d", rc)
	}
}

func TestGlibcAddrinfoBionicAiAddrNull(t *testing.T) {
	rc := glibcBionicAiAddrNull()
	if rc < 0 {
		t.Fatalf("glibc getaddrinfo 127.0.0.1 failed rc=%d", rc)
	}
	if rc != 1 {
		t.Fatal("host libc addrinfo already matches bionic field order; wrapper still required for AI_/EAI_ codes")
	}
}

func TestSigactionIsWrapper(t *testing.T) {
	for _, name := range []string{"sigaction", "sigaction64", "rt_sigaction", "__rt_sigaction"} {
		ours, err := Provider().Lookup("libc.so", name)
		if err != nil || ours == 0 {
			t.Fatalf("Lookup libc.so %s: p=%v err=%v", name, ours, err)
		}
		if name == "sigaction" {
			host := hostDlsym(name)
			if host != 0 && ours == host {
				t.Fatalf("sigaction Lookup=%#x is glibc, want bionic ABI wrapper", ours)
			}
		}
	}
}

func TestBionicSigactionSize(t *testing.T) {
	if bionicSigactionSize() != 32 {
		t.Fatalf("bionic LP64 sigaction size=%d, want 32", bionicSigactionSize())
	}
	if glibcSigactionSize() < 152 {
		t.Fatalf("glibc sigaction size=%d, want >=152 (the smash)", glibcSigactionSize())
	}
}

func TestGlibcSigactionSmashesBionicAct(t *testing.T) {
	rc := glibcSigactionSmashesBionicAct()
	if rc < 0 {
		t.Fatalf("glibc sigaction(SIGPIPE) query failed rc=%d", rc)
	}
	if rc != 1 {
		t.Fatal("host libc sigaction already writes 32 bytes; wrapper still required for sa_mask/restorer")
	}
}

func TestBionicSigactionQueryCanary(t *testing.T) {
	if rc := bionicSigactionQueryCanary(); rc != 0 {
		t.Fatalf("bionic sigaction(SIGPIPE) query smashed the 32-byte act canary rc=%d", rc)
	}
}

func TestBionicSigactionSetQuery(t *testing.T) {
	if rc := bionicSigactionSetQuery(); rc != 0 {
		t.Fatalf("bionic sigaction set/query SIGUSR2 rc=%d", rc)
	}
}

func TestRtSigactionQueryCanary(t *testing.T) {
	if rc := rtSigactionQueryCanary(); rc != 0 {
		t.Fatalf("rt_sigaction(SIGPIPE) query smashed the kernel-sized act canary rc=%d", rc)
	}
}

func TestDlIteratePhdr(t *testing.T) {
	n := dlIterateCount()
	if n < 1 {
		t.Fatalf("dl_iterate_phdr visited %d objects, want host libc at least", n)
	}
}

func TestProviderFreshCacheLookup(t *testing.T) {
	p := &provider{}
	for i := 0; i < 2; i++ {
		addr, err := p.Lookup("libc.so", "memcpy")
		if err != nil || addr == 0 {
			t.Fatalf("fresh provider Lookup #%d = %#x, %v", i, addr, err)
		}
	}
}

func TestProviderCacheInvalidatedByRegister(t *testing.T) {
	r := Provider()
	if _, err := r.Lookup("libcacheprobe.so", "probe_sym"); err == nil {
		t.Fatal("libcacheprobe.so unexpectedly resolvable before registration")
	}
	Register("libcacheprobe.so", func(sym string) (uintptr, error) {
		if sym == "probe_sym" {
			return 0xcafe, nil
		}
		return 0, errMissing
	})
	got, err := r.Lookup("libcacheprobe.so", "probe_sym")
	if err != nil || got != 0xcafe {
		t.Fatalf("Lookup after Register = %#x, %v; cached miss was not invalidated", got, err)
	}
}

func TestProviderLookupConcurrent(t *testing.T) {
	r := Provider()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch w % 4 {
				case 0:
					if p, err := r.Lookup("libc.so", "memcpy"); err != nil || p == 0 {
						t.Errorf("concurrent Lookup memcpy = %#x, %v", p, err)
						return
					}
				case 1:
					_, _ = r.Lookup("libc.so", "no_such_symbol_cache_race")
				case 2:
					_, _ = r.Lookup("libEGL.so", "eglSwapBuffers")
				default:
					_, _ = r.Lookup("", "no_such_symbol_cache_race")
				}
			}
		}(w)
	}
	wg.Wait()
}
