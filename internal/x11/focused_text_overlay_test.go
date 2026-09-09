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
	return FocusedTextSnapshot{
		Active: true, Configured: true, Editable: true, CursorVisible: true,
		IncludeFontPadding: true,
		Text:               "tipsyok",
		CursorUTF16:        7,
		Version:            1,
		Density:            1,
		X:                  10,
		Y:                  20,
		Width:              300,
		Height:             42,
		FontSize:           14.31,
		TextColor:          0xfffefefe,
	}
}

func settleFocusedTextSurfaceForTest(t *testing.T, overlay *FocusedTextOverlay,
	s *FocusedTextSnapshot) {
	t.Helper()
	if err := overlay.Update(*s); err != nil {
		t.Fatalf("initial deferred Update: %v", err)
	}
	if state := overlay.queryForTest(); state.Mapped {
		t.Fatalf("speculative pre-focus backing was mapped: %+v", state)
	}
	s.Version++
	if err := overlay.Update(*s); err != nil {
		t.Fatalf("post-show Update: %v", err)
	}
}

func TestPrepareFocusedTextPaintRequiresOwnedConfiguredGeometry(t *testing.T) {
	for _, mutate := range []func(*FocusedTextSnapshot){
		func(s *FocusedTextSnapshot) { s.Active = false },
		func(s *FocusedTextSnapshot) { s.Configured = false },
		func(s *FocusedTextSnapshot) { s.Width = 0 },
		func(s *FocusedTextSnapshot) { s.Height = -1 },
		func(s *FocusedTextSnapshot) { s.FontSize = 0 },
		func(s *FocusedTextSnapshot) { s.X = float32(math.NaN()) },
		func(s *FocusedTextSnapshot) { s.Y = 900 },
		func(s *FocusedTextSnapshot) { s.LetterSpacing = float32(math.Inf(1)) },
		func(s *FocusedTextSnapshot) { s.PaddingLeft = 400 },
	} {
		s := validFocusedTextSnapshot()
		mutate(&s)
		if got := prepareFocusedTextPaint(s, 800, 600); got.visible {
			t.Fatalf("invalid snapshot rendered: %+v", got)
		}
	}
}

func TestPrepareFocusedTextPaintUsesAndroidTruncationAndExactStyle(t *testing.T) {
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 12.9, 24.9, 320.9, 44.9
	s.FontFile = "/official/content/fonts/SourceSansPro-Regular.ttf"
	s.LetterSpacing = .04
	s.PaddingLeft, s.PaddingTop, s.PaddingRight, s.PaddingBottom = 1, 2, 3, 4
	s.XAlignment, s.YAlignment = 1, 2
	got := prepareFocusedTextPaint(s, 800, 600)
	if !got.visible || got.x != 12 || got.y != 24 || got.width != 320 || got.height != 44 {
		t.Fatalf("truncated geometry = %+v", got)
	}
	if got.fontSize != float64(s.FontSize) || got.fontFile != s.FontFile ||
		got.letterSpacing != float64(s.LetterSpacing) || got.xAlignment != 1 ||
		got.yAlignment != 2 || got.paddingLeft != 1 || got.paddingTop != 2 ||
		got.paddingRight != 3 || got.paddingBottom != 4 ||
		!got.cursorVisible || !got.includeFontPadding {
		t.Fatalf("exact style = %+v", got)
	}
}

func TestPrepareFocusedTextPaintClampsToClientAndKeepsARGB(t *testing.T) {
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = -5, 580, 900, 40
	got := prepareFocusedTextPaint(s, 800, 600)
	if !got.visible || got.x != 0 || got.y != 580 || got.width != 800 || got.height != 20 {
		t.Fatalf("clamped geometry = %+v", got)
	}
	if got.argb != s.TextColor {
		t.Fatalf("ARGB changed: got %#x want %#x", got.argb, s.TextColor)
	}
}

func TestPrepareFocusedTextPaintMasksPasswordBeforeRenderer(t *testing.T) {
	for _, inputType := range []int32{5, 9, 10} {
		s := validFocusedTextSnapshot()
		s.Text = "secret\U0001f512"
		s.CursorUTF16 = 8
		s.TextInputType = inputType
		got := prepareFocusedTextPaint(s, 800, 600)
		if strings.Contains(got.text, "secret") || got.text != strings.Repeat("•", 7) {
			t.Fatalf("type %d was not masked: %q", inputType, got.text)
		}
		if got.cursorByte != len(got.text) {
			t.Fatalf("type %d cursor byte = %d, want %d", inputType, got.cursorByte, len(got.text))
		}
	}
}

