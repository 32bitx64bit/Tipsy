/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Bionic-only libc symbols that glibc does not export (FORTIFY, __sF, SSP)
 * and bionic `sysconf` names (numeric `_SC_*` differ from glibc).
 */
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include "android_bridge.h"

#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

/* AOSP bits/struct_file.h: LP64 FILE is 152 bytes, 8-aligned. glibc FILE is
 * larger (~216). Roblox indexes __sF[i] with the bionic stride, so a glibc
 * FILE[3] made &__sF[1] land inside a zeroed glibc FILE (NULL _IO_lock). */
#define TIPSY_BIONIC_FILE_SIZE 152
#define TIPSY_BIONIC_NFILE 3

unsigned char tipsy_sF[TIPSY_BIONIC_FILE_SIZE * TIPSY_BIONIC_NFILE]
	__attribute__((aligned(sizeof(void *))));

int tipsy_fflush(FILE *stream);

static FILE *map_bionic_file(FILE *f)
{
	unsigned char *p = (unsigned char *)f;

	if (p >= tipsy_sF && p < tipsy_sF + sizeof(tipsy_sF)) {
		size_t i = (size_t)(p - tipsy_sF) / TIPSY_BIONIC_FILE_SIZE;
		switch (i) {
		case 0:
			return stdin;
		case 1:
			return stdout;
		case 2:
			return stderr;
		}
	}
	return f;
}

int tipsy_fflush_bionic_index(int idx)
{
	if (idx < 0 || idx >= TIPSY_BIONIC_NFILE) {
		return -1;
	}
	return tipsy_fflush((FILE *)(tipsy_sF + (size_t)idx * TIPSY_BIONIC_FILE_SIZE));
}

size_t tipsy_strlen_chk(const char *s, size_t n)
{
	(void)n;
	return strlen(s ? s : "");
}

char *tipsy_strchr_chk(const char *s, int c, size_t n)
{
	(void)n;
	return (char *)strchr(s, c);
}

char *tipsy_strncpy_chk2(char *dest, const char *src, size_t n, size_t dest_len, size_t slen)
{
	(void)dest_len;
	(void)slen;
	return strncpy(dest, src, n);
}

char *tipsy_strncpy_chk(char *dest, const char *src, size_t n, size_t dest_len)
{
	(void)dest_len;
	return strncpy(dest, src, n);
}

void *tipsy_memcpy_chk(void *dest, const void *src, size_t n, size_t dest_len)
{
	(void)dest_len;
	return memcpy(dest, src, n);
}

void *tipsy_memmove_chk(void *dest, const void *src, size_t n, size_t dest_len)
{
	(void)dest_len;
	return memmove(dest, src, n);
}

void *tipsy_memset_chk(void *dest, int c, size_t n, size_t dest_len)
{
	(void)dest_len;
	return memset(dest, c, n);
}

size_t tipsy_fwrite_chk(const void *ptr, size_t size, size_t nmemb, FILE *stream, size_t bufsize)
{
	FILE *s;

	(void)bufsize;
	s = map_bionic_file(stream);
	if (s == NULL) {
		return 0;
	}
	return fwrite(ptr, size, nmemb, s);
}

ssize_t tipsy_write_chk(int fd, const void *buf, size_t count, size_t bufsize)
{
	(void)bufsize;
	return write(fd, buf, count);
}

ssize_t tipsy_sendto_chk(int fd, const void *buf, size_t len, int flags,
			 const struct sockaddr *dest, socklen_t dest_len, size_t buflen)
{
	(void)buflen;
	return sendto(fd, buf, len, flags, dest, dest_len);
}

ssize_t tipsy_read_chk(int fd, void *buf, size_t count, size_t buflen)
{
	(void)buflen;
	return read(fd, buf, count);
}

