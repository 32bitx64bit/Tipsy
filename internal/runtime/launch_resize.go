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

// surfaceResizeSettleDelay debounces GameActivity surface-update re-entry
// during a resize drag: long enough to outlast the intermediate ConfigureNotify
// burst, short enough to stay responsive.
const surfaceResizeSettleDelay = 75 * time.Millisecond

// clampRobloxSurfaceSize clamps initial display metrics to the X11 window's WM
// minimum. Values are X11 client pixels; the Android density stays 1 here.
func clampRobloxSurfaceSize(width, height int) (int, int) {
	if width < x11.RobloxMinimumWidth {
		width = x11.RobloxMinimumWidth
	}
	if height < x11.RobloxMinimumHeight {
		height = x11.RobloxMinimumHeight
	}
	return width, height
}

// surfaceResizeDebouncer keeps only the most recent positive X11 rectangle from
// a resize burst, coalescing the GameActivity lifecycle work that must not
// re-enter per pixel of a drag. It is owned by the Launch goroutine.
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
// holders the engine reads once at startup and never updates afterward. It runs
// only on the Launch ticker goroutine, so engine-facing native calls stay
// serialized and need no extra synchronization.
type surfaceResize struct {
	sink resizeSink

	seeded              bool
	width, height       int
	minWidth, minHeight int
}

// resizeSink is the engine-facing update surface.
type resizeSink interface {
	resizeBuffers(width, height int) error
	setDisplaySize(width, height int)
	postAppCmd(cmd byte)
	updateSurface(width, height int)
	callNative(name, sig string, extra ...uintptr)
}

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
	// DisplayMetrics.density stays 1 in the desktop Android contract; keep the
	// editor's clipping viewport in lockstep (bounds themselves are not rescaled).
	jni.SetRbxTextOverlayViewport(width, height, 1)
	// Captured points pin to these bounds so a held desktop grab lands on-view;
	// captured motion stays unbounded (the engine differentiates positions).
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

// observe delivers at most one update per genuine positive size delta, in the
// order the engine reads them at startup: ANativeWindow geometry, DisplayMetrics,
// the GameActivity window-resized/redraw commands, the V2 surface-update bridge,
// then content-rect-changed and insets. A failed geometry update aborts the
// delivery rather than reporting a size the native window lacks.
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
