// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"testing"
	"unsafe"
)

func TestUpdateNoticePublishesPassiveForegroundAndRotates(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 640, 360)
	frame := UpdateNoticeFrame{
		Visible: true, Text: "Tipsy is outdated — update to v1.2.3",
		X: 100, Y: 20, Width: 440, Height: 64, Opacity: 1, Scale: 1,
	}
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	if !focusedTextOverlayLiveForTest() {
		t.Fatal("published update card did not wake the foreground compositor")
	}
	first, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("update card foreground was not acquired")
	}
	if first.x != frame.X || first.y != frame.Y || first.width != frame.Width || first.height != frame.Height {
		t.Fatalf("card geometry = (%d,%d) %dx%d, want (%d,%d) %dx%d",
			first.x, first.y, first.width, first.height, frame.X, frame.Y, frame.Width, frame.Height)
	}
	if alpha, ok := first.alphaAt(frame.X+20, frame.Y+32); !ok || alpha == 0 {
		t.Fatalf("card background alpha = %d, present = %v", alpha, ok)
	}
	first.release()

	frame.AngleDegrees = 90
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	second, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("rotated update card foreground was not acquired")
	}
	if second.generation <= first.generation {
		t.Fatalf("rotation generation = %d, want greater than %d", second.generation, first.generation)
	}
	second.release()
	if err := o.UpdateNotice(UpdateNoticeFrame{}); err != nil {
		t.Fatal(err)
	}
	if focusedTextOverlayLiveForTest() {
		t.Fatal("hidden update card remained live")
	}
	if _, ok := acquireFocusedTextForegroundForTest(); ok {
		t.Fatal("hidden update card remained published")
	}
}

func TestUpdateNoticeAppliesOpacityAndCenteredScale(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 640, 360)
	frame := UpdateNoticeFrame{
		Visible: true, Text: "Tipsy is outdated — update to v1.2.3",
		X: 100, Y: 20, Width: 440, Height: 64, Opacity: 1, Scale: 1,
	}
	pixelAt := func(foreground focusedTextForegroundLeaseForTest, x, y int) [4]uint8 {
		t.Helper()
		if foreground.pixels == nil || x < foreground.x || y < foreground.y ||
			x >= foreground.x+foreground.width || y >= foreground.y+foreground.height {
			t.Fatalf("pixel (%d,%d) outside foreground %+v", x, y, foreground)
		}
		offset := (y-foreground.y)*foreground.stride + (x-foreground.x)*4
		var pixel [4]uint8
		for channel := range pixel {
			pixel[channel] = *(*uint8)(unsafe.Add(foreground.pixels, offset+channel))
		}
		return pixel
	}
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	full, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("full-opacity card was not acquired")
	}
	fullPixel := pixelAt(full, frame.X+220, frame.Y+8)
	full.release()
	if fullPixel[3] < 240 {
		t.Fatalf("full card background = %v, want opaque premultiplied pixel", fullPixel)
	}

	frame.Opacity = 0.5
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	half, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("half-opacity card was not acquired")
	}
	halfPixel := pixelAt(half, frame.X+220, frame.Y+8)
	half.release()
	alphaDifference := int(halfPixel[3]) - int(fullPixel[3])/2
	if alphaDifference < -2 || alphaDifference > 2 {
		t.Fatalf("half-opacity pixel = %v, full-opacity pixel = %v", halfPixel, fullPixel)
	}
	for channel := 0; channel < 3; channel++ {
		if halfPixel[channel] > halfPixel[3] {
			t.Fatalf("non-premultiplied half-opacity pixel = %v", halfPixel)
		}
	}

	frame.Opacity = 1
	frame.Scale = 0.5
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	scaled, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("scaled card was not acquired")
	}
	edgeAlpha, _ := scaled.alphaAt(frame.X+20, frame.Y+32)
	centerAlpha, _ := scaled.alphaAt(frame.X+frame.Width/2, frame.Y+frame.Height/2)
	scaled.release()
	if edgeAlpha != 0 || centerAlpha == 0 {
		t.Fatalf("center-scaled card edge/center alpha = %d/%d, want 0/nonzero", edgeAlpha, centerAlpha)
	}
}

func TestUpdateNoticeCompositesWithFocusedTextAndHidesIndependently(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 640, 360)
	text := validFocusedTextSnapshot()
	text.Y = 200
	textX, textY, textWidth, textHeight := int(text.X), int(text.Y), int(text.Width), int(text.Height)
	if err := o.Update(text); err != nil {
		t.Fatal(err)
	}
	card := UpdateNoticeFrame{
		Visible: true, Text: "Tipsy is outdated — update to v1.2.3",
		X: 100, Y: 20, Width: 440, Height: 64, Opacity: 1, Scale: 1, AngleDegrees: 45,
	}
	if err := o.UpdateNotice(card); err != nil {
		t.Fatal(err)
	}
	combined, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("combined text and update card were not acquired")
	}
	if combined.x != textX || combined.y != card.Y || combined.width != card.X+card.Width-textX ||
		combined.height != textY+textHeight-card.Y {
		t.Fatalf("combined geometry = (%d,%d) %dx%d, want (%d,%d) %dx%d",
			combined.x, combined.y, combined.width, combined.height,
			textX, card.Y, card.X+card.Width-textX, textY+textHeight-card.Y)
	}
	if alpha, ok := combined.alphaAt(card.X+20, card.Y+32); !ok || alpha == 0 {
		t.Fatalf("composited card alpha = %d, present = %v", alpha, ok)
	}
	combined.release()

	if err := o.UpdateNotice(UpdateNoticeFrame{}); err != nil {
		t.Fatal(err)
	}
	remaining, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("hiding update card suppressed focused text")
	}
	if remaining.x != textX || remaining.y != textY || remaining.width != textWidth || remaining.height != textHeight {
		t.Fatalf("remaining text geometry = (%d,%d) %dx%d", remaining.x, remaining.y, remaining.width, remaining.height)
	}
	remaining.release()

	if err := o.UpdateNotice(card); err != nil {
		t.Fatal(err)
	}
	card.X = 640
	if err := o.UpdateNotice(card); err == nil {
		t.Fatal("off-viewport update card unexpectedly painted")
	}
	afterFailure, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("failed update card suppressed focused text")
	}
	if afterFailure.x != textX || afterFailure.y != textY || afterFailure.width != textWidth || afterFailure.height != textHeight {
		t.Fatalf("text after card failure = (%d,%d) %dx%d", afterFailure.x, afterFailure.y, afterFailure.width, afterFailure.height)
	}
	afterFailure.release()
	if err := o.Update(FocusedTextSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if focusedTextOverlayLiveForTest() {
		t.Fatal("hiding both foreground layers remained live")
	}
}
