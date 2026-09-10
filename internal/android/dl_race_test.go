// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"sync"
	"testing"
)

// TestDLErrorConcurrentNoDoubleFree drives setDLError, the C dlerror shim, and
// the exported Go entry point from several goroutines. Before the mutex and
// thread-local ownership fix this raced on dlErr/dlerrorC and could free the
// same string twice.
func TestDLErrorConcurrentNoDoubleFree(t *testing.T) {
	testDlErrorC() // drop any pending message
	const workers = 8
	const iters = 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch w % 3 {
				case 0:
					setDLError("dlopen: concurrent.so: not in Android module registry")
				case 1:
					_ = testDlErrorC()
				default:
					_ = testGoDlErrorAndFree()
				}
			}
		}(w)
	}
	wg.Wait()
	setDLError("")
	if got := testDlErrorC(); got != "" {
		t.Fatalf("dlerror still reports %q after clear", got)
	}
}
