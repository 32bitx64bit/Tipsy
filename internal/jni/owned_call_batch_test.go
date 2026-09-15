// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64 && tipsy_input_batch

package jni

import (
	"runtime"
	"testing"
)

// attachOwnedController detaches any current binding on this OS thread and
// attaches a fresh ordinary (non-Main) environment. The caller must already
// hold its OS thread and must detach when done.
func attachOwnedController(t *testing.T) uintptr {
	t.Helper()
	_ = detachCurrentThreadForTest()
	rc, env := attachCurrentThreadForTest(false)
	if rc != testJNIOK || env == nil {
		t.Fatalf("controller Attach = (%d, %p)", rc, env)
	}
	return uintptr(env)
}

func TestOwnedCallDirectInlineWithGoCallback(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	env := attachOwnedController(t)
	defer detachCurrentThreadForTest()

	got := runOwnedProbeForTest(env)
	if got.rc != 41 {
		t.Fatalf("direct run_owned = %d, want 41", got.rc)
	}
	if got.envArg != env || got.seenCurrent != env {
		t.Fatalf("guest saw env=%#x current=%#x, want both %#x", got.envArg, got.seenCurrent, env)
	}
	if got.seenOnMain {
		t.Fatal("direct call on controller thread reported native Main")
	}
	if got.goCalls != 1 || got.guestGoCalls != 1 {
		t.Fatalf("Go callback count = %d/%d, want 1/1", got.goCalls, got.guestGoCalls)
	}
	if got.nestedRC != 77 || !got.nestedCurrent {
		t.Fatalf("nested re-entry rc=%d current_ok=%v, want 77/true", got.nestedRC, got.nestedCurrent)
	}
}

func TestOwnedCallViaNativeMain(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	mainEnv := uintptr(vm.Env().raw)
	if mainEnv == 0 {
		t.Fatal("C Main JNIEnv is nil")
	}
	env := attachOwnedController(t)
	defer detachCurrentThreadForTest()
	if env == mainEnv {
		t.Fatal("controller attach returned the Main env")
	}

	got := runOwnedProbeForTest(mainEnv)
	if got.rc != 41 {
		t.Fatalf("Main-routed run_owned = %d, want 41", got.rc)
	}
	if got.envArg != mainEnv || got.seenCurrent != mainEnv {
		t.Fatalf("guest saw env=%#x current=%#x, want both Main %#x", got.envArg, got.seenCurrent, mainEnv)
	}
	if !got.seenOnMain {
		t.Fatal("Main-routed guest did not observe native Main")
	}
	if got.goCalls != 1 || got.guestGoCalls != 1 {
		t.Fatalf("Go callback count = %d/%d, want 1/1", got.goCalls, got.guestGoCalls)
	}
	if got.nestedRC != 77 || !got.nestedCurrent {
		t.Fatalf("nested Main re-entry rc=%d current_ok=%v, want 77/true", got.nestedRC, got.nestedCurrent)
	}
}

func TestOwnedCallForeignEnvFailsClosed(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	_ = attachOwnedController(t)
	defer detachCurrentThreadForTest()

	ready := make(chan uintptr, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_ = detachCurrentThreadForTest()
		rc, peer := attachCurrentThreadForTest(false)
		if rc != testJNIOK || peer == nil {
			ready <- 0
			<-release
			close(done)
			return
		}
		ready <- uintptr(peer)
		<-release
		detachCurrentThreadForTest()
		close(done)
	}()
	peer := <-ready
	if peer == 0 {
		close(release)
		<-done
		t.Fatal("peer attach failed")
	}
	defer func() { close(release); <-done }()

	got := runOwnedProbeForTest(peer)
	if got.rc != ownedJNIErr {
		t.Fatalf("foreign-env run_owned = %d, want JNI_ERR", got.rc)
	}
	if got.goCalls != 0 || got.guestGoCalls != 0 {
		t.Fatal("foreign-env call reached the guest")
	}
}

func TestNativeInputBatchValidationGates(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := NewVM(); err != nil {
		t.Fatal(err)
	}
	env := attachOwnedController(t)
	defer detachCurrentThreadForTest()

	// Empty batches succeed without invoking any guest.
	if done, locked, status := executeNativeInputBatch(env, nil); done != 0 || locked || status != ownedBatchOK {
		t.Fatalf("empty batch = (%d, %v, %d), want (0, false, OK)", done, locked, status)
	}
	// The Go wrapper rejects oversized batches before crossing into C.
	huge := make([]nativeInputCommand, ownedBatchMax+1)
	if _, _, status := executeNativeInputBatch(env, huge); status != ownedBatchInvalid {
		t.Fatalf("65-command batch status = %d, want INVALID", status)
	}
	// Unknown kinds never reach a guest function pointer.
	bad := []nativeInputCommand{{kind: 0, fn: 0x55, class: 0x55}}
	if done, _, status := executeNativeInputBatch(env, bad); status != ownedBatchInvalid || done != 0 {
		t.Fatalf("bad-kind batch = (%d, %d), want (0, INVALID)", done, status)
	}
	// Lock-state reads are immediate barriers, never grouped behind moves.
	pair := []nativeInputCommand{
		{kind: ownedBatchMouseLocked, fn: 0x55, class: 0x55},
		{kind: ownedBatchMouseLocked, fn: 0x55, class: 0x55},
	}
	if done, _, status := executeNativeInputBatch(env, pair); status != ownedBatchInvalid || done != 0 {
		t.Fatalf("grouped lock-state batch = (%d, %d), want (0, INVALID)", done, status)
	}
}
