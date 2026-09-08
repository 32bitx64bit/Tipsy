// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

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
