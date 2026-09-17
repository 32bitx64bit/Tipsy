// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package x11

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validFocusedTextSnapshot() FocusedTextSnapshot {
	return FocusedTextSnapshot{Active: true, Configured: true, Editable: true, CursorVisible: true,
		IncludeFontPadding: true, Text: "tipsyok", CursorUTF16: 7, Version: 1, Density: 1,
		X: 10, Y: 20, Width: 300, Height: 42, FontSize: 14.31, TextColor: 0xfffefefe}
}

func newFocusedTextOverlayForTest(t *testing.T, width, height int) *FocusedTextOverlay {
	t.Helper()
	// Rasterization is display-independent: Window carries only client bounds.
	o, err := NewFocusedTextOverlay(&Window{width: width, height: height})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.Close() })
	return o
}

func TestPrepareFocusedTextPaintRequiresOwnedConfiguredGeometry(t *testing.T) {
	for _, mutate := range []func(*FocusedTextSnapshot){
		func(s *FocusedTextSnapshot) { s.Active = false }, func(s *FocusedTextSnapshot) { s.Configured = false },
		func(s *FocusedTextSnapshot) { s.Width = 0 }, func(s *FocusedTextSnapshot) { s.Height = -1 },
		func(s *FocusedTextSnapshot) { s.FontSize = 0 }, func(s *FocusedTextSnapshot) { s.X = float32(math.NaN()) },
		func(s *FocusedTextSnapshot) { s.Y = 900 }, func(s *FocusedTextSnapshot) { s.LetterSpacing = float32(math.Inf(1)) },
		func(s *FocusedTextSnapshot) { s.PaddingLeft = 400 },
	} {
		s := validFocusedTextSnapshot()
		mutate(&s)
		if prepareFocusedTextPaint(s, 800, 600).visible {
			t.Fatalf("invalid snapshot rendered")
		}
	}
}

func TestPrepareFocusedTextPaintPreservesAPKGeometryStyleAndMasking(t *testing.T) {
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 12.9, 24.9, 320.9, 44.9
	s.FontFile, s.LetterSpacing = "/official/content/fonts/SourceSansPro-Regular.ttf", .04
	s.PaddingLeft, s.PaddingTop, s.PaddingRight, s.PaddingBottom = 1, 2, 3, 4
	s.XAlignment, s.YAlignment = 1, 2
	got := prepareFocusedTextPaint(s, 800, 600)
	if !got.visible || got.x != 12 || got.y != 24 || got.width != 320 || got.height != 44 || got.fontFile != s.FontFile || got.argb != s.TextColor || got.xAlignment != 1 || got.yAlignment != 2 {
		t.Fatalf("style = %+v", got)
	}
	for _, inputType := range []int32{5, 9, 10} {
		s := validFocusedTextSnapshot()
		s.Text, s.CursorUTF16, s.TextInputType = "secret\U0001f512", 8, inputType
		got := prepareFocusedTextPaint(s, 800, 600)
		if strings.Contains(got.text, "secret") || got.text != strings.Repeat("•", 7) || got.cursorByte != len(got.text) {
			t.Fatalf("type %d = %+v", inputType, got)
		}
	}
	s = validFocusedTextSnapshot()
	s.Text, s.TextInputType, s.TextColor = "visible", 6, 0
	if got := prepareFocusedTextPaint(s, 800, 600); got.text != s.Text || got.argb != 0 {
		t.Fatalf("visible password = %+v", got)
	}
}

func TestRuneAndByteIndexFromUTF16DoNotSplitSurrogatePair(t *testing.T) {
	const text = "a\U0001f642b"
	for units, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 1, 3: 2, 4: 3, 99: 3} {
		if got := runeIndexFromUTF16(text, units); got != want {
			t.Fatalf("runes %d: %d", units, got)
		}
	}
	for units, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 1, 3: 5, 4: 6, 99: 6} {
		if got := byteIndexFromUTF16(text, units); got != want {
			t.Fatalf("bytes %d: %d", units, got)
		}
	}
}

func TestFocusedTextForegroundIsAlphaOnlyAndClearedOnHide(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 320, 180)
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 11, 23, 180, 36
	s.Editable, s.CursorVisible = false, false
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	diag := o.Diagnostics()
	state := o.queryForTest()
	if !diag.Mapped || !diag.UsesARGB || diag.GlyphMaskPixels == 0 ||
		diag.AntialiasedPixels == 0 || state.x != 11 || state.y != 23 ||
		state.width != 180 || state.height != 36 {
		t.Fatalf("foreground = %+v", state)
	}
	if alpha, ok := o.foregroundAlphaForTest(179, 35); !ok || alpha != 0 {
		t.Fatalf("non-ink alpha %d ok=%v", alpha, ok)
	}
	s.Active, s.Text = false, ""
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if state := o.queryForTest(); state.Mapped || state.TextColorARGB != 0 || state.PaintedPixels != 0 {
		t.Fatalf("hidden = %+v", state)
	}
}

