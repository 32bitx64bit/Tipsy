// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"bytes"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestEmutlsGetAddressStable(t *testing.T) {
	runtime.LockOSThread()
	type ctl struct {
		size, align, index uint64
		value               uintptr
	}
	var a, b ctl
	a.size, a.align = 8, 8
	b.size, b.align = 16, 8
	p1 := emutlsGet(unsafe.Pointer(&a))
	p2 := emutlsGet(unsafe.Pointer(&a))
	q := emutlsGet(unsafe.Pointer(&b))
	if p1 == 0 || p1 != p2 {
		t.Fatalf("same control: %#x vs %#x", p1, p2)
	}
	if q == 0 || q == p1 {
		t.Fatalf("distinct controls alias: %#x %#x", p1, q)
	}
	if a.index == 0 || b.index == 0 || a.index == b.index {
		t.Fatalf("index a=%d b=%d", a.index, b.index)
	}
}

func TestEmutlsPinsRobloxJNIEnv(t *testing.T) {
	runtime.LockOSThread()
	type ctl struct {
		size, align, index uint64
		value              uintptr
	}
	var c ctl
	c.size, c.align = 8, 8
	bias := uintptr(unsafe.Pointer(&c)) - 0x6fd6120
	emutlsSetBias(bias)
	t.Cleanup(func() { emutlsSetBias(0); emutlsSetJNIFunctions(nil) })

	want := uintptr(0x12340000)
	emutlsSetJNIFunctions(unsafe.Pointer(want))
	p := emutlsGet(unsafe.Pointer(&c))
	got := *(*uintptr)(unsafe.Pointer(p))
	if got != want {
		t.Fatalf("first pin %#x want %#x", got, want)
	}
	*(*uintptr)(unsafe.Pointer(p)) = 0
	p2 := emutlsGet(unsafe.Pointer(&c))
	got = *(*uintptr)(unsafe.Pointer(p2))
	if p2 != p || got != want {
		t.Fatalf("re-pin p=%#x got=%#x want=%#x", p2, got, want)
	}
}

// retiredFiberPredicateSig is the exact 26-byte 2.734.917 pattern at file
// vaddr 0x29d20c0 (push rbp; 2x call 29cfb00; cmp [rax],[rax+0x68];
// setne) that hookLLVMEmutls used to hook to constant-0. §86 core
// forensics proved it is the shared fiber/exception-state predicate
// read by 257 call sites, including the Task constructor gate at
// 0x64c6a07 whose forced-0 caused "Tasks: Task initialized on
// non-fiber" on RBX Worker B. It must never be hooked again.
var retiredFiberPredicateSig = []byte{
	0x55, 0x48, 0x89, 0xe5, 0xe8, 0x37, 0xda, 0xff, 0xff, 0xe8, 0x32, 0xda, 0xff, 0xff,
	0x48, 0x8b, 0x00, 0x48, 0x3b, 0x40, 0x68, 0x0f, 0x95, 0xc0, 0x5d, 0xc3,
}

func TestHookLLVMEmutlsRetiresFiberPredicateHook(t *testing.T) {
	const size = 128
	code, err := syscall.Mmap(-1, 0, size,
		syscall.PROT_READ|syscall.PROT_WRITE|syscall.PROT_EXEC,
		syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		t.Fatalf("mmap: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(code) })

	const (
		emutlsOff = 0
		onceOff   = 32
		predOff   = 64
	)
	copy(code[emutlsOff:], llvmEmutlsSig)
	copy(code[onceOff:], cxaOnceSig)
	copy(code[predOff:], retiredFiberPredicateSig)
	wantPred := append([]byte(nil), code[predOff:predOff+len(retiredFiberPredicateSig)]...)

	base := uintptr(unsafe.Pointer(&code[0]))
	m := &Module{
		bias: base,
		segs: []loadSeg{{
			vaddr:  0,
			memsz:  size,
			filesz: size,
			prot:   syscall.PROT_EXEC,
		}},
	}
	m.hookLLVMEmutls()

	if !bytes.Equal(code[predOff:predOff+len(wantPred)], wantPred) {
		t.Fatalf("fiber predicate site modified: %x", code[predOff:predOff+len(wantPred)])
	}
	if bytes.Equal(code[emutlsOff:emutlsOff+len(llvmEmutlsSig)], llvmEmutlsSig) {
		t.Fatalf("emutls hook not installed")
	}
	wantOnce := []byte{0xf6, 0x00, 0x01, 0x75, 0x39, 0xc6, 0x00, 0x01}
	if !bytes.Equal(code[onceOff:onceOff+len(wantOnce)], wantOnce) {
		t.Fatalf("once patch not installed: %x", code[onceOff:onceOff+len(wantOnce)])
	}
}
