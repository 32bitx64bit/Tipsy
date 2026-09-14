// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func resetStringDiagnosticsTest(t testing.TB, enabled bool) {
	t.Helper()
	SetStringDiagnostics(false)
	_ = StringDiagnosticsSnapshot(true)
	SetStringDiagnostics(enabled)
	t.Cleanup(func() {
		SetStringDiagnostics(false)
		_ = StringDiagnosticsSnapshot(true)
	})
}

func diagnosticFixture(t testing.TB) (*VM, *Env, uintptr, uintptr, uintptr, uintptr) {
	t.Helper()
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	if env == nil {
		t.Fatal("nil Env")
	}
	value := env.NewString("diagnostic fixture")
	if value == 0 {
		t.Fatal("NewString fixture")
	}
	cls := env.FindClass("test/DiagnosticField")
	obj := env.AllocObject(cls)
	if cls == 0 || obj == 0 {
		t.Fatalf("field fixture class=%#x object=%#x", cls, obj)
	}
	env.PutField(obj, "text", "diagnostic field")
	field := testDiagnosticFieldID(vm.envRaw, cls, "text", "Ljava/lang/String;")
	if field == 0 {
		t.Fatal("GetFieldID")
	}
	return vm, env, value, cls, obj, field
}

func TestJNIStringDiagnosticsDefaultOff(t *testing.T) {
	resetStringDiagnosticsTest(t, false)
	vm, env, value, cls, obj, field := diagnosticFixture(t)
	if p := testDiagnosticGetChars(vm.envRaw, value); p != nil {
		testDiagnosticReleaseChars(vm.envRaw, value, p)
	}
	if p := testDiagnosticGetCritical(vm.envRaw, value); p != nil {
		testDiagnosticReleaseCritical(vm.envRaw, value, p)
	}
	if p := testDiagnosticGetUTFChars(vm.envRaw, value); p != nil {
		testDiagnosticReleaseUTFChars(vm.envRaw, value, p)
	}
	if got := env.NewStringUTF("default-off"); got == 0 {
		t.Fatal("NewStringUTF")
	}
	if !testDiagnosticInstanceOf(vm.envRaw, obj, cls) {
		t.Fatal("IsInstanceOf")
	}
	if got := testDiagnosticGetObjectField(vm.envRaw, obj, cls, field); got == 0 {
		t.Fatal("GetObjectField")
	}
	if got := StringDiagnosticsSnapshot(false); got != (JNIStringDiagnostics{}) {
		t.Fatalf("default-off diagnostics changed: %+v", got)
	}
}

func TestJNIStringDiagnosticsExactVTableMappingAndAggregation(t *testing.T) {
	resetStringDiagnosticsTest(t, true)
	vm, env, value, cls, obj, field := diagnosticFixture(t)
	const iterations = JNIStringDurationSampleEvery
	const fixtureUTF8Bytes = uint64(len("diagnostic fixture"))
	const fieldUTF8Bytes = uint64(len("diagnostic field"))
	fixtureUTF16Bytes := uint64(utf16UnitCount("diagnostic fixture") * int(unsafe.Sizeof(uint16(0))))

	for range iterations {
		chars := testDiagnosticGetChars(vm.envRaw, value)
		if chars == nil {
			t.Fatal("GetStringChars")
		}
		testDiagnosticReleaseChars(vm.envRaw, value, chars)

		critical := testDiagnosticGetCritical(vm.envRaw, value)
		if critical == nil {
			t.Fatal("GetStringCritical")
		}
		testDiagnosticReleaseCritical(vm.envRaw, value, critical)

		utf := testDiagnosticGetUTFChars(vm.envRaw, value)
		if utf == nil {
			t.Fatal("GetStringUTFChars")
		}
		testDiagnosticReleaseUTFChars(vm.envRaw, value, utf)

		if got := env.NewStringUTF("diagnostic fixture"); got == 0 {
			t.Fatal("NewStringUTF")
		}
		if !testDiagnosticInstanceOf(vm.envRaw, obj, cls) {
			t.Fatal("IsInstanceOf")
		}
		if got := testDiagnosticGetObjectField(vm.envRaw, obj, cls, field); got == 0 {
			t.Fatal("GetObjectField")
		}
	}

	got := StringDiagnosticsSnapshot(false)
	assertDiagnosticPath := func(path JNIStringDiagnosticPath, wantCalls uint64) JNIStringPathStats {
		t.Helper()
		stats := got.Paths[path]
		if stats.Calls != wantCalls || stats.DurationSamples != 1 {
			t.Fatalf("path %d calls/samples = %d/%d, want %d/1", path, stats.Calls, stats.DurationSamples, wantCalls)
		}
		var sampled uint64
		for _, count := range stats.DurationBuckets {
			sampled += count
		}
		if sampled != stats.DurationSamples {
			t.Fatalf("path %d bucket samples=%d, want %d", path, sampled, stats.DurationSamples)
		}
		return stats
	}
	chars := assertDiagnosticPath(JNIStringGetStringChars, iterations)
	critical := assertDiagnosticPath(JNIStringGetStringCritical, iterations)
	utf := assertDiagnosticPath(JNIStringGetStringUTFChars, iterations)
	newUTF := assertDiagnosticPath(JNIStringNewStringUTF, iterations)
	instance := assertDiagnosticPath(JNIStringIsInstanceOf, iterations)
	fieldStats := assertDiagnosticPath(JNIStringFieldGetterString, iterations)
	for _, path := range []JNIStringDiagnosticPath{JNIStringReleaseStringChars, JNIStringReleaseStringUTF, JNIStringReleaseStringCritical} {
		stats := assertDiagnosticPath(path, iterations)
		if stats.Succeeded != 0 || stats.InputUTF8Bytes != 0 || stats.OutputUTF8Bytes != 0 || stats.OutputUTF16Bytes != 0 || stats.CAllocatedBytes != 0 || stats.CopiedBytes != 0 || stats.StringObjects != 0 {
			t.Fatalf("release path %d retained unsupported payload metric: %+v", path, stats)
		}
	}
	for _, stats := range []JNIStringPathStats{chars, critical} {
		if stats.Succeeded != iterations || stats.OutputUTF16Bytes != iterations*fixtureUTF16Bytes || stats.CAllocatedBytes != iterations*(fixtureUTF16Bytes+2) || stats.CopiedBytes != iterations*fixtureUTF16Bytes {
			t.Fatalf("UTF-16 get aggregate = %+v", stats)
		}
	}
	if utf.Succeeded != iterations || utf.OutputUTF8Bytes != iterations*fixtureUTF8Bytes || utf.CAllocatedBytes != iterations*(fixtureUTF8Bytes+1) || utf.CopiedBytes != iterations*fixtureUTF8Bytes {
		t.Fatalf("UTF get aggregate = %+v", utf)
	}
	if newUTF.Succeeded != iterations || newUTF.InputUTF8Bytes != iterations*fixtureUTF8Bytes || newUTF.CopiedBytes != iterations*fixtureUTF8Bytes || newUTF.StringObjects != iterations {
		t.Fatalf("NewStringUTF aggregate = %+v", newUTF)
	}
	if instance.Succeeded != iterations || instance.InputUTF8Bytes != 0 || instance.OutputUTF8Bytes != 0 || instance.StringObjects != 0 {
		t.Fatalf("IsInstanceOf aggregate = %+v", instance)
	}
	if fieldStats.Succeeded != iterations || fieldStats.OutputUTF8Bytes != iterations*fieldUTF8Bytes || fieldStats.StringObjects != iterations || fieldStats.CopiedBytes != 0 {
		t.Fatalf("field String aggregate = %+v", fieldStats)
	}
}

