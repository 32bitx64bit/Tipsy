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

typedef EGLSurface (*egl_create_window_surface_fn)(EGLDisplay, EGLConfig, void *, const EGLint *);
typedef void *(*egl_get_proc_address_fn)(const char *);

static egl_create_window_surface_fn host_eglCreateWindowSurface;
static egl_get_proc_address_fn host_eglGetProcAddress;

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
	}
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

void *tipsy_egl_dlsym(const char *name)
{
	void *p;

	if (name == NULL) {
		return NULL;
	}
	if (strcmp(name, "eglCreateWindowSurface") == 0) {
		return (void *)tipsy_eglCreateWindowSurface;
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