/* Exact-match TLS trust-bundle compat (Android ABI platform shim).
 *
 * The official client opens the relative path "./exe/cacert.pem" (FLog:
 * "error adding trust anchors from file: ./exe/cacert.pem"; 231/231 cacert
 * mentions in the stable Landing session carry the "./" prefix, the bare
 * "exe/cacert.pem" form was never observed and is intentionally NOT mapped).
 * Runtime installs the official APK bytes at <runtime>/files/exe/cacert.pem
 * but must not chdir there: a FilesDir process CWD terminates the engine's
 * HttpClient thread right after Startup (docs/investigations/post-startup-exit.md).
 *
 * These wrappers are resolved through Tipsy's loader symbol table as named
 * GOT/PLT entries (no libroblox .text writes, no version-pinned file vaddrs).
 * Only the exact relative input above is remapped to the absolute FilesDir
 * bundle supplied by Go (GoAndroid_CACertBundle, malloc'd, caller frees);
 * every other path, dirfd, flag, mode, and errno passes through untouched.
 * A missing bundle is an honest ENOENT, never a host-CA fallback. Process
 * CWD is never changed and no file is created in the caller CWD.
 */
#define TIPSY_CACERT_REL "./exe/cacert.pem"

extern char *GoAndroid_CACertBundle(void);

const char *tipsy_cacert_wanted(void)
{
	return TIPSY_CACERT_REL;
}

static int tipsy_cacert_match(const char *path)
{
	if (path == NULL) {
		return 0;
	}
	return strcmp(path, TIPSY_CACERT_REL) == 0;
}

/* Remap predicate. For openat the redirect applies only under AT_FDCWD, so
 * an explicit dirfd keeps its exact relative-to-fd semantics. */
static char *tipsy_cacert_remap(const char *path, int check_dirfd, int dirfd)
{
	if (!tipsy_cacert_match(path)) {
		return NULL;
	}
	if (check_dirfd && dirfd != AT_FDCWD) {
		return NULL;
	}
	return GoAndroid_CACertBundle();
}

/* Best-effort one-shot diagnostics (races only duplicate a line, never
 * change behavior). Loud on a missing bundle, quiet otherwise. */
static int g_cacert_ok_logged = 0;
static int g_cacert_miss_logged = 0;

static void tipsy_cacert_log(int ok)
{
	if (ok) {
		if (!g_cacert_ok_logged) {
			g_cacert_ok_logged = 1;
			fprintf(stderr, "tipsy: cacert shim: redirected %s to the FilesDir bundle\n",
				TIPSY_CACERT_REL);
		}
		return;
	}
	if (!g_cacert_miss_logged) {
		g_cacert_miss_logged = 1;
		fprintf(stderr, "tipsy: cacert shim: FilesDir bundle absent for %s (honest ENOENT, no host-CA fallback)\n",
			TIPSY_CACERT_REL);
	}
}

int tipsy_open(const char *path, int flags, ...)
{
	char *remap = tipsy_cacert_remap(path, 0, 0);
	int need_mode = (flags & (O_CREAT | O_TMPFILE)) != 0;
	mode_t mode = 0;

	if (need_mode) {
		va_list ap;
		va_start(ap, flags);
		mode = va_arg(ap, mode_t);
		va_end(ap);
	}
	if (remap != NULL) {
		int fd = need_mode ? open(remap, flags, mode) : open(remap, flags);
		tipsy_cacert_log(fd >= 0);
		free(remap);
		return fd;
	}
	return need_mode ? open(path, flags, mode) : open(path, flags);
}

int tipsy_open_2(const char *path, int flags)
{
	char *remap = tipsy_cacert_remap(path, 0, 0);

	if (remap != NULL) {
		int fd = open(remap, flags);
		tipsy_cacert_log(fd >= 0);
		free(remap);
		return fd;
	}
	return open(path, flags);
}

int tipsy_openat(int dirfd, const char *path, int flags, ...)
{
	char *remap;
	int need_mode = (flags & (O_CREAT | O_TMPFILE)) != 0;
	mode_t mode = 0;

	if (need_mode) {
		va_list ap;
		va_start(ap, flags);
		mode = va_arg(ap, mode_t);
		va_end(ap);
	}
	remap = tipsy_cacert_remap(path, 1, dirfd);
	if (remap != NULL) {
		/* Absolute bundle path: dirfd is intentionally not consulted. */
		int fd = need_mode ? open(remap, flags, mode) : open(remap, flags);
		tipsy_cacert_log(fd >= 0);
		free(remap);
		return fd;
	}
	return need_mode ? openat(dirfd, path, flags, mode) : openat(dirfd, path, flags);
}

