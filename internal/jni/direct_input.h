/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_DIRECT_INPUT_H
#define TIPSY_JNI_DIRECT_INPUT_H

#include "jni_bridge.h"

#ifdef __cplusplus
extern "C" {
#endif

void tipsy_direct_mouse_button(void *fn, uintptr_t env, uintptr_t cls,
	float x, float y, unsigned char pressed, int button);
void tipsy_direct_mouse_move(void *fn, uintptr_t env, uintptr_t cls,
	float x, float y, float dx, float dy);
void tipsy_direct_mouse_wheel(void *fn, uintptr_t env, uintptr_t cls,
	float x, float y, float delta);
unsigned char tipsy_direct_mouse_locked(void *fn, uintptr_t env, uintptr_t cls);
void tipsy_direct_key_event(void *fn, uintptr_t env, uintptr_t cls,
	unsigned char down, int scan_code, int key_code, unsigned char repeat);
void *tipsy_direct_record_button_fn(void);
void *tipsy_direct_record_move_fn(void);
void *tipsy_direct_record_wheel_fn(void);
void *tipsy_direct_record_key_fn(void);
void *tipsy_direct_record_mouse_locked_fn(void);
uintptr_t tipsy_direct_rec_button_id(int i);
float tipsy_direct_rec_button_float(int i);
int tipsy_direct_rec_button_bool(void);
int tipsy_direct_rec_button_int(void);
uintptr_t tipsy_direct_rec_move_id(int i);
float tipsy_direct_rec_move_float(int i);
uintptr_t tipsy_direct_rec_wheel_id(int i);
float tipsy_direct_rec_wheel_float(int i);
uintptr_t tipsy_direct_rec_key_id(int i);
int tipsy_direct_rec_key_int(int i);
int tipsy_direct_rec_button_sequence_value(void);
int tipsy_direct_rec_lock_sequence_value(void);
void tipsy_direct_rec_set_mouse_locked(int locked);
void tipsy_direct_rec_reset(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_JNI_DIRECT_INPUT_H */
