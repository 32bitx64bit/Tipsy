// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"math"
	"strings"
	"unicode/utf8"
)

// FocusedTextSnapshot is the transient, window-facing portion of Roblox's
// RbxKeyboard EditText state. Text must only live long enough for Update to
// paint it; implementations must not log, retain, or persist it.
type FocusedTextSnapshot struct {
	Active        bool
	Configured    bool
	Editable      bool
	CursorVisible bool
	Multiline     bool
	TextWrapped   bool
	// IncludeFontPadding is Android TextView's line-box policy. The APK's
	// RbxKeyboard retains the platform default (true).
	IncludeFontPadding bool
	Text               string
	CursorUTF16        int
	Version            uint64
	Density            float32 // metadata only; geometry/font size arrive as final px
	X                  float32
	Y                  float32
	Width              float32
	Height             float32
	FontSize           float32 // final, non-truncated Android px
	TextColor          uint32  // Android ARGB
	Font               int32
	FontFile           string  // resolved official APK font file
	LetterSpacing      float32 // Android TextView em units
	PaddingLeft        int32
	PaddingTop         int32
	PaddingRight       int32
	PaddingBottom      int32
	TextInputType      int32
	XAlignment         int32
	YAlignment         int32
	ReturnKeyType      int32
}

// FocusedTextOverlayDiagnostics contains only privacy-safe render metadata.
// It deliberately exposes neither text nor glyph identities. Pixel counts are
// transient compositor health signals and are cleared when focus ownership
// ends.
type FocusedTextOverlayDiagnostics struct {
	Mapped            bool
	UsesARGB          bool
	TextColorARGB     uint32
	TextAlpha         uint8
	GlyphMaskPixels   uint64
	CaretMaskPixels   uint64
	PaintedPixels     uint64
	BrightGlyphPixels uint64
	AntialiasedPixels uint64
	BackgroundPixels  uint64
}

type focusedTextPaint struct {
	visible            bool
	x, y               int
	width, height      int
	fontSize           float64
	argb               uint32
	xAlignment         int
	yAlignment         int
	multiline          bool
	wrapped            bool
	editable           bool
	cursorVisible      bool
	includeFontPadding bool
	font               int
	fontFile           string
	letterSpacing      float64
	paddingLeft        int
	paddingTop         int
	paddingRight       int
	paddingBottom      int
	version            uint64
	text               string
	cursorByte         int
}

// prepareFocusedTextPaint validates geometry against the current client
// bounds, masks password-class input before it crosses into Xlib, and converts
// Java's UTF-16 cursor unit to a byte boundary in the visual UTF-8 string.
func prepareFocusedTextPaint(s FocusedTextSnapshot, clientWidth, clientHeight int) focusedTextPaint {
	if !s.Active || !s.Configured || clientWidth <= 0 || clientHeight <= 0 ||
		!finitePositive(s.Width) || !finitePositive(s.Height) ||
		!finite(s.X) || !finite(s.Y) || !finitePositive(s.FontSize) {
		return focusedTextPaint{}
	}
	// The APK multiplies layout values by density before this boundary and
	// Java float-to-int truncates them. Runtime supplies those final px values;
	// truncation here preserves the exact conversion and avoids rounding drift.
	x := int(s.X)
	y := int(s.Y)
	w := int(s.Width)
	h := int(s.Height)
	if w <= 0 || h <= 0 || x >= clientWidth || y >= clientHeight || x+w <= 0 || y+h <= 0 {
		return focusedTextPaint{}
	}
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	if x+w > clientWidth {
		w = clientWidth - x
	}
	if y+h > clientHeight {
		h = clientHeight - y
	}
	if w <= 0 || h <= 0 {
		return focusedTextPaint{}
	}

	text := s.Text
	cursorByte := byteIndexFromUTF16(text, s.CursorUTF16)
	if passwordTextInputType(s.TextInputType) {
		cursorRunes := runeIndexFromUTF16(text, s.CursorUTF16)
		var masked strings.Builder
		masked.Grow(utf8.RuneCountInString(text) * 3)
		at := 0
		cursorByte = 0
		for _, r := range text {
			if at == cursorRunes {
				cursorByte = masked.Len()
			}
			if r == '\n' || r == '\r' {
				masked.WriteRune(r)
			} else {
				masked.WriteRune('\u2022')
			}
			at++
		}
		if cursorRunes >= at {
			cursorByte = masked.Len()
		}
		text = masked.String()
	}
	fontSize := float64(s.FontSize)
	if fontSize <= 0 {
		return focusedTextPaint{}
	}
	// The Pango font cache is intentionally bounded even if a corrupt platform
	// payload supplies an extreme value. Geometry remains APK-derived.
	if fontSize > 256 {
		fontSize = 256
	}
	paddingLeft, paddingTop := int(s.PaddingLeft), int(s.PaddingTop)
	paddingRight, paddingBottom := int(s.PaddingRight), int(s.PaddingBottom)
	if paddingLeft < 0 || paddingTop < 0 || paddingRight < 0 || paddingBottom < 0 ||
		paddingLeft+paddingRight >= w || paddingTop+paddingBottom >= h ||
		!finite(s.LetterSpacing) {
		return focusedTextPaint{}
	}
	xAlign, yAlign := int(s.XAlignment), int(s.YAlignment)
	if xAlign < 0 || xAlign > 2 {
		xAlign = 0
	}
	if yAlign < 0 || yAlign > 2 {
		yAlign = 0
	}
	return focusedTextPaint{
		visible: true, x: x, y: y, width: w, height: h,
		fontSize: fontSize, argb: s.TextColor,
		xAlignment: xAlign, yAlignment: yAlign,
		multiline: s.Multiline, wrapped: s.TextWrapped,
		editable: s.Editable, cursorVisible: s.CursorVisible,
		includeFontPadding: s.IncludeFontPadding,
		font:               int(s.Font), fontFile: s.FontFile,
		letterSpacing: float64(s.LetterSpacing),
		paddingLeft:   paddingLeft, paddingTop: paddingTop,
		paddingRight: paddingRight, paddingBottom: paddingBottom,
		version: s.Version,
		text:    text, cursorByte: cursorByte,
	}
}

func finite(v float32) bool {
	f := float64(v)
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

func finitePositive(v float32) bool { return finite(v) && v > 0 }

func passwordTextInputType(inputType int32) bool {
	// Exact RbxKeyboard mapping: Password (5), NewPassword (9), and the
	// feature-branch new-password autofill type (10). Visible-password (6)
	// deliberately remains visible.
	return inputType == 5 || inputType == 9 || inputType == 10
}

func runeIndexFromUTF16(text string, units int) int {
	if units <= 0 {
		return 0
	}
	used := 0
	count := 0
	for _, r := range text {
		step := 1
		if r > 0xffff {
			step = 2
		}
		if used+step > units {
			return count
		}
		used += step
		if used == units {
			return count + 1
		}
		count++
	}
	return count
}

func byteIndexFromUTF16(text string, units int) int {
	if units <= 0 {
		return 0
	}
	used := 0
	for byteIndex, r := range text {
		step := 1
		if r > 0xffff {
			step = 2
		}
		if used+step > units {
			return byteIndex
		}
		used += step
		if used == units {
			return byteIndex + utf8.RuneLen(r)
		}
	}
	return len(text)
}
