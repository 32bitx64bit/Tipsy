// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64 && tipsy_perfbench

package jni

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf16"
)

const nativePerfDispatchExpected = int64(8 << 30)

var nativePerfFixtureSerial atomic.Uint64

type nativePerfFixture struct {
	vm            *VM
	env           *Env
	objectA       uintptr
	objectB       uintptr
	dispatchClass uintptr
	dispatchMID   uintptr
	instanceClass uintptr
	fieldMID      uintptr
	fieldClass    string
}

func newNativePerfFixture(tb testing.TB) *nativePerfFixture {
	tb.Helper()
	runtime.LockOSThread()
	vm, err := NewVM()
	if err != nil {
		runtime.UnlockOSThread()
		tb.Fatal(err)
	}
	f := &nativePerfFixture{vm: vm, env: vm.Env()}
	var rc int
	f.fieldClass = fmt.Sprintf("tipsy/perf/FieldFixture%d", nativePerfFixtureSerial.Add(1))
	vm.mu.Lock()
	vm.ensureClassLocked(f.fieldClass)
	vm.mu.Unlock()
	if f.objectA, rc = nativePerfMakeFieldObject(f.env, f.fieldClass); rc != nativePerfOK || f.objectA == 0 {
		f.close()
		tb.Fatalf("make first native global object = (%#x, %d)", f.objectA, rc)
	}
	if f.objectB, rc = nativePerfMakeFieldObject(f.env, f.fieldClass); rc != nativePerfOK || f.objectB == 0 || f.objectB == f.objectA {
		f.close()
		tb.Fatalf("make second native global object = (%#x, %d)", f.objectB, rc)
	}
	if !nativePerfSetStringField(vm, f.objectA, "first field value 😀") || !nativePerfSetStringField(vm, f.objectB, "second field value é") {
		f.close()
		tb.Fatal("seed native field-getter fixtures")
	}
	if f.dispatchClass, f.dispatchMID, rc = nativePerfMakeDispatch(f.env); rc != nativePerfOK || f.dispatchClass == 0 || f.dispatchMID == 0 {
		f.close()
		tb.Fatalf("make native dispatch fixture = (%#x, %#x, %d)", f.dispatchClass, f.dispatchMID, rc)
	}
	if f.instanceClass, rc = nativePerfMakeClass(f.env, "java/lang/Object"); rc != nativePerfOK || f.instanceClass == 0 {
		f.close()
		tb.Fatalf("make native instance target class = (%#x, %d)", f.instanceClass, rc)
	}
	if f.fieldMID, rc = nativePerfMakeFieldMethod(f.env, f.fieldClass); rc != nativePerfOK || f.fieldMID == 0 {
		f.close()
		tb.Fatalf("make native field-getter method = (%#x, %d)", f.fieldMID, rc)
	}
	return f
}

func (f *nativePerfFixture) close() {
	if f == nil {
		return
	}
	nativePerfDeleteGlobal(f.env, f.dispatchClass)
	nativePerfDeleteGlobal(f.env, f.instanceClass)
	nativePerfDeleteGlobal(f.env, f.objectB)
	nativePerfDeleteGlobal(f.env, f.objectA)
	runtime.UnlockOSThread()
}

func (f *nativePerfFixture) run(cfg nativePerfConfig) nativePerfResult {
	return nativePerfRun(f.vm, cfg)
}

func nativePerfLocalRefCount(vm *VM, ref uintptr) int32 {
	if vm == nil || ref == 0 {
		return -1
	}
	vm.mu.RLock()
	o := vm.objects[jobjectToID(ref)]
	vm.mu.RUnlock()
	if o == nil {
		return -1
	}
	return o.localRefs.Load()
}

func nativePerfStringMeasurement(s string) (int64, uint64) {
	units := utf16.Encode([]rune(s))
	var checksum uint64
	for _, unit := range units {
		checksum += uint64(unit)
	}
	return int64(len(units)), checksum
}

