// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package loader

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "emutls.h"
*/
import "C"

import (
	"bytes"
	"fmt"
	"log/slog"
	"syscall"
	"unsafe"
)

// Unique prologue of libroblox.so LLVM __emutls_get_address (x86-64).
var llvmEmutlsSig = []byte{
	0x41, 0x57, 0x41, 0x56, 0x41, 0x55, 0x41, 0x54,
	0x53, 0x48, 0x89, 0xfb, 0x4c, 0x8b, 0x6f, 0x10,
}

// 29cfb10 once-check (13 bytes); following call 29cfb70 is left intact.
var cxaOnceSig = []byte{0x8a, 0x00, 0x24, 0x01, 0x3c, 0x00, 0x75, 0x36, 0xe8, 0x53, 0x00, 0x00, 0x00}

// NOTE (§86): there is intentionally no hook for the 26-byte
// 29d20c0 pattern (push rbp; 2x call 29cfb00; cmp [rax],[rax+0x68];
// setne). It was previously hooked to constant-0 as a supposed C++
// uncaught check, but read-only core forensics on the §85 SIGTRAP
// proved it is the shared fiber/exception-state predicate consulted
// by 257 call sites, including the Task constructor gate at 0x64c6a07
// (call; test al,al; je fiber-assert "Tasks: Task initialized on
// non-fiber"). Forcing it to 0 made every fiber Task construction
// abort on RBX Worker B right after gameLoaded. Per ADR 0010 the
// lying hook is retired, not narrowed: JNI_OnLoad observes the true
// value (0 at load, no fiber/uncaught active), and workers observe
// the engine's own fiber-local afterwards.

func (m *Module) hookLLVMEmutls() {
	C.tipsy_set_roblox_bias(C.uintptr_t(m.bias))
	for _, s := range m.segs {
		if s.prot&syscall.PROT_EXEC == 0 || s.filesz < uint64(len(llvmEmutlsSig)) {
			continue
		}
		b := sliceAt(m.bias+uintptr(s.vaddr), int(s.filesz))
		if off := bytes.Index(b, llvmEmutlsSig); off >= 0 {
			addr := m.bias + uintptr(s.vaddr) + uintptr(off)
			C.tipsy_install_emutls_hook(ptrFromUintptr(addr))
			slog.Info("[loader] hooked LLVM emutls", "vaddr", fmt.Sprintf("%#x", s.vaddr+uint64(off)))
		}
		if off := bytes.Index(b, cxaOnceSig); off >= 0 {
			addr := m.bias + uintptr(s.vaddr) + uintptr(off)
			C.tipsy_patch_cxa_once(ptrFromUintptr(addr))
			slog.Info("[loader] patched __cxa_get_globals once", "vaddr", fmt.Sprintf("%#x", s.vaddr+uint64(off)))
		}
	}
}

func emutlsGet(ctl unsafe.Pointer) uintptr {
	return uintptr(C.tipsy_emutls_get_address(ctl))
}

func emutlsSetBias(bias uintptr) {
	C.tipsy_set_roblox_bias(C.uintptr_t(bias))
}

func emutlsSetJNIFunctions(p unsafe.Pointer) {
	C.tipsy_set_jni_functions(p)
}

// SetJNIFunctions pins Roblox's thread_local JNIEnv vtable slot to Tipsy's
// original JNINativeInterface*. Call once after jni.NewVM, before JNI_OnLoad.
func SetJNIFunctions(p uintptr) {
	if p == 0 {
		return
	}
	C.tipsy_set_jni_functions(unsafe.Pointer(p))
}
