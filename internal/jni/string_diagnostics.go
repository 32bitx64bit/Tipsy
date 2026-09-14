// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#include "string_diagnostics.h"
*/
import "C"

import (
	"sync/atomic"
	"time"
	"unsafe"
)

// JNIStringDiagnosticPath identifies one content-free JNI boundary. Names map
// to the JNI vtable entry, never to a Java class, method, object, address, or
// string payload.
type JNIStringDiagnosticPath int

const (
	JNIStringGetStringChars        = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_GET_CHARS)
	JNIStringGetStringUTFChars     = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_GET_UTF_CHARS)
	JNIStringGetStringCritical     = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_GET_CRITICAL)
	JNIStringReleaseStringChars    = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_RELEASE_CHARS)
	JNIStringReleaseStringUTF      = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_RELEASE_UTF_CHARS)
	JNIStringReleaseStringCritical = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_RELEASE_CRITICAL)
	JNIStringNewStringUTF          = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_NEW_UTF)
	JNIStringIsInstanceOf          = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_IS_INSTANCE_OF)
	JNIStringFieldGetterString     = JNIStringDiagnosticPath(C.TIPSY_JNI_STRING_FIELD_GETTER_STRING)

	jniStringDiagnosticPaths            = int(C.TIPSY_JNI_STRING_PATHS)
	jniStringDurationBuckets            = int(C.TIPSY_JNI_STRING_DURATION_BUCKETS)
	JNIStringDurationSampleEvery uint64 = 64
)

// JNIStringDurationBucketUpperNS gives the inclusive upper bound of each
// sampled-duration bucket. The final bucket contains every value above 50 us.
var JNIStringDurationBucketUpperNS = [...]uint64{250, 500, 1_000, 2_000, 5_000, 10_000, 50_000}

// JNIStringPathStats contains fixed-size, content-free aggregate data. The
// byte values describe boundary copies/allocations only; they are never text,
// object, handle, URL, credential, or raw-pointer data.
type JNIStringPathStats struct {
	Calls, Succeeded                   uint64
	InputUTF8Bytes, OutputUTF8Bytes    uint64
	OutputUTF16Bytes, CAllocatedBytes  uint64
	CopiedBytes, StringObjects         uint64
	DurationSamples, SampledDurationNS uint64
	MaxSampledDurationNS               uint64
	DurationBuckets                    [jniStringDurationBuckets]uint64
}

// JNIStringDiagnostics is resettable aggregate instrumentation for JNI string
// and type-test boundaries. A reset snapshot atomically takes each counter;
// concurrent work lands either in that snapshot or the next one.
type JNIStringDiagnostics struct {
	Paths [jniStringDiagnosticPaths]JNIStringPathStats
}

var (
	goStringDiagnosticsEnabled atomic.Bool
	stringFieldSampleSequence  atomic.Uint64
)

// SetStringDiagnostics enables aggregate JNI string diagnostics. It is
// default-off and is normally driven by the existing TIPSY_STUTTER_DIAG
// lifecycle through SetStutterDiagnostics. With it disabled, each Go-only
// field getter has one atomic gate load and no clock, counter update,
// allocation, or logging work.
func SetStringDiagnostics(enabled bool) {
	if !enabled {
		C.tipsy_jni_string_diag_set_enabled(0)
		goStringDiagnosticsEnabled.Store(false)
		stringFieldSampleSequence.Store(0)
		return
	}
	goStringDiagnosticsEnabled.Store(true)
	C.tipsy_jni_string_diag_set_enabled(1)
}

