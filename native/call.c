/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * SysV AMD64 trampolines. Roblox C++ runs on a dedicated 64 MiB
 * pthread that never enters Go except via JNIEnv callbacks.
 *
 * Constructors and JNI_OnLoad must share that thread: LLVM emutls
 * (__emutls_get_address) mallocs its TSD array through in-tree
 * mimalloc, and mimalloc's TLD is per-thread. A fresh thread recurses
 * emutls → malloc → emutls until the stack dies. Go's g0 stack must
 * not be used for those frames either.
 */
#define _GNU_SOURCE
#include "call.h"

#include <errno.h>
#include <pthread.h>
#include <stdint.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <sys/syscall.h>

/* android/ndk.c defines these when libandroid is linked. Loader-only
 * tests leave them NULL and keep a cond_wait idle loop. */
int tipsy_native_main_idle(int timeout_ms) __attribute__((weak));
void tipsy_native_main_wake(void) __attribute__((weak));
int tipsy_looper_can_park(void) __attribute__((weak));
int tipsy_looper_park_futex(int *uaddr) __attribute__((weak));
void tipsy_looper_unpark_futex(void) __attribute__((weak));
int tipsy_looper_consume_wake(void) __attribute__((weak));
uint64_t tipsy_stutter_wait_begin(int path) __attribute__((weak));
void tipsy_stutter_wait_slice(int path) __attribute__((weak));
void tipsy_stutter_wait_end(int path, uint64_t started_ns) __attribute__((weak));

enum { TIPSY_ALOOPER_POLL_ERROR = -4 };
enum { TIPSY_STUTTER_WAIT_FUTEX_PUMP = 2 };

#ifndef TIPSY_NATIVE_STACK
#define TIPSY_NATIVE_STACK (64u * 1024u * 1024u)
#endif

enum {
	JOB_CALL0 = 1,
	JOB_CALL0_RET,
	JOB_JNI_ONLOAD,
	JOB_P8,
	JOB_P3
};

struct tipsy_job {
	int kind;
	void *fn;
	void *a[8];
	void *reserved;
	uintptr_t ret;
	int done;
};

static pthread_t g_native;
static pthread_once_t g_native_once = PTHREAD_ONCE_INIT;
static int g_native_ok;

static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t g_work = PTHREAD_COND_INITIALIZER;
static pthread_cond_t g_done = PTHREAD_COND_INITIALIZER;
static pthread_cond_t g_idle = PTHREAD_COND_INITIALIZER;
static struct tipsy_job *g_pending;

int tipsy_on_native_main(void)
{
	if (!g_native_ok) {
		return 1;
	}
	return pthread_equal(pthread_self(), g_native) ? 1 : 0;
}

static uintptr_t exec_job(struct tipsy_job *j)
{
	switch (j->kind) {
	case JOB_CALL0:
		((void (*)(void))j->fn)();
		return 0;
	case JOB_CALL0_RET:
		return ((uintptr_t (*)(void))j->fn)();
	case JOB_JNI_ONLOAD:
		return (uintptr_t)(unsigned)((int (*)(void *, void *))j->fn)(j->a[0], j->reserved);
	case JOB_P8:
		return (uintptr_t)((int64_t (*)(void *, void *, void *, void *, void *, void *, void *, void *))j->fn)(
			j->a[0], j->a[1], j->a[2], j->a[3], j->a[4], j->a[5], j->a[6], j->a[7]);
	case JOB_P3:
		((void (*)(void *, void *, void *))j->fn)(j->a[0], j->a[1], j->a[2]);
		return 0;
	default:
		return 0;
	}
}