FILE *tipsy_fopen(const char *path, const char *mode)
{
	char *remap = tipsy_cacert_remap(path, 0, 0);

	if (remap != NULL) {
		FILE *fp = fopen(remap, mode);
		tipsy_cacert_log(fp != NULL);
		free(remap);
		return fp;
	}
	return fopen(path, mode);
}

void tipsy_FD_SET_chk(int fd, fd_set *set, size_t setlen)
{
	(void)setlen;
	FD_SET(fd, set);
}

void tipsy_FD_CLR_chk(int fd, fd_set *set, size_t setlen)
{
	(void)setlen;
	FD_CLR(fd, set);
}

int tipsy_FD_ISSET_chk(int fd, fd_set *set, size_t setlen)
{
	(void)setlen;
	return FD_ISSET(fd, set);
}

void tipsy_assert2(const char *file, int line, const char *func, const char *msg)
{
	fprintf(stderr, "tipsy: %s:%d: %s: assertion failed: %s\n", file, line, func ? func : "?", msg ? msg : "");
	abort();
}

int tipsy_gnu_strerror_r(int errnum, char *buf, size_t buflen)
{
	if (buf == NULL || buflen == 0) {
		return -1;
	}
	snprintf(buf, buflen, "%s", strerror(errnum));
	return 0;
}

char *tipsy_strcpy_chk(char *dest, const char *src, size_t dest_len)
{
	(void)dest_len;
	return strcpy(dest, src);
}

char *tipsy_strcat_chk(char *dest, const char *src, size_t dest_len)
{
	(void)dest_len;
	return strcat(dest, src);
}

int tipsy_snprintf_chk(char *s, size_t maxlen, int flags, size_t slen, const char *fmt, ...)
{
	va_list ap;
	int n;
	(void)flags;
	(void)slen;
	va_start(ap, fmt);
	n = vsnprintf(s, maxlen, fmt, ap);
	va_end(ap);
	return n;
}

int tipsy_vsnprintf_chk(char *s, size_t maxlen, int flags, size_t slen, const char *fmt, va_list ap)
{
	(void)flags;
	(void)slen;
	return vsnprintf(s, maxlen, fmt, ap);
}

int tipsy_poll_chk(struct pollfd *fds, nfds_t nfds, int timeout, size_t fdslen)
{
	(void)fdslen;
	return poll(fds, nfds, timeout);
}

void tipsy_FD_ZERO_chk(fd_set *set, size_t setlen)
{
	(void)setlen;
	FD_ZERO(set);
}

int tipsy_openat_2(int dirfd, const char *path, int flags)
{
	char *remap = tipsy_cacert_remap(path, 1, dirfd);

	if (remap != NULL) {
		/* Absolute bundle path: dirfd is intentionally not consulted. */
		int fd = open(remap, flags);
		tipsy_cacert_log(fd >= 0);
		free(remap);
		return fd;
	}
	return openat(dirfd, path, flags);
}

/* bionic __sF is a 152-byte FILE[3]. Wrappers map those slots onto host
 * stdin/stdout/stderr and never pass them to glibc stdio. */

int tipsy_fflush(FILE *stream)
{
	FILE *s;

	if (stream == NULL) {
		int r = 0;
		if (stdin != NULL) {
			r |= fflush(stdin);
		}
		if (stdout != NULL) {
			r |= fflush(stdout);
		}
		if (stderr != NULL) {
			r |= fflush(stderr);
		}
		return r;
	}
	s = map_bionic_file(stream);
	if (s == NULL) {
		return 0;
	}
	return fflush(s);
}

size_t tipsy_fwrite(const void *ptr, size_t size, size_t nmemb, FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		return 0;
	}
	return fwrite(ptr, size, nmemb, s);
}

size_t tipsy_fread(void *ptr, size_t size, size_t nmemb, FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		return 0;
	}
	return fread(ptr, size, nmemb, s);
}

size_t tipsy_fread_chk(void *ptr, size_t size, size_t nmemb, FILE *stream, size_t bufsize)
{
	(void)bufsize;
	return tipsy_fread(ptr, size, nmemb, stream);
}