func nativePerfFieldConfig(f *nativePerfFixture, iterations uint64, value string) nativePerfConfig {
	units, checksum := nativePerfStringMeasurement(value)
	return nativePerfConfig{
		iterations:       iterations,
		objectA:          f.objectA,
		dispatchMethod:   f.fieldMID,
		expected:         units,
		expectedChecksum: checksum,
		kind:             nativePerfFieldGetterString,
	}
}

func nativePerfStringConfig(ref uintptr, iterations uint64, value string) nativePerfConfig {
	units, checksum := nativePerfStringMeasurement(value)
	return nativePerfConfig{
		iterations:       iterations,
		objectA:          ref,
		expected:         units,
		expectedChecksum: checksum,
		kind:             nativePerfStringChars,
	}
}

func nativePerfNewStringConfig(iterations uint64, value string) nativePerfConfig {
	return nativePerfConfig{
		iterations: iterations,
		stringUTF:  value,
		expected:   int64(utf16UnitCount(value)),
		kind:       nativePerfNewStringUTF,
	}
}

func TestJNINativePerfFixtureMUTF8EncodingAndSetup(t *testing.T) {
	if got, ok := nativePerfModifiedUTF8("A\x00é😀B"); !ok || !sameBytes(got, []byte{'A', 0xc0, 0x80, 0xc3, 0xa9, 0xed, 0xa0, 0xbd, 0xed, 0xb8, 0x80, 'B'}) {
		t.Fatalf("fixture Modified UTF-8 = %x (ok=%t), want exact MUTF-8 payload", got, ok)
	}

	f := newNativePerfFixture(t)
	defer f.close()
	const iterations = uint64(7)
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "Unicode", value: "héllo 世界"},
		{name: "EmbeddedNUL", value: "before\x00after"},
		{name: "SupplementaryEmoji", value: "mixed Unicode 😀"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, rc := nativePerfMakeString(f.env, tc.value)
			if rc != nativePerfOK || ref == 0 {
				t.Fatalf("make native string fixture = (%#x, %d)", ref, rc)
			}
			defer nativePerfDeleteGlobal(f.env, ref)

			requireNativePerf(t, f.run(nativePerfStringConfig(ref, iterations, tc.value)), iterations)
			requireNativePerf(t, f.run(nativePerfNewStringConfig(iterations, tc.value)), iterations)
		})
	}
}

func requireNativePerf(t testing.TB, got nativePerfResult, operations uint64) {
	t.Helper()
	if got.status != nativePerfOK || got.attachRC != 0 || got.detachRC != 0 {
		t.Fatalf("native JNI run = %+v", got)
	}
	if got.operations != operations {
		t.Fatalf("native JNI operations = %d, want %d (%+v)", got.operations, operations, got)
	}
}

