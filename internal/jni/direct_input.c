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

// Exact callers for the six DEX-proven direct gamepad natives on
// NativeInputInterface (Phase 0 gamepad-ground-truth §1, 2.736.1408):
// nativeGamepadAxisEvent(IIFFF)V, nativeGamepadButtonEvent(III)V,
// nativeGamepadConnectEventWithGamepadType(II)V,
// nativeGamepadDisconnectEvent(I)V,
// nativeSetGamepadSupportedKeyWithGamepadType(IIZI)V,
// nativeSetGamepadSupportedMotionWithGamepadType(IIIZI)V.
// Axis f-slots are packed in Go (handleGamepadFrame): stick pairs
// (x,-y,0)/(z,-rz,0), hat/trigger singles (0,0,v) with HAT_Y negated.
void tipsy_direct_gamepad_axis(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int axis, float f1, float f2, float f3) {
	((void (*)(JNIEnv *, jclass, jint, jint, jfloat, jfloat, jfloat))fn)(
		(JNIEnv *)env, (jclass)cls, device_id, axis, f1, f2, f3);
}

void tipsy_direct_gamepad_button(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int key_code, int down) {
	((void (*)(JNIEnv *, jclass, jint, jint, jint))fn)(
		(JNIEnv *)env, (jclass)cls, device_id, key_code, down);
}

void tipsy_direct_gamepad_connect(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int gamepad_type) {
	((void (*)(JNIEnv *, jclass, jint, jint))fn)(
		(JNIEnv *)env, (jclass)cls, device_id, gamepad_type);
}

void tipsy_direct_gamepad_disconnect(void *fn, uintptr_t env, uintptr_t cls,
	int device_id) {
	((void (*)(JNIEnv *, jclass, jint))fn)(
		(JNIEnv *)env, (jclass)cls, device_id);
}

void tipsy_direct_gamepad_set_key(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int key_code, unsigned char supported, int gamepad_type) {
	((void (*)(JNIEnv *, jclass, jint, jint, jboolean, jint))fn)(
		(JNIEnv *)env, (jclass)cls, device_id, key_code, supported, gamepad_type);
}

void tipsy_direct_gamepad_set_motion(void *fn, uintptr_t env, uintptr_t cls,
	int device_id, int axis, int arg, unsigned char supported, int gamepad_type) {
	((void (*)(JNIEnv *, jclass, jint, jint, jint, jboolean, jint))fn)(
		(JNIEnv *)env, (jclass)cls, device_id, axis, arg, supported, gamepad_type);
}

// Recording natives are test-only ABI witnesses. They capture exactly what
// the typed callers above place in the native method's parameters.
#define TIPSY_GP_REC_CAP 256

typedef struct {
	int kind;
	int a;
	int b;
	int c;
	int d;
	int e;
	float f1;
	float f2;
	float f3;
} tipsy_gp_rec_t;

static tipsy_gp_rec_t tipsy_gp_rec[TIPSY_GP_REC_CAP];
static int tipsy_gp_rec_n;

static void tipsy_gp_push(int kind, int a, int b, int c, int d, int e,
	float f1, float f2, float f3) {
	if (tipsy_gp_rec_n < 0 || tipsy_gp_rec_n >= TIPSY_GP_REC_CAP) {
		return;
	}
	tipsy_gp_rec[tipsy_gp_rec_n].kind = kind;
	tipsy_gp_rec[tipsy_gp_rec_n].a = a;
	tipsy_gp_rec[tipsy_gp_rec_n].b = b;
	tipsy_gp_rec[tipsy_gp_rec_n].c = c;
	tipsy_gp_rec[tipsy_gp_rec_n].d = d;
	tipsy_gp_rec[tipsy_gp_rec_n].e = e;
	tipsy_gp_rec[tipsy_gp_rec_n].f1 = f1;
	tipsy_gp_rec[tipsy_gp_rec_n].f2 = f2;
	tipsy_gp_rec[tipsy_gp_rec_n].f3 = f3;
	tipsy_gp_rec_n++;
}

// Recording-native kinds, mirrored by the Go test hooks.
enum {
	TIPSY_GP_AXIS = 1,
	TIPSY_GP_BUTTON = 2,
	TIPSY_GP_CONNECT = 3,
	TIPSY_GP_DISCONNECT = 4,
	TIPSY_GP_SET_KEY = 5,
	TIPSY_GP_SET_MOTION = 6
};

