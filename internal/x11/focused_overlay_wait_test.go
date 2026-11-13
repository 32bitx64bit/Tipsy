// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && cgo

package x11

import (
	"sync"
	"testing"
	"time"
)

// These tests pin the bounded reader-drain contract: a compositor reader that
// never returns its lease (for example one wedged in a guest Vulkan fence wait)
// must not freeze the single Go launch loop, and the writer must never wipe,
// replace, or free pixels that a live lease still references. The destructive
// cleanup is deferred to the last reader release instead.

// leaseHasInk reports whether the leased frame still contains any non-zero
// alpha pixel. It reads only through the lease, exactly as the compositor does.
func leaseHasInk(f focusedTextForegroundLeaseForTest) bool {
	for y := f.y; y < f.y+f.height; y++ {
		for x := f.x; x < f.x+f.width; x++ {
			if a, ok := f.alphaAt(x, y); ok && a > 0 {
				return true
			}
		}
	}
	return false
}

// awaitBoundedWriter asserts that a writer call blocked on a stuck reader waits
// (does not clobber the lease immediately) yet returns within a bounded time
// (does not hang the launch loop). It returns the writer's error.
func awaitBoundedWriter(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("writer returned before the bounded reader drain (err=%v)", err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("writer blocked the launch loop on a stuck reader lease")
		return nil
	}
}

func TestFocusedTextDirtyUpdateBoundedWithStuckReader(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 320, 180)
	s := validFocusedTextSnapshot()
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	lease, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("no foreground lease")
	}
	release := sync.OnceFunc(lease.release)
	defer release()
	if !leaseHasInk(lease) {
		t.Fatal("published frame has no ink to verify")
	}
	s.Version++
	s.Text, s.CursorUTF16 = "replaced text", 13
	done := make(chan error, 1)
	go func() { done <- o.Update(s) }()
	if err := awaitBoundedWriter(t, done); err == nil {
		t.Fatal("dirty update replaced pixels under a stuck reader lease")
	}
	if !leaseHasInk(lease) {
		t.Fatal("dirty update wiped or replaced the leased pixels")
	}
	// Once the stuck lease returns, a dirty update publishes normally.
	release()
	s.Version++
	if err := o.Update(s); err != nil {
		t.Fatalf("dirty update after lease release: %v", err)
	}
}

func TestFocusedTextHideBoundedAndDefersWipeWithStuckReader(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 320, 180)
	s := validFocusedTextSnapshot()
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	lease, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("no foreground lease")
	}
	release := sync.OnceFunc(lease.release)
	defer release()
	if !leaseHasInk(lease) {
		t.Fatal("published frame has no ink to verify")
	}
	s.Active = false
	done := make(chan error, 1)
	go func() { done <- o.Update(s) }()
	if err := awaitBoundedWriter(t, done); err != nil {
		t.Fatalf("hide with stuck reader: %v", err)
	}
	// Hide unpublishes at once even though the wipe is deferred.
	if focusedTextOverlayLiveForTest() {
		t.Fatal("hide did not unpublish while a lease was stuck")
	}
	// The wipe must not run under the live lease.
	if !leaseHasInk(lease) {
		t.Fatal("hide wiped pixels that were still leased")
	}
	// Releasing the last lease performs the deferred wipe.
	release()
	if diag := o.Diagnostics(); diag.PaintedPixels != 0 {
		t.Fatalf("deferred wipe did not run: painted=%d", diag.PaintedPixels)
	}
}

func TestFocusedTextFreeBoundedAndDefersFreeWithStuckReader(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 320, 180)
	s := validFocusedTextSnapshot()
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	lease, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("no foreground lease")
	}
	release := sync.OnceFunc(lease.release)
	defer release()
	if !leaseHasInk(lease) {
		t.Fatal("published frame has no ink to verify")
	}
	// Close must not hang shutdown on a stuck reader, and must not free the
	// frame or its sync objects while the lease still references them.
	done := make(chan error, 1)
	go func() { done <- o.Close() }()
	if err := awaitBoundedWriter(t, done); err != nil {
		t.Fatalf("Close with stuck reader: %v", err)
	}
	if !leaseHasInk(lease) {
		t.Fatal("free wiped or freed pixels under a stuck reader lease")
	}
	// The last release completes the deferred free; a double free or a
	// use-after-free here would crash the test binary.
	release()
}

