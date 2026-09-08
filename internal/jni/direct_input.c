/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "direct_input.h"

// Exact JNI callers for the public static native methods declared by the
// official 2.734.917 classes2.dex NativeInputInterface. Float arguments must
// travel in the SysV XMM argument registers, so the pointer-only CallP8 helper
// is not ABI-correct for these methods.
void tipsy_direct_mouse_button(void *fn, uintptr_t env, uintptr_t cls,
	float x, float y, unsigned char pressed, int button) {
	((void (*)(JNIEnv *, jclass, jfloat, jfloat, jboolean, jint))fn)(
		(JNIEnv *)env, (jclass)cls, x, y, pressed, button);
}

void tipsy_direct_mouse_move(void *fn, uintptr_t env, uintptr_t cls,
	float x, float y, float dx, float dy) {
	((void (*)(JNIEnv *, jclass, jfloat, jfloat, jfloat, jfloat))fn)(
		(JNIEnv *)env, (jclass)cls, x, y, dx, dy);
}

void tipsy_direct_mouse_wheel(void *fn, uintptr_t env, uintptr_t cls,
	float x, float y, float delta) {
	((void (*)(JNIEnv *, jclass, jfloat, jfloat, jfloat))fn)(
		(JNIEnv *)env, (jclass)cls, x, y, delta);
}

unsigned char tipsy_direct_mouse_locked(void *fn, uintptr_t env,
	uintptr_t cls) {
	return ((jboolean (*)(JNIEnv *, jclass))fn)(
		(JNIEnv *)env, (jclass)cls);
}

// Exact caller for the public static native method declared by the official
// 2.734.917 classes2.dex NativeGLInterface:
// nativePassKeyEvent(ZIIZ)V. The four declared arguments are entirely
// integer-register values in the SysV x86-64 ABI.
void tipsy_direct_key_event(void *fn, uintptr_t env, uintptr_t cls,
	unsigned char down, int scan_code, int key_code, unsigned char repeat) {
	((void (*)(JNIEnv *, jclass, jboolean, jint, jint, jboolean))fn)(
		(JNIEnv *)env, (jclass)cls, down, scan_code, key_code, repeat);
}

// Recording natives are test-only ABI witnesses. They capture exactly what
// the typed callers above place in the native method's parameters.
static float tipsy_direct_rec_button_f[2];
static int tipsy_direct_rec_button_pressed;
static int tipsy_direct_rec_button_index;
static uintptr_t tipsy_direct_rec_button_ids[2];
static float tipsy_direct_rec_move_f[4];
static uintptr_t tipsy_direct_rec_move_ids[2];
static float tipsy_direct_rec_wheel_f[3];
static uintptr_t tipsy_direct_rec_wheel_ids[2];
static int tipsy_direct_rec_key_i[4];
static uintptr_t tipsy_direct_rec_key_ids[2];
static int tipsy_direct_rec_mouse_locked;
static int tipsy_direct_rec_sequence;
static int tipsy_direct_rec_button_sequence;
static int tipsy_direct_rec_lock_sequence;

void tipsy_direct_record_button(JNIEnv *env, jclass cls, jfloat x,
	jfloat y, jboolean pressed, jint button) {
	tipsy_direct_rec_button_sequence = ++tipsy_direct_rec_sequence;
	tipsy_direct_rec_button_ids[0] = (uintptr_t)env;
	tipsy_direct_rec_button_ids[1] = (uintptr_t)cls;
	tipsy_direct_rec_button_f[0] = x;
	tipsy_direct_rec_button_f[1] = y;
	tipsy_direct_rec_button_pressed = pressed;
	tipsy_direct_rec_button_index = button;
}

void tipsy_direct_record_move(JNIEnv *env, jclass cls, jfloat x,
	jfloat y, jfloat dx, jfloat dy) {
	tipsy_direct_rec_move_ids[0] = (uintptr_t)env;
	tipsy_direct_rec_move_ids[1] = (uintptr_t)cls;
	tipsy_direct_rec_move_f[0] = x;
	tipsy_direct_rec_move_f[1] = y;
	tipsy_direct_rec_move_f[2] = dx;
	tipsy_direct_rec_move_f[3] = dy;
}