int tipsy_fprintf(FILE *stream, const char *fmt, ...)
{
	FILE *s;
	va_list ap;
	int n;

	s = map_bionic_file(stream);
	if (s == NULL || fmt == NULL) {
		return -1;
	}
	va_start(ap, fmt);
	n = vfprintf(s, fmt, ap);
	va_end(ap);
	return n;
}

int tipsy_vfprintf(FILE *stream, const char *fmt, va_list ap)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL || fmt == NULL) {
		return -1;
	}
	return vfprintf(s, fmt, ap);
}

int tipsy_fclose(FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		return EOF;
	}
	if (s == stdin || s == stdout || s == stderr) {
		return fflush(s);
	}
	return fclose(s);
}

int tipsy_fileno(FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		errno = EBADF;
		return -1;
	}
	return fileno(s);
}

int tipsy_fputc(int c, FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		return EOF;
	}
	return fputc(c, s);
}

int tipsy_fputs(const char *s, FILE *stream)
{
	FILE *fp = map_bionic_file(stream);
	if (fp == NULL) {
		return EOF;
	}
	return fputs(s, fp);
}

char *tipsy_fgets(char *s, int n, FILE *stream)
{
	FILE *fp = map_bionic_file(stream);
	if (fp == NULL) {
		return NULL;
	}
	return fgets(s, n, fp);
}

int tipsy_fseek(FILE *stream, long off, int whence)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		errno = EBADF;
		return -1;
	}
	return fseek(s, off, whence);
}

int tipsy_fseeko(FILE *stream, off_t off, int whence)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		errno = EBADF;
		return -1;
	}
	return fseeko(s, off, whence);
}

long tipsy_ftell(FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		errno = EBADF;
		return -1;
	}
	return ftell(s);
}

off_t tipsy_ftello(FILE *stream)
{
	FILE *s = map_bionic_file(stream);
	if (s == NULL) {
		errno = EBADF;
		return (off_t)-1;
	}
	return ftello(s);
}

uint64_t tipsy_stack_chk_guard;

void tipsy_gcov_nop(void) {}

/* Bionic `_SC_*` values (AOSP bits/sysconf.h) are not glibc's enum.
 * libroblox.so passes these immediates; glibc sysconf(39) is
 * _SC_DELAYTIMER_MAX (1000), not the page size. Mimalloc then treats
 * the OS page as 1000 bytes and fails the first TLD mmap. */
