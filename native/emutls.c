/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Replacement for LLVM `__emutls_get_address` (compiler-rt). Control
 * layout is the public LLVM ABI: size, align, index, initial value.
 * Allocation uses glibc so we cannot re-enter Roblox mimalloc.
 */
#define _GNU_SOURCE
#include "emutls.h"

#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

struct emutls_control {
	size_t size;
	size_t align;
	uintptr_t index;
	void *value;
};

struct emutls_array {
	uintptr_t cap;
	void *slot[];
};

static pthread_key_t g_key;
static pthread_once_t g_once = PTHREAD_ONCE_INIT;
static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static uintptr_t g_next_index;
static uintptr_t g_roblox_bias;
static void *g_jni_functions;

/* libroblox.so thread_local JNINativeInterface* used by the 111 JNI thunks. */
#define TIPSY_ROBLOX_JNI_TLS 0x6fd6120ul

void tipsy_set_roblox_bias(uintptr_t bias)
{
	g_roblox_bias = bias;
}

void tipsy_set_jni_functions(void *functions)
{
	g_jni_functions = functions;
}

static int is_roblox_jni_tls(struct emutls_control *c)
{
	return c != NULL && c->size == sizeof(void *) && g_roblox_bias != 0 &&
	       (uintptr_t)c - g_roblox_bias == TIPSY_ROBLOX_JNI_TLS;
}

static void pin_roblox_jni_tls(void *slot)
{
	if (slot == NULL || g_jni_functions == NULL) {
		return;
	}
	memcpy(slot, &g_jni_functions, sizeof g_jni_functions);
}

static void emutls_dtor(void *p)
{
	struct emutls_array *a = p;
	uintptr_t i;

	if (a == NULL) {
		return;
	}
	for (i = 1; i <= a->cap; i++) {
		free(a->slot[i]);
	}
	free(a);
}

static void emutls_key_init(void)
{
	if (pthread_key_create(&g_key, emutls_dtor) != 0) {
		abort();
	}
}

static size_t emutls_align(size_t a)
{
	if (a < sizeof(void *)) {
		a = sizeof(void *);
	}
	if ((a & (a - 1)) != 0) {
		a = sizeof(void *);
	}
	return a;
}

static void *emutls_alloc_object(struct emutls_control *c)
{
	size_t al = emutls_align(c->align);
	size_t sz = c->size;
	void *p;

	if (sz == 0) {
		sz = 1;
	}
	if (posix_memalign(&p, al, (sz + al - 1) & ~(al - 1)) != 0) {
		abort();
	}
	if (c->value) {
		memcpy(p, c->value, c->size);
	} else if (is_roblox_jni_tls(c) && g_jni_functions) {
		pin_roblox_jni_tls(p);
	} else {
		memset(p, 0, sz);
	}
	return p;
}

static struct emutls_array *emutls_grow(struct emutls_array *old, uintptr_t need)
{
	uintptr_t cap = 16;
	struct emutls_array *n;
	size_t bytes;

	if (old && old->cap > cap) {
		cap = old->cap;
	}
	while (cap < need) {
		cap *= 2;
	}
	bytes = sizeof(struct emutls_array) + (cap + 1) * sizeof(void *);
	n = calloc(1, bytes);
	if (n == NULL) {
		abort();
	}
	n->cap = cap;
	if (old) {
		memcpy(n->slot, old->slot, (old->cap + 1) * sizeof(void *));
		free(old);
	}
	return n;
}

void *tipsy_emutls_get_address(void *control)
{
	struct emutls_control *c = control;
	struct emutls_array *arr;
	uintptr_t idx;

	if (c == NULL) {
		abort();
	}
	pthread_once(&g_once, emutls_key_init);
	if (c->index == 0) {
		pthread_mutex_lock(&g_mu);
		if (c->index == 0) {
			c->index = ++g_next_index;
		}
		pthread_mutex_unlock(&g_mu);
	}
	idx = c->index;
	arr = pthread_getspecific(g_key);
	if (arr == NULL || arr->cap < idx) {
		arr = emutls_grow(arr, idx);
		if (pthread_setspecific(g_key, arr) != 0) {
			abort();
		}
	}
	if (arr->slot[idx] == NULL) {
		arr->slot[idx] = emutls_alloc_object(c);
	}
	/* Roblox GetEnv wraps JNIEnv by overwriting env->functions with a
	 * 1864-byte shadow table (FindClass = 0x21b1132) and stores the
	 * original JNINativeInterface* in this slot. If the wrap writes
	 * NULL or the shadow table itself, call *[slot+0x30] either
	 * SIGSEGVs at 0x30 or recurses until the 64 MiB stack dies.
	 * Always republish our original table on get. */
	if (is_roblox_jni_tls(c)) {
		pin_roblox_jni_tls(arr->slot[idx]);
	}
	return arr->slot[idx];
}

static void install_abs_jmp(void *at, void *dest)
{
	unsigned char *p = at;
	uintptr_t d = (uintptr_t)dest;
	long ps = sysconf(_SC_PAGESIZE);
	uintptr_t page = (uintptr_t)at & ~(uintptr_t)(ps - 1);

	if (at == NULL || dest == NULL || ps <= 0) {
		return;
	}
	{
		uintptr_t end = ((uintptr_t)at + 12 + (uintptr_t)ps - 1) & ~(uintptr_t)(ps - 1);
		(void)mprotect((void *)page, (size_t)(end - page), PROT_READ | PROT_WRITE | PROT_EXEC);
	}
	p[0] = 0x48;
	p[1] = 0xb8;
	memcpy(p + 2, &d, 8);
	p[10] = 0xff;
	p[11] = 0xe0;
}

void tipsy_install_emutls_hook(void *at)
{
	install_abs_jmp(at, (void *)tipsy_emutls_get_address);
}

void tipsy_patch_cxa_once(void *at)
{
	/* 13-byte once-check at 29cfb10. The following call 29cfb70
	 * (e8 53 00 00 00) is left in place. Set the flag before that
	 * call so __cxa_get_globals cannot recurse. */
	static const unsigned char patch[8] = {
		0xf6, 0x00, 0x01, /* test byte [rax], 1 */
		0x75, 0x39,       /* jne 29cfb4e */
		0xc6, 0x00, 0x01, /* movb $1, [rax] */
	};
	unsigned char *p = at;
	long ps = sysconf(_SC_PAGESIZE);
	uintptr_t page = (uintptr_t)at & ~(uintptr_t)(ps - 1);

	if (at == NULL || ps <= 0) {
		return;
	}
	{
		uintptr_t end = ((uintptr_t)at + sizeof patch + (uintptr_t)ps - 1) & ~(uintptr_t)(ps - 1);
		(void)mprotect((void *)page, (size_t)(end - page), PROT_READ | PROT_WRITE | PROT_EXEC);
	}
	memcpy(p, patch, sizeof patch);
}
