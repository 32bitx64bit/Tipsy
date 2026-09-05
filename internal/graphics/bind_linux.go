// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package graphics

/*
#cgo pkg-config: x11 xrandr egl glesv2
#cgo LDFLAGS: -lX11 -lEGL -lGLESv2 -pthread

#define USE_X11 1
#include <X11/Xlib.h>
#include <X11/extensions/Xrandr.h>
#include <EGL/egl.h>
#include <EGL/eglext.h>
#include <GLES2/gl2.h>
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#ifndef EGL_PLATFORM_X11_KHR
#define EGL_PLATFORM_X11_KHR 0x31D5
#endif

static const char *tipsy_egl_path;

static EGLDisplay tipsy_egl_get_display(uintptr_t xdisplay) {
	typedef EGLDisplay (*get_platform_fn)(EGLenum, void *, const EGLAttrib *);
	typedef EGLDisplay (*get_platform_ext_fn)(EGLenum, void *, const EGLint *);

	void *sym = (void *)eglGetProcAddress("eglGetPlatformDisplay");
	if (sym != NULL) {
		EGLDisplay d = ((get_platform_fn)sym)(EGL_PLATFORM_X11_KHR, (void *)xdisplay, NULL);
		if (d != EGL_NO_DISPLAY) {
			tipsy_egl_path = "eglGetPlatformDisplay";
			return d;
		}
	}
	sym = (void *)eglGetProcAddress("eglGetPlatformDisplayEXT");
	if (sym != NULL) {
		EGLDisplay d = ((get_platform_ext_fn)sym)(EGL_PLATFORM_X11_KHR, (void *)xdisplay, NULL);
		if (d != EGL_NO_DISPLAY) {
			tipsy_egl_path = "eglGetPlatformDisplayEXT";
			return d;
		}
	}
	tipsy_egl_path = "eglGetDisplay";
	return eglGetDisplay((EGLNativeDisplayType)xdisplay);
}

const char *tipsy_egl_bind_path(void) {
	if (tipsy_egl_path == NULL) {
		return "";
	}
	return tipsy_egl_path;
}

// Return the refresh rate of the active CRTC containing the center of xid.
// XRandR mode timings are authoritative for the X11 presentation target. A
// zero result deliberately means unknown; callers can choose a conservative
// fallback without mistaking this observation for achieved application FPS.
static double tipsy_xrr_mode_refresh_rate(const XRRModeInfo *info) {
	if (info == NULL || info->dotClock == 0 || info->hTotal == 0 ||
		info->vTotal == 0) {
		return 0.0;
	}
	double hz = (double)info->dotClock /
		((double)info->hTotal * (double)info->vTotal);
	if ((info->modeFlags & RR_Interlace) != 0) {
		hz *= 2.0;
	}
	if ((info->modeFlags & RR_DoubleScan) != 0) {
		hz /= 2.0;
	}
	return hz;
}

static int tipsy_x11_active_crtc(Display *dpy, Window xid,
	XRRScreenResources *resources, Window root, RRCrtc *out_crtc,
	RRMode *out_mode) {
	XWindowAttributes wa;
	memset(&wa, 0, sizeof(wa));
	if (!XGetWindowAttributes(dpy, xid, &wa)) {
		return 0;
	}
	Window child = None;
	int root_x = 0, root_y = 0;
	if (!XTranslateCoordinates(dpy, xid, root, wa.width / 2, wa.height / 2,
		&root_x, &root_y, &child)) {
		return 0;
	}
	for (int i = 0; i < resources->ncrtc; i++) {
		XRRCrtcInfo *crtc = XRRGetCrtcInfo(dpy, resources, resources->crtcs[i]);
		if (crtc == NULL) {
			continue;
		}
		int contains = crtc->mode != None && root_x >= crtc->x &&
			root_y >= crtc->y && root_x < crtc->x + (int)crtc->width &&
			root_y < crtc->y + (int)crtc->height;
		if (contains) {
			*out_crtc = resources->crtcs[i];
			*out_mode = crtc->mode;
			XRRFreeCrtcInfo(crtc);
			return 1;
		}
		XRRFreeCrtcInfo(crtc);
	}
	return 0;
}

static double tipsy_x11_refresh_rate(uintptr_t xdisplay, unsigned long xid) {
	Display *dpy = (Display *)xdisplay;
	if (dpy == NULL || xid == 0) {
		return 0.0;
	}
	XWindowAttributes wa;
	memset(&wa, 0, sizeof(wa));
	if (!XGetWindowAttributes(dpy, (Window)xid, &wa)) {
		return 0.0;
	}
	Window root = RootWindowOfScreen(wa.screen);
	XRRScreenResources *resources = XRRGetScreenResourcesCurrent(dpy, root);
	if (resources == NULL) {
		return 0.0;
	}
	RRCrtc active_crtc = None;
	RRMode mode = None;
	(void)tipsy_x11_active_crtc(dpy, (Window)xid, resources, root,
		&active_crtc, &mode);
	double hz = 0.0;
	for (int i = 0; mode != None && i < resources->nmode; i++) {
		XRRModeInfo *info = &resources->modes[i];
		if (info->id != mode) {
			continue;
		}
		hz = tipsy_xrr_mode_refresh_rate(info);
		break;
	}
	XRRFreeScreenResources(resources);
	return hz;
}

static int tipsy_x11_supported_refresh_rates(uintptr_t xdisplay,
	unsigned long xid, double *out, int capacity) {
	Display *dpy = (Display *)xdisplay;
	if (dpy == NULL || xid == 0 || out == NULL || capacity <= 0) {
		return 0;
	}
	XWindowAttributes wa;
	memset(&wa, 0, sizeof(wa));
	if (!XGetWindowAttributes(dpy, (Window)xid, &wa)) {
		return 0;
	}
	Window root = RootWindowOfScreen(wa.screen);
	XRRScreenResources *resources = XRRGetScreenResourcesCurrent(dpy, root);
	if (resources == NULL) {
		return 0;
	}
	RRCrtc active_crtc = None;
	RRMode active_mode = None;
	if (!tipsy_x11_active_crtc(dpy, (Window)xid, resources, root,
		&active_crtc, &active_mode)) {
		XRRFreeScreenResources(resources);
		return 0;
	}
	int count = 0;
	for (int i = 0; i < resources->noutput && count < capacity; i++) {
		XRROutputInfo *output = XRRGetOutputInfo(dpy, resources,
			resources->outputs[i]);
		if (output == NULL) {
			continue;
		}
		if (output->connection == RR_Connected && output->crtc == active_crtc) {
			for (int j = 0; j < output->nmode && count < capacity; j++) {
				for (int k = 0; k < resources->nmode; k++) {
					if (resources->modes[k].id != output->modes[j]) {
						continue;
					}
					double hz = tipsy_xrr_mode_refresh_rate(&resources->modes[k]);
					if (hz > 0.0) {
						out[count++] = hz;
					}
					break;
				}
			}
		}
		XRRFreeOutputInfo(output);
	}
	XRRFreeScreenResources(resources);
	return count;
}

static int tipsy_choose_config(EGLDisplay dpy, VisualID visual, EGLConfig *out) {
	EGLint n = 0;
	const EGLint strict[] = {
		EGL_SURFACE_TYPE, EGL_WINDOW_BIT,
		EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT,
		EGL_RED_SIZE, 8,
		EGL_GREEN_SIZE, 8,
		EGL_BLUE_SIZE, 8,
		EGL_ALPHA_SIZE, 8,
		EGL_DEPTH_SIZE, 16,
		EGL_NATIVE_VISUAL_ID, (EGLint)visual,
		EGL_NONE,
	};
	if (visual != 0 && eglChooseConfig(dpy, strict, out, 1, &n) && n > 0) {
		return 0;
	}
	const EGLint relaxed[] = {
		EGL_SURFACE_TYPE, EGL_WINDOW_BIT,
		EGL_RENDERABLE_TYPE, EGL_OPENGL_ES2_BIT,
		EGL_RED_SIZE, 8,
		EGL_GREEN_SIZE, 8,
		EGL_BLUE_SIZE, 8,
		EGL_NONE,
	};
	n = 0;
	if (eglChooseConfig(dpy, relaxed, out, 1, &n) && n > 0) {
		return 0;
	}
	return -1;
}

int tipsy_egl_bind(uintptr_t xdisplay, unsigned long xid,
	uintptr_t *out_dpy, uintptr_t *out_surf, uintptr_t *out_ctx) {
	Display *xdpy = (Display *)xdisplay;
	if (xdpy == NULL || xid == 0) {
		return EGL_BAD_NATIVE_WINDOW;
	}

	EGLDisplay dpy = tipsy_egl_get_display(xdisplay);
	if (dpy == EGL_NO_DISPLAY) {
		return eglGetError();
	}
	EGLint major = 0, minor = 0;
	if (!eglInitialize(dpy, &major, &minor)) {
		return eglGetError();
	}
	if (!eglBindAPI(EGL_OPENGL_ES_API)) {
		EGLint err = eglGetError();
		eglTerminate(dpy);
		return err;
	}

	VisualID visual = 0;
	XWindowAttributes wa;
	memset(&wa, 0, sizeof(wa));
	if (XGetWindowAttributes(xdpy, (Window)xid, &wa) && wa.visual != NULL) {
		visual = XVisualIDFromVisual(wa.visual);
	}

	EGLConfig cfg;
	if (tipsy_choose_config(dpy, visual, &cfg) != 0) {
		EGLint err = eglGetError();
		eglTerminate(dpy);
		if (err == EGL_SUCCESS) {
			err = EGL_BAD_CONFIG;
		}
		return err;
	}

	const EGLint ctx_attr[] = {
		EGL_CONTEXT_CLIENT_VERSION, 2,
		EGL_NONE,
	};
	EGLContext ctx = eglCreateContext(dpy, cfg, EGL_NO_CONTEXT, ctx_attr);
	if (ctx == EGL_NO_CONTEXT) {
		EGLint err = eglGetError();
		eglTerminate(dpy);
		return err;
	}

	EGLSurface surf = eglCreateWindowSurface(dpy, cfg, (EGLNativeWindowType)xid, NULL);
	if (surf == EGL_NO_SURFACE) {
		EGLint err = eglGetError();
		eglDestroyContext(dpy, ctx);
		eglTerminate(dpy);
		return err;
	}
	if (!eglMakeCurrent(dpy, surf, surf, ctx)) {
		EGLint err = eglGetError();
		eglDestroySurface(dpy, surf);
		eglDestroyContext(dpy, ctx);
		eglTerminate(dpy);
		return err;
	}

	*out_dpy = (uintptr_t)dpy;
	*out_surf = (uintptr_t)surf;
	*out_ctx = (uintptr_t)ctx;
	return EGL_SUCCESS;
}

int tipsy_egl_make_current(uintptr_t dpy, uintptr_t surf, uintptr_t ctx) {
	if (dpy == 0 || surf == 0 || ctx == 0) {
		return EGL_BAD_CONTEXT;
	}
	if (!eglMakeCurrent((EGLDisplay)dpy, (EGLSurface)surf, (EGLSurface)surf, (EGLContext)ctx)) {
		return eglGetError();
	}
	return EGL_SUCCESS;
}

int tipsy_egl_swap(uintptr_t dpy, uintptr_t surf) {
	if (dpy == 0 || surf == 0) {
		return EGL_BAD_SURFACE;
	}
	if (!eglSwapBuffers((EGLDisplay)dpy, (EGLSurface)surf)) {
		return eglGetError();
	}
	return EGL_SUCCESS;
}

int tipsy_egl_release_current(uintptr_t dpy) {
	if (dpy == 0) {
		return EGL_BAD_DISPLAY;
	}
	if (!eglMakeCurrent((EGLDisplay)dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT)) {
		return eglGetError();
	}
	return EGL_SUCCESS;
}

struct tipsy_swap {
	uintptr_t xdpy; // Xlib Display* for observation-only content probes
	unsigned long xid; // X11 Window being presented
	uintptr_t dpy;
	uintptr_t surf;
	uintptr_t ctx;
	volatile int run;
	volatile int fail;
	volatile int retired; // 1 = client presenter detected; Tipsy stopped presenting
	unsigned long probes;
	unsigned long probes_failed;
	pthread_t thr;
};

// Sample small interior patches of the window through Xlib. The only frame
// Tipsy ever presents is a pure-black sentinel, so any non-black pixel is
// evidence that another presenter (the Roblox RenderJob) owns the window.
// This is observation-only: no writes to any window but our own, and no
// engine memory access.
static int tipsy_swap_probe_foreign_frame(struct tipsy_swap *p) {
	Display *d = (Display *)p->xdpy;
	if (d == NULL || p->xid == 0) {
		return 0;
	}
	Window root;
	int x, y;
	unsigned int w, h, bw, depth;
	if (!XGetGeometry(d, (Window)p->xid, &root, &x, &y, &w, &h, &bw, &depth)) {
		p->probes_failed++;
		return 0;
	}
	if (w < 8 || h < 8) {
		return 0;
	}
	static const float spots[3][2] = {{0.5f, 0.5f}, {0.25f, 0.25f}, {0.75f, 0.75f}};
	for (int i = 0; i < 3; i++) {
		int px = (int)(w * spots[i][0]);
		int py = (int)(h * spots[i][1]);
		if (px > (int)w - 8) {
			px = (int)w - 8;
		}
		if (py > (int)h - 8) {
			py = (int)h - 8;
		}
		XImage *img = XGetImage(d, (Window)p->xid, px, py, 8, 8, AllPlanes, ZPixmap);
		if (img == NULL) {
			p->probes_failed++;
			continue;
		}
		int foreign = 0;
		for (int yy = 0; yy < 8 && !foreign; yy++) {
			for (int xx = 0; xx < 8 && !foreign; xx++) {
				unsigned long v = XGetPixel(img, xx, yy);
				if ((v & img->red_mask) != 0 || (v & img->green_mask) != 0 ||
					(v & img->blue_mask) != 0) {
					foreign = 1;
				}
			}
		}
		XDestroyImage(img);
		if (foreign) {
			return 1;
		}
	}
	return 0;
}

static void *tipsy_swap_main(void *arg) {
	struct tipsy_swap *p = (struct tipsy_swap *)arg;
	if (!eglMakeCurrent((EGLDisplay)p->dpy, (EGLSurface)p->surf, (EGLSurface)p->surf, (EGLContext)p->ctx)) {
		p->fail = eglGetError();
		return NULL;
	}
	// Present exactly one sentinel frame so the boot window is a defined
	// black instead of an undefined back buffer. Two presenters swapping on
	// one XID (Tipsy + Roblox RenderJob) visibly flicker and can evict the
	// client's frames, so after this single present Tipsy never presents
	// again; the thread only watches for the client to take over.
	eglSwapInterval((EGLDisplay)p->dpy, 0);
	glClearColor(0.0f, 0.0f, 0.0f, 1.0f);
	glClear(GL_COLOR_BUFFER_BIT);
	if (!eglSwapBuffers((EGLDisplay)p->dpy, (EGLSurface)p->surf)) {
		p->fail = eglGetError();
		eglMakeCurrent((EGLDisplay)p->dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
		return NULL;
	}
	while (p->run && !p->retired) {
		p->probes++;
		if (tipsy_swap_probe_foreign_frame(p)) {
			p->retired = 1;
			break;
		}
		usleep(125000);
	}
	eglMakeCurrent((EGLDisplay)p->dpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
	return NULL;
}

uintptr_t tipsy_egl_swap_thread_start(uintptr_t xdpy, unsigned long xid,
	uintptr_t dpy, uintptr_t surf, uintptr_t ctx) {
	struct tipsy_swap *p = (struct tipsy_swap *)calloc(1, sizeof(*p));
	if (p == NULL) {
		return 0;
	}
	p->xdpy = xdpy;
	p->xid = xid;
	p->dpy = dpy;
	p->surf = surf;
	p->ctx = ctx;
	p->run = 1;
	if (pthread_create(&p->thr, NULL, tipsy_swap_main, p) != 0) {
		free(p);
		return 0;
	}
	return (uintptr_t)p;
}

int tipsy_egl_swap_thread_stop(uintptr_t ptr) {
	struct tipsy_swap *p = (struct tipsy_swap *)ptr;
	int fail = 0;
	if (p == NULL) {
		return 0;
	}
	p->run = 0;
	pthread_join(p->thr, NULL);
	fail = p->fail;
	free(p);
	return fail;
}

void tipsy_egl_swap_thread_state(uintptr_t ptr, int *out_retired,
	unsigned long *out_probes, unsigned long *out_failed) {
	struct tipsy_swap *p = (struct tipsy_swap *)ptr;
	if (p == NULL) {
		*out_retired = 0;
		*out_probes = 0;
		*out_failed = 0;
		return;
	}
	*out_retired = p->retired;
	*out_probes = p->probes;
	*out_failed = p->probes_failed;
}

int tipsy_egl_clear(float r, float g, float b, float a) {
	glClearColor(r, g, b, a);
	glClear(GL_COLOR_BUFFER_BIT);
	GLenum err = glGetError();
	if (err != GL_NO_ERROR) {
		return (int)err;
	}
	return 0;
}

int tipsy_egl_close(uintptr_t dpy, uintptr_t surf, uintptr_t ctx) {
	EGLDisplay edpy = (EGLDisplay)dpy;
	if (edpy == NULL || edpy == EGL_NO_DISPLAY) {
		return EGL_SUCCESS;
	}
	eglMakeCurrent(edpy, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT);
	if (surf != 0) {
		eglDestroySurface(edpy, (EGLSurface)surf);
	}
	if (ctx != 0) {
		eglDestroyContext(edpy, (EGLContext)ctx);
	}
	eglTerminate(edpy);
	return EGL_SUCCESS;
}

const char *tipsy_egl_query(uintptr_t dpy, int name) {
	const char *s = eglQueryString((EGLDisplay)dpy, (EGLint)name);
	if (s == NULL) {
		return "";
	}
	return s;
}
*/
import "C"

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"time"

	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

