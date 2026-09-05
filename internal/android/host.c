/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Host glibc/Mesa dlsym helpers and EGL window-surface translation.
 */
#include "android_bridge.h"

#include <dlfcn.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>
#include <stdio.h>
#include <stdatomic.h>
#include <time.h>

extern void GoAndroid_LogMissing(char *name);

static void *lib_libc;
static void *lib_libm;
static void *lib_libz;
static void *lib_egl;
static void *lib_gles;

typedef void *EGLDisplay;
typedef void *EGLConfig;
typedef void *EGLSurface;
typedef int32_t EGLint;
typedef uint32_t EGLBoolean;

typedef EGLSurface (*egl_create_window_surface_fn)(EGLDisplay, EGLConfig, void *, const EGLint *);
typedef void *(*egl_get_proc_address_fn)(const char *);
typedef EGLBoolean (*egl_swap_interval_fn)(EGLDisplay, EGLint);
typedef EGLBoolean (*egl_swap_buffers_fn)(EGLDisplay, EGLSurface);
typedef EGLint (*egl_get_error_fn)(void);

static egl_create_window_surface_fn host_eglCreateWindowSurface;
static egl_get_proc_address_fn host_eglGetProcAddress;
static egl_swap_interval_fn host_eglSwapInterval;
static egl_swap_buffers_fn host_eglSwapBuffers;
static egl_get_error_fn host_eglGetError;
static _Atomic int egl_vsync_enabled;
static _Atomic uint64_t egl_successful_swaps;
static _Atomic uint64_t egl_first_swap_ns;
static _Atomic uint64_t egl_last_swap_ns;

#define TIPSY_EGL_FALSE ((EGLBoolean)0)
#define TIPSY_EGL_TRUE ((EGLBoolean)1)
#define TIPSY_EGL_SUCCESS ((EGLint)0x3000)

extern void GoAndroid_LogEGLSwapInterval(int enabled, int requested, int effective,
	int primary_ok, int primary_error, int fallback_ok, int fallback_error);

static void *open_lib(const char *name)
{
	return dlopen(name, RTLD_NOW | RTLD_LOCAL);
}

