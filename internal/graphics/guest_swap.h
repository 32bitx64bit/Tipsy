/* Copyright 2026 The Tipsy Authors
 * SPDX-License-Identifier: GPL-3.0-or-later
 *
 * Android EGL guest-swap handoff contract. The Android ABI shim must call
 * GoEGLGuestSurfaceCreated after a host eglCreateWindowSurface succeeds and
 * retain its nonzero generation with that exact surface. It must call
 * GoEGLGuestSwap only after that exact host eglSwapBuffers succeeds, and call
 * GoEGLGuestSurfaceDestroyed before forwarding destruction of the surface.
 */
#ifndef TIPSY_GRAPHICS_GUEST_SWAP_H
#define TIPSY_GRAPHICS_GUEST_SWAP_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

uint64_t GoEGLGuestSurfaceCreated(uintptr_t window, uintptr_t display,
	uintptr_t surface);
int GoEGLGuestSwap(uintptr_t window, uintptr_t display, uintptr_t surface,
	uint64_t generation);
void GoEGLGuestSurfaceDestroyed(uintptr_t window, uintptr_t display,
	uintptr_t surface, uint64_t generation);

#ifdef __cplusplus
}
#endif

#endif /* TIPSY_GRAPHICS_GUEST_SWAP_H */
