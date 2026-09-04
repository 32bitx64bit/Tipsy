// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

// luaTextBoxString builds a VM String object carrying the given content
// (test-only carrier; production never reads content, only length).
func luaTextBoxString(t *testing.T, vm *VM, s string) int64 {
	t.Helper()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	return vm.newStringLocked(s).id
}

// TestLuaTextBoxChangedBothClasses drives the exact production dispatch path
// for the String twin on both classes (verified-Called in
// /tmp/tipsy-direct-retest.log 20:38:26): handled, counted, length-only
// record, log line emitted, nothing fabricated or acted on.
func TestLuaTextBoxChangedBothClasses(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	sid := luaTextBoxString(t, vm, "hello")

	changedBefore, propertyBefore, _, _, _ := LuaTextBoxState()

	v, handled := vm.dispatch(jnull(), nativeHelperClass,
		luaTextBoxChangedHelper, luaTextBoxChangedSig,
		testPackObjectArg(sid))
	if !handled {
		t.Fatal("gameActivity_onLuaTextBoxChanged not handled by dispatchLuaTextBox")
	}
	if uintptr(v) != 0 {
		t.Fatalf("void nil-receiver return = %#x, want null", uintptr(v))
	}
	v, handled = vm.dispatch(jnull(), nativeGLClass,
		luaTextBoxChangedCallback, luaTextBoxChangedSig,
		testPackObjectArg(sid))
	if !handled {
		t.Fatal("onLuaTextBoxChangedCallback not handled by dispatchLuaTextBox")
	}
	if uintptr(v) != 0 {
		t.Fatalf("void nil-receiver return = %#x, want null", uintptr(v))
	}

	changed, property, lastLen, lastChangedClass, _ := LuaTextBoxState()
	if changed != changedBefore+2 {
		t.Fatalf("changed count = %d, want %d", changed, changedBefore+2)
	}
	if property != propertyBefore {
		t.Fatalf("property count = %d, want unchanged %d", property, propertyBefore)
	}
	if lastLen != len("hello") {
		t.Fatalf("last text length = %d, want %d", lastLen, len("hello"))
	}
	if lastChangedClass != nativeGLClass {
		t.Fatalf("last changed class = %q, want NativeGL", lastChangedClass)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] luaTextBoxChanged") || !strings.Contains(out, "textLen=5") {
		t.Fatalf("log missing length-only changed record: %s", out)
	}

	for _, tc := range []struct{ name, sig string }{
		{luaTextBoxChangedHelper, luaTextBoxChangedSig},
		{luaTextBoxPropertyHelper, luaTextBoxPropertyChangedSig},
		{luaTextBoxChangedCallback, luaTextBoxChangedSig},
		{luaTextBoxPropertyCallback, luaTextBoxPropertyChangedSig},
	} {
		if !isImplementedMethod(tc.name, tc.sig) {
			t.Fatalf("%s%s missing from implementedMethods", tc.name, tc.sig)
		}
	}

	// Wrong class with the same name/sig is not the contract.
	if _, handled := vm.dispatch(jnull(), "java/io/File",
		luaTextBoxChangedHelper, luaTextBoxChangedSig,
		testPackObjectArg(sid)); handled {
		t.Fatal("non-text class handled by Lua-textbox dispatch")
	}
	// Wrong sig on the right class stays unhandled.
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		luaTextBoxChangedCallback, "()V", nil); handled {
		t.Fatal("wrong-sig changed callback handled by Lua-textbox dispatch")
	}
	// Nil args degrade to a zero length without a panic.
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		luaTextBoxChangedCallback, luaTextBoxChangedSig, nil); !handled {
		t.Fatal("nil-arg changed callback not handled")
	}
	if _, _, lastLen, _, _ := LuaTextBoxState(); lastLen != 0 {
		t.Fatalf("nil-arg last length = %d, want 0", lastLen)
	}
}

// TestLuaTextBoxPropertyChangedBothClasses drives the exact production
// dispatch path for the ()V twin on both classes: handled, counted, class
// recorded, nothing fabricated or acted on. The twin carries no payload.
func TestLuaTextBoxPropertyChangedBothClasses(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	vm.mu.Unlock()

	changedBefore, propertyBefore, _, _, _ := LuaTextBoxState()

	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass,
		luaTextBoxPropertyHelper, luaTextBoxPropertyChangedSig, nil)
	if !handled {
		t.Fatal("gameActivity_onLuaTextBoxPropertyChanged not handled")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		luaTextBoxPropertyCallback, luaTextBoxPropertyChangedSig, nil); !handled {
		t.Fatal("onLuaTextBoxPropertyChangedCallback not handled")
	}

	changed, property, _, _, lastPropertyClass := LuaTextBoxState()
	if property != propertyBefore+2 {
		t.Fatalf("property count = %d, want %d", property, propertyBefore+2)
	}
	if changed != changedBefore {
		t.Fatalf("changed count = %d, want unchanged %d", changed, changedBefore)
	}
	if lastPropertyClass != nativeGLClass {
		t.Fatalf("last property class = %q, want NativeGL", lastPropertyClass)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] luaTextBoxPropertyChanged") {
		t.Fatalf("log missing property record: %s", out)
	}
}

// TestLuaTextBoxNeverLogsTextContent is the privacy boundary: a String twin
// carrying a secret marker appears in the log only as a length, never as
// content, and no accessor exposes content.
func TestLuaTextBoxNeverLogsTextContent(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	const secret = "s3cr3t-lua-textbox-marker"
	sid := luaTextBoxString(t, vm, secret)
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		luaTextBoxChangedCallback, luaTextBoxChangedSig,
		testPackObjectArg(sid)); !handled {
		t.Fatal("changed callback not handled")
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log leaked textbox content: %s", out)
	}
	if !strings.Contains(out, "textLen=") {
		t.Fatalf("log missing length-only aggregate: %s", out)
	}
	if _, _, lastLen, _, _ := LuaTextBoxState(); lastLen != len(secret) {
		t.Fatalf("last length = %d, want %d", lastLen, len(secret))
	}
}

// TestLuaTextBoxCommitStaysDormant pins the no-synthesis rule for the new
// surface: Lua-textbox announcements move only their own counters — State
// transition counts, the commit gate, and the physical key path do not move.
func TestLuaTextBoxCommitStaysDormant(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)

	setBefore, softBefore, restartBefore, _ := TextInputStateTransitions()

	sid := luaTextBoxString(t, vm, "dormant")
	if _, handled := vm.dispatch(jnull(), nativeHelperClass,
		luaTextBoxChangedHelper, luaTextBoxChangedSig,
		testPackObjectArg(sid)); !handled {
		t.Fatal("helper changed twin not handled")
	}
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		luaTextBoxPropertyCallback, luaTextBoxPropertyChangedSig, nil); !handled {
		t.Fatal("NativeGL property twin not handled")
	}

	setAfter, softAfter, restartAfter, committed := TextInputStateTransitions()
	if setAfter != setBefore || softAfter != softBefore || restartAfter != restartBefore {
		t.Fatal("Lua-textbox announcements moved State transition counts")
	}
	if committed != 0 || TextInputCommitPending() {
		t.Fatal("Lua-textbox announcements fabricated committed text")
	}
}