static uintptr_t submit(struct tipsy_job *j)
{
	if (tipsy_on_native_main()) {
		return exec_job(j);
	}
	pthread_mutex_lock(&g_mu);
	while (g_pending != NULL) {
		pthread_cond_wait(&g_idle, &g_mu);
	}
	j->done = 0;
	j->ret = 0;
	g_pending = j;
	pthread_cond_signal(&g_work);
	if (tipsy_native_main_wake != NULL) {
		tipsy_native_main_wake();
	}
	while (!j->done) {
		pthread_cond_wait(&g_done, &g_mu);
	}
	g_pending = NULL;
	pthread_cond_signal(&g_idle);
	uintptr_t ret = j->ret;
	pthread_mutex_unlock(&g_mu);
	return ret;
}

static void *native_main_thunk(void *arg)
{
	(void)arg;
#if defined(__linux__)
	(void)pthread_setname_np(pthread_self(), "Main");
#endif
	pthread_mutex_lock(&g_mu);
	for (;;) {
		while (g_pending == NULL || g_pending->done) {
			if (tipsy_native_main_idle != NULL) {
				pthread_mutex_unlock(&g_mu);
				int rc = tipsy_native_main_idle(-1);
				pthread_mutex_lock(&g_mu);
				if (g_pending != NULL && !g_pending->done) {
					break;
				}
				if (rc == TIPSY_ALOOPER_POLL_ERROR) {
					pthread_cond_wait(&g_work, &g_mu);
				}
				continue;
			}
			pthread_cond_wait(&g_work, &g_mu);
		}
		struct tipsy_job *j = g_pending;
		pthread_mutex_unlock(&g_mu);
		uintptr_t ret = exec_job(j);
		pthread_mutex_lock(&g_mu);
		j->ret = ret;
		j->done = 1;
		pthread_cond_signal(&g_done);
	}
}

static void native_main_start_once(void)
{
	pthread_attr_t attr;

	if (pthread_attr_init(&attr) != 0) {
		return;
	}
	(void)pthread_attr_setstacksize(&attr, TIPSY_NATIVE_STACK);
	(void)pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
	if (pthread_create(&g_native, &attr, native_main_thunk, NULL) == 0) {
		g_native_ok = 1;
	}
	pthread_attr_destroy(&attr);
}

void tipsy_start_native_main(void)
{
	(void)pthread_once(&g_native_once, native_main_start_once);
}

void tipsy_call0(void *fn)
{
	struct tipsy_job j;

	tipsy_start_native_main();
	memset(&j, 0, sizeof j);
	j.kind = JOB_CALL0;
	j.fn = fn;
	(void)submit(&j);
}

uintptr_t tipsy_call0_ret(void *fn)
{
	struct tipsy_job j;

	tipsy_start_native_main();
	memset(&j, 0, sizeof j);
	j.kind = JOB_CALL0_RET;
	j.fn = fn;
	return submit(&j);
}

int tipsy_call_jni_onload(void *fn, void *vm, void *reserved)
{
	struct tipsy_job j;

	tipsy_start_native_main();
	memset(&j, 0, sizeof j);
	j.kind = JOB_JNI_ONLOAD;
	j.fn = fn;
	j.a[0] = vm;
	j.reserved = reserved;
	return (int)submit(&j);
}

int64_t tipsy_call_p8(void *fn, void *a0, void *a1, void *a2, void *a3, void *a4, void *a5, void *a6, void *a7)
{
	struct tipsy_job j;

	tipsy_start_native_main();
	memset(&j, 0, sizeof j);
	j.kind = JOB_P8;
	j.fn = fn;
	j.a[0] = a0;
	j.a[1] = a1;
	j.a[2] = a2;
	j.a[3] = a3;
	j.a[4] = a4;
	j.a[5] = a5;
	j.a[6] = a6;
	j.a[7] = a7;
	return (int64_t)submit(&j);
}

void tipsy_call_p3(void *fn, void *a0, void *a1, void *a2)
{
	struct tipsy_job j;

	tipsy_start_native_main();
	memset(&j, 0, sizeof j);
	j.kind = JOB_P3;
	j.fn = fn;
	j.a[0] = a0;
	j.a[1] = a1;
	j.a[2] = a2;
	(void)submit(&j);
}