void *tipsy_host_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	p = dlsym(RTLD_DEFAULT, name);
	if (p != NULL) {
		return p;
	}
	if (lib_libc == NULL) {
		lib_libc = open_lib("libc.so.6");
	}
	if (lib_libm == NULL) {
		lib_libm = open_lib("libm.so.6");
	}
	if (lib_libz == NULL) {
		lib_libz = open_lib("libz.so.1");
	}
	if (lib_libc != NULL) {
		p = dlsym(lib_libc, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_libm != NULL) {
		p = dlsym(lib_libm, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_libz != NULL) {
		p = dlsym(lib_libz, name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

static void ensure_egl(void)
{
	if (lib_egl == NULL) {
		lib_egl = open_lib("libEGL.so.1");
		if (lib_egl == NULL) {
			lib_egl = open_lib("libEGL.so");
		}
	}
	if (lib_gles == NULL) {
		lib_gles = open_lib("libGLESv2.so.2");
		if (lib_gles == NULL) {
			lib_gles = open_lib("libGLESv2.so");
		}
	}
	if (lib_egl != NULL) {
		if (host_eglCreateWindowSurface == NULL) {
			host_eglCreateWindowSurface = (egl_create_window_surface_fn)dlsym(lib_egl, "eglCreateWindowSurface");
		}
		if (host_eglGetProcAddress == NULL) {
			host_eglGetProcAddress = (egl_get_proc_address_fn)dlsym(lib_egl, "eglGetProcAddress");
		}
		if (host_eglSwapInterval == NULL) {
			host_eglSwapInterval = (egl_swap_interval_fn)dlsym(lib_egl, "eglSwapInterval");
		}
		if (host_eglSwapBuffers == NULL) {
			host_eglSwapBuffers = (egl_swap_buffers_fn)dlsym(lib_egl, "eglSwapBuffers");
		}
		if (host_eglGetError == NULL) {
			host_eglGetError = (egl_get_error_fn)dlsym(lib_egl, "eglGetError");
		}
	}
}

static void tipsy_egl_record_successful_swap(uint64_t now_ns)
{
	uint64_t expected = 0;
	atomic_compare_exchange_strong_explicit(&egl_first_swap_ns, &expected, now_ns,
		memory_order_acq_rel, memory_order_acquire);
	atomic_store_explicit(&egl_last_swap_ns, now_ns, memory_order_release);
	atomic_fetch_add_explicit(&egl_successful_swaps, 1, memory_order_acq_rel);
}

static uint64_t tipsy_monotonic_ns(void)
{
	struct timespec ts;
	if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) {
		return 0;
	}
	return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

void tipsy_egl_reset_swap_stats(void)
{
	atomic_store_explicit(&egl_successful_swaps, 0, memory_order_release);
	atomic_store_explicit(&egl_first_swap_ns, 0, memory_order_release);
	atomic_store_explicit(&egl_last_swap_ns, 0, memory_order_release);
}

void tipsy_egl_swap_stats(uint64_t *successful_swaps, uint64_t *first_ns,
	uint64_t *last_ns)
{
	if (successful_swaps != NULL) {
		*successful_swaps = atomic_load_explicit(&egl_successful_swaps, memory_order_acquire);
	}
	if (first_ns != NULL) {
		*first_ns = atomic_load_explicit(&egl_first_swap_ns, memory_order_acquire);
	}
	if (last_ns != NULL) {
		*last_ns = atomic_load_explicit(&egl_last_swap_ns, memory_order_acquire);
	}
}

void tipsy_test_egl_record_swap(uint64_t now_ns)
{
	tipsy_egl_record_successful_swap(now_ns);
}

void tipsy_egl_set_vsync(int enabled)
{
	atomic_store_explicit(&egl_vsync_enabled, enabled != 0, memory_order_release);
}

int tipsy_egl_vsync_enabled(void)
{
	return atomic_load_explicit(&egl_vsync_enabled, memory_order_acquire);
}

EGLBoolean tipsy_eglSwapInterval(EGLDisplay dpy, EGLint interval)
{
	EGLBoolean primary_ok;
	EGLBoolean fallback_ok = TIPSY_EGL_FALSE;
	EGLint primary_error;
	EGLint fallback_error = TIPSY_EGL_SUCCESS;
	int vsync;
	EGLint desired;

	ensure_egl();
	if (host_eglSwapInterval == NULL) {
		GoAndroid_LogMissing("eglSwapInterval");
		return TIPSY_EGL_FALSE;
	}
	vsync = tipsy_egl_vsync_enabled();
	desired = vsync ? 1 : 0;
	primary_ok = host_eglSwapInterval(dpy, desired);
	if (primary_ok == TIPSY_EGL_TRUE) {
		/* A new client presentation epoch (notably the transition from
		 * Landing to an experience surface) owns a fresh rate window. */
		tipsy_egl_reset_swap_stats();
		GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 1,
			TIPSY_EGL_SUCCESS, 0, TIPSY_EGL_SUCCESS);
		return TIPSY_EGL_TRUE;
	}

	if (desired == interval) {
		/* Do not consume a failed passthrough call's thread-local EGL error;
		 * the official client owns the next eglGetError. */
		GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 0, 0, 0,
			TIPSY_EGL_SUCCESS);
		return TIPSY_EGL_FALSE;
	}

	primary_error = host_eglGetError != NULL ? host_eglGetError() : 0;
	/* Preserve the official client's exact request when the host rejects the
	 * independent VSync policy. */
	fallback_ok = host_eglSwapInterval(dpy, interval);
	/* As with ordinary passthrough failure, leave fallback's EGL error for
	 * the client. Zero means deliberately unconsumed, not EGL_SUCCESS. */
	fallback_error = fallback_ok == TIPSY_EGL_TRUE ? TIPSY_EGL_SUCCESS : 0;
	if (fallback_ok == TIPSY_EGL_TRUE) {
		tipsy_egl_reset_swap_stats();
	}
	GoAndroid_LogEGLSwapInterval(vsync, interval, desired, 0, primary_error,
		fallback_ok == TIPSY_EGL_TRUE, fallback_error);
	return fallback_ok;
}

EGLBoolean tipsy_eglSwapBuffers(EGLDisplay dpy, EGLSurface surface)
{
	EGLBoolean ok;
	uint64_t now_ns;

	ensure_egl();
	if (host_eglSwapBuffers == NULL) {
		GoAndroid_LogMissing("eglSwapBuffers");
		return TIPSY_EGL_FALSE;
	}
	ok = host_eglSwapBuffers(dpy, surface);
	if (ok == TIPSY_EGL_TRUE) {
		now_ns = tipsy_monotonic_ns();
		if (now_ns != 0) {
			tipsy_egl_record_successful_swap(now_ns);
		}
	}
	return ok;
}

EGLSurface tipsy_eglCreateWindowSurface(EGLDisplay dpy, EGLConfig config, void *native_window, const EGLint *attrib_list)
{
	void *host_win = native_window;

	ensure_egl();
	if (tipsy_is_anative_window(native_window)) {
		host_win = (void *)tipsy_ANativeWindow_get_handle(native_window);
	}
	if (host_eglCreateWindowSurface == NULL) {
		GoAndroid_LogMissing("eglCreateWindowSurface");
		return NULL;
	}
	return host_eglCreateWindowSurface(dpy, config, host_win, attrib_list);
}

void *tipsy_eglGetProcAddress(const char *name)
{
	void *p;

	ensure_egl();
	if (name != NULL) {
		if (strcmp(name, "eglCreateWindowSurface") == 0) {
			return (void *)tipsy_eglCreateWindowSurface;
		}
		if (strcmp(name, "eglSwapInterval") == 0) {
			return (void *)tipsy_eglSwapInterval;
		}
		if (strcmp(name, "eglSwapBuffers") == 0) {
			return (void *)tipsy_eglSwapBuffers;
		}
		if (strcmp(name, "eglGetProcAddress") == 0) {
			return (void *)tipsy_eglGetProcAddress;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_egl != NULL && name != NULL) {
		p = dlsym(lib_egl, name);
		if (p != NULL) {
			return p;
		}
	}
	if (lib_gles != NULL && name != NULL) {
		p = dlsym(lib_gles, name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

int tipsy_test_egl_proc_is_wrapped(const char *name)
{
	void *got = tipsy_eglGetProcAddress(name);
	if (name == NULL) {
		return 0;
	}
	if (strcmp(name, "eglSwapInterval") == 0) {
		return got == (void *)tipsy_eglSwapInterval;
	}
	if (strcmp(name, "eglSwapBuffers") == 0) {
		return got == (void *)tipsy_eglSwapBuffers;
	}
	return 0;
}

void *tipsy_egl_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, "eglCreateWindowSurface") == 0) {
		return (void *)tipsy_eglCreateWindowSurface;
	}
	if (strcmp(name, "eglSwapInterval") == 0) {
		return (void *)tipsy_eglSwapInterval;
	}
	if (strcmp(name, "eglSwapBuffers") == 0) {
		return (void *)tipsy_eglSwapBuffers;
	}
	if (strcmp(name, "eglGetProcAddress") == 0) {
		return (void *)tipsy_eglGetProcAddress;
	}
	ensure_egl();
	if (lib_egl != NULL) {
		p = dlsym(lib_egl, name);
		if (p != NULL) {
			return p;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}

struct tipsy_egl_policy_test {
	int policy_result;
	int policy_error;
	int client_result;
	int client_error;
	int desired;
	int requested;
	int first_interval;
	int second_interval;
	int calls;
	int reported_error;
};

static struct tipsy_egl_policy_test *active_egl_policy_test;

static EGLBoolean tipsy_test_egl_swap_interval(EGLDisplay dpy, EGLint interval)
{
	struct tipsy_egl_policy_test *t = active_egl_policy_test;
	(void)dpy;
	if (t == NULL) {
		return TIPSY_EGL_FALSE;
	}
	if (t->calls == 0) {
		t->first_interval = interval;
	} else if (t->calls == 1) {
		t->second_interval = interval;
	}
	t->calls++;
	if (interval == t->desired) {
		t->reported_error = t->policy_error;
		return t->policy_result ? TIPSY_EGL_TRUE : TIPSY_EGL_FALSE;
	}
	t->reported_error = t->client_error;
	return t->client_result ? TIPSY_EGL_TRUE : TIPSY_EGL_FALSE;
}

static EGLint tipsy_test_egl_get_error(void)
{
	EGLint result;
	if (active_egl_policy_test == NULL) {
		return 0;
	}
	result = active_egl_policy_test->reported_error;
	active_egl_policy_test->reported_error = 0;
	return result;
}

int tipsy_test_egl_swap_interval_policy(int vsync, int requested,
	int policy_result, int policy_error, int client_result, int client_error,
	int *first_interval, int *second_interval, int *calls, int *reported_error)
{
	struct tipsy_egl_policy_test t = {0};
	egl_swap_interval_fn saved_swap;
	egl_get_error_fn saved_error;
	EGLBoolean result;

	t.policy_result = policy_result;
	t.policy_error = policy_error;
	t.client_result = client_result;
	t.client_error = client_error;
	t.desired = vsync ? 1 : 0;
	t.requested = requested;
	saved_swap = host_eglSwapInterval;
	saved_error = host_eglGetError;
	active_egl_policy_test = &t;
	host_eglSwapInterval = tipsy_test_egl_swap_interval;
	host_eglGetError = tipsy_test_egl_get_error;
	tipsy_egl_set_vsync(vsync);
	result = tipsy_eglSwapInterval((EGLDisplay)(uintptr_t)1, requested);
	host_eglSwapInterval = saved_swap;
	host_eglGetError = saved_error;
	active_egl_policy_test = NULL;
	if (first_interval != NULL) {
		*first_interval = t.first_interval;
	}
	if (second_interval != NULL) {
		*second_interval = t.second_interval;
	}
	if (calls != NULL) {
		*calls = t.calls;
	}
	if (reported_error != NULL) {
		*reported_error = t.reported_error;
	}
	return result == TIPSY_EGL_TRUE;
}

void *tipsy_gles_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	ensure_egl();
	if (lib_gles != NULL) {
		p = dlsym(lib_gles, name);
		if (p != NULL) {
			return p;
		}
	}
	if (host_eglGetProcAddress != NULL) {
		p = host_eglGetProcAddress(name);
		if (p != NULL) {
			return p;
		}
	}
	return NULL;
}
