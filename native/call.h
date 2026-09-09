/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * SysV AMD64 trampolines for relocated function pointers.
 * Constructors and JNI_OnLoad share a 64 MiB "Main" pthread (mimalloc TLD).
 */
#ifndef TIPSY_NATIVE_CALL_H
#define TIPSY_NATIVE_CALL_H

#include <stdint.h>

void tipsy_call0(void *fn);
uintptr_t tipsy_call0_ret(void *fn);
int tipsy_call_jni_onload(void *fn, void *vm, void *reserved);
int64_t tipsy_call_p8(void *fn, void *a0, void *a1, void *a2, void *a3, void *a4, void *a5, void *a6, void *a7);
void tipsy_call_p3(void *fn, void *a0, void *a1, void *a2);

void tipsy_start_native_main(void);
int tipsy_on_native_main(void);

/* Optional (android package): poll Main's ALooper while the job queue is
 * idle, and wake that poll when a job is submitted. Weak in call.c. */
int tipsy_native_main_idle(int timeout_ms);
void tipsy_native_main_wake(void);

/* Park Roblox parking-lot SYS_futex (0x29cfa14, FUTEX_WAIT_BITSET|
 * PRIVATE, val3=-1) so Main can ALooper_pollOnce. Event-driven when the
 * TLS looper watcher is running; 16 ms slices otherwise. */
long tipsy_park_poll_futex(int *uaddr, unsigned val);
void *tipsy_park_poll_futex_addr(void);

void tipsy_test_mark_main(void);
int tipsy_test_took_main(void);
void *tipsy_test_mark_main_addr(void);

#endif
