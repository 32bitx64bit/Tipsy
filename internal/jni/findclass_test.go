// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

// The tests drive callDispatchOrStub — the exact production path
// GoJNI_CallA uses — per the stubdispatch_test.go convention. The name
// argument is a real jstring packed with the production packJobject helper.

const findClassSig = "(Ljava/lang/String;)Ljava/lang/Class;"

func callFindClass(t *testing.T, vm *VM, name string) (uintptr, bool) {
	t.Helper()
	js := vm.Env().NewString(name)
	if js == 0 {
		t.Fatal("NewString for findClass arg")
	}
	arg := packJobject(idToJobject(jobjectToID(js)))
	v, handled := callDispatchOrStub(vm, jnull(), "java/lang/ClassLoader", "findClass", findClassSig, arg, 'L')
	return uintptr(v), handled
}

func TestFindClassReturnsCanonicalClassObject(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	const name = "com/roblox/engine/jni/model/ApplicationExitInfoCpp"
	v, handled := callFindClass(t, vm, name)
	if !handled {
		t.Fatal("findClass not handled by dispatch")
	}
	if v == 0 {
		t.Fatal("findClass returned NULL for a resolvable name")
	}
	o := vm.get(jobjectToID(v))
	if o == nil || o.class == nil || o.class.name != "java/lang/Class" {
		t.Fatalf("findClass result = %v, want a java/lang/Class object", o)
	}
	if o.fields["name"] != name {
		t.Fatalf("findClass result name = %v, want %q", o.fields["name"], name)
	}
	// Canonical identity: the same Class object the JNIEnv FindClass path
	// serves, so IsSameObject-style comparisons agree across both paths.
	if want := vm.Env().FindClass(name); v != want {
		t.Fatalf("findClass = %d, FindClass = %d (identity mismatch)", v, want)
	}
	if cn := classNameOf(vm, jclassOf(idToJobject(jobjectToID(v)))); cn != name {
		t.Fatalf("classNameOf(findClass result) = %q, want %q", cn, name)
	}
	if out := buf.String(); strings.Contains(out, "stub-dispatch") {
		t.Fatalf("stub diagnostic fired on the implemented path: %s", out)
	}
}

func TestFindClassAutoCreatesUnknownName(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	const name = "com/example/OnlyViaClassLoaderFindClass"
	v, handled := callFindClass(t, vm, name)
	if !handled || v == 0 {
		t.Fatalf("findClass unknown name: handled=%v v=%d, want handled non-NULL", handled, v)
	}
	c := vm.classes[name]
	if c == nil || c.obj == nil {
		t.Fatal("auto-created class not registered in the class map")
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] auto-class: "+name) {
		t.Fatalf("missing auto-class diagnostic: %s", out)
	}
	if strings.Contains(out, "stub-dispatch") {
		t.Fatalf("stub diagnostic fired on the implemented path: %s", out)
	}

	// Repeat resolves the same canonical object and logs the auto-class
	// diagnostic exactly once.
	v2, _ := callFindClass(t, vm, name)
	if v2 != v {
		t.Fatalf("second findClass = %d, want the same object %d", v2, v)
	}
	if got := strings.Count(buf.String(), "[jni] auto-class: "+name); got != 1 {
		t.Fatalf("auto-class diagnostics = %d, want 1", got)
	}
}

func TestFindClassEmptyOrAbsentNameResolvesNothing(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	v, handled := callFindClass(t, vm, "")
	if !handled {
		t.Fatal("empty name must still be handled by dispatch, not stubbed")
	}
	if v != 0 {
		t.Fatalf("empty name resolved to %d, want NULL", v)
	}

	// Absent argument object: no name to resolve, still handled.
	cv, handled := callDispatchOrStub(vm, jnull(), "java/lang/ClassLoader", "findClass", findClassSig, nil, 'L')
	if !handled || uintptr(cv) != 0 {
		t.Fatalf("absent arg: handled=%v v=%d, want handled NULL", handled, uintptr(cv))
	}
	if out := buf.String(); strings.Contains(out, "auto-class: ") {
		t.Fatalf("empty/absent name must not auto-create a class: %s", out)
	}
}

func TestFindClassRegisteredAsImplemented(t *testing.T) {
	if !isImplementedMethod("findClass", findClassSig) {
		t.Fatal("findClass not in implementedMethods; GetMethodID would log missing")
	}
}
