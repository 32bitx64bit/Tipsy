/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_EGL_H
#define TIPSY_EGL_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

const char *tipsy_egl_bind_path(void);
int tipsy_x11_refresh_rates(uintptr_t xdisplay, unsigned long xid,
	double *current, double *out, int capacity);
int tipsy_egl_bind(uintptr_t xdisplay, unsigned long xid,
	uintptr_t *out_dpy, uintptr_t *out_surf, uintptr_t *out_ctx);
int tipsy_egl_make_current(uintptr_t dpy, uintptr_t surf, uintptr_t ctx);
int tipsy_egl_swap(uintptr_t dpy, uintptr_t surf);
int tipsy_egl_release_current(uintptr_t dpy);
uintptr_t tipsy_egl_swap_thread_start(uintptr_t xdpy, unsigned long xid,
	uintptr_t dpy, uintptr_t surf, uintptr_t ctx, uintptr_t go_handle);
int tipsy_egl_swap_thread_stop(uintptr_t ptr);
void tipsy_egl_swap_thread_state(uintptr_t ptr, int *out_retired,
	unsigned long *out_probes, unsigned long *out_failed);
int tipsy_egl_close(uintptr_t dpy, uintptr_t surf, uintptr_t ctx);
const char *tipsy_egl_query(uintptr_t dpy, int name);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_EGL_H */