void tipsy_direct_record_wheel(JNIEnv *env, jclass cls, jfloat x,
	jfloat y, jfloat delta) {
	tipsy_direct_rec_wheel_ids[0] = (uintptr_t)env;
	tipsy_direct_rec_wheel_ids[1] = (uintptr_t)cls;
	tipsy_direct_rec_wheel_f[0] = x;
	tipsy_direct_rec_wheel_f[1] = y;
	tipsy_direct_rec_wheel_f[2] = delta;
}

void tipsy_direct_record_key(JNIEnv *env, jclass cls, jboolean down,
	jint scan_code, jint key_code, jboolean repeat) {
	tipsy_direct_rec_key_ids[0] = (uintptr_t)env;
	tipsy_direct_rec_key_ids[1] = (uintptr_t)cls;
	tipsy_direct_rec_key_i[0] = down;
	tipsy_direct_rec_key_i[1] = scan_code;
	tipsy_direct_rec_key_i[2] = key_code;
	tipsy_direct_rec_key_i[3] = repeat;
}

jboolean tipsy_direct_record_mouse_locked(JNIEnv *env, jclass cls) {
	(void)env;
	(void)cls;
	tipsy_direct_rec_lock_sequence = ++tipsy_direct_rec_sequence;
	return tipsy_direct_rec_mouse_locked ? 1 : 0;
}

void *tipsy_direct_record_button_fn(void) { return (void *)tipsy_direct_record_button; }
void *tipsy_direct_record_move_fn(void) { return (void *)tipsy_direct_record_move; }
void *tipsy_direct_record_wheel_fn(void) { return (void *)tipsy_direct_record_wheel; }
void *tipsy_direct_record_key_fn(void) { return (void *)tipsy_direct_record_key; }
void *tipsy_direct_record_mouse_locked_fn(void) { return (void *)tipsy_direct_record_mouse_locked; }
uintptr_t tipsy_direct_rec_button_id(int i) { return tipsy_direct_rec_button_ids[i]; }
float tipsy_direct_rec_button_float(int i) { return tipsy_direct_rec_button_f[i]; }
int tipsy_direct_rec_button_bool(void) { return tipsy_direct_rec_button_pressed; }
int tipsy_direct_rec_button_int(void) { return tipsy_direct_rec_button_index; }
uintptr_t tipsy_direct_rec_move_id(int i) { return tipsy_direct_rec_move_ids[i]; }
float tipsy_direct_rec_move_float(int i) { return tipsy_direct_rec_move_f[i]; }
uintptr_t tipsy_direct_rec_wheel_id(int i) { return tipsy_direct_rec_wheel_ids[i]; }
float tipsy_direct_rec_wheel_float(int i) { return tipsy_direct_rec_wheel_f[i]; }
uintptr_t tipsy_direct_rec_key_id(int i) { return tipsy_direct_rec_key_ids[i]; }
int tipsy_direct_rec_key_int(int i) { return tipsy_direct_rec_key_i[i]; }
int tipsy_direct_rec_button_sequence_value(void) { return tipsy_direct_rec_button_sequence; }
int tipsy_direct_rec_lock_sequence_value(void) { return tipsy_direct_rec_lock_sequence; }
void tipsy_direct_rec_set_mouse_locked(int locked) { tipsy_direct_rec_mouse_locked = locked; }
void tipsy_direct_rec_reset(void) {
	for (int i = 0; i < 2; ++i) {
		tipsy_direct_rec_button_f[i] = 0;
		tipsy_direct_rec_button_ids[i] = 0;
		tipsy_direct_rec_move_ids[i] = 0;
	}
	for (int i = 0; i < 4; ++i) tipsy_direct_rec_move_f[i] = 0;
	for (int i = 0; i < 3; ++i) tipsy_direct_rec_wheel_f[i] = 0;
	for (int i = 0; i < 2; ++i) tipsy_direct_rec_wheel_ids[i] = 0;
	for (int i = 0; i < 4; ++i) tipsy_direct_rec_key_i[i] = 0;
	for (int i = 0; i < 2; ++i) tipsy_direct_rec_key_ids[i] = 0;
	tipsy_direct_rec_button_pressed = 0;
	tipsy_direct_rec_button_index = 0;
	tipsy_direct_rec_mouse_locked = 0;
	tipsy_direct_rec_sequence = 0;
	tipsy_direct_rec_button_sequence = 0;
	tipsy_direct_rec_lock_sequence = 0;
}
