// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

// TestNativeHelperOnAppReady drives the exact production dispatch path the
// engine's CallVoidMethod takes for
// com/roblox/client/startup/NativeHelper.gameActivity_onAppReady(Ljava/lang/String;)V:
// the call is handled, the engine-provided step name is received and
// recorded (never invented), and nothing else in the class is claimed.
func TestNativeHelperOnAppReady(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	s := vm.newStringLocked("Startup")
	vm.mu.Unlock()

	before, _ := NativeHelperAppReady()
	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onAppReady", "(Ljava/lang/String;)V", testPackObjectArg(s.id))
	if !handled {
		t.Fatal("gameActivity_onAppReady not handled by dispatchNativeHelper")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	count, lastStep := NativeHelperAppReady()
	if count != before+1 {
		t.Fatalf("AppReady count = %d, want %d", count, before+1)
	}
	if lastStep != "Startup" {
		t.Fatalf("last step = %q, want Startup", lastStep)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] onAppReady") || !strings.Contains(out, "step=Startup") {
		t.Fatalf("log missing onAppReady record: %s", out)
	}

	// The identity joins implementedMethods, so GetMethodID stops logging
	// it as missing (the contract exists on the Java side now).
	if !isImplementedMethod("gameActivity_onAppReady", "(Ljava/lang/String;)V") {
		t.Fatal("gameActivity_onAppReady missing from implementedMethods")
	}

	// Other classes with the same method name are not NativeHelper's
	// contract.
	if _, handled := vm.dispatch(idToJobject(h.id), "java/io/File", "gameActivity_onAppReady", "(Ljava/lang/String;)V", testPackObjectArg(s.id)); handled {
		t.Fatal("non-NativeHelper class handled by NativeHelper dispatch")
	}
}

// TestNativeHelperOnScreenOrientationChanged drives the exact production
// dispatch path the engine's CallVoidMethod takes for
// com/roblox/client/startup/NativeHelper.gameActivity_onScreenOrientationChanged(IZ)V
// (GetMethodID'd AND called at startup, launch logs): the call is handled,
// the engine-provided orientation and requestDefault flag are received and
// recorded (never invented, never reported as applied), and nothing else
// in the class is claimed.
func TestNativeHelperOnScreenOrientationChanged(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	vm.mu.Unlock()

	countBefore, _, _ := NativeHelperOrientationAnnouncements()
	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onScreenOrientationChanged", "(IZ)V", testPackTwoInts(4, 0))
	if !handled {
		t.Fatal("gameActivity_onScreenOrientationChanged not handled by dispatchNativeHelper")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	count, orientation, def := NativeHelperOrientationAnnouncements()
	if count != countBefore+1 {
		t.Fatalf("orientation count = %d, want %d", count, countBefore+1)
	}
	if orientation != 4 || def {
		t.Fatalf("recorded announcement = (%d, %v), want (4, false)", orientation, def)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] onScreenOrientationChanged") || !strings.Contains(out, "orientation=4") || !strings.Contains(out, "requestDefault=false") {
		t.Fatalf("log missing onScreenOrientationChanged record: %s", out)
	}

	// The identity joins implementedMethods, so GetMethodID stops logging
	// it as missing (the contract exists on the Java side now).
	if !isImplementedMethod("gameActivity_onScreenOrientationChanged", "(IZ)V") {
		t.Fatal("gameActivity_onScreenOrientationChanged missing from implementedMethods")
	}

	// Other classes with the same method name are not NativeHelper's
	// contract.
	if _, handled := vm.dispatch(idToJobject(h.id), "java/io/File", "gameActivity_onScreenOrientationChanged", "(IZ)V", testPackTwoInts(4, 0)); handled {
		t.Fatal("non-NativeHelper class handled by NativeHelper dispatch")
	}
}

// TestNativeHelperOnGameLoaded drives the exact production dispatch path
// the engine's CallVoidMethod takes for
// com/roblox/client/startup/NativeHelper.gameActivity_onGameLoaded(J)V
// (GetMethodID'd AND called once per launch at startup, launch logs, with
// a J argument of 0): the call is handled, the engine-provided handle is
// received and recorded (never invented, never acted on), and nothing else
// in the class is claimed.
func TestNativeHelperOnGameLoaded(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	vm.mu.Unlock()

	countBefore, _ := NativeHelperGameLoaded()
	// The live-observed startup value first, then a distinct handle to
	// prove the record tracks the engine's announcement verbatim.
	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(0))
	if !handled {
		t.Fatal("gameActivity_onGameLoaded not handled by dispatchNativeHelper")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	if _, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onGameLoaded", "(J)V", packJlong(77)); !handled {
		t.Fatal("second gameActivity_onGameLoaded not handled")
	}
	count, handle := NativeHelperGameLoaded()
	if count != countBefore+2 {
		t.Fatalf("gameLoaded count = %d, want %d", count, countBefore+2)
	}
	if handle != 77 {
		t.Fatalf("recorded handle = %d, want 77", handle)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] onGameLoaded") || !strings.Contains(out, "handle=0") {
		t.Fatalf("log missing onGameLoaded record: %s", out)
	}

	// The identity joins implementedMethods, so GetMethodID stops logging
	// it as missing (the contract exists on the Java side now).
	if !isImplementedMethod("gameActivity_onGameLoaded", "(J)V") {
		t.Fatal("gameActivity_onGameLoaded missing from implementedMethods")
	}

	// Other classes with the same method name are not NativeHelper's
	// contract.
	if _, handled := vm.dispatch(idToJobject(h.id), "java/io/File", "gameActivity_onGameLoaded", "(J)V", packJlong(0)); handled {
		t.Fatal("non-NativeHelper class handled by NativeHelper dispatch")
	}
}

// TestAppReadyStepNameSanitized proves the log-safety boundary: the
// engine-provided step string is capped and control bytes never reach the
// log line.
func TestAppReadyStepNameSanitized(t *testing.T) {
	if got := appReadyStepName("Landing"); got != "Landing" {
		t.Fatalf("plain step = %q, want Landing", got)
	}
	if got := appReadyStepName("a\x01b\nc"); got != "a?b?c" {
		t.Fatalf("control bytes = %q, want a?b?c", got)
	}
	if got := appReadyStepName(strings.Repeat("x", 500)); len(got) != 128 {
		t.Fatalf("capped length = %d, want 128", len(got))
	}
	if got := appReadyStepName(""); got != "" {
		t.Fatalf("empty step = %q, want empty", got)
	}
}
