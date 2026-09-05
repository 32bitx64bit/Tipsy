// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include <stdint.h>
#include <stdlib.h>

typedef void (*tipsy_current_refresh_fn)(JNIEnv *, jclass, jfloat);
typedef void (*tipsy_supported_refresh_fn)(JNIEnv *, jclass, jfloatArray);

// This function is itself submitted through loader.CallP8, preserving the
// dedicated native-main pthread/stack used by every other Roblox JNI entry.
static int64_t tipsy_publish_display_refresh_rates(void *current_ptr,
	void *supported_ptr, void *env_ptr, void *class_ptr, void *current_hz_ptr,
	void *rates_ptr, void *rates_len_ptr, void *unused) {
	tipsy_current_refresh_fn current_fn = (tipsy_current_refresh_fn)current_ptr;
	tipsy_supported_refresh_fn supported_fn = (tipsy_supported_refresh_fn)supported_ptr;
	JNIEnv *env = (JNIEnv *)env_ptr;
	jclass cls = (jclass)class_ptr;
	jfloat *current_hz = (jfloat *)current_hz_ptr;
	jfloat *rates = (jfloat *)rates_ptr;
	jsize rates_len = (jsize)(uintptr_t)rates_len_ptr;
	(void)unused;
	if (current_fn == NULL || supported_fn == NULL || env == NULL || cls == NULL ||
		current_hz == NULL || rates_len <= 0 || rates == NULL) {
		return -1;
	}
	current_fn(env, cls, *current_hz);
	jfloatArray array = env->functions->NewFloatArray(env, rates_len);
	if (array == NULL) {
		return -2;
	}
	env->functions->SetFloatArrayRegion(env, array, 0, rates_len, rates);
	supported_fn(env, cls, array);
	return 0;
}

static uintptr_t tipsy_display_refresh_publisher_addr(void) {
	return (uintptr_t)tipsy_publish_display_refresh_rates;
}

static jfloat tipsy_test_current_hz;
static jfloat tipsy_test_supported_hz[32];
static jsize tipsy_test_supported_count;

static void tipsy_test_current_refresh(JNIEnv *env, jclass cls, jfloat hz) {
	(void)env;
	(void)cls;
	tipsy_test_current_hz = hz;
}

static void tipsy_test_supported_refresh(JNIEnv *env, jclass cls, jfloatArray rates) {
	(void)cls;
	tipsy_test_supported_count = env->functions->GetArrayLength(env, rates);
	if (tipsy_test_supported_count > 32) {
		tipsy_test_supported_count = 32;
	}
	if (tipsy_test_supported_count > 0) {
		env->functions->GetFloatArrayRegion(env, rates, 0,
			tipsy_test_supported_count, tipsy_test_supported_hz);
	}
}

static uintptr_t tipsy_test_current_refresh_addr(void) {
	return (uintptr_t)tipsy_test_current_refresh;
}

static uintptr_t tipsy_test_supported_refresh_addr(void) {
	return (uintptr_t)tipsy_test_supported_refresh;
}

static void tipsy_test_reset_display_refresh(void) {
	tipsy_test_current_hz = 0;
	tipsy_test_supported_count = 0;
}

static jfloat tipsy_test_recorded_current_refresh(void) {
	return tipsy_test_current_hz;
}

static jsize tipsy_test_recorded_supported_count(void) {
	return tipsy_test_supported_count;
}

static jfloat tipsy_test_recorded_supported_refresh(jsize index) {
	if (index < 0 || index >= tipsy_test_supported_count) {
		return 0;
	}
	return tipsy_test_supported_hz[index];
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

const (
	currentDisplayRefreshRateSym = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassCurrentDisplayRefreshRate"
	supportedRefreshRatesSym     = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassSupportedRefreshRates"
)

func callDisplayRefreshRateExports(env *jni.Env, class, currentFn, supportedFn uintptr, currentHz float32, supportedHz []float32) error {
	if env == nil || env.Raw() == 0 || class == 0 || currentFn == 0 || supportedFn == 0 {
		return fmt.Errorf("display refresh JNI arguments unavailable")
	}
	if currentHz <= 0 || len(supportedHz) == 0 {
		return fmt.Errorf("display refresh rates unavailable")
	}
	current := C.malloc(C.size_t(unsafe.Sizeof(C.jfloat(0))))
	if current == nil {
		return fmt.Errorf("allocate current display refresh rate")
	}
	defer C.free(current)
	*(*C.jfloat)(current) = C.jfloat(currentHz)

	ratesBytes := C.size_t(len(supportedHz)) * C.size_t(unsafe.Sizeof(C.jfloat(0)))
	rates := C.malloc(ratesBytes)
	if rates == nil {
		return fmt.Errorf("allocate supported display refresh rates")
	}
	defer C.free(rates)
	cRates := unsafe.Slice((*C.jfloat)(rates), len(supportedHz))
	for i, hz := range supportedHz {
		cRates[i] = C.jfloat(hz)
	}

	rc := loader.CallP8(uintptr(C.tipsy_display_refresh_publisher_addr()),
		currentFn, supportedFn, env.Raw(), class, uintptr(current), uintptr(rates), uintptr(len(supportedHz)), 0)
	if rc != 0 {
		return fmt.Errorf("publish display refresh rates: native call returned %d", rc)
	}
	return nil
}

func publishDisplayRefreshRates(mod *loader.Module, env *jni.Env, currentHz float32, supportedHz []float32) error {
	if mod == nil || env == nil {
		return fmt.Errorf("display refresh JNI unavailable")
	}
	currentFn, err := mod.Lookup(currentDisplayRefreshRateSym)
	if err != nil {
		return fmt.Errorf("%s: %w", currentDisplayRefreshRateSym, err)
	}
	if currentFn == 0 {
		return fmt.Errorf("%s resolved to nil", currentDisplayRefreshRateSym)
	}
	supportedFn, err := mod.Lookup(supportedRefreshRatesSym)
	if err != nil {
		return fmt.Errorf("%s: %w", supportedRefreshRatesSym, err)
	}
	if supportedFn == 0 {
		return fmt.Errorf("%s resolved to nil", supportedRefreshRatesSym)
	}
	class := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	if err := callDisplayRefreshRateExports(env, class, currentFn, supportedFn, currentHz, supportedHz); err != nil {
		return err
	}
	logging.Logger(logging.CatGraphics).Info("published Android display refresh rates",
		"currentHz", currentHz, "supportedHz", supportedHz)
	return nil
}

func displayRefreshRatesChanged(currentHz float32, supportedHz []float32, nextCurrentHz float32, nextSupportedHz []float32) bool {
	if currentHz != nextCurrentHz || len(supportedHz) != len(nextSupportedHz) {
		return true
	}
	for i := range supportedHz {
		if supportedHz[i] != nextSupportedHz[i] {
			return true
		}
	}
	return false
}

func testDisplayRefreshExportFunctions() (current, supported uintptr) {
	C.tipsy_test_reset_display_refresh()
	return uintptr(C.tipsy_test_current_refresh_addr()), uintptr(C.tipsy_test_supported_refresh_addr())
}

func testDisplayRefreshValues() (float32, []float32) {
	n := int(C.tipsy_test_recorded_supported_count())
	rates := make([]float32, n)
	for i := range rates {
		rates[i] = float32(C.tipsy_test_recorded_supported_refresh(C.jsize(i)))
	}
	return float32(C.tipsy_test_recorded_current_refresh()), rates
}