func TestFocusedTextForegroundLeaseHasDirtyGeometryAndClearsOnHide(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 320, 180)
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 17, 29, 190, 38
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if !focusedTextOverlayLiveForTest() {
		t.Fatal("published overlay did not advertise live")
	}
	first, ok := acquireFocusedTextForegroundForTest()
	if !ok || first.x != 17 || first.y != 29 || first.width != 190 ||
		first.height != 38 || first.stride != 190*4 || first.generation == 0 {
		t.Fatalf("first foreground lease = %+v ok=%v", first, ok)
	}
	first.release()
	s.Version++
	s.X = 23
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	second, ok := acquireFocusedTextForegroundForTest()
	if !ok || second.x != 23 || second.generation <= first.generation {
		t.Fatalf("second foreground lease = %+v first=%+v ok=%v", second, first, ok)
	}
	second.release()
	s.Active = false
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if focusedTextOverlayLiveForTest() {
		t.Fatal("hidden overlay stayed live")
	}
	if _, ok := acquireFocusedTextForegroundForTest(); ok {
		t.Fatal("inactive field retained a host foreground lease")
	}
}

func TestFocusedTextOverlayLiveTracksPublishFailAndFree(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 240, 120)
	if focusedTextOverlayLiveForTest() {
		t.Fatal("new overlay advertised live before publish")
	}
	s := validFocusedTextSnapshot()
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if !focusedTextOverlayLiveForTest() {
		t.Fatal("successful publish did not set overlay live")
	}
	s.Version++
	s.FontFile = "/definitely/missing/tipsy-official-font.ttf"
	if err := o.Update(s); err == nil {
		t.Fatal("missing official font silently fell back")
	}
	if focusedTextOverlayLiveForTest() {
		t.Fatal("failed republish left overlay live")
	}
	if _, ok := acquireFocusedTextForegroundForTest(); ok {
		t.Fatal("failed republish retained a host foreground lease")
	}
	s = validFocusedTextSnapshot()
	s.Version += 2
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if !focusedTextOverlayLiveForTest() {
		t.Fatal("republish after failure did not set overlay live")
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if focusedTextOverlayLiveForTest() {
		t.Fatal("freed overlay stayed live")
	}
}

func TestFocusedTextForegroundPaddingGravityWrapAndZeroAlpha(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 360, 220)
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 19.9, 31.9, 220.9, 48.9
	s.PaddingLeft, s.PaddingTop, s.PaddingRight, s.PaddingBottom = 7, 3, 9, 5
	s.XAlignment, s.YAlignment = 0, 1
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	left := o.queryForTest()
	wantTop := int(s.PaddingTop) + (left.height-int(s.PaddingTop)-int(s.PaddingBottom)-left.lineBoxHeight)/2
	if left.textOriginX != int(s.PaddingLeft) || left.lineBoxTop != wantTop || left.baselineY <= left.lineBoxTop {
		t.Fatalf("left = %+v", left)
	}
	s.Version++
	s.XAlignment = 1
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	right := o.queryForTest()
	s.Version++
	s.XAlignment = 2
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	center := o.queryForTest()
	if !(left.textOriginX < center.textOriginX && center.textOriginX < right.textOriginX) {
		t.Fatalf("gravity %d %d %d", left.textOriginX, center.textOriginX, right.textOriginX)
	}
	s.Version++
	s.Text, s.CursorUTF16, s.Width, s.Height, s.TextWrapped = "wrapped words wrapped words", 27, 90, 80, true
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if state := o.queryForTest(); state.lineBoxHeight <= int(math.Ceil(float64(s.FontSize))) {
		t.Fatalf("wrap = %+v", state)
	}
	s.Version++
	s.TextColor = 0
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if state := o.queryForTest(); state.TextAlpha != 0 || state.PaintedPixels != 0 {
		t.Fatalf("zero alpha = %+v", state)
	}
}

func TestFocusedTextSurfaceRejectsMissingOfficialFont(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 240, 120)
	s := validFocusedTextSnapshot()
	s.FontFile = "/definitely/missing/tipsy-official-font.ttf"
	if err := o.Update(s); err == nil {
		t.Fatal("missing official font silently fell back")
	}
}

func TestFocusedTextSurfaceLoadsInstalledOfficialFont(t *testing.T) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no data root")
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	fontFile := filepath.Join(dataHome, "tipsy", "runtime", "assets", "fonts", "SourceSansPro-Regular.ttf")
	if info, err := os.Stat(fontFile); err != nil || !info.Mode().IsRegular() {
		t.Skip("official font unavailable")
	}
	o := newFocusedTextOverlayForTest(t, 240, 120)
	s := validFocusedTextSnapshot()
	s.FontFile = fontFile
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	if state := o.queryForTest(); !state.Mapped || state.GlyphMaskPixels == 0 {
		t.Fatalf("font = %+v", state)
	}
}