func TestUpdateNoticeDirtyUpdateBoundedWithStuckReader(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 640, 360)
	frame := UpdateNoticeFrame{
		Visible: true, Text: "Tipsy is outdated — update to v1.2.3",
		X: 100, Y: 20, Width: 440, Height: 64, Opacity: 1, Scale: 1,
	}
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.UpdateNotice(UpdateNoticeFrame{}) })
	lease, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("no update notice lease")
	}
	release := sync.OnceFunc(lease.release)
	defer release()
	if !leaseHasInk(lease) {
		t.Fatal("published notice has no ink to verify")
	}
	frame.Text = "Tipsy is outdated — update to v9.9.9"
	done := make(chan error, 1)
	go func() { done <- o.UpdateNotice(frame) }()
	if err := awaitBoundedWriter(t, done); err == nil {
		t.Fatal("notice update replaced pixels under a stuck reader lease")
	}
	if !leaseHasInk(lease) {
		t.Fatal("notice update wiped or replaced the leased pixels")
	}
	release()
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatalf("notice update after lease release: %v", err)
	}
}

func TestUpdateNoticeFreeBoundedAndDefersFreeWithStuckReader(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 640, 360)
	frame := UpdateNoticeFrame{
		Visible: true, Text: "Tipsy is outdated — update to v1.2.3",
		X: 100, Y: 20, Width: 440, Height: 64, Opacity: 1, Scale: 1,
	}
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	lease, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("no update notice lease")
	}
	release := sync.OnceFunc(lease.release)
	defer release()
	if !leaseHasInk(lease) {
		t.Fatal("published notice has no ink to verify")
	}
	// Close frees the notice while its lease is stuck: it must return
	// bounded and defer the frame free to the last release (retire == 2)
	// instead of freeing pixels the lease still references.
	done := make(chan error, 1)
	go func() { done <- o.Close() }()
	if err := awaitBoundedWriter(t, done); err != nil {
		t.Fatalf("Close with stuck notice reader: %v", err)
	}
	if !leaseHasInk(lease) {
		t.Fatal("notice free wiped or freed pixels under a stuck reader lease")
	}
	// The last release completes the deferred free; a double free or a
	// use-after-free here would crash the test binary.
	release()
	if _, ok := acquireFocusedTextForegroundForTest(); ok {
		t.Fatal("notice still acquirable after free and last release")
	}
}

func TestCompositeLeaseFreeBoundedAndDefersBothFrees(t *testing.T) {
	o := newFocusedTextOverlayForTest(t, 640, 360)
	s := validFocusedTextSnapshot()
	if err := o.Update(s); err != nil {
		t.Fatal(err)
	}
	frame := UpdateNoticeFrame{
		Visible: true, Text: "Tipsy is outdated — update to v1.2.3",
		X: 100, Y: 20, Width: 440, Height: 64, Opacity: 1, Scale: 1,
	}
	if err := o.UpdateNotice(frame); err != nil {
		t.Fatal(err)
	}
	lease, ok := acquireFocusedTextForegroundForTest()
	if !ok {
		t.Fatal("no composite lease")
	}
	if lease.lease&1 != 1 {
		t.Fatalf("expected a composite text+notice lease, got lease=%d", lease.lease)
	}
	release := sync.OnceFunc(lease.release)
	defer release()
	if !leaseHasInk(lease) {
		t.Fatal("composite frame has no ink to verify")
	}
	// Close frees both the text overlay and the notice while the composite
	// lease holds a reader on each: both frees must return bounded and
	// defer to the last release instead of freeing under live leases.
	done := make(chan error, 1)
	go func() { done <- o.Close() }()
	if err := awaitBoundedWriter(t, done); err != nil {
		t.Fatalf("Close with stuck composite reader: %v", err)
	}
	if !leaseHasInk(lease) {
		t.Fatal("free wiped or freed pixels under a stuck composite lease")
	}
	// One release completes both deferred frees and frees the composite
	// copy; a double free or use-after-free would crash the test binary.
	release()
	if _, ok := acquireFocusedTextForegroundForTest(); ok {
		t.Fatal("foreground still acquirable after free and last release")
	}
}