const (
	eglSuccess = 0x3000
	eglVendor  = 0x3053
	eglVersion = 0x3054
)

func platformDisplayRefreshRates(xdisplay, xid uintptr) (float64, []float32) {
	if xdisplay == 0 || xid == 0 {
		return 0, nil
	}
	current := float64(C.tipsy_x11_refresh_rate(C.uintptr_t(xdisplay), C.ulong(xid)))
	var nativeRates [64]C.double
	nativeRateCount := int(C.tipsy_x11_supported_refresh_rates(
		C.uintptr_t(xdisplay), C.ulong(xid), &nativeRates[0], C.int(len(nativeRates))))
	uniqueRates := make(map[int64]float64, nativeRateCount+1)
	for i := 0; i < nativeRateCount; i++ {
		hz := float64(nativeRates[i])
		if hz > 0 {
			uniqueRates[int64(math.Round(hz*1000))] = hz
		}
	}
	if current > 0 {
		uniqueRates[int64(math.Round(current*1000))] = current
	}
	rates := make([]float64, 0, len(uniqueRates))
	for _, hz := range uniqueRates {
		rates = append(rates, hz)
	}
	sort.Float64s(rates)
	supported := make([]float32, 0, len(rates))
	for _, hz := range rates {
		supported = append(supported, float32(hz))
	}
	return current, supported
}

