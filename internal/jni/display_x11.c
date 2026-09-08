/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "display_x11.h"

#include <X11/Xlib.h>

// DisplayWidthMM/DisplayHeightMM are Xlib macros; cgo cannot call them
// directly. This returns the X server's reported physical screen size in
// millimeters for screen scr (0 = default), or 0,0 when unavailable.
void tipsy_x11_physical_mm(uintptr_t dpy_ptr, int scr, int *w_mm, int *h_mm) {
	Display *dpy = (Display *)dpy_ptr;
	if (dpy == NULL || w_mm == NULL || h_mm == NULL) {
		return;
	}
	*w_mm = DisplayWidthMM(dpy, scr);
	*h_mm = DisplayHeightMM(dpy, scr);
}
