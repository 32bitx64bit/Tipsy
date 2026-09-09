// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"testing"
	"unicode/utf16"
	"unsafe"
)

func TestUTF16UnitCountMatchesEncode(t *testing.T) {
	for _, s := range []string{"", "ascii", "héllo", "😀", "a😀b"} {
		if got, want := utf16UnitCount(s), len(utf16.Encode([]rune(s))); got != want {
			t.Fatalf("%q units=%d want %d", s, got, want)
		}
	}
}

func TestGetStringLengthAndChars(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	const want = "héllo😀"
	js := testNewUTF16String(vm, want)
	if js == 0 {
		t.Fatal("NewString")
	}
	if got := testStringUTF16Len(vm.envRaw, js); got != utf16UnitCount(want) {
		t.Fatalf("GetStringLength=%d want %d", got, utf16UnitCount(want))
	}
	chars, isCopy := testGetStringChars(vm.envRaw, js)
	if chars == nil {
		t.Fatal("GetStringChars")
	}
	if !isCopy {
		t.Fatal("GetStringChars isCopy=false, want true")
	}
	n := utf16UnitCount(want)
	got := string(utf16.Decode(unsafe.Slice((*uint16)(chars), n)))
	if got != want {
		t.Fatalf("GetStringChars text=%q want %q", got, want)
	}
	testReleaseStringChars(vm.envRaw, js, chars)
}

func TestPrimitiveArrayCriticalPins(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	arr := testNewByteObject(vm, []byte{1, 2, 3, 4})
	if arr == 0 {
		t.Fatal("byte array")
	}
	p, isCopy := testPrimitiveArrayCritical(vm.envRaw, arr)
	if p == nil {
		t.Fatal("GetPrimitiveArrayCritical")
	}
	if isCopy {
		t.Fatal("critical isCopy=true, want false")
	}
	sl := unsafe.Slice((*byte)(p), 4)
	sl[0] = 9
	testReleasePrimitiveArrayCritical(vm.envRaw, arr, p, 0)
	o := vm.get(arr)
	if o == nil || o.bytes[0] != 9 {
		t.Fatalf("pin write not visible: %#v", o)
	}
}

func TestGetArrayElementsStillCopies(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	arr := testNewByteObject(vm, []byte{1, 2, 3, 4})
	p, isCopy := testGetArrayElements(vm.envRaw, arr)
	if p == nil {
		t.Fatal("GetArrayElements")
	}
	if !isCopy {
		t.Fatal("GetArrayElements isCopy=false, want true (honest copy)")
	}
	sl := unsafe.Slice((*byte)(p), 4)
	sl[0] = 7
	o := vm.get(arr)
	if o.bytes[0] != 1 {
		t.Fatal("copy path mutated the Java array before Release")
	}
	testReleaseArrayElements(vm.envRaw, arr, p, 0)
	if o.bytes[0] != 7 {
		t.Fatal("Release JNI_COMMIT did not copy back")
	}
}

func TestExceptionCheckNoPending(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if testExceptionCheck(vm.envRaw) {
		t.Fatal("ExceptionCheck on a clean env")
	}
	js := testNewUTF16String(vm, "x")
	if testThrowID(vm.envRaw, js) != 0 {
		t.Fatal("Throw")
	}
	if !testExceptionCheck(vm.envRaw) {
		t.Fatal("ExceptionCheck after Throw")
	}
	testExceptionClear(vm.envRaw)
	if testExceptionCheck(vm.envRaw) {
		t.Fatal("ExceptionCheck after Clear")
	}
}

func BenchmarkExceptionCheck(b *testing.B) {
	vm, err := NewVM()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = testExceptionCheck(vm.envRaw)
	}
}

func BenchmarkJNILocalRef(b *testing.B) {
	vm, err := NewVM()
	if err != nil {
		b.Fatal(err)
	}
	id := allocLocalObject(vm)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if testNewLocalRef(vm.envRaw, id) == 0 {
			b.Fatal("NewLocalRef")
		}
		testDeleteLocalRef(vm.envRaw, id)
	}
}

func BenchmarkDispatchCoreHit(b *testing.B) {
	vm, err := NewVM()
	if err != nil {
		b.Fatal(err)
	}
	mid, _ := internMethod("tipsy/test/BenchCore", "getAllocatableBytes", "()J", true)
	if out := vm.callA(0, mid, 1, 'J'); out.j != 8<<30 {
		b.Fatalf("warmup getAllocatableBytes = %d", out.j)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := vm.callA(0, mid, 1, 'J'); out.j != 8<<30 {
			b.Fatal(out.j)
		}
	}
}

func BenchmarkInternedStringHit(b *testing.B) {
	vm, err := NewVM()
	if err != nil {
		b.Fatal(err)
	}
	mid, _ := internMethod("android/content/Context", "getPackageName", "()Ljava/lang/String;", false)
	if out := vm.callA(0, mid, 0, 'L'); out.l == 0 {
		b.Fatal("warmup getPackageName")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := vm.callA(0, mid, 0, 'L'); out.l == 0 {
			b.Fatal("getPackageName")
		}
	}
}