func TestJNINativePerfHarnessCorrectness(t *testing.T) {
	f := newNativePerfFixture(t)
	defer f.close()
	const iterations = uint64(257)

	before := attachedThreadCountForTest()
	cases := []struct {
		name      string
		cfg       nativePerfConfig
		checksum  uint64
		lastValue uintptr
	}{
		{
			name: "ExceptionClear",
			cfg:  nativePerfConfig{iterations: iterations, kind: nativePerfExceptionClear},
		},
		{
			name:      "ExceptionPending",
			cfg:       nativePerfConfig{iterations: iterations, kind: nativePerfExceptionPending},
			checksum:  iterations,
			lastValue: 1,
		},
		{
			name:      "LocalRefPair",
			cfg:       nativePerfConfig{iterations: iterations, objectA: f.objectA, kind: nativePerfLocalRefPair},
			checksum:  iterations * uint64(f.objectA),
			lastValue: f.objectA,
		},
		{
			name:      "DispatchCoreHit",
			cfg:       nativePerfConfig{iterations: iterations, dispatchClass: f.dispatchClass, dispatchMethod: f.dispatchMID, expected: nativePerfDispatchExpected, kind: nativePerfDispatchCoreHit},
			checksum:  uint64(iterations) * uint64(nativePerfDispatchExpected),
			lastValue: uintptr(nativePerfDispatchExpected),
		},
		{
			name:      "IsSameObjectEqual",
			cfg:       nativePerfConfig{iterations: iterations, objectA: f.objectA, objectB: f.objectA, expected: 1, kind: nativePerfIsSameObject},
			checksum:  iterations,
			lastValue: 1,
		},
		{
			name: "IsSameObjectUnequal",
			cfg:  nativePerfConfig{iterations: iterations, objectA: f.objectA, objectB: f.objectB, expected: 0, kind: nativePerfIsSameObject},
		},
		{
			name: "IsSameObjectNull",
			cfg:  nativePerfConfig{iterations: iterations, objectA: f.objectA, objectB: 0, expected: 0, kind: nativePerfIsSameObject},
		},
		{
			name:      "IsInstanceOfObject",
			cfg:       nativePerfConfig{iterations: iterations, objectA: f.objectA, dispatchClass: f.instanceClass, expected: 1, kind: nativePerfIsInstanceOf},
			checksum:  iterations,
			lastValue: 1,
		},
		{
			name:      "IsInstanceOfNull",
			cfg:       nativePerfConfig{iterations: iterations, objectA: 0, dispatchClass: f.instanceClass, expected: 1, kind: nativePerfIsInstanceOf},
			checksum:  iterations,
			lastValue: 1,
		},
		{
			name:      "GetVersion",
			cfg:       nativePerfConfig{iterations: iterations, expected: JNIVersion16, kind: nativePerfGetVersion},
			checksum:  uint64(iterations) * uint64(JNIVersion16),
			lastValue: uintptr(JNIVersion16),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localRefsBefore := int32(-1)
			if tc.cfg.kind == nativePerfLocalRefPair {
				localRefsBefore = nativePerfLocalRefCount(f.vm, f.objectA)
				if localRefsBefore < 0 {
					t.Fatal("local-ref fixture disappeared before the native loop")
				}
			}
			instanceRefsBefore := int32(-1)
			if tc.cfg.kind == nativePerfIsInstanceOf {
				instanceRefsBefore = nativePerfLocalRefCount(f.vm, f.instanceClass)
				if instanceRefsBefore < 0 {
					t.Fatal("instance target class disappeared before the native loop")
				}
			}
			got := f.run(tc.cfg)
			requireNativePerf(t, got, iterations)
			if got.checksum != tc.checksum || got.lastValue != tc.lastValue {
				t.Fatalf("native JNI result = %+v, want checksum=%d last=%#x", got, tc.checksum, tc.lastValue)
			}
			if localRefsBefore >= 0 {
				if localRefsAfter := nativePerfLocalRefCount(f.vm, f.objectA); localRefsAfter != localRefsBefore {
					t.Fatalf("NewLocalRef/DeleteLocalRef pair left %d local refs, want %d", localRefsAfter, localRefsBefore)
				}
			}
			if instanceRefsBefore >= 0 {
				if instanceRefsAfter := nativePerfLocalRefCount(f.vm, f.instanceClass); instanceRefsAfter != instanceRefsBefore {
					t.Fatalf("IsInstanceOf left %d local refs, want %d", instanceRefsAfter, instanceRefsBefore)
				}
			}
		})
	}
	if got := attachedThreadCountForTest(); got != before {
		t.Fatalf("attached native environments after benchmark harness = %d, want %d", got, before)
	}
}

func TestJNINativePerfThreadExceptionIsolationAndCleanup(t *testing.T) {
	f := newNativePerfFixture(t)
	defer f.close()
	before := attachedThreadCountForTest()
	got := nativePerfCheckThreadExceptionIsolation(f.vm)
	requireNativePerf(t, got, 2)
	if got.checksum != 2 || got.lastValue != 0x100 {
		t.Fatalf("two native thread exception states = %+v, want clear=0 pending=1", got)
	}
	if after := attachedThreadCountForTest(); after != before {
		t.Fatalf("attached native environments after isolation test = %d, want %d", after, before)
	}
}

