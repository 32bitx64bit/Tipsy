// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fillCommandPipe writes exactly the pipe's capacity so the next byte cannot be
// written and the pipe stays full and unconsumed.
func fillCommandPipe(t *testing.T, writeFD int) {
	t.Helper()
	sz, err := unix.FcntlInt(uintptr(writeFD), unix.F_GETPIPE_SZ, 0)
	if err != nil {
		t.Fatal(err)
	}
	fill := make([]byte, sz)
	for off := 0; off < sz; {
		n, err := unix.Write(writeFD, fill[off:])
		if err != nil {
			t.Fatal(err)
		}
		off += n
	}
}

// TestGameActivityCommandWriteTimeoutOnFullPipe proves WriteCommand returns a
// clean, typed timeout within a bounded deadline when the command pipe is full
// and unconsumed, instead of blocking the launch loop / closeLifecycle forever.
// The pipe is deliberately blocking (no O_NONBLOCK) to prove the bound does not
// rely on the caller having set the flag, and that the flag is restored after.
func TestGameActivityCommandWriteTimeoutOnFullPipe(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	})
	fillCommandPipe(t, fds[1])

	handle, backing := gameActivityHandleForTest(t, fds[0], fds[1])
	w, err := NewGameActivityCommandWriter(handle, 0)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = w.WriteCommand(1)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("write to a full, unconsumed command pipe succeeded")
	}
	var commandErr *GameActivityCommandError
	if !errors.As(err, &commandErr) || commandErr.Stage != GameActivityCommandWrite {
		t.Fatalf("error = %#v, want typed write-stage failure", err)
	}
	if !errors.Is(err, errGameActivityCommandWriteTimeout) {
		t.Fatalf("error = %v, want the clean timeout cause", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("WriteCommand took %v; want a bounded ~%v timeout", elapsed, gameActivityCommandWriteTimeout)
	}
	if elapsed < gameActivityCommandWriteTimeout/2 {
		t.Fatalf("WriteCommand returned in %v; the pipe was likely not full", elapsed)
	}

	// The transient O_NONBLOCK flip must not be left on the shared descriptor.
	flags, err := unix.FcntlInt(uintptr(fds[1]), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.O_NONBLOCK != 0 {
		t.Fatal("WriteCommand permanently set O_NONBLOCK on the command pipe write end")
	}
	runtime.KeepAlive(backing)
}

// TestGameActivityCommandWriteSucceedsAndRestoresFlags proves the bounded write
// still delivers a command on a blocking pipe and leaves the descriptor's flags
// exactly as it found them.
func TestGameActivityCommandWriteSucceedsAndRestoresFlags(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	})
	before, err := unix.FcntlInt(uintptr(fds[1]), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}

	handle, backing := gameActivityHandleForTest(t, fds[0], fds[1])
	w, err := NewGameActivityCommandWriter(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCommand(7); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if n, err := unix.Read(fds[0], got[:]); err != nil || n != 1 || got[0] != 7 {
		t.Fatalf("command read n=%d err=%v value=%v", n, err, got)
	}
	after, err := unix.FcntlInt(uintptr(fds[1]), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("descriptor flags changed: before=%#x after=%#x", before, after)
	}
	runtime.KeepAlive(backing)
}