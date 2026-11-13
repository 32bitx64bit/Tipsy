// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/updatecheck"
)

func TestUpdateNoticeTagRequiresUpdateReleaseBuildAndSetting(t *testing.T) {
	release := updatecheck.Release{TagName: "v1.1.0"}
	tests := []struct {
		name         string
		localVersion string
		enabled      bool
		release      updatecheck.Release
		fetchErr     error
		want         bool
	}{
		{name: "update available", localVersion: "1.0.0", enabled: true, release: release, want: true},
		{name: "setting disabled", localVersion: "1.0.0", enabled: false, release: release},
		{name: "development build", localVersion: "0.0.0-dev", enabled: true, release: release},
		{name: "up to date", localVersion: "1.1.0", enabled: true, release: release},
		{name: "offline", localVersion: "1.0.0", enabled: true, release: release, fetchErr: errors.New("offline")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tag, got := updateNoticeTag(tc.localVersion, tc.enabled, tc.release, tc.fetchErr)
			if got != tc.want {
				t.Fatalf("updateNoticeTag() visible = %v, want %v", got, tc.want)
			}
			if tc.want && tag != release.TagName {
				t.Fatalf("updateNoticeTag() tag = %q, want %q", tag, release.TagName)
			}
			if !tc.want && tag != "" {
				t.Fatalf("hidden update returned tag %q", tag)
			}
		})
	}
}

func TestUpdateNoticeAnimationWaitsForStartupOrLateResult(t *testing.T) {
	loopStart := time.Unix(100, 0)
	resultArrival := loopStart.Add(2 * time.Second)
	var notice updateNoticeAnimation
	notice.start(" v1.2.3 ", resultArrival, loopStart)
	for _, now := range []time.Time{resultArrival, loopStart.Add(updateNoticeStartupDelay - time.Nanosecond)} {
		if frame := notice.frame(now, 1280); frame.Visible {
			t.Fatalf("frame before startup delay at %v = %+v", now, frame)
		}
	}
	first := notice.frame(loopStart.Add(updateNoticeStartupDelay), 1280)
	if !first.Visible || first.X != 420 || first.Y != updateNoticeTopMargin-10 ||
		first.Width != updateNoticeWidth || first.Height != updateNoticeHeight {
		t.Fatalf("first delayed frame = %+v, want centered 440x64 card at y=10", first)
	}
	if math.Abs(first.Opacity) > 1e-9 || math.Abs(first.Scale-0.9) > 1e-9 ||
		first.AngleDegrees != 0 || !strings.Contains(first.Text, "Tipsy is outdated") || !strings.Contains(first.Text, "v1.2.3") {
		t.Fatalf("first delayed frame transform/text/angle = %+v", first)
	}

	lateArrival := loopStart.Add(8 * time.Second)
	var late updateNoticeAnimation
	late.start("v1.2.3", lateArrival, loopStart)
	if frame := late.frame(lateArrival, 1280); !frame.Visible || late.startedAt != lateArrival {
		t.Fatalf("late result frame = %+v, startedAt = %v, want immediate arrival at %v", frame, late.startedAt, lateArrival)
	}
}

func TestUpdateNoticeAnimationPopInHoldPopOut(t *testing.T) {
	start := time.Unix(100, 0)
	var notice updateNoticeAnimation
	notice.start("v1.2.3", start, start.Add(-updateNoticeStartupDelay))
	previousOpacity := -1.0
	for elapsed := time.Duration(0); elapsed <= updateNoticePopInDuration; elapsed += 25 * time.Millisecond {
		frame := notice.frame(start.Add(elapsed), 1280)
		if !frame.Visible || frame.Opacity < previousOpacity || frame.Opacity < 0 || frame.Opacity > 1 {
			t.Fatalf("pop-in at %v = %+v, previous opacity = %v", elapsed, frame, previousOpacity)
		}
		previousOpacity = frame.Opacity
	}
	rotated := notice.frame(start.Add(250*time.Millisecond), 1280)
	if rotated.AngleDegrees != 45 {
		t.Fatalf("pop-in frame after 250ms angle = %v, want 45 degrees", rotated)
	}
	for _, elapsed := range []time.Duration{updateNoticePopInDuration, time.Second, updateNoticePopInDuration + updateNoticeHoldDuration - time.Nanosecond} {
		frame := notice.frame(start.Add(elapsed), 1280)
		if !frame.Visible || frame.Opacity != 1 || frame.Scale != 1 || frame.Y != updateNoticeTopMargin {
			t.Fatalf("full-opacity hold at %v = %+v", elapsed, frame)
		}
	}

	popOutStart := updateNoticePopInDuration + updateNoticeHoldDuration
	midpoint := notice.frame(start.Add(popOutStart+updateNoticePopOutDuration/2), 1280)
	if !midpoint.Visible || math.Abs(midpoint.Opacity-0.875) > 1e-9 ||
		math.Abs(midpoint.Scale-0.99) > 1e-9 || midpoint.Y != updateNoticeTopMargin-1 {
		t.Fatalf("pop-out midpoint = %+v, want opacity .875, scale .99, y=19", midpoint)
	}
	last := notice.frame(start.Add(updateNoticeLifetime-time.Nanosecond), 800)
	if !last.Visible || last.X != 180 || last.Opacity <= 0 || last.Opacity >= midpoint.Opacity {
		t.Fatalf("last pop-out frame = %+v, want fading card centered in 800px viewport", last)
	}
	if hidden := notice.frame(start.Add(updateNoticeLifetime), 1280); hidden.Visible {
		t.Fatalf("frame after pop-out remains visible: %+v", hidden)
	}
	if hidden := notice.frame(start.Add(updateNoticeLifetime+time.Second), 1280); hidden.Visible {
		t.Fatalf("expired card became visible again: %+v", hidden)
	}
}

