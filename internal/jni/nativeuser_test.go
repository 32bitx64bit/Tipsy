// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

func resetNativeUserLogForTest() {
	nativeUserLogged.Range(func(k, _ any) bool {
		nativeUserLogged.Delete(k)
		return true
	})
	immortalNativeUserPlatform = nil
}

func TestNativeUserPlatformNameIsWindowsSpoof(t *testing.T) {
	if nativeUserPlatformName != "Windows" {
		t.Fatalf("getPlatformName = %q, want Enum.Platform.Windows spoof", nativeUserPlatformName)
	}

	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetNativeUserLogForTest()
	resetStubDispatchForTest()
	buf := captureLogs(t)
	env := vm.Env()

	cls := env.FindClass(nativeUserClass)
	if cls == 0 {
		t.Fatal("NativeUserJavaInterface class missing")
	}
	obj := env.AllocObject(cls)
	recv := idToJobject(jobjectToID(obj))

	v, handled := callDispatchOrStub(vm, recv, nativeUserClass, "getPlatformName", "()Ljava/lang/String;", nil, 'L')
	if !handled {
		t.Fatal("getPlatformName not handled")
	}
	got, err := env.GetStringUTFChars(uintptr(v))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Windows" {
		t.Fatalf("getPlatformName = %q, want Windows", got)
	}

	out := buf.String()
	if strings.Contains(out, "stub-dispatch") {
		t.Fatalf("stub-dispatch fired: %s", out)
	}
	if strings.Contains(out, "Windows") {
		t.Fatalf("product string leaked into log: %s", out)
	}
	if !strings.Contains(out, "method=getPlatformName") || !strings.Contains(out, "spoof=pc") {
		t.Fatalf("missing pc spoof record: %s", out)
	}

	if _, handled := callDispatchOrStub(vm, recv, nativeUserClass, "getUsername", "()Ljava/lang/String;", nil, 'L'); handled {
		t.Fatal("getUsername should stay on the generic stub")
	}
}

func TestNativeUserWrongClassFallsThrough(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	if _, handled := callDispatchOrStub(vm, jnull(), "java/io/File", "getPlatformName", "()Ljava/lang/String;", nil, 'L'); handled {
		t.Fatal("non-NativeUser class handled by NativeUser dispatch")
	}
	if _, handled := callDispatchOrStub(vm, jnull(), nativeUserClass, "getPlatformName", "()I", nil, 'I'); handled {
		t.Fatal("wrong sig handled")
	}
}

func TestNativeUserPlatformNameIsImplemented(t *testing.T) {
	if !isImplementedMethod("getPlatformName", "()Ljava/lang/String;") {
		t.Fatal("getPlatformName()Ljava/lang/String; missing from implementedMethods")
	}
}
