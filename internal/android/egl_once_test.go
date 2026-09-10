// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"sync"
	"testing"
)

// TestEnsureEGLInitializesOnce exercises ensure_egl from many goroutines and
// checks that host EGL/GLES resolution ran exactly once (pthread_once caches a
// failed dlopen instead of retrying from every swap).
func TestEnsureEGLInitializesOnce(t *testing.T) {
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				testEGLProcIsWrapped("eglSwapBuffers")
			}
		}()
	}
	wg.Wait()
	if got := testEGLInitCalls(); got != 1 {
		t.Fatalf("EGL host resolution ran %d times, want exactly 1", got)
	}
}
