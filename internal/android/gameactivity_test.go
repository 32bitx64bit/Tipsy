// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"encoding/binary"
	"errors"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func gameActivityHandleForTest(t *testing.T, readFD, writeFD int) (uintptr, []byte) {
	t.Helper()
	buf := make([]byte, 0x158)
	binary.LittleEndian.PutUint32(buf[0x150:], uint32(readFD))
	binary.LittleEndian.PutUint32(buf[0x154:], uint32(writeFD))
	return uintptr(unsafe.Pointer(&buf[0])), buf
}

func TestGameActivityCommandWriterValidatedDescriptor(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	})
	handle, backing := gameActivityHandleForTest(t, fds[0], fds[1])
	w, err := NewGameActivityCommandWriter(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCommand(5); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if n, err := unix.Read(fds[0], got[:]); err != nil || n != 1 || got[0] != 5 {
		t.Fatalf("command read n=%d err=%v value=%v", n, err, got)
	}
	runtime.KeepAlive(backing)
}

func TestGameActivityCommandWriterRejectsMismatchedPipes(t *testing.T) {
	var first, second [2]int
	if err := unix.Pipe2(first[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if err := unix.Pipe2(second[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, fd := range []int{first[0], first[1], second[0], second[1]} {
			_ = unix.Close(fd)
		}
	})
	handle, backing := gameActivityHandleForTest(t, first[0], second[1])
	_, err := NewGameActivityCommandWriter(handle, 0)
	var commandErr *GameActivityCommandError
	if !errors.As(err, &commandErr) || commandErr.Stage != GameActivityCommandTransport {
		t.Fatalf("error = %#v, want typed transport failure", err)
	}
	runtime.KeepAlive(backing)
}

func TestGameActivityCommandWriterRevalidatesBeforeWrite(t *testing.T) {
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[0]) })
	handle, backing := gameActivityHandleForTest(t, fds[0], fds[1])
	w, err := NewGameActivityCommandWriter(handle, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(fds[1]); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCommand(1); err == nil {
		t.Fatal("write through a closed command endpoint succeeded")
	} else {
		var commandErr *GameActivityCommandError
		if !errors.As(err, &commandErr) || commandErr.Stage != GameActivityCommandTransport {
			t.Fatalf("error = %#v, want typed transport failure", err)
		}
	}
	runtime.KeepAlive(backing)
}

func TestGameActivityCommandWriterRejectsInvalidHandle(t *testing.T) {
	_, err := NewGameActivityCommandWriter(0, 0)
	var commandErr *GameActivityCommandError
	if !errors.As(err, &commandErr) || commandErr.Stage != GameActivityCommandHandle {
		t.Fatalf("error = %#v, want typed handle failure", err)
	}
}

func TestGameActivityCommandWriterPrefersNamedAPI(t *testing.T) {
	w, err := NewGameActivityCommandWriter(0x10000, 0x20000)
	if err != nil {
		t.Fatal(err)
	}
	if w.namedWrite != 0x20000 || w.abi != nil {
		t.Fatalf("writer = %#v, want named API without descriptor", w)
	}
}

func TestGameActivityCommandWriterRejectsUnmappedAndOverflowHandles(t *testing.T) {
	mapped, err := unix.Mmap(-1, 0, 2*unix.Getpagesize(), unix.PROT_NONE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Munmap(mapped) })
	guardAddr := uintptr(unsafe.Pointer(&mapped[0]))

	for _, tc := range []struct {
		handle uintptr
		stage  GameActivityCommandStage
	}{
		{0x1000, GameActivityCommandHandle},
		{guardAddr, GameActivityCommandDescriptor},
		{^uintptr(0) - 0x80, GameActivityCommandDescriptor},
	} {
		_, err := NewGameActivityCommandWriter(tc.handle, 0)
		var commandErr *GameActivityCommandError
		if !errors.As(err, &commandErr) || commandErr.Stage != tc.stage {
			t.Fatalf("handle %#x: error = %#v, want stage %q", tc.handle, err, tc.stage)
		}
	}
}

func TestGameActivityCommandWriterEnforcesModuleBounds(t *testing.T) {
	mod, entry, _ := bionicSyncMappedFixture(t)

	// Named write export must reside inside the module's executable segment.
	w, err := NewGameActivityCommandWriter(0x20000, entry, mod)
	if err != nil || w.namedWrite != entry {
		t.Fatalf("valid module entry rejected: w=%#v err=%v", w, err)
	}
	if _, err := NewGameActivityCommandWriter(0x20000, mod.Base+uintptr(mod.Size)+0x1000, mod); err == nil {
		t.Fatal("out-of-module namedWrite accepted")
	}

	// Fallback handle inside executable .text must be rejected without reading code bytes as FDs.
	_, err = NewGameActivityCommandWriter(entry, 0, mod)
	var commandErr *GameActivityCommandError
	if !errors.As(err, &commandErr) || commandErr.Stage != GameActivityCommandHandle {
		t.Fatalf("exec-segment handle: error = %#v, want GameActivityCommandHandle", err)
	}

	// Valid heap handle with live pipe descriptors succeeds when paired with an open module.
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
	})
	handle, backing := gameActivityHandleForTest(t, fds[0], fds[1])
	w2, err := NewGameActivityCommandWriter(handle, 0, mod)
	if err != nil {
		t.Fatalf("heap handle with valid module rejected: %v", err)
	}
	if err := w2.WriteCommand(9); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if n, err := unix.Read(fds[0], got[:]); err != nil || n != 1 || got[0] != 9 {
		t.Fatalf("pipe read n=%d err=%v got=%v", n, err, got)
	}
	runtime.KeepAlive(backing)
}
