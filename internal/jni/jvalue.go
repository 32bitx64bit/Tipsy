// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import "unsafe"

// C jvalue is a union; every member lives at offset 0. These helpers write
// that overlay from Go so CallA / field / argument packing do not cross
// cgo for a store or load.

func jvalueZero(v *C.jvalue) {
	if v != nil {
		*v = C.jvalue{}
	}
}

func jvalueSetL(v *C.jvalue, l C.jobject) {
	if v != nil {
		*(*C.jobject)(unsafe.Pointer(v)) = l
	}
}

func jvalueSetI(v *C.jvalue, i C.jint) {
	if v != nil {
		*(*C.jint)(unsafe.Pointer(v)) = i
	}
}

func jvalueSetJ(v *C.jvalue, x C.jlong) {
	if v != nil {
		*(*C.jlong)(unsafe.Pointer(v)) = x
	}
}

func jvalueSetZ(v *C.jvalue, z C.jboolean) {
	if v != nil {
		*(*C.jboolean)(unsafe.Pointer(v)) = z
	}
}

func jvalueSetF(v *C.jvalue, f C.jfloat) {
	if v != nil {
		*(*C.jfloat)(unsafe.Pointer(v)) = f
	}
}

func jvalueL(v *C.jvalue) C.jobject {
	if v == nil {
		return jnull()
	}
	return *(*C.jobject)(unsafe.Pointer(v))
}

func jvalueI(v *C.jvalue) C.jint {
	if v == nil {
		return 0
	}
	return *(*C.jint)(unsafe.Pointer(v))
}

func jvalueJ(v *C.jvalue) C.jlong {
	if v == nil {
		return 0
	}
	return *(*C.jlong)(unsafe.Pointer(v))
}

func jvalueSlot(args *C.jvalue, i int) *C.jvalue {
	if args == nil || i < 0 {
		return nil
	}
	return (*C.jvalue)(unsafe.Add(unsafe.Pointer(args), i*int(unsafe.Sizeof(C.jvalue{}))))
}

func jvalueLAt(args *C.jvalue, i int) C.jobject {
	return jvalueL(jvalueSlot(args, i))
}