func TestPrepareFocusedTextPaintLeavesVisiblePasswordAndARGBZeroUntouched(t *testing.T) {
	s := validFocusedTextSnapshot()
	s.Text = "visible"
	s.TextInputType = 6
	s.TextColor = 0
	got := prepareFocusedTextPaint(s, 800, 600)
	if got.text != s.Text || got.argb != 0 {
		t.Fatalf("paint = %+v", got)
	}
}

func TestRuneAndByteIndexFromUTF16DoNotSplitSurrogatePair(t *testing.T) {
	const text = "a\U0001f642b"
	for units, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 1, 3: 2, 4: 3, 99: 3} {
		if got := runeIndexFromUTF16(text, units); got != want {
			t.Fatalf("runes units %d: got %d want %d", units, got, want)
		}
	}
	for units, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 1, 3: 5, 4: 6, 99: 6} {
		if got := byteIndexFromUTF16(text, units); got != want {
			t.Fatalf("bytes units %d: got %d want %d", units, got, want)
		}
	}
}

func TestFocusedTextSurfaceMapsAntialiasesAndHides(t *testing.T) {
	win, focusBefore, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(320, 180)
	if err != nil {
		t.Fatalf("open unfocused test window: %v", err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatalf("NewFocusedTextOverlay: %v", err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 11, 23, 180, 36
	s.Editable, s.CursorVisible = false, false
	before, ok := overlay.fillParentAndSampleForTest(11, 23, 180, 36)
	if !ok {
		t.Fatal("could not seed/sample parent background")
	}
	settleFocusedTextSurfaceForTest(t, overlay, &s)
	state := overlay.queryForTest()
	if !state.Mapped || state.UsesARGB || state.x != 11 || state.y != 23 ||
		state.width != 180 || state.height != 36 {
		t.Fatalf("mapped=%v argb=%v geometry=%d,%d %dx%d",
			state.Mapped, state.UsesARGB, state.x, state.y, state.width, state.height)
	}
	if state.GlyphMaskPixels == 0 || state.PaintedPixels == 0 ||
		state.AntialiasedPixels == 0 || state.antialiasedPixels == 0 ||
		state.BrightGlyphPixels == 0 || state.CaretMaskPixels != 0 {
		t.Fatalf("antialiased glyph diagnostics = %+v aa=%d",
			state.FocusedTextOverlayDiagnostics, state.antialiasedPixels)
	}
	if !state.backgroundPreserved || state.backgroundPixels == 0 ||
		state.BackgroundPixels == 0 || state.inputShapePixels != 0 {
		t.Fatalf("background/input state = %+v", state)
	}
	after, ok := overlay.sampleRootForTest(11+180-1, 23+36-1)
	if !ok || after != before {
		t.Fatalf("parent background changed: before=%#x after=%#x ok=%v", before, after, ok)
	}
	if focusAfter := focusedOverlayTestFocus(win); focusAfter != focusBefore {
		t.Fatalf("surface stole focus: before=%#x after=%#x", focusBefore, focusAfter)
	}
	s.Active = false
	s.Text = ""
	if err := overlay.Update(s); err != nil {
		t.Fatalf("Update hidden: %v", err)
	}
	if state := overlay.queryForTest(); state.Mapped || state.TextColorARGB != 0 ||
		state.GlyphMaskPixels != 0 || state.BackgroundPixels != 0 {
		t.Fatalf("hidden surface retained state: %+v", state)
	}
}

func TestFocusedTextSurfaceCapturesVisibleRootWhenParentDrawableIsStale(t *testing.T) {
	win, focusBefore, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(320, 180)
	if err != nil {
		t.Fatalf("open unfocused test window: %v", err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatalf("NewFocusedTextOverlay: %v", err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 17, 29, 190, 38
	s.Editable, s.CursorVisible = false, false
	s.Text, s.CursorUTF16 = "", 0
	parentPixel, ok := overlay.fillParentAndSampleForTest(17, 29, 190, 38)
	if !ok {
		t.Fatal("could not seed parent drawable")
	}
	visiblePixel, ok := overlay.addVisibleUnderlayForTest(17, 29, 190, 38)
	if !ok || visiblePixel == parentPixel {
		t.Fatalf("visible/root mismatch was not established: parent=%#x root=%#x ok=%v",
			parentPixel, visiblePixel, ok)
	}
	if err := overlay.Update(s); err != nil {
		t.Fatalf("initial deferred Update: %v", err)
	}
	if state := overlay.queryForTest(); state.Mapped {
		t.Fatalf("stale initial backing flashed: %+v", state)
	}
	s.Version++
	if err := overlay.Update(s); err != nil {
		t.Fatalf("empty property Update: %v", err)
	}
	if state := overlay.queryForTest(); state.Mapped {
		t.Fatalf("empty property churn consumed deferred capture: %+v", state)
	}
	changedPixel, ok := overlay.changeVisibleUnderlayForTest()
	if !ok || changedPixel == visiblePixel {
		t.Fatalf("visible underlay did not change: before=%#x changed=%#x ok=%v",
			visiblePixel, changedPixel, ok)
	}
	s.Text, s.CursorUTF16 = "tipsyok", 7
	s.Version++
	if err := overlay.Update(s); err != nil {
		t.Fatalf("Update after focused-field frame: %v", err)
	}
	state := overlay.queryForTest()
	if !state.Mapped || state.GlyphMaskPixels == 0 ||
		state.AntialiasedPixels == 0 || !state.backgroundPreserved {
		t.Fatalf("visible-root surface state = %+v", state)
	}
	refreshed, ok := overlay.sampleRootForTest(17+190-1, 29+38-1)
	if !ok || refreshed != changedPixel {
		t.Fatalf("deferred capture missed focused field: want=%#x got=%#x ok=%v",
			changedPixel, refreshed, ok)
	}
	if focusAfter := focusedOverlayTestFocus(win); focusAfter != focusBefore {
		t.Fatalf("surface stole focus: before=%#x after=%#x", focusBefore, focusAfter)
	}
}

func TestFocusedTextSurfaceUsesExactPaddingGravityAndBaseline(t *testing.T) {
	win, _, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(360, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 19.9, 31.9, 220.9, 48.9
	s.PaddingLeft, s.PaddingTop, s.PaddingRight, s.PaddingBottom = 7, 3, 9, 5
	s.XAlignment, s.YAlignment = 0, 1 // exact APK LEFT + CENTER_VERTICAL
	if _, ok := overlay.fillParentAndSampleForTest(19, 31, 220, 48); !ok {
		t.Fatal("seed parent")
	}
	settleFocusedTextSurfaceForTest(t, overlay, &s)
	left := overlay.queryForTest()
	wantTop := int(s.PaddingTop) + (left.height-int(s.PaddingTop)-int(s.PaddingBottom)-left.lineBoxHeight)/2
	if left.x != 19 || left.y != 31 || left.width != 220 || left.height != 48 ||
		left.textOriginX != int(s.PaddingLeft) || left.lineBoxTop != wantTop ||
		left.baselineY <= left.lineBoxTop ||
		left.baselineY > left.lineBoxTop+left.lineBoxHeight {
		t.Fatalf("exact layout = %+v wantTop=%d", left, wantTop)
	}
	s.XAlignment = 1 // exact APK RIGHT
	if err := overlay.Update(s); err != nil {
		t.Fatal(err)
	}
	right := overlay.queryForTest()
	s.XAlignment = 2 // exact APK CENTER
	if err := overlay.Update(s); err != nil {
		t.Fatal(err)
	}
	center := overlay.queryForTest()
	if !(left.textOriginX < center.textOriginX && center.textOriginX < right.textOriginX) {
		t.Fatalf("APK gravity order left=%d center=%d right=%d",
			left.textOriginX, center.textOriginX, right.textOriginX)
	}
}

func TestFocusedTextSurfaceWrapExposeResizeAndAlphaZero(t *testing.T) {
	win, focusBefore, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(360, 220)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.Text = "wrapped words wrapped words"
	s.CursorUTF16 = len(s.Text)
	s.X, s.Y, s.Width, s.Height = 14, 18, 90, 80
	s.Multiline, s.TextWrapped = false, true // either flag enables APK wrapping
	s.Editable, s.CursorVisible = false, false
	before, ok := overlay.fillParentAndSampleForTest(14, 18, 90, 80)
	if !ok {
		t.Fatal("seed parent")
	}
	settleFocusedTextSurfaceForTest(t, overlay, &s)
	wrapped := overlay.queryForTest()
	if wrapped.lineBoxHeight <= int(math.Ceil(float64(s.FontSize))) {
		t.Fatalf("TextWrapped did not enable multiline layout: %+v", wrapped)
	}
	if !overlay.exposeForTest() {
		t.Fatal("could not expose surface")
	}
	if after, ok := overlay.sampleRootForTest(14+90-1, 18+80-1); !ok || after != before {
		t.Fatalf("Expose lost captured background: before=%#x after=%#x ok=%v", before, after, ok)
	}

	// A genuine geometry change destroys/wipes the old snapshot and captures
	// the parent again before mapping the resized surface.
	s.Version++
	s.X, s.Y, s.Width, s.Height = 130, 30, 150, 54
	newBackground, ok := overlay.fillParentAndSampleForTest(130, 30, 150, 54)
	if !ok {
		t.Fatal("seed resized parent")
	}
	if err := overlay.Update(s); err != nil {
		t.Fatalf("initial resized Update: %v", err)
	}
	if state := overlay.queryForTest(); state.Mapped {
		t.Fatalf("resized stale backing was mapped: %+v", state)
	}
	s.Version++
	if err := overlay.Update(s); err != nil {
		t.Fatalf("post-layout resized Update: %v", err)
	}
	resized := overlay.queryForTest()
	if resized.x != 130 || resized.y != 30 || resized.width != 150 || resized.height != 54 {
		t.Fatalf("resized geometry = %+v", resized)
	}
	if after, ok := overlay.sampleRootForTest(130+150-1, 30+54-1); !ok || after != newBackground {
		t.Fatalf("resize lost new background: before=%#x after=%#x ok=%v", newBackground, after, ok)
	}

	s.TextColor = 0
	if err := overlay.Update(s); err != nil {
		t.Fatal(err)
	}
	transparent := overlay.queryForTest()
	if transparent.TextAlpha != 0 || transparent.PaintedPixels != 0 ||
		transparent.GlyphMaskPixels != 0 || transparent.CaretMaskPixels != 0 ||
		transparent.AntialiasedPixels != 0 {
		t.Fatalf("alpha-zero text became visible: %+v", transparent)
	}
	if focusAfter := focusedOverlayTestFocus(win); focusAfter != focusBefore {
		t.Fatalf("resize/expose stole focus: before=%#x after=%#x", focusBefore, focusAfter)
	}
}

func TestOverlayPixelDiagnosticsDefaultOff(t *testing.T) {
	if os.Getenv("TIPSY_OVERLAY_PIXEL_DIAG") == "1" {
		t.Skip("TIPSY_OVERLAY_PIXEL_DIAG=1 forces paint-path measurement")
	}
	win, _, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(320, 180)
	if err != nil {
		t.Fatalf("open unfocused test window: %v", err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatalf("NewFocusedTextOverlay: %v", err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.X, s.Y, s.Width, s.Height = 11, 23, 180, 36
	s.Editable, s.CursorVisible = false, false
	if _, ok := overlay.fillParentAndSampleForTest(11, 23, 180, 36); !ok {
		t.Fatal("could not seed parent background")
	}
	settleFocusedTextSurfaceForTest(t, overlay, &s)
	diag := overlay.Diagnostics()
	if !diag.Mapped {
		t.Fatal("overlay paint did not map without pixel diagnostics")
	}
	if diag.GlyphMaskPixels != 0 || diag.PaintedPixels != 0 ||
		diag.AntialiasedPixels != 0 || diag.BrightGlyphPixels != 0 {
		t.Fatalf("paint-path measured pixels without TIPSY_OVERLAY_PIXEL_DIAG: %+v", diag)
	}
	state := overlay.queryForTest()
	if state.GlyphMaskPixels == 0 || state.AntialiasedPixels == 0 {
		t.Fatalf("on-demand test measurement produced no ink: %+v", state)
	}
}

func TestFocusedTextSurfaceRejectsMissingOfficialFont(t *testing.T) {
	win, _, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(240, 120)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.FontFile = "/definitely/missing/tipsy-official-font.ttf"
	if err := overlay.Update(s); err == nil {
		t.Fatal("missing official font silently fell back to a host face")
	}
}

func TestFocusedTextSurfaceLoadsInstalledOfficialFont(t *testing.T) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no user data root")
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	fontFile := filepath.Join(dataHome, "tipsy", "runtime", "assets",
		"fonts", "SourceSansPro-Regular.ttf")
	if info, err := os.Stat(fontFile); err != nil || !info.Mode().IsRegular() {
		t.Skip("official installed font unavailable")
	}
	win, _, closeWindow, err := newUnfocusedWindowForFocusedOverlayTest(240, 120)
	if err != nil {
		t.Fatal(err)
	}
	defer closeWindow()
	overlay, err := NewFocusedTextOverlay(win)
	if err != nil {
		t.Fatal(err)
	}
	defer overlay.Close()
	s := validFocusedTextSnapshot()
	s.FontFile = fontFile
	settleFocusedTextSurfaceForTest(t, overlay, &s)
	state := overlay.queryForTest()
	if !state.Mapped || state.GlyphMaskPixels == 0 || state.AntialiasedPixels == 0 {
		t.Fatalf("official font did not produce antialiased ink: %+v", state)
	}
}
