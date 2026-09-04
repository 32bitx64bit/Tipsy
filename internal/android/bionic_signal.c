/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Bionic LP64 sigaction / rt_sigaction. glibc's struct sigaction is ~152
 * bytes (128-byte sigset_t). AOSP bionic LP64 is 32 bytes (8-byte sigset_t)
 * with a different field order than the x86-64 kernel struct. libroblox.so
 * 2.734.917 job-dtor wrapper 0x227a033 queries sigaction(SIGPIPE) into
 * [rbp-0x70]; glibc writes past the bionic act and overwrites the canary at
 * [rbp-0x18] (EXIT 134 at 0x227a0e1). Layout: AOSP
 * libc/include/bits/signal_types.h (LP64 __SIGACTION_BODY) and
 * libc/bionic/sigaction.cpp (translate to kernel + SA_RESTORER).
 */
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include "android_bridge.h"

#include <errno.h>
#include <signal.h>
#include <stdint.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

/* glibc maps sa_handler/sa_sigaction onto a union; drop those so our
 * field names are real members (AOSP uses the POSIX names). */
#undef sa_handler
#undef sa_sigaction

/* AOSP LP64: int sa_flags; handler union; sigset_t (8); restorer. */
struct tipsy_bionic_sigaction {
	int sa_flags;
	void (*handler)(int);
	unsigned long sa_mask;
	void (*restorer)(void);
};

/* x86-64 kernel struct sigaction (uapi asm/signal.h). */
struct tipsy_kernel_sigaction {
	void (*handler)(int);
	unsigned long sa_flags;
	void (*restorer)(void);
	unsigned long sa_mask;
};

#ifndef SA_RESTORER
#define SA_RESTORER 0x04000000
#endif

#define TIPSY_BIONIC_SIGSET_SIZE 8

_Static_assert(sizeof(struct tipsy_bionic_sigaction) == 32,
	       "bionic LP64 sigaction must be 32 bytes");
_Static_assert(sizeof(struct tipsy_kernel_sigaction) == 32,
	       "x86-64 kernel sigaction must be 32 bytes");
_Static_assert(sizeof(struct sigaction) >= 152,
	       "glibc sigaction is the 152-byte smash source");

void tipsy_restore_rt(void);
__asm__(
	".pushsection .text,\"ax\",@progbits\n"
	".align 16\n"
	".globl tipsy_restore_rt\n"
	".type tipsy_restore_rt, @function\n"
	"tipsy_restore_rt:\n"
	"	movl $15, %eax\n" /* SYS_rt_sigreturn */
	"	syscall\n"
	".size tipsy_restore_rt, .-tipsy_restore_rt\n"
	".popsection\n");

static void bionic_to_kernel(const struct tipsy_bionic_sigaction *b,
			    struct tipsy_kernel_sigaction *k)
{
	memset(k, 0, sizeof(*k));
	k->handler = b->handler;
	k->sa_flags = (unsigned long)(unsigned int)b->sa_flags;
	k->sa_mask = b->sa_mask;
	k->restorer = b->restorer;
	/* x86-64 kernel requires SA_RESTORER (AOSP sigaction.cpp). */
	if ((k->sa_flags & SA_RESTORER) == 0) {
		k->sa_flags |= SA_RESTORER;
		k->restorer = tipsy_restore_rt;
	}
}

static void kernel_to_bionic(const struct tipsy_kernel_sigaction *k,
			     struct tipsy_bionic_sigaction *b)
{
	memset(b, 0, sizeof(*b));
	b->sa_flags = (int)k->sa_flags;
	b->handler = k->handler;
	b->sa_mask = k->sa_mask;
	b->restorer = k->restorer;
}

long tipsy_rt_sigaction(int sig, const void *act, void *oact, size_t sigsetsize)
{
	long r;

	if (sigsetsize == 0) {
		sigsetsize = TIPSY_BIONIC_SIGSET_SIZE;
	}
	r = syscall(SYS_rt_sigaction, sig, act, oact, sigsetsize);
	return r;
}