static int g_test_on_main;

void tipsy_test_mark_main(void)
{
	g_test_on_main = tipsy_on_native_main();
}

int tipsy_test_took_main(void)
{
	return g_test_on_main;
}

void *tipsy_test_mark_main_addr(void)
{
	return (void *)tipsy_test_mark_main;
}

enum { TIPSY_FUTEX_WAIT_BITSET_PRIVATE = 0x89 };

void *tipsy_park_poll_futex_addr(void)
{
	return (void *)tipsy_park_poll_futex;
}

long tipsy_park_poll_futex(int *uaddr, unsigned val)
{
	long rc;
	uint64_t diag_started = 0;

	if (uaddr == NULL) {
		errno = EFAULT;
		return -1;
	}
	if (tipsy_native_main_idle == NULL) {
		return syscall(SYS_futex, uaddr, TIPSY_FUTEX_WAIT_BITSET_PRIVATE,
			(int)val, NULL, NULL, 0xffffffffu);
	}
	if (tipsy_stutter_wait_begin != NULL) {
		diag_started = tipsy_stutter_wait_begin(TIPSY_STUTTER_WAIT_FUTEX_PUMP);
	}
	for (;;) {
		struct timespec ts;
		int can_park = tipsy_looper_can_park != NULL && tipsy_looper_can_park();

		if (can_park && tipsy_looper_park_futex != NULL &&
		    tipsy_looper_park_futex(uaddr)) {
			(void)tipsy_native_main_idle(0);
			continue;
		}
		if (can_park) {
			rc = syscall(SYS_futex, uaddr, TIPSY_FUTEX_WAIT_BITSET_PRIVATE,
				(int)val, NULL, NULL, 0xffffffffu);
			if (tipsy_looper_unpark_futex != NULL) {
				tipsy_looper_unpark_futex();
			}
			if (tipsy_looper_consume_wake != NULL && tipsy_looper_consume_wake()) {
				(void)tipsy_native_main_idle(0);
				continue;
			}
			if (rc == 0 || errno == EAGAIN) {
				if (tipsy_stutter_wait_end != NULL) {
					tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_FUTEX_PUMP, diag_started);
				}
				return 0;
			}
			if (errno == EINTR) {
				(void)tipsy_native_main_idle(0);
				continue;
			}
			if (tipsy_stutter_wait_end != NULL) {
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_FUTEX_PUMP, diag_started);
			}
			return rc;
		}
		if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
			return syscall(SYS_futex, uaddr, TIPSY_FUTEX_WAIT_BITSET_PRIVATE,
				(int)val, NULL, NULL, 0xffffffffu);
		}
		ts.tv_nsec += 16L * 1000000L;
		if (ts.tv_nsec >= 1000000000L) {
			ts.tv_sec++;
			ts.tv_nsec -= 1000000000L;
		}
		rc = syscall(SYS_futex, uaddr, TIPSY_FUTEX_WAIT_BITSET_PRIVATE,
			(int)val, &ts, NULL, 0xffffffffu);
		if (tipsy_stutter_wait_slice != NULL) {
			tipsy_stutter_wait_slice(TIPSY_STUTTER_WAIT_FUTEX_PUMP);
		}
		if (rc == 0) {
			if (tipsy_stutter_wait_end != NULL) {
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_FUTEX_PUMP, diag_started);
			}
			return 0;
		}
		if (errno == EAGAIN) {
			if (tipsy_stutter_wait_end != NULL) {
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_FUTEX_PUMP, diag_started);
			}
			return 0;
		}
		if (errno != ETIMEDOUT && errno != EINTR) {
			if (tipsy_stutter_wait_end != NULL) {
				tipsy_stutter_wait_end(TIPSY_STUTTER_WAIT_FUTEX_PUMP, diag_started);
			}
			return rc;
		}
		(void)tipsy_native_main_idle(0);
	}
}
