/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "gameactivity_input.h"

// Callers for the GameActivity natives the engine registers
// (RegisterNatives on com/google/androidgamesdk/GameActivity). Signatures
// mirror the registered JNI descriptors exactly; uintptr_t carries
// pointer/int JNI arguments of equal width in the SysV x86-64 ABI.

void tipsy_input_call_void4(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	((void (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t))fn)(a0, a1, a2, a3);
}

unsigned char tipsy_input_call_bool4(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	return ((unsigned char (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t))fn)(a0, a1, a2, a3);
}

// onTouchEventNative(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z
unsigned char tipsy_input_call_touch(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3,
	int i1, int i2, int i3, int i4, int i5,
	long long j1, long long j2,
	int k1, int k2, int k3, int k4, int k5, int k6,
	float f1, float f2) {
	return ((unsigned char (*)(uintptr_t, uintptr_t, uintptr_t, uintptr_t,
		int, int, int, int, int,
		long long, long long,
		int, int, int, int, int, int,
		float, float))fn)(a0, a1, a2, a3,
		i1, i2, i3, i4, i5, j1, j2, k1, k2, k3, k4, k5, k6, f1, f2);
}

// Recording natives: test-only fake engine natives that capture arguments
// so unit tests can verify marshaling without loading libroblox.so. Not
// used outside tests; they store and return 1 (consumed).
static uintptr_t tipsy_rec4_args[4];

void tipsy_input_record_focus(uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	tipsy_rec4_args[0] = a0;
	tipsy_rec4_args[1] = a1;
	tipsy_rec4_args[2] = a2;
	tipsy_rec4_args[3] = a3;
}

unsigned char tipsy_input_record_key(uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3) {
	tipsy_rec4_args[0] = a0;
	tipsy_rec4_args[1] = a1;
	tipsy_rec4_args[2] = a2;
	tipsy_rec4_args[3] = a3;
	return 1;
}

static uintptr_t tipsy_rec_touch_ints[13];
static long long tipsy_rec_touch_longs[2];
static float tipsy_rec_touch_floats[2];
static uintptr_t tipsy_rec_touch_ids[4];

unsigned char tipsy_input_record_touch(uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3,
	int i1, int i2, int i3, int i4, int i5,
	long long j1, long long j2,
	int k1, int k2, int k3, int k4, int k5, int k6,
	float f1, float f2) {
	tipsy_rec_touch_ids[0] = a0;
	tipsy_rec_touch_ids[1] = a1;
	tipsy_rec_touch_ids[2] = a2;
	tipsy_rec_touch_ids[3] = a3;
	tipsy_rec_touch_ints[0] = (uintptr_t)i1;
	tipsy_rec_touch_ints[1] = (uintptr_t)i2;
	tipsy_rec_touch_ints[2] = (uintptr_t)i3;
	tipsy_rec_touch_ints[3] = (uintptr_t)i4;
	tipsy_rec_touch_ints[4] = (uintptr_t)i5;
	tipsy_rec_touch_ints[5] = (uintptr_t)k1;
	tipsy_rec_touch_ints[6] = (uintptr_t)k2;
	tipsy_rec_touch_ints[7] = (uintptr_t)k3;
	tipsy_rec_touch_ints[8] = (uintptr_t)k4;
	tipsy_rec_touch_ints[9] = (uintptr_t)k5;
	tipsy_rec_touch_ints[10] = (uintptr_t)k6;
	tipsy_rec_touch_longs[0] = j1;
	tipsy_rec_touch_longs[1] = j2;
	tipsy_rec_touch_floats[0] = f1;
	tipsy_rec_touch_floats[1] = f2;
	return 1;
}

void *tipsy_input_record_focus_fn(void) { return (void *)tipsy_input_record_focus; }
void *tipsy_input_record_key_fn(void) { return (void *)tipsy_input_record_key; }
void *tipsy_input_record_touch_fn(void) { return (void *)tipsy_input_record_touch; }
uintptr_t tipsy_input_rec4_get(int i) { return tipsy_rec4_args[i]; }
uintptr_t tipsy_input_rec_touch_int(int i) { return tipsy_rec_touch_ints[i]; }
long long tipsy_input_rec_touch_long(int i) { return tipsy_rec_touch_longs[i]; }
float tipsy_input_rec_touch_float(int i) { return tipsy_rec_touch_floats[i]; }
uintptr_t tipsy_input_rec_touch_id(int i) { return tipsy_rec_touch_ids[i]; }
void tipsy_input_rec_reset(void) {
	tipsy_rec4_args[0] = 0;
	tipsy_rec4_args[1] = 0;
	tipsy_rec4_args[2] = 0;
	tipsy_rec4_args[3] = 0;
	for (int i = 0; i < 13; i++) tipsy_rec_touch_ints[i] = 0;
	tipsy_rec_touch_longs[0] = 0;
	tipsy_rec_touch_longs[1] = 0;
	tipsy_rec_touch_floats[0] = 0;
	tipsy_rec_touch_floats[1] = 0;
}
