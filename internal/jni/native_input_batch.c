/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later */
#include "native_input_batch.h"
#include "direct_input.h"

struct batch_call {
 const TipsyInputCommand *commands;
 size_t count;
 TipsyInputBatchResult *result;
};

static int run_batch(JNIEnv *env, void *opaque)
{
 struct batch_call *batch=opaque;
 const struct JNINativeInterface_ *table=env->functions;
 if (table==NULL || table->ExceptionCheck==NULL) return TIPSY_BATCH_INVALID;
 if (table->ExceptionCheck(env)) return TIPSY_BATCH_EXCEPTION;
 for (size_t n=0;n<batch->count;n++) {
  const TipsyInputCommand *c=&batch->commands[n];
  void *fn=(void *)c->fn;
  uintptr_t e=(uintptr_t)env, cls=c->clazz;
  switch (c->kind) {
  case TIPSY_BATCH_MOUSE_BUTTON: tipsy_direct_mouse_button(fn,e,cls,c->f[0],c->f[1],c->i[0],c->i[1]); break;
  case TIPSY_BATCH_MOUSE_MOVE: tipsy_direct_mouse_move(fn,e,cls,c->f[0],c->f[1],c->f[2],c->f[3]); break;
  case TIPSY_BATCH_MOUSE_WHEEL: tipsy_direct_mouse_wheel(fn,e,cls,c->f[0],c->f[1],c->f[2]); break;
  case TIPSY_BATCH_MOUSE_LOCKED: batch->result->mouse_locked=tipsy_direct_mouse_locked(fn,e,cls); break;
  case TIPSY_BATCH_KEY: tipsy_direct_key_event(fn,e,cls,c->i[0],c->i[1],c->i[2],c->i[3]); break;
  case TIPSY_BATCH_PAD_AXIS: tipsy_direct_gamepad_axis(fn,e,cls,c->i[0],c->i[1],c->f[0],c->f[1],c->f[2]); break;
  case TIPSY_BATCH_PAD_BUTTON: tipsy_direct_gamepad_button(fn,e,cls,c->i[0],c->i[1],c->i[2]); break;
  case TIPSY_BATCH_PAD_CONNECT: tipsy_direct_gamepad_connect(fn,e,cls,c->i[0],c->i[1]); break;
  case TIPSY_BATCH_PAD_DISCONNECT: tipsy_direct_gamepad_disconnect(fn,e,cls,c->i[0]); break;
  case TIPSY_BATCH_PAD_KEY: tipsy_direct_gamepad_set_key(fn,e,cls,c->i[0],c->i[1],c->i[2],c->i[3]); break;
  case TIPSY_BATCH_PAD_MOTION: tipsy_direct_gamepad_set_motion(fn,e,cls,c->i[0],c->i[1],c->i[2],c->i[3],c->i[4]); break;
  default: return TIPSY_BATCH_INVALID; /* prevalidated below */
  }
  batch->result->completed++;
  /* Preserve any client-installed JNI wrapper, and stop on exceptions. This
   * correctness-first check can itself cross C->Go once per command. Measure
   * that cost; do not advertise N->1 TOTAL transitions from this prototype. */
  table=env->functions;
  if (table==NULL || table->ExceptionCheck==NULL) return TIPSY_BATCH_INVALID;
  if (table->ExceptionCheck(env)) return TIPSY_BATCH_EXCEPTION;
 }
 return TIPSY_BATCH_OK;
}

int tipsy_jni_input_batch(JNIEnv *env, const TipsyInputCommand *commands,
 size_t count, TipsyInputBatchResult *result)
{
 if (result==NULL) return TIPSY_BATCH_INVALID;
 *result=(TipsyInputBatchResult){0};
 if (count>TIPSY_INPUT_BATCH_MAX || (count!=0 && commands==NULL)) return TIPSY_BATCH_INVALID;
 for (size_t n=0;n<count;n++) {
  const TipsyInputCommand *c=&commands[n];
  if (c->kind<TIPSY_BATCH_MOUSE_BUTTON || c->kind>TIPSY_BATCH_PAD_MOTION || c->fn==0 || c->clazz==0) return TIPSY_BATCH_INVALID;
  /* Lock-state reads are immediate barriers, never grouped behind moves. */
  if (c->kind==TIPSY_BATCH_MOUSE_LOCKED && count!=1) return TIPSY_BATCH_INVALID;
 }
 if (count==0) return TIPSY_BATCH_OK;
 struct batch_call batch={commands,count,result};
 return tipsy_jni_run_owned(env,run_batch,&batch);
}