int tipsy_sigaction(int sig, const void *bionic_new, void *bionic_old)
{
	struct tipsy_kernel_sigaction knew;
	struct tipsy_kernel_sigaction kold;
	const struct tipsy_kernel_sigaction *knewp = NULL;
	struct tipsy_kernel_sigaction *koldp = NULL;
	long r;

	if (bionic_new != NULL) {
		bionic_to_kernel((const struct tipsy_bionic_sigaction *)bionic_new, &knew);
		knewp = &knew;
	}
	if (bionic_old != NULL) {
		memset(&kold, 0, sizeof(kold));
		koldp = &kold;
	}
	r = tipsy_rt_sigaction(sig, knewp, koldp, TIPSY_BIONIC_SIGSET_SIZE);
	if (r != 0) {
		return -1;
	}
	if (bionic_old != NULL) {
		kernel_to_bionic(&kold, (struct tipsy_bionic_sigaction *)bionic_old);
	}
	return 0;
}

/* 1 if glibc sigaction(SIGPIPE, NULL, 32-byte act) overwrites the canary
 * immediately after a bionic-sized slot (the 0x227a033 smash). A sink
 * absorbs the rest of the 152-byte write so this test does not itself
 * smash the host frame canary. */
int tipsy_test_glibc_sigaction_smashes_bionic_act(void)
{
	struct {
		unsigned char act[32];
		uint64_t canary;
		unsigned char sink[256];
	} buf;

	memset(&buf, 0, sizeof(buf));
	memset(buf.act, 0xa5, sizeof(buf.act));
	buf.canary = 0xc0defeedc0defeedull;
	if (sigaction(SIGPIPE, NULL, (struct sigaction *)buf.act) != 0) {
		return -1;
	}
	return buf.canary != 0xc0defeedc0defeedull ? 1 : 0;
}

/* 0 if bionic-layout query writes only 32 bytes (canary after act lives). */
int tipsy_test_bionic_sigaction_query_canary(void)
{
	struct {
		unsigned char act[32];
		uint64_t canary;
	} buf;

	memset(buf.act, 0xa5, sizeof(buf.act));
	buf.canary = 0xc0defeedc0defeedull;
	if (tipsy_sigaction(SIGPIPE, NULL, buf.act) != 0) {
		return -1;
	}
	if (buf.canary != 0xc0defeedc0defeedull) {
		return 1;
	}
	return 0;
}

static void tipsy_test_sigusr2_handler(int sig)
{
	(void)sig;
}

/* 0 if set+query round-trips a handler through bionic layout. */
int tipsy_test_bionic_sigaction_set_query(void)
{
	struct tipsy_bionic_sigaction neu;
	struct tipsy_bionic_sigaction old;
	struct tipsy_bionic_sigaction got;
	struct tipsy_bionic_sigaction restore;

	memset(&neu, 0, sizeof(neu));
	memset(&old, 0, sizeof(old));
	memset(&got, 0, sizeof(got));
	memset(&restore, 0, sizeof(restore));
	neu.handler = tipsy_test_sigusr2_handler;
	neu.sa_flags = SA_RESTART;

	if (tipsy_sigaction(SIGUSR2, &neu, &old) != 0) {
		return -1;
	}
	if (tipsy_sigaction(SIGUSR2, NULL, &got) != 0) {
		(void)tipsy_sigaction(SIGUSR2, &old, NULL);
		return -2;
	}
	if (got.handler != tipsy_test_sigusr2_handler) {
		(void)tipsy_sigaction(SIGUSR2, &old, NULL);
		return -3;
	}
	if ((got.sa_flags & SA_RESTART) == 0) {
		(void)tipsy_sigaction(SIGUSR2, &old, NULL);
		return -4;
	}
	if (tipsy_sigaction(SIGUSR2, &old, &restore) != 0) {
		return -5;
	}
	return 0;
}

/* 0 if rt_sigaction query writes kernel-sized 32 bytes only. */
int tipsy_test_rt_sigaction_query_canary(void)
{
	struct {
		unsigned char act[32];
		uint64_t canary;
	} buf;

	memset(buf.act, 0xa5, sizeof(buf.act));
	buf.canary = 0xc0defeedc0defeedull;
	if (tipsy_rt_sigaction(SIGPIPE, NULL, buf.act, TIPSY_BIONIC_SIGSET_SIZE) != 0) {
		return -1;
	}
	if (buf.canary != 0xc0defeedc0defeedull) {
		return 1;
	}
	return 0;
}

size_t tipsy_bionic_sigaction_size(void)
{
	return sizeof(struct tipsy_bionic_sigaction);
}

size_t tipsy_glibc_sigaction_size(void)
{
	return sizeof(struct sigaction);
}
