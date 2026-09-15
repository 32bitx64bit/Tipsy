/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later */
#ifndef TIPSY_NATIVE_INPUT_BATCH_H
#define TIPSY_NATIVE_INPUT_BATCH_H
#include "jni_bridge.h"
#include <stddef.h>
#include <stdint.h>

enum { TIPSY_INPUT_BATCH_MAX = 64 };
enum {
 TIPSY_BATCH_MOUSE_BUTTON=1, TIPSY_BATCH_MOUSE_MOVE, TIPSY_BATCH_MOUSE_WHEEL,
 TIPSY_BATCH_MOUSE_LOCKED, TIPSY_BATCH_KEY,
 TIPSY_BATCH_PAD_AXIS, TIPSY_BATCH_PAD_BUTTON, TIPSY_BATCH_PAD_CONNECT,
 TIPSY_BATCH_PAD_DISCONNECT, TIPSY_BATCH_PAD_KEY, TIPSY_BATCH_PAD_MOTION
};
enum { TIPSY_BATCH_OK=0, TIPSY_BATCH_INVALID=-1, TIPSY_BATCH_EXCEPTION=-2 };

/* Pointer-free POD from Go's perspective: fn/env/class are C addresses/opaque
 * JNI identities, NEVER pointers into Go objects. Exact argument placement:
 * mouse-button i[0]=pressed,i[1]=button, f[0..1]=x,y
 * mouse-move f[0..3]=x,y,dx,dy; wheel f[0..2]=x,y,delta
 * key i[0..3]=down,scan,key,repeat
 * pad-axis i[0..1]=dev,axis, f[0..2]=f1,f2,f3
 * pad-button i[0..2]=dev,key,down; connect i[0..1]=dev,type
 * disconnect i[0]=dev; pad-key i[0..3]=dev,key,supported,type
 * pad-motion i[0..4]=dev,axis,arg,supported,type. */
typedef struct TipsyInputCommand {
 uint32_t kind;
 uintptr_t fn;
 uintptr_t clazz;
 int32_t i[5];
 float f[4];
} TipsyInputCommand;

typedef struct TipsyInputBatchResult {
 size_t completed;
 unsigned char mouse_locked;
} TipsyInputBatchResult;

/* No allocations, no retained pointers, synchronous owner-thread transport.
 * Every guest command may callback into Go. DO NOT mark nocallback/noescape.
 * All externally visible input policy and epoch checks belong to the caller. */
int tipsy_jni_input_batch(JNIEnv *env, const TipsyInputCommand *commands,
 size_t count, TipsyInputBatchResult *result);
#endif
