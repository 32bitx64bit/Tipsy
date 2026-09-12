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
// Gamepad direct natives (Phase 0 §1: six DEX-proven NativeInputInterface
// dynsyms, static jclass ABI; never a guessed nativePassGamepad* name).
// Float arguments travel in SysV XMM registers, so the pointer-only CallP8
// helper is not ABI-correct for the axis event.
void tipsy_direct_gamepad_axis(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int axis, float f1, float f2, float f3);
void tipsy_direct_gamepad_button(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int key_code, int down);
void tipsy_direct_gamepad_connect(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int gamepad_type);
void tipsy_direct_gamepad_disconnect(void *fn, uintptr_t env, uintptr_t cls,
	int device_id);
void tipsy_direct_gamepad_set_key(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int key_code, unsigned char supported, int gamepad_type);
void tipsy_direct_gamepad_set_motion(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int axis, int arg, unsigned char supported, int gamepad_type);
// Test-only ABI witnesses for the gamepad callers above: a bounded
// sequence buffer so golden tests can pin the connect-time E() capability
// order as well as single event args.
void *tipsy_direct_gamepad_rec_fn_ptr(int kind);
int tipsy_direct_gamepad_rec_count(void);
int tipsy_direct_gamepad_rec_kind(int i);
int tipsy_direct_gamepad_rec_int(int i, int j);
float tipsy_direct_gamepad_rec_float(int i, int j);
void tipsy_direct_gamepad_rec_reset(void);
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
