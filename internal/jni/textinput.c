/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "textinput.h"

// NativeTextBoxInfo's full constructor carries five float arguments. Keep
// their union reads local to this APK-specific adapter instead of widening the
// generic JNI bridge API merely for one model object.
float tipsy_text_jvalue_f_at(const jvalue *args, int i) {
	return args != NULL && i >= 0 ? args[i].f : 0.0f;
}

void tipsy_text_jvalue_set_f_at(jvalue *args, int i, float value) {
	if (args != NULL && i >= 0) {
		args[i].f = value;
	}
}

// Typed callers for the APK's RbxKeyboard/EditText JNI bridge. All declared
// arguments use integer registers, but keeping the signature here prevents a
// future call-site from confusing the textbox J handle with the jclass slot.
void tipsy_rbx_pass_text(void *fn, uintptr_t env, uintptr_t cls,
	long long handle, jobject text, unsigned char submit, int cursor) {
	((void (*)(JNIEnv *, jclass, jlong, jstring, jboolean, jint))fn)(
		(JNIEnv *)env, (jclass)cls, (jlong)handle, (jstring)text,
		(jboolean)submit, (jint)cursor);
}

void tipsy_rbx_return_pressed(void *fn, uintptr_t env, uintptr_t cls,
	long long handle) {
	((void (*)(JNIEnv *, jclass, jlong))fn)(
		(JNIEnv *)env, (jclass)cls, (jlong)handle);
}

void tipsy_rbx_sync_selection(void *fn, uintptr_t env, uintptr_t cls,
	jobject text, int cursor) {
	((void (*)(JNIEnv *, jclass, jstring, jint))fn)(
		(JNIEnv *)env, (jclass)cls, (jstring)text, (jint)cursor);
}

// ABI witnesses used only by Go tests. They record ids/counts, never copy or
// inspect the jstring payload.
static unsigned long long tipsy_rbx_rec_handle;
static uintptr_t tipsy_rbx_rec_text;
static int tipsy_rbx_rec_submit;
static int tipsy_rbx_rec_cursor;
static int tipsy_rbx_rec_pass_count;
static int tipsy_rbx_rec_return_count;
static int tipsy_rbx_rec_sync_count;
static int tipsy_rbx_rec_sequence;
static int tipsy_rbx_rec_pass_sequence;
static int tipsy_rbx_rec_sync_sequence;
void tipsy_rbx_record_pass(JNIEnv *env, jclass cls, jlong handle,
	jstring text, jboolean submit, jint cursor) {
	(void)env; (void)cls;
	tipsy_rbx_rec_handle = (unsigned long long)handle;
	tipsy_rbx_rec_text = (uintptr_t)text;
	tipsy_rbx_rec_submit = submit;
	tipsy_rbx_rec_cursor = cursor;
	tipsy_rbx_rec_pass_count++;
	tipsy_rbx_rec_pass_sequence = ++tipsy_rbx_rec_sequence;
}
void tipsy_rbx_record_return(JNIEnv *env, jclass cls, jlong handle) {
	(void)env; (void)cls; (void)handle; tipsy_rbx_rec_return_count++;
}
void tipsy_rbx_record_sync(JNIEnv *env, jclass cls, jstring text, jint cursor) {
	(void)env; (void)cls; (void)text; tipsy_rbx_rec_cursor = cursor;
	tipsy_rbx_rec_sync_count++;
	tipsy_rbx_rec_sync_sequence = ++tipsy_rbx_rec_sequence;
}
void tipsy_rbx_rec_reset(void) {
	tipsy_rbx_rec_handle = 0; tipsy_rbx_rec_text = 0;
	tipsy_rbx_rec_submit = 0; tipsy_rbx_rec_cursor = 0;
	tipsy_rbx_rec_pass_count = 0; tipsy_rbx_rec_return_count = 0;
	tipsy_rbx_rec_sync_count = 0;
	tipsy_rbx_rec_sequence = 0; tipsy_rbx_rec_pass_sequence = 0;
	tipsy_rbx_rec_sync_sequence = 0;
}
void *tipsy_rbx_record_pass_fn(void) { return (void *)tipsy_rbx_record_pass; }
void *tipsy_rbx_record_return_fn(void) { return (void *)tipsy_rbx_record_return; }
void *tipsy_rbx_record_sync_fn(void) { return (void *)tipsy_rbx_record_sync; }
unsigned long long tipsy_rbx_rec_handle_get(void) { return tipsy_rbx_rec_handle; }
uintptr_t tipsy_rbx_rec_text_get(void) { return tipsy_rbx_rec_text; }
int tipsy_rbx_rec_submit_get(void) { return tipsy_rbx_rec_submit; }
int tipsy_rbx_rec_cursor_get(void) { return tipsy_rbx_rec_cursor; }
int tipsy_rbx_rec_pass_count_get(void) { return tipsy_rbx_rec_pass_count; }
int tipsy_rbx_rec_return_count_get(void) { return tipsy_rbx_rec_return_count; }
int tipsy_rbx_rec_sync_count_get(void) { return tipsy_rbx_rec_sync_count; }
int tipsy_rbx_rec_pass_sequence_get(void) { return tipsy_rbx_rec_pass_sequence; }
int tipsy_rbx_rec_sync_sequence_get(void) { return tipsy_rbx_rec_sync_sequence; }
