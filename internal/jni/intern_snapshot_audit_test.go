// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestInternSnapshotImmutablePrefix(t *testing.T) {
	var old *[]*int
	var snapshots []*[]*int
	for i := 1; i <= 4096; i++ {
		v := i
		next, slot := appendInternedSnapshot(old, &v)
		if int(slot) != i || (*next)[slot] == nil {
			t.Fatal("slot mismatch")
		}
		snapshots = append(snapshots, next)
		old = next
	}
	for i, s := range snapshots {
		if len(*s) != i+2 || (*s)[0] != nil {
			t.Fatal("old length changed")
		}
		for j := 1; j < len(*s); j++ {
			if *(*s)[j] != j {
				t.Fatal("published slot changed")
			}
		}
	}
}

func TestInternSnapshotConcurrentPublication(t *testing.T) {
	var tab atomic.Pointer[[]*int]
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p := tab.Load()
				if p == nil {
					continue
				}
				for j := 1; j < len(*p); j++ {
					if (*p)[j] == nil || *(*p)[j] != j {
						panic("torn intern snapshot")
					}
				}
			}
		}()
	}
	for i := 1; i <= 4096; i++ {
		v := i
		next, _ := appendInternedSnapshot(tab.Load(), &v)
		tab.Store(next)
	}
	close(stop)
	wg.Wait()
}

var auditInternSink *[]*int

func BenchmarkAuditInternAppend4096(b *testing.B) {
	info := new(int)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		var tab *[]*int
		for i := 0; i < 4096; i++ {
			tab, _ = appendInternedSnapshot(tab, info)
		}
		auditInternSink = tab
	}
}
func BenchmarkAuditInternCopy4096(b *testing.B) {
	info := new(int)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		var tab *[]*int
		for i := 0; i < 4096; i++ {
			size := 1
			if tab != nil {
				size = len(*tab)
			}
			next := make([]*int, size+1)
			if tab != nil {
				copy(next, *tab)
			}
			next[size] = info
			tab = &next
		}
		auditInternSink = tab
	}
}
