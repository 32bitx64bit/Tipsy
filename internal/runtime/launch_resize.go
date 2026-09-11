// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// surfaceResizeSettleDelay bounds GameActivity/V2 surface-update re-entry
// during a manual X11 resize drag. It is deliberately short enough that a
// completed resize remains responsive, yet longer than the dense intermediate
// ConfigureNotify burst that otherwise reaches the official client as a chain
// of overlapping surface transitions.
const surfaceResizeSettleDelay = 75 * time.Millisecond

// clampRobloxSurfaceSize keeps GameActivity's initial display metrics aligned
// with the X11 Roblox window's WM minimum. These are X11 client pixels; the
// Android density remains one in this desktop adapter.
func clampRobloxSurfaceSize(width, height int) (int, int) {
	if width < x11.RobloxMinimumWidth {
		width = x11.RobloxMinimumWidth
	}
	if height < x11.RobloxMinimumHeight {
		height = x11.RobloxMinimumHeight
	}
	return width, height
}

// surfaceResizeDebouncer holds only the most recent positive X11 rectangle
// from a resize burst. The X11 Window continues to track each real
// ConfigureNotify for input and presentation; this type coalesces only the
// Android/GameActivity lifecycle work that must not be re-entered per pixel of
// a title-bar drag. It is owned by the Launch goroutine.
type surfaceResizeDebouncer struct {
	pending             bool
	width, height       int
	minWidth, minHeight int
}

func (d *surfaceResizeDebouncer) queue(w, h int) bool {
	if d == nil || w <= 0 || h <= 0 ||
		(d.minWidth > 0 && w < d.minWidth) ||
		(d.minHeight > 0 && h < d.minHeight) {
		return false
	}
	d.pending, d.width, d.height = true, w, h
	return true
}

func (d *surfaceResizeDebouncer) take() (w, h int, ok bool) {
	if d == nil || !d.pending {
		return 0, 0, false
	}
	w, h = d.width, d.height
	d.pending = false
	return w, h, true
}

// surfaceResize propagates genuine X11 size deltas into the surface-geometry
// holders the engine reads once at startup and otherwise never updates: the
// ANativeWindow buffer geometry, the JNI DisplayMetrics, and the GameActivity
// content-rect/insets contract. It runs only on the Launch ticker goroutine —
// the same thread that delivered the startup lifecycle callbacks and the X11
// input events — so callback lookups and native calls stay serialized with
// the rest of the engine-facing surface and need no extra synchronization.
type surfaceResize struct {
	sink resizeSink

	seeded              bool
	width, height       int
	minWidth, minHeight int
}

// resizeSink is the engine-facing update surface, factored out so tests can
// pin ordering and dedupe deterministically without a mapped engine.
type resizeSink interface {
	resizeBuffers(width, height int) error
	setDisplaySize(width, height int)
	postAppCmd(cmd byte)
	updateSurface(width, height int)
	callNative(name, sig string, extra ...uintptr)
}

// engineResizeSink adapts the real engine-facing updates.
type engineResizeSink struct {
	mod      *loader.Module
	vm       *jni.VM
	env      *jni.Env
	activity uintptr
	handle   uintptr
	commands appCommandWriter
	aw       *android.Window
	gl       uintptr
	surface  uintptr
	platform uintptr
}

func (s *engineResizeSink) resizeBuffers(width, height int) error { return s.aw.Resize(width, height) }

func (s *engineResizeSink) setDisplaySize(width, height int) {
	s.vm.SetDisplaySize(width, height)
	// DisplayMetrics.density remains 1 in the desktop Android contract. Keep
	// the transient editor's clipping metadata in lockstep with that same
	// surface resize; NativeTextBoxInfo bounds themselves are not rescaled.
	jni.SetRbxTextOverlayViewport(width, height, 1)
	// Captured points (button edges, wheel detents) pin to these same bounds
	// so a long-held desktop grab always lands clicks and zoom on-view.
	// Captured motion stays unbounded: the engine differentiates positions.
	jni.SetPointerClampViewport(width, height)
}

func (s *engineResizeSink) postAppCmd(cmd byte) { postAndroidAppCmd(s.commands, cmd) }

func (s *engineResizeSink) updateSurface(width, height int) {
	if s == nil || s.mod == nil || s.env == nil || s.gl == 0 || s.surface == 0 || s.platform == 0 {
		return
	}
	setPlatformViewport(s.env, s.platform, width, height)
	callRobloxJNI(s.mod, s.env.Raw(), s.gl, updateSurfaceSym, s.surface, s.platform)
}

func (s *engineResizeSink) callNative(name, sig string, extra ...uintptr) {
	callGameActivityNative(s.vm, s.env, s.activity, s.handle, name, sig, extra...)
}

// observe compares win.Size() after a Pump with the last delivered
// dimensions. Invalid sizes are ignored, unchanged sizes are deduplicated,
// and one genuine positive delta produces exactly one delivery, in the
// startup order: ANativeWindow geometry, DisplayMetrics, then the public
// GameActivity resize contract — APP_CMD_WINDOW_RESIZED (3) and
// APP_CMD_WINDOW_REDRAW_NEEDED (4). The current client consumes those
// commands but its NativeDM fallback may return before updating the render
// size, so follow them with the APK-declared V2 surface-update JNI bridge and
// refreshed PlatformParams. APP_CMD_CONTENT_RECT_CHANGED (5) plus the
// content-rect/insets callbacks remain last. A failed geometry update aborts
// the delivery honestly instead of delivering a surface size the native
// window does not have.
func (s *surfaceResize) observe(w, h int) {
	if s == nil || w <= 0 || h <= 0 ||
		(s.minWidth > 0 && w < s.minWidth) ||
		(s.minHeight > 0 && h < s.minHeight) {
		return
	}
	if s.seeded && s.width == w && s.height == h {
		return
	}
	if err := s.sink.resizeBuffers(w, h); err != nil {
		logging.Logger(logging.CatRuntime).Info("surface resize skipped", "err", err)
		return
	}
	s.sink.setDisplaySize(w, h)
	s.sink.postAppCmd(appCmdWindowResized)
	s.sink.postAppCmd(appCmdWindowRedraw)
	s.sink.updateSurface(w, h)
	s.sink.postAppCmd(appCmdContentRectChanged)
	s.sink.callNative("onContentRectChangedNative", "(JIIII)V", 0, 0, uintptr(w), uintptr(h))
	s.sink.callNative("onWindowInsetsChangedNative", "(J)V")
	s.seeded, s.width, s.height = true, w, h
	logging.Logger(logging.CatGameActivity).Info("surface resize delivered", "width", w, "height", h)
}