static void tipsy_direct_record_gp_axis(JNIEnv *env, jclass cls,
	jint dev, jint axis, jfloat f1, jfloat f2, jfloat f3) {
	(void)env;
	(void)cls;
	tipsy_gp_push(TIPSY_GP_AXIS, dev, axis, 0, 0, 0, f1, f2, f3);
}

static void tipsy_direct_record_gp_button(JNIEnv *env, jclass cls,
	jint dev, jint key, jint down) {
	(void)env;
	(void)cls;
	tipsy_gp_push(TIPSY_GP_BUTTON, dev, key, down, 0, 0, 0, 0, 0);
}

static void tipsy_direct_record_gp_connect(JNIEnv *env, jclass cls,
	jint dev, jint type) {
	(void)env;
	(void)cls;
	tipsy_gp_push(TIPSY_GP_CONNECT, dev, type, 0, 0, 0, 0, 0, 0);
}

static void tipsy_direct_record_gp_disconnect(JNIEnv *env, jclass cls,
	jint dev) {
	(void)env;
	(void)cls;
	tipsy_gp_push(TIPSY_GP_DISCONNECT, dev, 0, 0, 0, 0, 0, 0, 0);
}

static void tipsy_direct_record_gp_set_key(JNIEnv *env, jclass cls,
	jint dev, jint key, jboolean sup, jint type) {
	(void)env;
	(void)cls;
	tipsy_gp_push(TIPSY_GP_SET_KEY, dev, key, sup, type, 0, 0, 0, 0);
}

static void tipsy_direct_record_gp_set_motion(JNIEnv *env, jclass cls,
	jint dev, jint axis, jint arg, jboolean sup, jint type) {
	(void)env;
	(void)cls;
	tipsy_gp_push(TIPSY_GP_SET_MOTION, dev, axis, arg, sup, type, 0, 0, 0);
}

void *tipsy_direct_gamepad_rec_fn_ptr(int kind) {
	switch (kind) {
	case TIPSY_GP_AXIS:
		return (void *)tipsy_direct_record_gp_axis;
	case TIPSY_GP_BUTTON:
		return (void *)tipsy_direct_record_gp_button;
	case TIPSY_GP_CONNECT:
		return (void *)tipsy_direct_record_gp_connect;
	case TIPSY_GP_DISCONNECT:
		return (void *)tipsy_direct_record_gp_disconnect;
	case TIPSY_GP_SET_KEY:
		return (void *)tipsy_direct_record_gp_set_key;
	case TIPSY_GP_SET_MOTION:
		return (void *)tipsy_direct_record_gp_set_motion;
	default:
		return 0;
	}
}

int tipsy_direct_gamepad_rec_count(void) { return tipsy_gp_rec_n; }

int tipsy_direct_gamepad_rec_kind(int i) {
	if (i < 0 || i >= tipsy_gp_rec_n) {
		return 0;
	}
	return tipsy_gp_rec[i].kind;
}

int tipsy_direct_gamepad_rec_int(int i, int j) {
	if (i < 0 || i >= tipsy_gp_rec_n) {
		return 0;
	}
	switch (j) {
	case 0:
		return tipsy_gp_rec[i].a;
	case 1:
		return tipsy_gp_rec[i].b;
	case 2:
		return tipsy_gp_rec[i].c;
	case 3:
		return tipsy_gp_rec[i].d;
	default:
		return tipsy_gp_rec[i].e;
	}
}

float tipsy_direct_gamepad_rec_float(int i, int j) {
	if (i < 0 || i >= tipsy_gp_rec_n) {
		return 0;
	}
	switch (j) {
	case 0:
		return tipsy_gp_rec[i].f1;
	case 1:
		return tipsy_gp_rec[i].f2;
	default:
		return tipsy_gp_rec[i].f3;
	}
}

void tipsy_direct_gamepad_rec_reset(void) {
	tipsy_gp_rec_n = 0;
	for (int i = 0; i < TIPSY_GP_REC_CAP; ++i) {
		tipsy_gp_rec[i].kind = 0;
		tipsy_gp_rec[i].a = 0;
		tipsy_gp_rec[i].b = 0;
		tipsy_gp_rec[i].c = 0;
		tipsy_gp_rec[i].d = 0;
		tipsy_gp_rec[i].e = 0;
		tipsy_gp_rec[i].f1 = 0;
		tipsy_gp_rec[i].f2 = 0;
		tipsy_gp_rec[i].f3 = 0;
	}
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
