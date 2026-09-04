// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#include "jni_bridge.h"
*/
import "C"

import "unsafe"

// windowInsetsTypeMasks holds the official androidx
// WindowInsetsCompat.Type static mask values, verified against
// androidx-main WindowInsetsCompat.java (2026-09-03): STATUS_BARS=1,
// NAVIGATION_BARS=1<<1, CAPTION_BAR=1<<2, IME=1<<3, SYSTEM_GESTURES=1<<4,
// MANDATORY_SYSTEM_GESTURES=1<<5, TAPPABLE_ELEMENT=1<<6, DISPLAY_CUTOUT=1<<7,
// SYSTEM_OVERLAYS=1<<9 (WINDOW_DECOR=1<<8 has no public Type method), and
// systemBars() = STATUS_BARS|NAVIGATION_BARS|CAPTION_BAR|SYSTEM_OVERLAYS = 519.
var windowInsetsTypeMasks = map[string]int32{
	"statusBars":              1,
	"navigationBars":          2,
	"captionBar":              4,
	"ime":                     8,
	"systemGestures":          16,
	"mandatorySystemGestures": 32,
	"tappableElement":         64,
	"displayCutout":           128,
	"systemOverlays":          512,
	"systemBars":              519,
}

// dispatchInsets serves the seeded androidx Insets surface and the
// GameActivity insets getters Roblox calls. Only officially requested
// signatures are handled; everything else falls through to the normal
// dispatch/stub path.
func (vm *VM) dispatchInsets(class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if class == "androidx/core/view/WindowInsetsCompat$Type" {
		if sig == "()I" {
			if mask, ok := windowInsetsTypeMasks[name]; ok {
				return C.jobject(unsafe.Pointer(uintptr(uint32(mask)))), true
			}
		}
		return jnull(), false
	}
	if class != "com/google/androidgamesdk/GameActivity" {
		return jnull(), false
	}
	switch name + sig {
	case "getWindowInsets(I)Landroidx/core/graphics/Insets;":
		// Official GameActivity.getWindowInsets: null when there is no
		// root insets state, and null when the computed insets are
		// Insets.NONE. The real X11 geometry yields NONE for every mask.
		l, t, r, b := vm.windowInsetsForType(jvalueIAt(args, 0))
		if l == 0 && t == 0 && r == 0 && b == 0 {
			return jnull(), true
		}
		return vm.newInsetsObject(l, t, r, b), true
	case "getWaterfallInsets()Landroidx/core/graphics/Insets;":
		// Official: null without a display cutout; X11 has none.
		return jnull(), true
	}
	return jnull(), false
}

// windowInsetsForType derives the real system insets for a Type mask from
// the live X11 window: the content rect is the whole window (an X11 desktop
// has no status/navigation/caption bars, display cutout, IME, or system
// gesture areas), so every inset in the mask is genuinely zero — Insets.NONE.
// The launch pump keeps the live window size current; it does not change the
// bar/cutout offsets, which are zero by platform geometry.
func (vm *VM) windowInsetsForType(_ int32) (int32, int32, int32, int32) {
	return 0, 0, 0, 0
}

// newInsetsObject builds an androidx/core/graphics/Insets with the official
// public int fields left/top/right/bottom, readable via GetFieldID/IntField.
func (vm *VM) newInsetsObject(left, top, right, bottom int32) C.jobject {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	cls := vm.classes["androidx/core/graphics/Insets"]
	if cls == nil {
		return jnull()
	}
	o := vm.newObjectLocked(cls)
	o.fields["left"] = left
	o.fields["top"] = top
	o.fields["right"] = right
	o.fields["bottom"] = bottom
	return idToJobject(o.id)
}
