// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import (
	"math"
	"sync/atomic"
	"unsafe"
)

type familyFn func(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool)

func familyFmod(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchFmodAudio(o, class, name, sig, args)
}

func familyInput(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchInput(o, class, name, sig, args)
}

func familyConnectivity(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchConnectivity(o, class, name, sig, args)
}

func familyInsets(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	_ = o
	return vm.dispatchInsets(class, name, sig, args)
}

func familyNativeHelper(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchNativeHelper(o, class, name, sig, args)
}

func familyAuthCookies(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchAuthCookies(o, class, name, sig, args)
}

func familyTextInput(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchTextInput(o, class, name, sig, args)
}

func familyTextConnection(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchTextConnection(o, class, name, sig, args)
}

func familyLuaTextBox(vm *VM, o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	return vm.dispatchLuaTextBox(o, class, name, sig, args)
}

var dispatchFamilies = []familyFn{
	familyFmod,
	familyInput,
	familyConnectivity,
	familyInsets,
	familyNativeHelper,
	familyAuthCookies,
	familyTextInput,
	familyTextConnection,
	familyLuaTextBox,
}

func initCallHandler(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool) {
	_ = vm
	_ = env
	_ = args
	_ = retKind
	return obj, true
}

func stubFallback(vm *VM, class, name, sig string, args *C.jvalue, retKind rune) (C.jobject, bool) {
	logStubDispatch(class, name, sig, retKind)
	logLifecycleStubArgs(class, name, sig, args)
	return vm.stubCall(class, name, sig, C.jint(retKind)), false
}

func wrapFamily(fn familyFn, class, name, sig string) callHandler {
	return func(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool) {
		_ = env
		o := vm.get(jobjectToID(uintptr(obj)))
		if v, ok := fn(vm, o, class, name, sig, args); ok {
			return v, true
		}
		return stubFallback(vm, class, name, sig, args, retKind)
	}
}

func wrapFieldGetter(name, sig string) callHandler {
	return func(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool) {
		_ = env
		_ = args
		o := vm.get(jobjectToID(uintptr(obj)))
		if v, ok := vm.fieldGetter(o, name, sig); ok {
			return v, true
		}
		return stubFallback(vm, oClassName(o), name, sig, args, retKind)
	}
}

func wrapStub(class, name, sig string) callHandler {
	return func(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool) {
		_ = env
		o := vm.get(jobjectToID(uintptr(obj)))
		if class == motionEventClass || class == keyEventClass {
			if v, ok := vm.dispatchInput(o, class, name, sig, args); ok {
				return v, true
			}
		}
		if o != nil {
			if v, ok := vm.fieldGetter(o, name, sig); ok {
				return v, true
			}
		}
		return stubFallback(vm, class, name, sig, args, retKind)
	}
}

func oClassName(o *Object) string {
	if o != nil && o.class != nil {
		return o.class.name
	}
	return ""
}

func (vm *VM) resolveDispatch(env unsafe.Pointer, obj C.jobject, class, name, sig string, args *C.jvalue) (C.jobject, bool, callHandler) {
	if name == "<init>" {
		return obj, true, initCallHandler
	}
	o := vm.get(jobjectToID(uintptr(obj)))
	for _, fn := range dispatchFamilies {
		if v, ok := fn(vm, o, class, name, sig, args); ok {
			return v, true, wrapFamily(fn, class, name, sig)
		}
	}
	if h := lookupCoreHandler(name, sig); h != nil {
		v, ok := h(vm, env, obj, args, 0)
		return v, ok, h
	}
	if o != nil {
		if v, ok := vm.fieldGetter(o, name, sig); ok {
			return v, true, wrapFieldGetter(name, sig)
		}
	}
	return jnull(), false, wrapStub(class, name, sig)
}

func (vm *VM) dispatch(obj C.jobject, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	env := unsafe.Pointer(nil)
	if vm != nil {
		env = vm.envRaw
	}
	v, handled, _ := vm.resolveDispatch(env, obj, class, name, sig, args)
	return v, handled
}

func resolveCallClass(vm *VM, info *internedMethod, obj C.jobject, clazz C.jclass, isStatic C.jint) string {
	if info != nil && info.class != "" {
		return info.class
	}
	if isStatic != 0 {
		return classNameOf(vm, clazz)
	}
	o := vm.get(jobjectToID(uintptr(obj)))
	if o != nil && o.class != nil {
		return o.class.name
	}
	return ""
}

func packCallResult(out *C.jvalue, retKind C.jint, v C.jobject) {
	switch rune(retKind) {
	case 'L':
		jvalueSetL(out, v)
	case 'I', 'B', 'C', 'S', 'Z':
		jvalueSetI(out, C.jint(v))
	case 'J':
		jvalueSetJ(out, C.jlong(v))
	case 'F':
		jvalueSetF(out, C.jfloat(math.Float32frombits(uint32(uintptr(v)))))
	default:
	}
}

type callAResult struct {
	l int64
	i int32
	j int64
}

func (vm *VM) callA(objID int64, mid C.jmethodID, isStatic int, retKind rune) callAResult {
	var out C.jvalue
	GoJNI_CallA((*C.JNIEnv)(vm.envRaw), idToJobject(objID), jclassNull(), mid, nil, C.jint(isStatic), C.jint(retKind), &out)
	return callAResult{
		l: jobjectToID(uintptr(jvalueL(&out))),
		i: int32(jvalueI(&out)),
		j: int64(jvalueJ(&out)),
	}
}

func (m *internedMethod) wrapHandlerHits(hits *atomic.Int32) {
	orig := m.loadHandler()
	if orig == nil {
		return
	}
	m.handler.Store(callHandler(func(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool) {
		hits.Add(1)
		return orig(vm, env, obj, args, retKind)
	}))
}
