/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_GAMEACTIVITY_INPUT_H
#define TIPSY_JNI_GAMEACTIVITY_INPUT_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void tipsy_input_call_void4(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3);
unsigned char tipsy_input_call_bool4(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3);
unsigned char tipsy_input_call_touch(void *fn, uintptr_t a0, uintptr_t a1, uintptr_t a2, uintptr_t a3,
	int i1, int i2, int i3, int i4, int i5,
	long long j1, long long j2,
	int k1, int k2, int k3, int k4, int k5, int k6,
	float f1, float f2);
void *tipsy_input_record_focus_fn(void);
void *tipsy_input_record_key_fn(void);
void *tipsy_input_record_touch_fn(void);
uintptr_t tipsy_input_rec4_get(int i);
uintptr_t tipsy_input_rec_touch_int(int i);
long long tipsy_input_rec_touch_long(int i);
float tipsy_input_rec_touch_float(int i);
uintptr_t tipsy_input_rec_touch_id(int i);
void tipsy_input_rec_reset(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_JNI_GAMEACTIVITY_INPUT_H */