func TestJNINativeFieldGetterVTableColdWarmAndConcurrent(t *testing.T) {
	f := newNativePerfFixture(t)
	defer f.close()
	const iterations = uint64(193)
	first := "first field value 😀"
	if nativePerfMethodCached(f.fieldMID) {
		t.Fatal("field handler unexpectedly warm before first native vtable call")
	}
	cold := f.run(nativePerfFieldConfig(f, 1, first))
	requireNativePerf(t, cold, 1)
	if !nativePerfMethodCached(f.fieldMID) {
		t.Fatal("first native CallObjectMethodA did not cache the field handler")
	}
	warm := f.run(nativePerfFieldConfig(f, iterations, first))
	requireNativePerf(t, warm, iterations)
	updatedFirst := "first refreshed value 😀"
	if !nativePerfSetStringField(f.vm, f.objectA, updatedFirst) {
		t.Fatal("replace first receiver field")
	}

	beforeAttached := attachedThreadCountForTest()
	results := make(chan nativePerfResult, 2)
	go func() { results <- f.run(nativePerfFieldConfig(f, iterations, updatedFirst)) }()
	go func() {
		units, checksum := nativePerfStringMeasurement("second field value é")
		results <- f.run(nativePerfConfig{
			iterations:       iterations,
			objectA:          f.objectB,
			dispatchMethod:   f.fieldMID,
			expected:         units,
			expectedChecksum: checksum,
			kind:             nativePerfFieldGetterString,
		})
	}()
	for range 2 {
		requireNativePerf(t, <-results, iterations)
	}
	if afterAttached := attachedThreadCountForTest(); afterAttached != beforeAttached {
		t.Fatalf("field getter native-thread cleanup = %d attached, want %d", afterAttached, beforeAttached)
	}
}

func runNativePerfBenchmark(b *testing.B, cfg func(*nativePerfFixture, uint64) nativePerfConfig) {
	b.Helper()
	f := newNativePerfFixture(b)
	b.Cleanup(f.close)
	b.ResetTimer()
	// Go's ns/op includes the one native pthread lifecycle for each benchmark
	// invocation. native-ns/op is the authoritative C monotonic interval: it
	// contains only the repeated indirect JNIEnv calls below.
	got := f.run(cfg(f, uint64(b.N)))
	b.StopTimer()
	if got.status != nativePerfOK || got.operations != uint64(b.N) {
		b.Fatalf("native JNI benchmark = %+v", got)
	}
	b.ReportMetric(float64(got.elapsedNS)/float64(got.operations), "native-ns/op")
}

func BenchmarkJNINativeExceptionCheck(b *testing.B) {
	for _, pending := range []bool{false, true} {
		b.Run(map[bool]string{false: "Clear", true: "Pending"}[pending], func(b *testing.B) {
			runNativePerfBenchmark(b, func(_ *nativePerfFixture, n uint64) nativePerfConfig {
				kind := nativePerfExceptionClear
				if pending {
					kind = nativePerfExceptionPending
				}
				return nativePerfConfig{iterations: n, kind: kind}
			})
		})
	}
}

func BenchmarkJNINativeLocalRef(b *testing.B) {
	runNativePerfBenchmark(b, func(f *nativePerfFixture, n uint64) nativePerfConfig {
		return nativePerfConfig{iterations: n, objectA: f.objectA, kind: nativePerfLocalRefPair}
	})
}

func BenchmarkJNINativeDispatchCoreHit(b *testing.B) {
	runNativePerfBenchmark(b, func(f *nativePerfFixture, n uint64) nativePerfConfig {
		return nativePerfConfig{iterations: n, dispatchClass: f.dispatchClass, dispatchMethod: f.dispatchMID, expected: nativePerfDispatchExpected, kind: nativePerfDispatchCoreHit}
	})
}

func BenchmarkJNINativeIsSameObject(b *testing.B) {
	cases := []struct {
		name string
		make func(*nativePerfFixture, uint64) nativePerfConfig
	}{
		{"Equal", func(f *nativePerfFixture, n uint64) nativePerfConfig {
			return nativePerfConfig{iterations: n, objectA: f.objectA, objectB: f.objectA, expected: 1, kind: nativePerfIsSameObject}
		}},
		{"Unequal", func(f *nativePerfFixture, n uint64) nativePerfConfig {
			return nativePerfConfig{iterations: n, objectA: f.objectA, objectB: f.objectB, expected: 0, kind: nativePerfIsSameObject}
		}},
		{"Null", func(f *nativePerfFixture, n uint64) nativePerfConfig {
			return nativePerfConfig{iterations: n, objectA: f.objectA, objectB: 0, expected: 0, kind: nativePerfIsSameObject}
		}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) { runNativePerfBenchmark(b, tc.make) })
	}
}

