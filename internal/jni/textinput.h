/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_TEXTINPUT_H
#define TIPSY_JNI_TEXTINPUT_H

#include "jni_bridge.h"

#ifdef __cplusplus
extern "C" {
#endif

float tipsy_text_jvalue_f_at(const jvalue *args, int i);
void tipsy_text_jvalue_set_f_at(jvalue *args, int i, float value);
void tipsy_rbx_pass_text(void *fn, uintptr_t env, uintptr_t cls,
	long long handle, jobject text, unsigned char submit, int cursor);
void tipsy_rbx_return_pressed(void *fn, uintptr_t env, uintptr_t cls,
	long long handle);
void tipsy_rbx_sync_selection(void *fn, uintptr_t env, uintptr_t cls,
	jobject text, int cursor);
void tipsy_rbx_rec_reset(void);
void *tipsy_rbx_record_pass_fn(void);
void *tipsy_rbx_record_return_fn(void);
void *tipsy_rbx_record_sync_fn(void);
unsigned long long tipsy_rbx_rec_handle_get(void);
uintptr_t tipsy_rbx_rec_text_get(void);
int tipsy_rbx_rec_submit_get(void);
int tipsy_rbx_rec_cursor_get(void);
int tipsy_rbx_rec_pass_count_get(void);
int tipsy_rbx_rec_return_count_get(void);
int tipsy_rbx_rec_sync_count_get(void);
int tipsy_rbx_rec_pass_sequence_get(void);
int tipsy_rbx_rec_sync_sequence_get(void);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_JNI_TEXTINPUT_H */