func TestStartLaunchUpdateCheckDoesNotWaitAndBoundsFetch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetchStarted := make(chan struct{})
	unblock := make(chan struct{})
	results := startLaunchUpdateCheck(ctx, "1.0.0", func(fetchCtx context.Context) (updatecheck.Release, error) {
		deadline, ok := fetchCtx.Deadline()
		if !ok || time.Until(deadline) > launchUpdateCheckTimeout {
			t.Errorf("fetch context deadline = %v, present = %v", deadline, ok)
		}
		close(fetchStarted)
		<-unblock
		return updatecheck.Release{TagName: "v2.0.0"}, nil
	})
	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("background fetch did not start")
	}
	select {
	case result := <-results:
		t.Fatalf("blocked fetch published early: %+v", result)
	default:
	}
	close(unblock)
	select {
	case result := <-results:
		if result.tag != "v2.0.0" {
			t.Fatalf("release tag = %q, want v2.0.0", result.tag)
		}
	case <-time.After(time.Second):
		t.Fatal("background fetch did not publish its result")
	}
}

func TestStartLaunchUpdateCheckOfflinePublishesNothing(t *testing.T) {
	fetchReturned := make(chan struct{})
	results := startLaunchUpdateCheck(context.Background(), "1.0.0", func(context.Context) (updatecheck.Release, error) {
		defer close(fetchReturned)
		return updatecheck.Release{}, errors.New("offline")
	})
	select {
	case <-fetchReturned:
	case <-time.After(time.Second):
		t.Fatal("offline fetch did not return")
	}
	select {
	case result := <-results:
		t.Fatalf("offline fetch published a card: %+v", result)
	default:
	}
}

func TestLaunchLoopUpdateNoticeResultHonorsSetting(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			results := make(chan updateNoticeResult, 1)
			wake := make(chan time.Time, 1)
			started := make(chan string, 1)
			refreshed := make(chan time.Time, 1)
			src := newTestLaunchLoopSources(ctx)
			src.updateNoticeResults = results
			src.updateNoticeEnabled = enabled
			src.updateNoticeWake = func() <-chan time.Time { return wake }
			src.startUpdateNotice = func(tag string, _ time.Time) { started <- tag }
			src.refreshUpdateNotice = func(now time.Time) { refreshed <- now }
			done := make(chan error, 1)
			go func() { done <- runLaunchLoop(src, func(string) {}, time.Hour, time.Hour) }()

			results <- updateNoticeResult{tag: "v2.0.0"}
			select {
			case tag := <-started:
				if !enabled || tag != "v2.0.0" {
					t.Fatalf("startUpdateNotice(%q) with enabled=%v", tag, enabled)
				}
			case <-time.After(200 * time.Millisecond):
				if enabled {
					t.Fatal("enabled update result did not start the card")
				}
			}
			tick := time.Unix(200, 0)
			wake <- tick
			select {
			case got := <-refreshed:
				if !got.Equal(tick) {
					t.Fatalf("refreshUpdateNotice(%v), want injected tick %v", got, tick)
				}
			case <-time.After(time.Second):
				t.Fatal("animation tick did not refresh the card")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("loop exit = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("launch loop did not stop")
			}
		})
	}
}

func TestLaunchLoopRunsWithoutUpdateCheckResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pumps atomic.Int32
	ready := make(chan struct{}, 1)
	src := newTestLaunchLoopSources(ctx)
	src.inputReady = ready
	src.pump = func() error { pumps.Add(1); return nil }
	// A pending check never publishes; input must still be serviced.
	src.updateNoticeResults = make(chan updateNoticeResult)
	done := make(chan error, 1)
	go func() { done <- runLaunchLoop(src, func(string) {}, time.Hour, time.Hour) }()
	ready <- struct{}{}
	waitForLoopCounter(t, &pumps, 1, "input pumps while update check pending")
	cancel()
	<-done
}

func TestUpdateNoticeWakeUsesDelayTimerAndTransitionCadence(t *testing.T) {
	var wake updateNoticeWake
	defer wake.stop()
	if wake.C() != nil {
		t.Fatal("idle wake exposes a timer")
	}
	now := time.Now()
	var notice updateNoticeAnimation
	notice.start("v1.2.3", now, now.Add(25*time.Millisecond-updateNoticeStartupDelay))
	wake.sync(&notice, now)
	if wake.timer == nil || wake.ticker != nil || wake.C() == nil {
		t.Fatal("startup delay did not arm only a one-shot timer")
	}
	select {
	case fired := <-wake.C():
		wake.sync(&notice, fired)
	case <-time.After(time.Second):
		t.Fatal("startup delay timer did not fire")
	}
	if wake.timer != nil || wake.ticker == nil || wake.interval != updateNoticeTransitionInterval {
		t.Fatal("pop-in did not switch to the 60Hz transition ticker")
	}
	wake.sync(&notice, notice.startedAt.Add(updateNoticePopInDuration))
	if wake.interval != updateNoticeFrameInterval {
		t.Fatalf("hold interval = %v, want %v", wake.interval, updateNoticeFrameInterval)
	}
	wake.sync(&notice, notice.startedAt.Add(updateNoticePopInDuration+updateNoticeHoldDuration))
	if wake.interval != updateNoticeTransitionInterval {
		t.Fatalf("pop-out interval = %v, want %v", wake.interval, updateNoticeTransitionInterval)
	}
	wake.stop()
	if wake.C() != nil {
		t.Fatal("hidden card left its animation timer running")
	}
}
