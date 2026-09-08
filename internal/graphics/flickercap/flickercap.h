/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_FLICKERCAP_H
#define TIPSY_FLICKERCAP_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

int tipsy_fcap_find(uintptr_t *out_dpy, unsigned long *out_win, int *out_w, int *out_h);
int tipsy_fcap_grid(uintptr_t dpy, unsigned long win, int w, int h,
	int gw, int gh, unsigned char *out);
int tipsy_fcap_full(uintptr_t dpy, unsigned long win, int w, int h, unsigned char **out);
void tipsy_fcap_free(unsigned char *p);
void tipsy_fcap_close(uintptr_t dpy);
int tipsy_fcap_attach(uintptr_t *out_dpy, unsigned long xid, int *out_w, int *out_h);
int tipsy_fcap_paint_white(uintptr_t dpy, unsigned long xid, int w, int h);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_FLICKERCAP_H */