static int bionic_sc_to_host(int name)
{
	switch (name) {
	case 0x0000:
		return _SC_ARG_MAX;
	case 0x0001:
		return _SC_BC_BASE_MAX;
	case 0x0002:
		return _SC_BC_DIM_MAX;
	case 0x0003:
		return _SC_BC_SCALE_MAX;
	case 0x0004:
		return _SC_BC_STRING_MAX;
	case 0x0005:
		return _SC_CHILD_MAX;
	case 0x0006:
		return _SC_CLK_TCK;
	case 0x0007:
		return _SC_COLL_WEIGHTS_MAX;
	case 0x0008:
		return _SC_EXPR_NEST_MAX;
	case 0x0009:
		return _SC_LINE_MAX;
	case 0x000a:
		return _SC_NGROUPS_MAX;
	case 0x000b:
		return _SC_OPEN_MAX;
	case 0x000c:
		return _SC_PASS_MAX;
	case 0x000d:
		return _SC_2_C_BIND;
	case 0x000e:
		return _SC_2_C_DEV;
	case 0x000f:
		return _SC_2_C_VERSION;
	case 0x0010:
		return _SC_2_CHAR_TERM;
	case 0x0011:
		return _SC_2_FORT_DEV;
	case 0x0012:
		return _SC_2_FORT_RUN;
	case 0x0013:
		return _SC_2_LOCALEDEF;
	case 0x0014:
		return _SC_2_SW_DEV;
	case 0x0015:
		return _SC_2_UPE;
	case 0x0016:
		return _SC_2_VERSION;
	case 0x0017:
		return _SC_JOB_CONTROL;
	case 0x0018:
		return _SC_SAVED_IDS;
	case 0x0019:
		return _SC_VERSION;
	case 0x001a:
		return _SC_RE_DUP_MAX;
	case 0x001b:
		return _SC_STREAM_MAX;
	case 0x001c:
		return _SC_TZNAME_MAX;
	case 0x001d:
		return _SC_XOPEN_CRYPT;
	case 0x001e:
		return _SC_XOPEN_ENH_I18N;
	case 0x001f:
		return _SC_XOPEN_SHM;
	case 0x0020:
		return _SC_XOPEN_VERSION;
	case 0x0021:
		return _SC_XOPEN_XCU_VERSION;
	case 0x0022:
		return _SC_XOPEN_REALTIME;
	case 0x0023:
		return _SC_XOPEN_REALTIME_THREADS;
	case 0x0024:
		return _SC_XOPEN_LEGACY;
	case 0x0025:
		return _SC_ATEXIT_MAX;
	case 0x0026: /* _SC_IOV_MAX / _SC_UIO_MAXIOV */
		return _SC_IOV_MAX;
	case 0x0027: /* bionic _SC_PAGESIZE */
	case 0x0028: /* bionic _SC_PAGE_SIZE */
		return _SC_PAGESIZE;
	case 0x0029:
		return _SC_XOPEN_UNIX;
	case 0x002e:
		return _SC_AIO_LISTIO_MAX;
	case 0x002f:
		return _SC_AIO_MAX;
	case 0x0030:
		return _SC_AIO_PRIO_DELTA_MAX;
	case 0x0031:
		return _SC_DELAYTIMER_MAX;
	case 0x0032:
		return _SC_MQ_OPEN_MAX;
	case 0x0033:
		return _SC_MQ_PRIO_MAX;
	case 0x0034:
		return _SC_RTSIG_MAX;
	case 0x0035:
		return _SC_SEM_NSEMS_MAX;
	case 0x0036:
		return _SC_SEM_VALUE_MAX;
	case 0x0037:
		return _SC_SIGQUEUE_MAX;
	case 0x0038:
		return _SC_TIMER_MAX;
	case 0x0039:
		return _SC_ASYNCHRONOUS_IO;
	case 0x003a:
		return _SC_FSYNC;
	case 0x003b:
		return _SC_MAPPED_FILES;
	case 0x003c:
		return _SC_MEMLOCK;
	case 0x003d:
		return _SC_MEMLOCK_RANGE;
	case 0x003e:
		return _SC_MEMORY_PROTECTION;
	case 0x003f:
		return _SC_MESSAGE_PASSING;
	case 0x0040:
		return _SC_PRIORITIZED_IO;
	case 0x0041:
		return _SC_PRIORITY_SCHEDULING;
	case 0x0042:
		return _SC_REALTIME_SIGNALS;
	case 0x0043:
		return _SC_SEMAPHORES;
	case 0x0044:
		return _SC_SHARED_MEMORY_OBJECTS;
	case 0x0045:
		return _SC_SYNCHRONIZED_IO;
	case 0x0046:
		return _SC_TIMERS;
	case 0x0047:
		return _SC_GETGR_R_SIZE_MAX;
	case 0x0048:
		return _SC_GETPW_R_SIZE_MAX;
	case 0x0049:
		return _SC_LOGIN_NAME_MAX;
	case 0x004a:
		return _SC_THREAD_DESTRUCTOR_ITERATIONS;
	case 0x004b:
		return _SC_THREAD_KEYS_MAX;
	case 0x004c:
		return _SC_THREAD_STACK_MIN;
	case 0x004d:
		return _SC_THREAD_THREADS_MAX;
	case 0x004e:
		return _SC_TTY_NAME_MAX;
	case 0x004f:
		return _SC_THREADS;
	case 0x0050:
		return _SC_THREAD_ATTR_STACKADDR;
	case 0x0051:
		return _SC_THREAD_ATTR_STACKSIZE;
	case 0x0052:
		return _SC_THREAD_PRIORITY_SCHEDULING;
	case 0x0053:
		return _SC_THREAD_PRIO_INHERIT;
	case 0x0054:
		return _SC_THREAD_PRIO_PROTECT;
	case 0x0055:
		return _SC_THREAD_SAFE_FUNCTIONS;
	case 0x0060:
		return _SC_NPROCESSORS_CONF;
	case 0x0061:
		return _SC_NPROCESSORS_ONLN;
	case 0x0062:
		return _SC_PHYS_PAGES;
	case 0x0063:
		return _SC_AVPHYS_PAGES;
	case 0x0064:
		return _SC_MONOTONIC_CLOCK;
	case 0x006b:
		return _SC_ADVISORY_INFO;
	case 0x006c:
		return _SC_BARRIERS;
	case 0x006d:
		return _SC_CLOCK_SELECTION;
	case 0x006e:
		return _SC_CPUTIME;
	case 0x006f:
		return _SC_HOST_NAME_MAX;
	case 0x0070:
		return _SC_IPV6;
	case 0x0071:
		return _SC_RAW_SOCKETS;
	case 0x0072:
		return _SC_READER_WRITER_LOCKS;
	case 0x0073:
		return _SC_REGEXP;
	case 0x0074:
		return _SC_SHELL;
	case 0x0075:
		return _SC_SPAWN;
	case 0x0076:
		return _SC_SPIN_LOCKS;
	case 0x0079:
		return _SC_SYMLOOP_MAX;
	case 0x007a:
		return _SC_THREAD_CPUTIME;
	case 0x007b:
		return _SC_THREAD_PROCESS_SHARED;
#ifdef _SC_THREAD_ROBUST_PRIO_INHERIT
	case 0x007c:
		return _SC_THREAD_ROBUST_PRIO_INHERIT;
#endif
#ifdef _SC_THREAD_ROBUST_PRIO_PROTECT
	case 0x007d:
		return _SC_THREAD_ROBUST_PRIO_PROTECT;
#endif
	case 0x007f:
		return _SC_TIMEOUTS;
#ifdef _SC_LEVEL1_ICACHE_SIZE
	case 0x008f:
		return _SC_LEVEL1_ICACHE_SIZE;
	case 0x0090:
		return _SC_LEVEL1_ICACHE_ASSOC;
	case 0x0091:
		return _SC_LEVEL1_ICACHE_LINESIZE;
	case 0x0092:
		return _SC_LEVEL1_DCACHE_SIZE;
	case 0x0093:
		return _SC_LEVEL1_DCACHE_ASSOC;
	case 0x0094:
		return _SC_LEVEL1_DCACHE_LINESIZE;
	case 0x0095:
		return _SC_LEVEL2_CACHE_SIZE;
	case 0x0096:
		return _SC_LEVEL2_CACHE_ASSOC;
	case 0x0097:
		return _SC_LEVEL2_CACHE_LINESIZE;
	case 0x0098:
		return _SC_LEVEL3_CACHE_SIZE;
	case 0x0099:
		return _SC_LEVEL3_CACHE_ASSOC;
	case 0x009a:
		return _SC_LEVEL3_CACHE_LINESIZE;
	case 0x009b:
		return _SC_LEVEL4_CACHE_SIZE;
	case 0x009c:
		return _SC_LEVEL4_CACHE_ASSOC;
	case 0x009d:
		return _SC_LEVEL4_CACHE_LINESIZE;
#endif
	default:
		return -1;
	}
}

