// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package x11

/*
#include "focused_overlay.h"
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"math"
	"strings"
	"unsafe"
)

// UpdateNotice publishes a passive card into the same leased foreground as
// focused text. The embedded icon is decoded once by windowIconARGB; no X11
// child window, focus request or pointer grab is created.
func (o *FocusedTextOverlay) UpdateNotice(frame UpdateNoticeFrame) error {
	if o == nil {
		return ErrUnavailable
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.native == 0 || o.window == nil {
		return ErrClosed
	}
	o.window.mu.Lock()
	defer o.window.mu.Unlock()
	if o.window.closed {
		return ErrClosed
	}
	if !frame.Visible {
		if C.tipsy_update_notice_update(0, 0, 0, 0, 0, 0, 0, 0, 0, 0, nil, 0, nil, 0, 0) != 0 {
			return fmt.Errorf("x11: hide update notice")
		}
		return nil
	}
	if frame.Width < 1 || frame.Height < 1 || frame.Text == "" ||
		frame.Opacity < 0 || frame.Opacity > 1 || frame.Scale <= 0 ||
		math.IsNaN(frame.Opacity) || math.IsInf(frame.Opacity, 0) ||
		math.IsNaN(frame.Scale) || math.IsInf(frame.Scale, 0) ||
		math.IsNaN(frame.AngleDegrees) || math.IsInf(frame.AngleDegrees, 0) || strings.IndexByte(frame.Text, 0) >= 0 {
		return fmt.Errorf("x11: invalid update notice frame")
	}
	icon, err := windowIconARGB()
	if err != nil {
		return err
	}
	if len(icon) < 2 || icon[0] == 0 || icon[1] == 0 || len(icon) != 2+int(icon[0])*int(icon[1]) {
		return fmt.Errorf("x11: invalid embedded Tipsy icon")
	}
	text := C.CString(frame.Text)
	defer C.free(unsafe.Pointer(text))
	rc := C.tipsy_update_notice_update(1,
		C.int(o.window.width), C.int(o.window.height),
		C.int(frame.X), C.int(frame.Y), C.int(frame.Width), C.int(frame.Height),
		C.double(frame.AngleDegrees), C.double(frame.Opacity), C.double(frame.Scale),
		text, C.int(len(frame.Text)), (*C.uint32_t)(unsafe.Pointer(&icon[2])), C.int(icon[0]), C.int(icon[1]))
	if rc != 0 {
		return fmt.Errorf("x11: paint update notice")
	}
	return nil
}
