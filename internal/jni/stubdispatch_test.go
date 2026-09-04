// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// The tests drive callDispatchOrStub — the exact production path GoJNI_CallA
// uses for every method call — because cgo is unsupported in this package's
// test files. jnull()/nil arguments mirror a static methodID-carried class
// call; the methodID class field resolves the class in GoJNI_CallA and the
// dispatch key in vm.dispatch does not need it.

func resetStubDispatchForTest() {
	stubDispatchLogged.Range(func(k, _ any) bool {
		stubDispatchLogged.Delete(k)
		return true
	})
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return &buf
}

func TestStubDispatchDiagnosticDedup(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	// Same identity (class.name+sig+retKind) twice: exactly one diagnostic.
	_, _ = callDispatchOrStub(vm, jnull(), "tipsy/test/StubDedup", "mystery", "(I)V", nil, 'V')
	_, _ = callDispatchOrStub(vm, jnull(), "tipsy/test/StubDedup", "mystery", "(I)V", nil, 'V')
	if got := strings.Count(buf.String(), "stub-dispatch"); got != 1 {
		t.Fatalf("diagnostics after duplicate = %d, want 1: %s", got, buf.String())
	}

	// Different signature: the sig is part of the key, so a second diagnostic.
	_, _ = callDispatchOrStub(vm, jnull(), "tipsy/test/StubDedup", "mystery", "(I)I", nil, 'I')
	if got := strings.Count(buf.String(), "stub-dispatch"); got != 2 {
		t.Fatalf("diagnostics after new sig = %d, want 2", got)
	}

	// Same name+sig, different return kind: the retKind is part of the key.
	_, _ = callDispatchOrStub(vm, jnull(), "tipsy/test/StubDedup", "mystery2", "(I)V", nil, 'V')
	_, _ = callDispatchOrStub(vm, jnull(), "tipsy/test/StubDedup", "mystery2", "(I)V", nil, 'I')
	if got := strings.Count(buf.String(), "stub-dispatch"); got != 4 {
		t.Fatalf("diagnostics after retKind pair = %d, want 4", got)
	}
}

func TestStubDispatchDiagnosticIdentityFields(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	_, _ = callDispatchOrStub(vm, jnull(), "tipsy/test/StubIdent", "mystery", "(I)V", nil, 'V')
	out := buf.String()
	for _, want := range []string{
		`[jni] stub-dispatch`,
		`category=jni`,
		`tipsy/test/StubIdent.mystery(I)V`,
		`retKind=V`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("diagnostic missing %q: %s", want, out)
		}
	}
}

func TestStubDispatchAbsentOnImplemented(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	// Implemented dispatch path: handled without stubCall, no diagnostic,
	// and the real return value is unchanged (8 GiB smuggled as jobject).
	v, handled := callDispatchOrStub(vm, jnull(), "com/roblox/client/LocalStorageManager", "getAllocatableBytes", "()J", nil, 'J')
	if !handled {
		t.Fatal("implemented getAllocatableBytes reported unhandled")
	}
	if uintptr(v) != 8<<30 {
		t.Fatalf("implemented getAllocatableBytes return changed: %d", uintptr(v))
	}
	if strings.Contains(buf.String(), "stub-dispatch") {
		t.Fatalf("diagnostic fired on implemented path: %s", buf.String())
	}

	// Stub fallback return unchanged: 'L' still yields the normal empty
	// String object from stubCall, with exactly one diagnostic recorded.
	v2, handled := callDispatchOrStub(vm, jnull(), "tipsy/test/StubRet", "mystery", "()Ljava/lang/String;", nil, 'L')
	if handled {
		t.Fatal("stub fallback reported handled")
	}
	if uintptr(v2) == 0 {
		t.Fatal("stubCall return changed: NULL string")
	}
	got, err := vm.Env().GetStringUTFChars(uintptr(v2))
	if err != nil || got != "" {
		t.Fatalf("stubCall string = %q err=%v, want empty", got, err)
	}
	if got := strings.Count(buf.String(), "stub-dispatch"); got != 1 {
		t.Fatalf("diagnostics on stub path = %d, want 1: %s", got, buf.String())
	}
}