long tipsy_sysconf(int name)
{
	int host = bionic_sc_to_host(name);

	if (host < 0) {
		errno = EINVAL;
		return -1;
	}
	return sysconf(host);
}

void *tipsy_zstd_trace_begin(const void *ctx)
{
	(void)ctx;
	return NULL;
}

void tipsy_zstd_trace_end(void *trace, const void *info)
{
	(void)trace;
	(void)info;
}

void tipsy_bionic_compat_init(void)
{
	uint64_t host_canary = 0;

	/* Match glibc's TLS canary (fs:0x28) so NDK stack-protector frames
	 * that mix a global __stack_chk_guard with host libc agree. */
	__asm__ volatile("movq %%fs:0x28, %0" : "=r"(host_canary));
	if (host_canary != 0) {
		tipsy_stack_chk_guard = host_canary;
	} else if (tipsy_stack_chk_guard == 0) {
		tipsy_stack_chk_guard = ((uint64_t)(uintptr_t)&tipsy_stack_chk_guard << 8) ^ 0x9e3779b97f4a7c15ull;
		if (tipsy_stack_chk_guard == 0) {
			tipsy_stack_chk_guard = 0xa5a5a5a5a5a5a5a5ull;
		}
	}
}

__attribute__((constructor)) static void tipsy_bionic_compat_ctor(void)
{
	tipsy_bionic_compat_init();
}
