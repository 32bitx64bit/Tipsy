// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"math/rand"
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

func checkStringCharsAgainstEncode(t *testing.T, vm *VM, value string) {
	t.Helper()
	js := testNewUTF16String(vm, value)
	if js == 0 {
		t.Fatal("NewString")
	}
	chars, isCopy := testGetStringChars(vm.envRaw, js)
	if chars == nil || !isCopy {
		t.Fatalf("GetStringChars(%q) = (%p, isCopy=%v)", value, chars, isCopy)
	}
	want := utf16.Encode([]rune(value))
	got := unsafe.Slice((*uint16)(chars), len(want)+1)
	for i, unit := range want {
		if got[i] != unit {
			t.Fatalf("GetStringChars(%q)[%d] = %#x, want %#x", value, i, got[i], unit)
		}
	}
	if got[len(want)] != 0 {
		t.Fatalf("GetStringChars(%q) missing trailing zero: %#x", value, got[len(want)])
	}
	testReleaseStringChars(vm.envRaw, js, chars)
}

func TestGetStringCharsDirectUTF16Encoding(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"",
		"plain ASCII",
		"héllo 世界",
		"a😀b𐐷",
		"embedded\x00zero",
		string([]byte{0xff, 'x', 0xc0, 0xaf, 0xe2, 0x28, 0xa1}),
	} {
		checkStringCharsAgainstEncode(t, vm, value)
	}
	if chars, isCopy := testGetStringChars(vm.envRaw, 1<<30); chars != nil || !isCopy {
		t.Fatalf("invalid GetStringChars = (%p, isCopy=%v), want (nil, true)", chars, isCopy)
	}
}

func TestGetStringCharsDirectUTF16EncodingRandomized(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(0x5449505359))
	for i := 0; i < 128; i++ {
		data := make([]byte, rng.Intn(257))
		for j := range data {
			data[j] = byte(rng.Intn(256))
		}
		value := string(data)
		for repeat := 0; repeat < 3; repeat++ {
			checkStringCharsAgainstEncode(t, vm, value)
		}
	}
}

func TestGetStringCharsUsesOnlyItsFinalUTF16Buffer(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		value      string
		units      []uint16
		wantRaw    bool
		wantAllocs float64
	}{
		{
			name:       "ordinary Unicode string",
			value:      "normal \x00 text \U0001f600 \u4e16\u754c",
			wantAllocs: 0,
		},
		{
			name:       "retained unpaired surrogates",
			units:      []uint16{0x0041, 0x0000, 0xd800, 0x0042, 0xdc00},
			wantRaw:    true,
			wantAllocs: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var id int64
			if tc.units == nil {
				id = testNewUTF16String(vm, tc.value)
			} else {
				vm.mu.Lock()
				id = vm.newStringUTF16On(vm.envRaw, tc.units).id
				vm.mu.Unlock()
			}
			o := vm.get(id)
			if o == nil {
				t.Fatal("string object")
			}
			if got := o.utf16 != nil; got != tc.wantRaw {
				t.Fatalf("retained raw UTF-16 = %v, want %v", got, tc.wantRaw)
			}
			want := tc.units
			if want == nil {
				want = utf16.Encode([]rune(tc.value))
			}

			// C.malloc owns the returned JNI buffer and is intentionally outside
			// Go's allocation count. A nonzero count here would mean the normal
			// path rebuilt a transient UTF-16 slice instead of using that buffer.
			allocs := testing.AllocsPerRun(100, func() {
				chars, isCopy := testGetStringChars(vm.envRaw, id)
				if chars == nil || !isCopy {
					t.Fatal("GetStringChars")
				}
				testReleaseStringChars(vm.envRaw, id, chars)
			})
			if allocs != tc.wantAllocs {
				t.Fatalf("GetStringChars allocations/run = %v, want %v", allocs, tc.wantAllocs)
			}

			chars, isCopy := testGetStringChars(vm.envRaw, id)
			if chars == nil || !isCopy {
				t.Fatal("GetStringChars final check")
			}
			got := unsafe.Slice((*uint16)(chars), len(want)+1)
			for i, unit := range want {
				if got[i] != unit {
					t.Fatalf("unit[%d] = %#x, want %#x", i, got[i], unit)
				}
			}
			if got[len(want)] != 0 {
				t.Fatalf("terminator = %#x, want 0", got[len(want)])
			}
			testReleaseStringChars(vm.envRaw, id, chars)
		})
	}
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

func TestReleaseArrayElementsCommitDoesNotFree(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	arr := testNewByteObject(vm, []byte{1, 2, 3, 4})
	p, _ := testGetArrayElements(vm.envRaw, arr)
	sl := unsafe.Slice((*byte)(p), 4)
	sl[0] = 9
	// mode 1 == JNI_COMMIT: copy back and keep the caller's buffer.
	testReleaseArrayElements(vm.envRaw, arr, p, 1)
	if got := vm.get(arr).bytes[0]; got != 9 {
		t.Fatalf("JNI_COMMIT copy-back = %d, want 9", got)
	}
	// The caller still owns the buffer after COMMIT: further writes must not
	// fault, and a later ABORT release (mode 2) frees without copying.
	sl[0] = 8
	testReleaseArrayElements(vm.envRaw, arr, p, 2)
	if got := vm.get(arr).bytes[0]; got != 9 {
		t.Fatalf("JNI_ABORT must not copy back: %d, want 9", got)
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
