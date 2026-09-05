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
	"encoding/binary"
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

// jniFindClassCall is `call qword ptr [rax+0x30]`: JNINativeInterface.FindClass.
var jniFindClassCall = []byte{0xff, 0x50, 0x30}

func (m *Module) hookLLVMEmutls() {
	C.tipsy_set_roblox_bias(C.uintptr_t(m.bias))
	var emutlsVA uint64
	for _, s := range m.segs {
		if s.prot&syscall.PROT_EXEC == 0 || s.filesz < uint64(len(llvmEmutlsSig)) {
			continue
		}
		b := sliceAt(m.bias+uintptr(s.vaddr), int(s.filesz))
		if off := bytes.Index(b, llvmEmutlsSig); off >= 0 {
			addr := m.bias + uintptr(s.vaddr) + uintptr(off)
			C.tipsy_install_emutls_hook(ptrFromUintptr(addr))
			emutlsVA = s.vaddr + uint64(off)
			slog.Info("[loader] hooked LLVM emutls", "vaddr", fmt.Sprintf("%#x", emutlsVA))
		}
		if off := bytes.Index(b, cxaOnceSig); off >= 0 {
			addr := m.bias + uintptr(s.vaddr) + uintptr(off)
			C.tipsy_patch_cxa_once(ptrFromUintptr(addr))
			slog.Info("[loader] patched __cxa_get_globals once", "vaddr", fmt.Sprintf("%#x", s.vaddr+uint64(off)))
		}
	}
	if emutlsVA == 0 {
		return
	}
	if tls := m.discoverJNITLSControl(emutlsVA); tls != 0 {
		C.tipsy_set_roblox_jni_tls(C.uintptr_t(tls))
		slog.Info("[loader] pinned Roblox JNI emutls", "vaddr", fmt.Sprintf("%#x", tls))
		return
	}
	slog.Error("[loader] missing Roblox JNI emutls control")
}

// discoverJNITLSControl finds the LLVM emutls control the official JNIEnv
// FindClass thunks pass to __emutls_get_address immediately before
// `call *[slot+0x30]`. That control identity moves between APKs; the
// instruction sequence does not.
func (m *Module) discoverJNITLSControl(emutlsFileVA uint64) uintptr {
	if m == nil || emutlsFileVA == 0 {
		return 0
	}
	votes := map[uintptr]int{}
	for _, s := range m.segs {
		if s.prot&syscall.PROT_EXEC == 0 || s.filesz < 24 {
			continue
		}
		b := sliceAt(m.bias+uintptr(s.vaddr), int(s.filesz))
		start := 0
		for {
			off := bytes.Index(b[start:], jniFindClassCall)
			if off < 0 {
				break
			}
			at := start + off
			start = at + 1
			if tls, ok := jniTLSControlAt(b, s.vaddr, at, emutlsFileVA); ok && m.emutlsControlIsPointer(tls) {
				votes[tls]++
			}
		}
	}
	best, bestN := uintptr(0), 0
	for va, n := range votes {
		if n > bestN {
			best, bestN = va, n
		}
	}
	return best
}

func jniTLSControlAt(b []byte, segVA uint64, findClassOff int, emutlsFileVA uint64) (uintptr, bool) {
	if findClassOff < 16 {
		return 0, false
	}
	lo := findClassOff - 40
	if lo < 0 {
		lo = 0
	}
	for i := lo; i+12 <= findClassOff; i++ {
		if b[i] != 0x48 || b[i+1] != 0x8d || b[i+2] != 0x3d || b[i+7] != 0xe8 {
			continue
		}
		leaRel := int32(binary.LittleEndian.Uint32(b[i+3 : i+7]))
		leaEnd := segVA + uint64(i+7)
		tgt := uint64(int64(leaEnd) + int64(leaRel))
		callRel := int32(binary.LittleEndian.Uint32(b[i+8 : i+12]))
		callEnd := segVA + uint64(i+12)
		if uint64(int64(callEnd)+int64(callRel)) != emutlsFileVA {
			continue
		}
		if !bytes.Contains(b[i+12:findClassOff], []byte{0x48, 0x8b, 0x00}) {
			continue
		}
		return uintptr(tgt), true
	}
	return 0, false
}

func (m *Module) emutlsControlIsPointer(fileVA uintptr) bool {
	if m == nil || fileVA == 0 {
		return false
	}
	va := uint64(fileVA)
	for _, s := range m.segs {
		if va < s.vaddr || va+16 > s.vaddr+s.memsz {
			continue
		}
		b := sliceAt(m.bias+fileVA, 16)
		if len(b) < 16 {
			return false
		}
		size := binary.LittleEndian.Uint64(b[:8])
		align := binary.LittleEndian.Uint64(b[8:16])
		return size == 8 && (align == 0 || align == 8)
	}
	return false
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

func emutlsSetJNITLS(vaddr uintptr) {
	C.tipsy_set_roblox_jni_tls(C.uintptr_t(vaddr))
}

// SetJNIFunctions pins Roblox's thread_local JNIEnv vtable slot to Tipsy's
// original JNINativeInterface*. Call once after jni.NewVM, before JNI_OnLoad.
func SetJNIFunctions(p uintptr) {
	if p == 0 {
		return
	}
	C.tipsy_set_jni_functions(unsafe.Pointer(p))
}
