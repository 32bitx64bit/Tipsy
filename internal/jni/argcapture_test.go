// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

func resetArgCaptureForTest() {
	argCaptureLogged.Range(func(k, _ any) bool {
		argCaptureLogged.Delete(k)
		return true
	})
}

// setDiagnosticsForTest toggles the cached TIPSY_DIAG flag for one test.
// diagnosticsEnabled is cached at process start, so t.Setenv alone cannot
// flip it after init.
func setDiagnosticsForTest(t *testing.T, on bool) {
	t.Helper()
	prev := diagnosticsOn.Load()
	diagnosticsOn.Store(on)
	t.Cleanup(func() { diagnosticsOn.Store(prev) })
}

func TestFindClassNameDiagnostic(t *testing.T) {
	setDiagnosticsForTest(t, true)
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetArgCaptureForTest()
	resetStubDispatchForTest()
	buf := captureLogs(t)

	const n1 = "com/roblox/engine/jni/model/ApplicationExitInfoCpp"
	v, handled := callFindClass(t, vm, n1)
	if !handled || v == 0 {
		t.Fatalf("findClass behavior changed: handled=%v v=%d", handled, v)
	}
	// Dispatch semantics untouched: the canonical object is returned and
	// the same name resolves to the same object again.
	v2, _ := callFindClass(t, vm, n1)
	if v2 != v {
		t.Fatalf("repeat findClass = %d, want canonical %d", v2, v)
	}
	const n2 = "com/example/OtherCaptureProbe"
	if v3, _ := callFindClass(t, vm, n2); v3 == 0 {
		t.Fatal("second name unresolved")
	}

	out := buf.String()
	if got := strings.Count(out, "[jni] findClass-name"); got != 2 {
		t.Fatalf("findClass-name diagnostics = %d, want 2 (dedupe by name): %s", got, out)
	}
	if !strings.Contains(out, "class="+n1) || !strings.Contains(out, "class="+n2) {
		t.Fatalf("diagnostic missing captured names: %s", out)
	}
	if strings.Contains(out, "lifecycle-handle") {
		t.Fatalf("stray lifecycle capture on findClass path: %s", out)
	}
}

func TestLifecycleHandleDiagnostic(t *testing.T) {
	setDiagnosticsForTest(t, true)
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetArgCaptureForTest()
	resetStubDispatchForTest()
	buf := captureLogs(t)

	glc := "com/roblox/engine/jni/NativeGLJavaInterface"

	// Fallback behavior preserved exactly: unhandled, stub V return.
	v, handled := callDispatchOrStub(vm, jnull(), glc, "gameLoadedCallback", "(J)V", packJlong(0x123456789), 'V')
	if handled || uintptr(v) != 0 {
		t.Fatalf("fallback changed: handled=%v v=%d", handled, uintptr(v))
	}
	// Same value dedupes; a distinct value records a new line.
	_, _ = callDispatchOrStub(vm, jnull(), glc, "gameLoadedCallback", "(J)V", packJlong(0x123456789), 'V')
	_, _ = callDispatchOrStub(vm, jnull(), glc, "gameLoadedCallback", "(J)V", packJlong(4886718345+7), 'V')
	// NativeHelper.gameActivity_onGameLoaded(J)V is now received and
	// recorded by the dispatchNativeHelper receiver (nativehelper.go), not
	// the stub path: handled, counted, and logged as onGameLoaded.
	glBefore, _ := NativeHelperGameLoaded()
	if _, handled = callDispatchOrStub(vm, jnull(), "com/roblox/client/startup/NativeHelper", "gameActivity_onGameLoaded", "(J)V", packJlong(77), 'V'); !handled {
		t.Fatal("onGameLoaded receiver missing: expected handled")
	}
	if glAfter, glHandle := NativeHelperGameLoaded(); glAfter != glBefore+1 || glHandle != 77 {
		t.Fatalf("onGameLoaded record = (%d, %d), want (%d, 77)", glAfter, glHandle, glBefore+1)
	}
	// Absent argument slot: no capture, no panic.
	_, _ = callDispatchOrStub(vm, jnull(), glc, "gameLoadedCallback", "(J)V", nil, 'V')

	out := buf.String()
	if got := strings.Count(out, "[jni] lifecycle-handle"); got != 2 {
		t.Fatalf("lifecycle-handle diagnostics = %d, want 2: %s", got, out)
	}
	if !strings.Contains(out, "handle=4886718345") || !strings.Contains(out, "handle=4886718352") {
		t.Fatalf("missing captured handles: %s", out)
	}
	if !strings.Contains(out, "[jni] onGameLoaded") {
		t.Fatalf("missing onGameLoaded receiver record: %s", out)
	}
	if !strings.Contains(out, "stub-dispatch") {
		t.Fatal("stub-dispatch diagnostic must still fire beside the capture")
	}
	if strings.Contains(out, "findClass-name") {
		t.Fatalf("stray findClass capture on lifecycle path: %s", out)
	}
}

func TestArgCaptureFiltered(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetArgCaptureForTest()
	resetStubDispatchForTest()
	buf := captureLogs(t)

	glc := "com/roblox/engine/jni/NativeGLJavaInterface"
	// Real user-adjacent identity: never captured, whatever the args.
	_, _ = callDispatchOrStub(vm, jnull(), "com/roblox/engine/jni/user/NativeUserJavaInterface", "getUsername", "()Ljava/lang/String;", nil, 'L')
	_, _ = callDispatchOrStub(vm, jnull(), "com/roblox/engine/jni/user/NativeUserJavaInterface", "gameLoadedCallback", "(J)V", packJlong(9), 'V')
	// Lookalike identities: wrong sig, wrong class.
	_, _ = callDispatchOrStub(vm, jnull(), glc, "gameLoadedCallback", "(JI)V", packJlong(9), 'V')
	_, _ = callDispatchOrStub(vm, jnull(), "com/roblox/client/startup/NotNativeHelper", "gameActivity_onGameLoaded", "(J)V", packJlong(9), 'V')
	// String-carrying lookalike: findClass capture must not leak to loadClass.
	loadCls := vm.Env().NewString("java/lang/String")
	_, _ = callDispatchOrStub(vm, jnull(), "java/lang/ClassLoader", "loadClass", "(Ljava/lang/String;)Ljava/lang/Class;", packJobject(idToJobject(jobjectToID(loadCls))), 'L')

	out := buf.String()
	if strings.Contains(out, "[jni] findClass-name") || strings.Contains(out, "[jni] lifecycle-handle") {
		t.Fatalf("capture leaked outside approved identities: %s", out)
	}
	// Fallback diagnostics unchanged: every unhandled call still reports.
	if got := strings.Count(out, "stub-dispatch"); got != 5 {
		t.Fatalf("stub-dispatch diagnostics = %d, want 5: %s", got, out)
	}
}
