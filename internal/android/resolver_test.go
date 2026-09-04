// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"bytes"
	"log/slog"
	"strings"
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
