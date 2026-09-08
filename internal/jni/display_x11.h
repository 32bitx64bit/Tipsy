/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */
#ifndef TIPSY_JNI_DISPLAY_X11_H
#define TIPSY_JNI_DISPLAY_X11_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void tipsy_x11_physical_mm(uintptr_t dpy_ptr, int scr, int *w_mm, int *h_mm);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_JNI_DISPLAY_X11_H */