// BindEGL creates an OpenGL ES 2 context and window surface on x.
func BindEGL(x *x11.Window) (*EGL, error) {
	if x == nil || x.Display() == 0 || x.XID() == 0 {
		return nil, ErrNoWindow
	}
	runtime.LockOSThread()

	var dpy, surf, ctx C.uintptr_t
	rc := C.tipsy_egl_bind(C.uintptr_t(x.Display()), C.ulong(x.XID()), &dpy, &surf, &ctx)
	if rc != eglSuccess || dpy == 0 || surf == 0 || ctx == 0 {
		return nil, fmt.Errorf("graphics: BindEGL failed: %s", eglErrorName(int(rc)))
	}

	refreshHz, supportedHz := platformDisplayRefreshRates(x.Display(), x.XID())
	e := &EGL{
		display:     uintptr(dpy),
		surface:     uintptr(surf),
		context:     uintptr(ctx),
		x11Display:  x.Display(),
		x11XID:      x.XID(),
		refreshHz:   refreshHz,
		supportedHz: supportedHz,
	}
	path := C.GoString(C.tipsy_egl_bind_path())
	vendor := C.GoString(C.tipsy_egl_query(dpy, eglVendor))
	version := C.GoString(C.tipsy_egl_query(dpy, eglVersion))
	logging.Logger(logging.CatGraphics).Info("bound EGL on X11",
		"path", path, "vendor", vendor, "version", version,
		"refreshHz", e.refreshHz, "supportedRefreshRates", e.supportedHz)
	return e, nil
}

