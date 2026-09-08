/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

#include "flickercap.h"

#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <X11/Xutil.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

static int tipsy_fcap_match(Display *d, Window w) {
	XClassHint ch;
	memset(&ch, 0, sizeof(ch));
	if (XGetClassHint(d, w, &ch)) {
		int hit = ch.res_class && strcmp(ch.res_class, "roblox") == 0;
		XFree(ch.res_name);
		if (ch.res_class) {
			XFree(ch.res_class);
		}
		if (hit) {
			return 1;
		}
	}
	char *name = NULL;
	XFetchName(d, w, &name);
	if (name != NULL) {
		int hit = strcmp(name, "Roblox") == 0;
		XFree(name);
		if (hit) {
			return 1;
		}
	}
	return 0;
}

static int tipsy_fcap_walk(Display *d, Window w, int depth, Window *out) {
	if (depth > 12) {
		return 0;
	}
	if (tipsy_fcap_match(d, w)) {
		*out = w;
		return 1;
	}
	Window root, parent, *kids = NULL;
	unsigned int n = 0;
	if (!XQueryTree(d, w, &root, &parent, &kids, &n)) {
		return 0;
	}
	int found = 0;
	for (unsigned int i = 0; i < n && !found; i++) {
		found = tipsy_fcap_walk(d, kids[i], depth + 1, out);
	}
	if (kids != NULL) {
		XFree(kids);
	}
	return found;
}

int tipsy_fcap_find(uintptr_t *out_dpy, unsigned long *out_win, int *out_w, int *out_h) {
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		return -1;
	}
	Window target = 0;
	if (!tipsy_fcap_walk(d, DefaultRootWindow(d), 0, &target)) {
		XCloseDisplay(d);
		return -2;
	}
	XWindowAttributes wa;
	if (!XGetWindowAttributes(d, target, &wa) || wa.map_state != IsViewable) {
		XCloseDisplay(d);
		return -3;
	}
	*out_dpy = (uintptr_t)d;
	*out_win = (unsigned long)target;
	*out_w = wa.width;
	*out_h = wa.height;
	return 0;
}

int tipsy_fcap_grid(uintptr_t dpy, unsigned long win, int w, int h,
	int gw, int gh, unsigned char *out) {
	Display *d = (Display *)dpy;
	XImage *img = XGetImage(d, (Window)win, 0, 0, (unsigned)w, (unsigned)h, AllPlanes, ZPixmap);
	if (img == NULL) {
		return -1;
	}
	for (int gy = 0; gy < gh; gy++) {
		int den = gh - 1 > 0 ? gh - 1 : 1;
		int py = (h - 1) * gy / den;
		for (int gx = 0; gx < gw; gx++) {
			int gden = gw - 1 > 0 ? gw - 1 : 1;
			int px = (w - 1) * gx / gden;
			unsigned long p = XGetPixel(img, px, py);
			unsigned char *o = out + ((size_t)gy * (size_t)gw + (size_t)gx) * 4;
			o[0] = (unsigned char)((p & img->red_mask) >> 16);
			o[1] = (unsigned char)((p & img->green_mask) >> 8);
			o[2] = (unsigned char)(p & img->blue_mask);
			o[3] = 0;
		}
	}
	XDestroyImage(img);
	return 0;
}

int tipsy_fcap_full(uintptr_t dpy, unsigned long win, int w, int h, unsigned char **out) {
	Display *d = (Display *)dpy;
	XImage *img = XGetImage(d, (Window)win, 0, 0, (unsigned)w, (unsigned)h, AllPlanes, ZPixmap);
	if (img == NULL) {
		return -1;
	}
	size_t n = (size_t)w * (size_t)h * 4;
	unsigned char *buf = (unsigned char *)malloc(n);
	if (buf == NULL) {
		XDestroyImage(img);
		return -2;
	}
	for (int y = 0; y < h; y++) {
		for (int x = 0; x < w; x++) {
			unsigned long p = XGetPixel(img, x, y);
			unsigned char *o = buf + ((size_t)y * (size_t)w + (size_t)x) * 4;
			o[0] = (unsigned char)((p & img->red_mask) >> 16);
			o[1] = (unsigned char)((p & img->green_mask) >> 8);
			o[2] = (unsigned char)(p & img->blue_mask);
			o[3] = 0;
		}
	}
	XDestroyImage(img);
	*out = buf;
	return 0;
}

void tipsy_fcap_free(unsigned char *p) {
	free(p);
}

void tipsy_fcap_close(uintptr_t dpy) {
	XCloseDisplay((Display *)dpy);
}

// Attach to an existing window by XID through a fresh connection.
int tipsy_fcap_attach(uintptr_t *out_dpy, unsigned long xid, int *out_w, int *out_h) {
	Display *d = XOpenDisplay(NULL);
	if (d == NULL) {
		return -1;
	}
	XWindowAttributes wa;
	if (!XGetWindowAttributes(d, (Window)xid, &wa) || wa.map_state != IsViewable) {
		XCloseDisplay(d);
		return -2;
	}
	*out_dpy = (uintptr_t)d;
	*out_w = wa.width;
	*out_h = wa.height;
	return 0;
}

// Paint the whole window white through this connection (diagnostics only).
int tipsy_fcap_paint_white(uintptr_t dpy, unsigned long xid, int w, int h) {
	Display *d = (Display *)dpy;
	GC gc = XCreateGC(d, (Window)xid, 0, NULL);
	if (gc == NULL) {
		return -1;
	}
	XSetForeground(d, gc, WhitePixel(d, DefaultScreen(d)));
	XFillRectangle(d, (Window)xid, gc, 0, 0, (unsigned)w, (unsigned)h);
	XFreeGC(d, gc);
	XFlush(d);
	XSync(d, False);
	return 0;
}
