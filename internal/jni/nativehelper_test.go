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
// (GetMethodID'd AND called for every loaded DataModel, launch logs: J=0
// for Home at startup, then the joined place id): the call is handled, the
// engine-provided place id is received and recorded (never invented), and
// nothing else in the class is claimed.
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
	// The live-observed startup value first, then a distinct place id to
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
	count, placeID := NativeHelperGameLoaded()
	if count != countBefore+2 {
		t.Fatalf("gameLoaded count = %d, want %d", count, countBefore+2)
	}
	if placeID != 77 {
		t.Fatalf("recorded placeId = %d, want 77", placeID)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] onGameLoaded") || !strings.Contains(out, "placeId=0") {
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

// TestNativeHelperOnDidLogInReceived drives the exact production dispatch
// path for gameActivity_onDidLogInReceived(Ljava/lang/String;)V: the call
// is handled, planted DID_LOG_IN JSON fills NativeUser getters, and the
// payload is never logged.
func TestNativeHelperOnDidLogInReceived(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetNativeUserForTest()
	t.Cleanup(ResetNativeUserForTest)
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	s := vm.newStringLocked(fakeNativeUserLoginJSON)
	vm.mu.Unlock()

	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", testPackObjectArg(s.id))
	if !handled {
		t.Fatal("gameActivity_onDidLogInReceived not handled by dispatchNativeHelper")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	if !isImplementedMethod("gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V") {
		t.Fatal("gameActivity_onDidLogInReceived missing from implementedMethods")
	}
	if _, handled := vm.dispatch(idToJobject(h.id), "java/io/File", "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", testPackObjectArg(s.id)); handled {
		t.Fatal("non-NativeHelper class handled by NativeHelper dispatch")
	}

	recv := nativeUserRecv(allocNativeUser(t, vm))
	if nativeUserString(t, vm, recv, "getUsername") != "tester" {
		t.Fatalf("getUsername after login = %q, want tester", nativeUserString(t, vm, recv, "getUsername"))
	}
	uid, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getUserId", "()J", nil, 'J')
	if !handled || int64(uintptr(uid)) != 1 {
		t.Fatalf("getUserId after login handled=%v v=%d, want 1", handled, uintptr(uid))
	}
	if nativeUserString(t, vm, recv, "getPlatformName") != "Windows" {
		t.Fatal("getPlatformName after login want Windows")
	}

	out := buf.String()
	if !strings.Contains(out, "[jni] native-user snapshot") || !strings.Contains(out, "hasUserId=true") {
		t.Fatalf("log missing native-user snapshot: %s", out)
	}
	if strings.Contains(out, "tester") || strings.Contains(out, fakeNativeUserLoginJSON) {
		t.Fatalf("login identity leaked into log: %s", out)
	}
}

// TestNativeHelperOnDidLogInReceivedMalformedLeavesSnapshot pins that empty
// or malformed JSON still handles the void call and does not wipe a
// previously planted snapshot (and does not crash).
func TestNativeHelperOnDidLogInReceivedMalformedLeavesSnapshot(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetNativeUserForTest()
	t.Cleanup(ResetNativeUserForTest)

	plantNativeUserLogin(t, vm, fakeNativeUserLoginJSON)
	recv := nativeUserRecv(allocNativeUser(t, vm))

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	bad := vm.newStringLocked("{")
	empty := vm.newStringLocked("")
	junk := vm.newStringLocked("not-json")
	vm.mu.Unlock()

	if _, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", testPackObjectArg(bad.id)); !handled {
		t.Fatal("malformed JSON must still be handled")
	}
	if _, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", testPackObjectArg(empty.id)); !handled {
		t.Fatal("empty JSON must still be handled")
	}
	if _, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", testPackObjectArg(junk.id)); !handled {
		t.Fatal("non-JSON must still be handled")
	}
	if _, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass, "gameActivity_onDidLogInReceived", "(Ljava/lang/String;)V", nil); !handled {
		t.Fatal("nil args must still be handled")
	}

	if nativeUserString(t, vm, recv, "getUsername") != "tester" {
		t.Fatalf("snapshot wiped by malformed JSON: %q", nativeUserString(t, vm, recv, "getUsername"))
	}
	uid, handled := callDispatchOrStub(vm, idToJobject(jobjectToID(recv)), nativeUserClass, "getUserId", "()J", nil, 'J')
	if !handled || int64(uintptr(uid)) != 1 {
		t.Fatalf("userId after malformed = handled=%v v=%d, want 1", handled, uintptr(uid))
	}
}