func TestJNIStringDiagnosticsResetAndConcurrentSnapshots(t *testing.T) {
	resetStringDiagnosticsTest(t, true)
	vm, _, value, _, _, _ := diagnosticFixture(t)
	const workers = 6
	const perWorker = 128
	var observed atomic.Uint64
	var done atomic.Bool
	var snapshots sync.WaitGroup
	snapshots.Add(1)
	go func() {
		defer snapshots.Done()
		for !done.Load() {
			observed.Add(StringDiagnosticsSnapshot(true).Paths[JNIStringGetStringChars].Calls)
			runtime.Gosched()
		}
	}()
	var work sync.WaitGroup
	work.Add(workers)
	for range workers {
		go func() {
			defer work.Done()
			for range perWorker {
				chars := testDiagnosticGetChars(vm.envRaw, value)
				if chars == nil {
					t.Error("concurrent GetStringChars")
					return
				}
				testDiagnosticReleaseChars(vm.envRaw, value, chars)
			}
		}()
	}
	work.Wait()
	done.Store(true)
	snapshots.Wait()
	observed.Add(StringDiagnosticsSnapshot(true).Paths[JNIStringGetStringChars].Calls)
	if got, want := observed.Load(), uint64(workers*perWorker); got != want {
		t.Fatalf("concurrent reset snapshots observed %d calls, want %d", got, want)
	}
	if got := StringDiagnosticsSnapshot(false); got != (JNIStringDiagnostics{}) {
		t.Fatalf("reset left diagnostics: %+v", got)
	}
}

func TestJNIStringDiagnosticsSnapshotCannotContainContent(t *testing.T) {
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		t.Helper()
		switch typ.Kind() {
		case reflect.String:
			t.Fatalf("diagnostic snapshot exposes string type at %v", typ)
		case reflect.Array:
			walk(typ.Elem())
		case reflect.Struct:
			for field := range typ.NumField() {
				walk(typ.Field(field).Type)
			}
		}
	}
	walk(reflect.TypeOf(JNIStringDiagnostics{}))
}

func BenchmarkJNIStringDiagnosticsObserver(b *testing.B) {
	vm, _, value, _, _, _ := diagnosticFixture(b)
	for _, enabled := range []bool{false, true} {
		b.Run(map[bool]string{false: "off", true: "on"}[enabled], func(b *testing.B) {
			resetStringDiagnosticsTest(b, enabled)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				chars := testDiagnosticGetChars(vm.envRaw, value)
				if chars == nil {
					b.Fatal("GetStringChars")
				}
				testDiagnosticReleaseChars(vm.envRaw, value, chars)
			}
			b.StopTimer()
			_ = StringDiagnosticsSnapshot(true)
		})
	}
}