func BenchmarkJNINativeGetVersion(b *testing.B) {
	runNativePerfBenchmark(b, func(_ *nativePerfFixture, n uint64) nativePerfConfig {
		return nativePerfConfig{iterations: n, expected: JNIVersion16, kind: nativePerfGetVersion}
	})
}

func BenchmarkJNINativeFieldGetterString(b *testing.B) {
	runNativePerfBenchmark(b, func(f *nativePerfFixture, n uint64) nativePerfConfig {
		return nativePerfFieldConfig(f, n, "first field value 😀")
	})
}

func BenchmarkJNINativeStringChars(b *testing.B) {
	cases := []struct {
		name  string
		value string
	}{
		{"Empty", ""},
		{"Short", "roblox"},
		{"MixedUnicode", "héllo 😀 Roblox"},
		{"Large", "Roblox 😀 field value " + strings.Repeat("x", 1024)},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			f := newNativePerfFixture(b)
			ref, rc := nativePerfMakeString(f.env, tc.value)
			if rc != nativePerfOK || ref == 0 {
				f.close()
				b.Fatalf("make native string fixture = (%#x, %d)", ref, rc)
			}
			b.Cleanup(func() {
				nativePerfDeleteGlobal(f.env, ref)
				f.close()
			})
			b.ResetTimer()
			got := f.run(nativePerfStringConfig(ref, uint64(b.N), tc.value))
			b.StopTimer()
			if got.status != nativePerfOK || got.operations != uint64(b.N) {
				b.Fatalf("native JNI GetStringChars/GetRelease benchmark = %+v", got)
			}
			b.ReportMetric(float64(got.elapsedNS)/float64(got.operations), "native-ns/op")
		})
	}
}

func BenchmarkJNINativeStringCharsKnownLength(b *testing.B) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "Empty", value: ""},
		{name: "ShortASCII", value: "short string"},
		{name: "MixedUnicode", value: "héllo 世界 😀"},
		{name: "Large", value: strings.Repeat("abc😀", 256)},
	} {
		b.Run(tc.name, func(b *testing.B) {
			runNativePerfBenchmark(b, func(f *nativePerfFixture, n uint64) nativePerfConfig {
				ref, rc := nativePerfMakeString(f.env, tc.value)
				if rc != nativePerfOK || ref == 0 {
					b.Fatalf("make native string fixture = (%#x, %d)", ref, rc)
				}
				b.Cleanup(func() { nativePerfDeleteGlobal(f.env, ref) })
				cfg := nativePerfStringConfig(ref, n, tc.value)
				cfg.kind = nativePerfStringCharsKnown
				return cfg
			})
		})
	}
}

func BenchmarkJNINativeNewStringUTFDelete(b *testing.B) {
	runNativePerfBenchmark(b, func(_ *nativePerfFixture, n uint64) nativePerfConfig {
		return nativePerfNewStringConfig(n, "new string fixture 😀")
	})
}

func BenchmarkJNINativeIsInstanceOf(b *testing.B) {
	for _, tc := range []struct {
		name string
		obj  func(*nativePerfFixture) uintptr
	}{
		{name: "Object", obj: func(f *nativePerfFixture) uintptr { return f.objectA }},
		{name: "Null", obj: func(*nativePerfFixture) uintptr { return 0 }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			runNativePerfBenchmark(b, func(f *nativePerfFixture, n uint64) nativePerfConfig {
				return nativePerfConfig{iterations: n, objectA: tc.obj(f), dispatchClass: f.instanceClass, expected: 1, kind: nativePerfIsInstanceOf}
			})
		})
	}
}
