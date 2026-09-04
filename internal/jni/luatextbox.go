// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import (
	"sync"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Lua-textbox receiver identities (live-log ground truth, official Roblox
// 2.734.917 — descriptors only, no code copied):
//
//	com/roblox/client/startup/NativeHelper.gameActivity_onLuaTextBoxChanged(Ljava/lang/String;)V
//	com/roblox/client/startup/NativeHelper.gameActivity_onLuaTextBoxPropertyChanged()V
//	com/roblox/engine/jni/NativeGLJavaInterface.onLuaTextBoxChangedCallback(Ljava/lang/String;)V
//	com/roblox/engine/jni/NativeGLJavaInterface.onLuaTextBoxPropertyChangedCallback()V
//
// Direction is engine→Java in the showKeyboard twin pattern: the engine
// GetMethodIDs the NativeGL pair at startup (missing-method lines every
// launch) and the NativeHelper pair lazily at call time, then Calls all
// four during real Lua-textbox activity (/tmp/tipsy-direct-retest.log
// 20:38:26 — one stub-dispatch each, verified-Called). The String twin can
// carry user text, so only its LENGTH crosses into Tipsy — bytes are never
// read, stored, or logged (same privacy shape as §85/§87). Nothing here
// fabricates visibility, focuses a field, moves State counts, or feeds the
// commit route: receiving and recording the engine's announcement is the
// complete behavior, and the commit path stays dormant by construction.
const (
	luaTextBoxChangedSig         = "(Ljava/lang/String;)V"
	luaTextBoxPropertyChangedSig = "()V"
	luaTextBoxChangedHelper      = "gameActivity_onLuaTextBoxChanged"
	luaTextBoxPropertyHelper     = "gameActivity_onLuaTextBoxPropertyChanged"
	luaTextBoxChangedCallback    = "onLuaTextBoxChangedCallback"
	luaTextBoxPropertyCallback   = "onLuaTextBoxPropertyChangedCallback"
)

// luaTextBoxState records the engine's Lua-textbox announcements verbatim:
// per-twin call counts, the last String-twin payload LENGTH (never content),
// and the calling class of each twin. Zero counts mean the engine has not
// driven textbox activity — never a fabricated value.
var luaTextBoxState struct {
	mu                sync.Mutex
	changedCount      uint64
	lastChangedLen    int
	lastChangedClass  string
	propertyCount     uint64
	lastPropertyClass string
}

// dispatchLuaTextBox serves the engine→Java Lua-textbox contract on both
// classes that actually call it. Only the four verified-Called identities
// are handled; everything else falls through to the honest stub path.
// Void semantics mirror dispatchTextInput/dispatchNativeHelper.
func (vm *VM) dispatchLuaTextBox(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	isChanged := sig == luaTextBoxChangedSig &&
		((class == nativeHelperClass && name == luaTextBoxChangedHelper) ||
			(class == nativeGLClass && name == luaTextBoxChangedCallback))
	isProperty := sig == luaTextBoxPropertyChangedSig &&
		((class == nativeHelperClass && name == luaTextBoxPropertyHelper) ||
			(class == nativeGLClass && name == luaTextBoxPropertyCallback))
	if !isChanged && !isProperty {
		return jnull(), false
	}
	if isChanged {
		textLen := luaTextBoxStringLen(vm, args)
		luaTextBoxState.mu.Lock()
		luaTextBoxState.changedCount++
		luaTextBoxState.lastChangedLen = textLen
		luaTextBoxState.lastChangedClass = class
		n := luaTextBoxState.changedCount
		luaTextBoxState.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] luaTextBoxChanged",
			"class", class,
			"count", n,
			"textLen", textLen)
	} else {
		luaTextBoxState.mu.Lock()
		luaTextBoxState.propertyCount++
		luaTextBoxState.lastPropertyClass = class
		n := luaTextBoxState.propertyCount
		luaTextBoxState.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] luaTextBoxPropertyChanged",
			"class", class,
			"count", n)
	}
	if o != nil {
		return idToJobject(o.id), true
	}
	return jnull(), true
}

// luaTextBoxStringLen reports the length of the String-twin payload slot
// without reading a single payload byte. An absent slot or unknown object
// reads as 0, never a fabricated value. Only the string header length is
// observed — content never crosses into Tipsy.
func luaTextBoxStringLen(vm *VM, args *C.jvalue) int {
	if vm == nil || args == nil {
		return 0
	}
	if o := vm.get(jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 0)))); o != nil {
		return len(o.str)
	}
	return 0
}

// LuaTextBoxState reports the Lua-textbox announcements received from the
// engine: per-twin counts, the last String-twin payload length (never
// content), and the calling class of each twin. Zero counts mean no textbox
// activity yet — never a fabricated value. No text content is exposed here
// by construction.
func LuaTextBoxState() (changed, property uint64, lastLen int, lastChangedClass, lastPropertyClass string) {
	luaTextBoxState.mu.Lock()
	defer luaTextBoxState.mu.Unlock()
	return luaTextBoxState.changedCount, luaTextBoxState.propertyCount,
		luaTextBoxState.lastChangedLen,
		luaTextBoxState.lastChangedClass, luaTextBoxState.lastPropertyClass
}
