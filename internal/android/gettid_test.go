// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import "testing"

func TestGettidUsesTLSCache(t *testing.T) {
	if rc := testGettidSameThread(); rc != 0 {
		t.Fatalf("same-thread gettid rc=%d", rc)
	}
}

func TestGettidTwoThreads(t *testing.T) {
	if rc := testGettidTwoThreads(); rc != 0 {
		t.Fatalf("two-thread gettid rc=%d", rc)
	}
}

func TestGettidAtforkChild(t *testing.T) {
	if rc := testGettidAtforkChild(); rc != 0 {
		t.Fatalf("at-fork child gettid rc=%d", rc)
	}
}

func TestGettidCachedCheaperThanSyscall(t *testing.T) {
	const n = 200000
	_ = testGettid()
	cached := testGettidNS(n, true)
	raw := testGettidNS(n, false)
	if cached < 0 || raw < 0 {
		t.Fatalf("gettid timing cached=%d raw=%d", cached, raw)
	}
	t.Logf("gettid n=%d cached=%d ns (%.2f ns/op) syscall=%d ns (%.2f ns/op)",
		n, cached, float64(cached)/float64(n), raw, float64(raw)/float64(n))
}

func BenchmarkGettid(b *testing.B) {
	b.Run("cached", func(b *testing.B) {
		_ = testGettid()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			testGettid()
		}
	})
	b.Run("syscall", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			testGettidSys()
		}
	})
}