// Swap presents the current back buffer on the calling thread (EGL owner).
// This is for Tipsy's own pre-client frames only: once the Roblox RenderJob
// presents, it is the sole presenter on the XID and must not be fought.
func (e *EGL) Swap() error {
	if e == nil {
		return ErrClosed
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 || e.surface == 0 || e.context == 0 {
		return ErrClosed
	}
	if rc := C.tipsy_egl_make_current(C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context)); rc != eglSuccess {
		return fmt.Errorf("graphics: MakeCurrent: %s", eglErrorName(int(rc)))
	}
	rc := C.tipsy_egl_swap(C.uintptr_t(e.display), C.uintptr_t(e.surface))
	if rc != eglSuccess {
		return fmt.Errorf("graphics: Swap: %s", eglErrorName(int(rc)))
	}
	return nil
}

// ReleaseCurrent unbinds the context so another thread can MakeCurrent.
func (e *EGL) ReleaseCurrent() error {
	if e == nil {
		return ErrClosed
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 {
		return ErrClosed
	}
	if rc := C.tipsy_egl_release_current(C.uintptr_t(e.display)); rc != eglSuccess {
		return fmt.Errorf("graphics: ReleaseCurrent: %s", eglErrorName(int(rc)))
	}
	return nil
}

// StartSwapThread presents one sentinel frame on a C pthread, then yields
// the window: it never presents again and retires permanently on the first
// client-presented frame, so the Roblox RenderJob is the sole presenter.
func (e *EGL) StartSwapThread() error {
	if e == nil {
		return ErrClosed
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 || e.surface == 0 || e.context == 0 {
		return ErrClosed
	}
	if e.swap != 0 {
		return nil
	}
	p := C.tipsy_egl_swap_thread_start(C.uintptr_t(e.x11Display), C.ulong(e.x11XID),
		C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context))
	if p == 0 {
		return fmt.Errorf("graphics: swap thread")
	}
	e.swap = uintptr(p)
	go e.watchSwapHandoff()
	return nil
}

// watchSwapHandoff logs the single presenter handoff when the C thread
// detects a client-presented frame. It exits once the thread is stopped or
// the handoff is observed.
func (e *EGL) watchSwapHandoff() {
	for {
		time.Sleep(250 * time.Millisecond)
		e.mu.Lock()
		if e.swap == 0 {
			e.mu.Unlock()
			return
		}
		retired, probes, failed := e.swapHandoffStatsLocked()
		e.mu.Unlock()
		if retired {
			logging.Logger(logging.CatGraphics).Info("client presenter detected on window; Tipsy swap thread retired",
				"probes", probes, "probeFailures", failed)
			return
		}
	}
}

// SwapHandedOff reports whether the swap thread detected a client-presented
// frame on the window and permanently stopped presenting. After handoff the
// Roblox RenderJob is the only presenter on the XID.
func (e *EGL) SwapHandedOff() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	retired, _, _ := e.swapHandoffStatsLocked()
	return retired
}