// StringDiagnosticsSnapshot returns fixed-size aggregate counters. reset
// atomically clears the returned interval without retaining contents.
func StringDiagnosticsSnapshot(reset bool) JNIStringDiagnostics {
	var raw C.TipsyJNIStringDiagnostics
	r := C.int(0)
	if reset {
		r = 1
	}
	C.tipsy_jni_string_diag_snapshot(&raw, r)
	paths := (*[jniStringDiagnosticPaths]C.TipsyJNIStringPathStats)(unsafe.Pointer(&raw.paths[0]))
	var out JNIStringDiagnostics
	for path := range out.Paths {
		src := &paths[path]
		dst := &out.Paths[path]
		dst.Calls = uint64(src.calls)
		dst.Succeeded = uint64(src.succeeded)
		dst.InputUTF8Bytes = uint64(src.input_utf8_bytes)
		dst.OutputUTF8Bytes = uint64(src.output_utf8_bytes)
		dst.OutputUTF16Bytes = uint64(src.output_utf16_bytes)
		dst.CAllocatedBytes = uint64(src.c_allocated_bytes)
		dst.CopiedBytes = uint64(src.copied_bytes)
		dst.StringObjects = uint64(src.string_objects)
		dst.DurationSamples = uint64(src.duration_samples)
		dst.SampledDurationNS = uint64(src.sampled_duration_ns)
		dst.MaxSampledDurationNS = uint64(src.max_sampled_duration_ns)
		buckets := (*[jniStringDurationBuckets]C.uint64_t)(unsafe.Pointer(&src.duration_buckets[0]))
		for bucket := range dst.DurationBuckets {
			dst.DurationBuckets[bucket] = uint64(buckets[bucket])
		}
	}
	return out
}

func stringDiagnosticsEnabled() bool {
	return goStringDiagnosticsEnabled.Load()
}

func stringDiagnosticsCurrentPath() (JNIStringDiagnosticPath, bool) {
	if !stringDiagnosticsEnabled() {
		return 0, false
	}
	path := int(C.tipsy_jni_string_diag_current_path())
	if path < 0 || path >= jniStringDiagnosticPaths {
		return 0, false
	}
	return JNIStringDiagnosticPath(path), true
}

func stringDiagnosticsAdd(path JNIStringDiagnosticPath, succeeded, inputUTF8, outputUTF8, outputUTF16, cAllocated, copied, objects uint64) {
	C.tipsy_jni_string_diag_add(C.int(path), C.uint64_t(succeeded), C.uint64_t(inputUTF8),
		C.uint64_t(outputUTF8), C.uint64_t(outputUTF16), C.uint64_t(cAllocated),
		C.uint64_t(copied), C.uint64_t(objects))
}

func stringDiagnosticsAddCurrent(succeeded, inputUTF8, outputUTF8, outputUTF16, cAllocated, copied, objects uint64) {
	if path, ok := stringDiagnosticsCurrentPath(); ok {
		stringDiagnosticsAdd(path, succeeded, inputUTF8, outputUTF8, outputUTF16, cAllocated, copied, objects)
	}
}

type stringFieldDiagnosticToken struct {
	active  bool
	sampled bool
	start   time.Time
}

func beginStringFieldDiagnostics() stringFieldDiagnosticToken {
	if !stringDiagnosticsEnabled() {
		return stringFieldDiagnosticToken{}
	}
	token := stringFieldDiagnosticToken{active: true}
	// Sampling candidates before the map lookup avoids a clock on all but one
	// in 64 object-field reads. Only a completed String-valued getter is then
	// published, so no other field identity or value is exposed.
	token.sampled = stringFieldSampleSequence.Add(1)%JNIStringDurationSampleEvery == 0
	if token.sampled {
		token.start = time.Now()
	}
	return token
}

func (t stringFieldDiagnosticToken) completeStringField(outputUTF8 uint64) {
	if !t.active {
		return
	}
	C.tipsy_jni_string_diag_record_unsampled_call(C.int(JNIStringFieldGetterString))
	stringDiagnosticsAdd(JNIStringFieldGetterString, 1, 0, outputUTF8, 0, 0, 0, 1)
	if t.sampled {
		C.tipsy_jni_string_diag_record_sample(C.int(JNIStringFieldGetterString), C.uint64_t(time.Since(t.start).Nanoseconds()))
	}
}
