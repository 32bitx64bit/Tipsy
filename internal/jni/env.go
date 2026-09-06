// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"unsafe"
)

// FindClass looks up or auto-defines a class and returns its handle.
func (e *Env) FindClass(name string) uintptr {
	if e == nil || e.vm == nil {
		return 0
	}
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	cls := C.tipsy_jni_FindClass((*C.JNIEnv)(e.raw), cs)
	return uintptr(cls)
}

// NewStringUTF creates a java/lang/String and returns the jobject.
func (e *Env) NewStringUTF(s string) uintptr {
	if e == nil {
		return 0
	}
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	str := C.tipsy_jni_NewStringUTF((*C.JNIEnv)(e.raw), cs)
	return uintptr(str)
}

// NewString stores s on a java/lang/String without a cgo CString copy.
// Use this for large payloads (client-settings JSON).
func (e *Env) NewString(s string) uintptr {
	if e == nil || e.vm == nil {
		return 0
	}
	e.vm.mu.Lock()
	o := e.vm.newStringLocked(s)
	e.vm.mu.Unlock()
	return uintptr(idToJobject(o.id))
}

// GetStringUTFChars returns the UTF-8 contents of a jstring created by this VM.
func (e *Env) GetStringUTFChars(str uintptr) (string, error) {
	if e == nil || str == 0 {
		return "", nil
	}
	var isCopy C.jboolean
	p := C.tipsy_jni_GetStringUTFChars((*C.JNIEnv)(e.raw), C.jstring(str), &isCopy)
	if p == nil {
		return "", nil
	}
	s := C.GoString(p)
	C.tipsy_jni_ReleaseStringUTFChars((*C.JNIEnv)(e.raw), C.jstring(str), p)
	return s, nil
}

// Raw returns the JNIEnv*.
func (e *Env) Raw() uintptr {
	if e == nil {
		return 0
	}
	return uintptr(e.raw)
}

// AllocObject allocates an uninitialized instance of clazz (FindClass handle).
func (e *Env) AllocObject(clazz uintptr) uintptr {
	if e == nil || clazz == 0 {
		return 0
	}
	obj := C.tipsy_jni_AllocObject((*C.JNIEnv)(e.raw), C.jclass(unsafe.Pointer(clazz)))
	return uintptr(unsafe.Pointer(obj))
}

// NewByteArray allocates a Java byte[] of length n.
func (e *Env) NewByteArray(n int) uintptr {
	if e == nil {
		return 0
	}
	if n < 0 {
		n = 0
	}
	arr := C.tipsy_jni_NewByteArray((*C.JNIEnv)(e.raw), C.jsize(n))
	return uintptr(unsafe.Pointer(arr))
}

// BytesArray allocates a Java byte[] and copies data into it.
func (e *Env) BytesArray(data []byte) uintptr {
	arr := e.NewByteArray(len(data))
	if arr == 0 || e.vm == nil || len(data) == 0 {
		return arr
	}
	o := e.vm.get(jobjectToID(arr))
	if o == nil {
		return arr
	}
	e.vm.mu.Lock()
	if len(o.bytes) < len(data) {
		o.bytes = make([]byte, len(data))
	}
	copy(o.bytes, data)
	e.vm.mu.Unlock()
	return arr
}

// PutField stores a value on a Tipsy jobject for later GetField / AutoValue getters.
func (e *Env) PutField(obj uintptr, name string, val any) {
	if e == nil || e.vm == nil || obj == 0 || name == "" {
		return
	}
	o := e.vm.get(jobjectToID(obj))
	if o == nil {
		return
	}
	className := ""
	if o.class != nil {
		className = o.class.name
	}
	e.vm.mu.Lock()
	if o.fields == nil {
		o.fields = make(map[string]any)
	}
	switch t := val.(type) {
	case uintptr:
		e.vm.storeFieldObjLocked(o, name, jobjectToID(t))
	default:
		o.fields[name] = val
	}
	e.vm.mu.Unlock()
	if id, ok := int64Field(val); ok {
		noteStartGamePlaceID(className, name, id)
	}
}

// BoolField reads a boolean field previously stored on a Tipsy jobject
// (PutField). Missing objects or fields read false.
func (e *Env) BoolField(obj uintptr, name string) bool {
	if e == nil || e.vm == nil || obj == 0 {
		return false
	}
	o := e.vm.get(jobjectToID(obj))
	if o == nil {
		return false
	}
	e.vm.mu.Lock()
	defer e.vm.mu.Unlock()
	v, _ := o.fields[name].(bool)
	return v
}

// NewArrayList allocates an empty java.util.ArrayList (elems stored in Object.elems).
func (e *Env) NewArrayList() uintptr {
	if e == nil || e.vm == nil {
		return 0
	}
	obj := e.AllocObject(e.FindClass("java/util/ArrayList"))
	o := e.vm.get(jobjectToID(obj))
	if o != nil {
		e.vm.mu.Lock()
		o.elems = []int64{}
		e.vm.mu.Unlock()
	}
	return obj
}