func (e *EGL) swapHandoffStatsLocked() (retired bool, probes uint64, failed uint64) {
	if e.swap == 0 {
		return false, 0, 0
	}
	var cRetired C.int
	var cProbes, cFailed C.ulong
	C.tipsy_egl_swap_thread_state(C.uintptr_t(e.swap), &cRetired, &cProbes, &cFailed)
	return cRetired != 0, uint64(cProbes), uint64(cFailed)
}

// StopSwapThread joins the C present thread and releases its current context.
func (e *EGL) StopSwapThread() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopSwapThreadLocked()
}

func (e *EGL) stopSwapThreadLocked() error {
	if e.swap == 0 {
		return nil
	}
	fail := C.tipsy_egl_swap_thread_stop(C.uintptr_t(e.swap))
	e.swap = 0
	if fail != 0 && fail != eglSuccess {
		return fmt.Errorf("graphics: swap thread: %s", eglErrorName(int(fail)))
	}
	return nil
}

// Close releases the EGL surface, context, and display.
func (e *EGL) Close() error {
	if e == nil {
		return nil
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 {
		return nil
	}
	_ = e.stopSwapThreadLocked()
	C.tipsy_egl_close(C.uintptr_t(e.display), C.uintptr_t(e.surface), C.uintptr_t(e.context))
	logging.Logger(logging.CatGraphics).Info("closed EGL")
	e.display = 0
	e.surface = 0
	e.context = 0
	return nil
}

func (e *EGL) clearRGBA(r, g, b, a float32) error {
	if e == nil {
		return ErrClosed
	}
	runtime.LockOSThread()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.display == 0 {
		return ErrClosed
	}
	if rc := C.tipsy_egl_clear(C.float(r), C.float(g), C.float(b), C.float(a)); rc != 0 {
		return fmt.Errorf("graphics: glClear error 0x%x", int(rc))
	}
	return nil
}

func eglErrorName(code int) string {
	switch code {
	case 0x3000:
		return "EGL_SUCCESS"
	case 0x3001:
		return "EGL_NOT_INITIALIZED"
	case 0x3002:
		return "EGL_BAD_ACCESS"
	case 0x3003:
		return "EGL_BAD_ALLOC"
	case 0x3004:
		return "EGL_BAD_ATTRIBUTE"
	case 0x3005:
		return "EGL_BAD_CONFIG"
	case 0x3006:
		return "EGL_BAD_CONTEXT"
	case 0x3007:
		return "EGL_BAD_CURRENT_SURFACE"
	case 0x3008:
		return "EGL_BAD_DISPLAY"
	case 0x3009:
		return "EGL_BAD_MATCH"
	case 0x300A:
		return "EGL_BAD_NATIVE_PIXMAP"
	case 0x300B:
		return "EGL_BAD_NATIVE_WINDOW"
	case 0x300C:
		return "EGL_BAD_PARAMETER"
	case 0x300D:
		return "EGL_BAD_SURFACE"
	case 0x300E:
		return "EGL_CONTEXT_LOST"
	default:
		return fmt.Sprintf("EGL error 0x%x", code)
	}
}
