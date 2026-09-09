// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import "testing"

func TestInternMethodIDsReuse(t *testing.T) {
	a, first := internMethod("java/lang/String", "length", "()I", false)
	if jmethodNil(a) || !first {
		t.Fatalf("first internMethod = %v first=%t", a, first)
	}
	b, second := internMethod("java/lang/String", "length", "()I", false)
	if a != b || second {
		t.Fatalf("repeat internMethod = %v first=%t, want same id and first=false", b, second)
	}
	class, name, sig, static, ok := parseMethod(a)
	if !ok || class != "java/lang/String" || name != "length" || sig != "()I" || static {
		t.Fatalf("parseMethod = %q %q %q static=%t ok=%t", class, name, sig, static, ok)
	}
	other, firstOther := internMethod("java/lang/String", "isEmpty", "()Z", false)
	if jmethodNil(other) || !firstOther || other == a {
		t.Fatalf("distinct method interned poorly: %v first=%t", other, firstOther)
	}
	infoA, okA := lookupMethod(a)
	infoB, okB := lookupMethod(other)
	if !okA || !okB || infoA == infoB || infoA.name != "length" || infoB.name != "isEmpty" {
		t.Fatalf("slot lookup failed: ok=%t/%t a=%v b=%v", okA, okB, infoA, infoB)
	}
}

func TestInternFieldIDsReuse(t *testing.T) {
	a := internField("android/util/DisplayMetrics", "widthPixels", "I", false)
	if jfieldNil(a) {
		t.Fatal("internField returned null")
	}
	b := internField("android/util/DisplayMetrics", "widthPixels", "I", false)
	if a != b {
		t.Fatal("repeat internField must return the same jfieldID")
	}
	class, name, sig, static, ok := parseField(a)
	if !ok || class != "android/util/DisplayMetrics" || name != "widthPixels" || sig != "I" || static {
		t.Fatalf("parseField = %q %q %q static=%t ok=%t", class, name, sig, static, ok)
	}
}

func TestImmortalPackageNameAndFilesDir(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	a, ok := vm.dispatch(jnull(), "android/content/Context", "getPackageName", "()Ljava/lang/String;", nil)
	if !ok || jobjectToID(uintptr(a)) == 0 {
		t.Fatal("getPackageName")
	}
	b, ok := vm.dispatch(jnull(), "android/content/Context", "getPackageName", "()Ljava/lang/String;", nil)
	if !ok || a != b {
		t.Fatal("getPackageName must reuse the interned String")
	}
	files, ok := vm.dispatch(jnull(), "android/content/Context", "getFilesDir", "()Ljava/io/File;", nil)
	if !ok || jobjectToID(uintptr(files)) == 0 {
		t.Fatal("getFilesDir")
	}
	files2, ok := vm.dispatch(jnull(), "android/content/Context", "getFilesDir", "()Ljava/io/File;", nil)
	if !ok || files != files2 {
		t.Fatal("getFilesDir must reuse the interned File")
	}
	mgr, ok := vm.dispatch(jnull(), "android/content/Context", "getPackageManager", "()Landroid/content/pm/PackageManager;", nil)
	if !ok || jobjectToID(uintptr(mgr)) == 0 {
		t.Fatal("getPackageManager")
	}
	mgr2, ok := vm.dispatch(jnull(), "android/content/Context", "getPackageManager", "()Landroid/content/pm/PackageManager;", nil)
	if !ok || mgr != mgr2 {
		t.Fatal("getPackageManager must reuse the interned manager")
	}
}

func TestConnectivityDispatchSkipsUnrelatedClasses(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	_, ok := vm.dispatchConnectivity(nil, "java/lang/String", "length", "()I", nil)
	if ok {
		t.Fatal("unrelated class must not be claimed by connectivity dispatch")
	}
	_, ok = vm.dispatchConnectivity(nil, "android/app/Activity", "getSystemService", "(Ljava/lang/Class;)Ljava/lang/Object;", nil)
	if !ok {
		t.Fatal("Context.getSystemService(Class) must remain handled")
	}
}
