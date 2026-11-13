// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/updatecheck"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

const (
	launchUpdateCheckTimeout       = 10 * time.Second
	updateNoticeStartupDelay       = 6 * time.Second
	updateNoticePopInDuration      = 300 * time.Millisecond
	updateNoticeHoldDuration       = 5 * time.Second
	updateNoticePopOutDuration     = 250 * time.Millisecond
	updateNoticeLifetime           = updateNoticePopInDuration + updateNoticeHoldDuration + updateNoticePopOutDuration
	updateNoticeFrameInterval      = time.Second / 30
	updateNoticeTransitionInterval = time.Second / 60
	updateNoticeTopMargin          = 20
	updateNoticeWidth              = 440
	updateNoticeHeight             = 64
)

type updateNoticeResult struct {
	tag string
}

// updateNoticeTag applies every display gate to one completed release fetch.
// Offline, malformed, up-to-date and development results stay quiet.
func updateNoticeTag(localVersion string, enabled bool, release updatecheck.Release, fetchErr error) (string, bool) {
	if fetchErr != nil || !enabled || updatecheck.Compare(localVersion, release.TagName) != updatecheck.UpdateAvailable {
		return "", false
	}
	tag := strings.TrimSpace(release.TagName)
	if tag == "" {
		return "", false
	}
	return tag, true
}

type updateNoticeFetch func(context.Context) (updatecheck.Release, error)

// startLaunchUpdateCheck returns immediately. Its capacity-one result channel
// lets the launch loop consume an available update without waiting for network.
func startLaunchUpdateCheck(ctx context.Context, localVersion string, fetch updateNoticeFetch) <-chan updateNoticeResult {
	results := make(chan updateNoticeResult, 1)
	go func() {
		checkCtx, cancel := context.WithTimeout(ctx, launchUpdateCheckTimeout)
		defer cancel()
		release, err := fetch(checkCtx)
		if tag, ok := updateNoticeTag(localVersion, true, release, err); ok {
			select {
			case results <- updateNoticeResult{tag: tag}:
			case <-ctx.Done():
			}
		}
	}()
	return results
}

// updateNoticeAnimation owns only launch-loop state. The renderer receives an
// immutable frame and never owns the delay timer, ticker or animation lifetime.
type updateNoticeAnimation struct {
	tag       string
	startedAt time.Time
	active    bool
}

func (a *updateNoticeAnimation) start(tag string, now, loopStartedAt time.Time) {
	a.tag = strings.TrimSpace(tag)
	a.startedAt = now
	if earliest := loopStartedAt.Add(updateNoticeStartupDelay); earliest.After(now) {
		a.startedAt = earliest
	}
	a.active = a.tag != ""
}

func (a *updateNoticeAnimation) frame(now time.Time, viewportWidth int) x11.UpdateNoticeFrame {
	if !a.active || now.Before(a.startedAt) {
		return x11.UpdateNoticeFrame{}
	}
	elapsed := now.Sub(a.startedAt)
	if elapsed >= updateNoticeLifetime {
		a.active = false
		return x11.UpdateNoticeFrame{}
	}
	opacity, scale, yOffset := updateNoticeTransform(elapsed)
	return x11.UpdateNoticeFrame{
		Visible:      true,
		Text:         fmt.Sprintf("Tipsy is outdated — update to %s", a.tag),
		X:            updateNoticeX(viewportWidth),
		Y:            updateNoticeTopMargin + yOffset,
		Width:        updateNoticeWidth,
		Height:       updateNoticeHeight,
		Opacity:      opacity,
		Scale:        scale,
		AngleDegrees: math.Mod(elapsed.Seconds()*180, 360),
	}
}

func updateNoticeTransform(elapsed time.Duration) (opacity, scale float64, yOffset int) {
	switch {
	case elapsed < updateNoticePopInDuration:
		progress := elapsed.Seconds() / updateNoticePopInDuration.Seconds()
		eased := updateNoticeEaseOutBack(progress)
		return min(1, max(0, eased)), 0.9 + 0.1*eased, -int(math.Round(10 * (1 - eased)))
	case elapsed < updateNoticePopInDuration+updateNoticeHoldDuration:
		return 1, 1, 0
	default:
		progress := (elapsed - updateNoticePopInDuration - updateNoticeHoldDuration).Seconds() / updateNoticePopOutDuration.Seconds()
		eased := progress * progress * progress
		return 1 - eased, 1 - 0.08*eased, -int(math.Round(10 * eased))
	}
}

func updateNoticeEaseOutBack(progress float64) float64 {
	const overshoot = 1.70158
	shifted := progress - 1
	return 1 + (overshoot+1)*shifted*shifted*shifted + overshoot*shifted*shifted
}

func updateNoticeX(viewportWidth int) int {
	x := (viewportWidth - updateNoticeWidth) / 2
	if x < 0 {
		return 0
	}
	return x
}

// updateNoticeWake uses a one-shot timer for the startup delay, then ticks
// faster during transitions than during the full-opacity hold.
type updateNoticeWake struct {
	timer    *time.Timer
	ticker   *time.Ticker
	interval time.Duration
}

func (w *updateNoticeWake) C() <-chan time.Time {
	if w == nil {
		return nil
	}
	if w.timer != nil {
		return w.timer.C
	}
	if w.ticker == nil {
		return nil
	}
	return w.ticker.C
}

func (w *updateNoticeWake) sync(a *updateNoticeAnimation, now time.Time) {
	if w == nil {
		return
	}
	if a == nil || !a.active {
		w.stop()
		return
	}
	if delay := a.startedAt.Sub(now); delay > 0 {
		w.stopTicker()
		if w.timer == nil {
			w.timer = time.NewTimer(delay)
		} else {
			w.timer.Reset(delay)
		}
		return
	}
	w.stopTimer()
	interval := updateNoticeFrameInterval
	elapsed := now.Sub(a.startedAt)
	if elapsed < updateNoticePopInDuration || elapsed >= updateNoticePopInDuration+updateNoticeHoldDuration {
		interval = updateNoticeTransitionInterval
	}
	if w.ticker == nil {
		w.ticker = time.NewTicker(interval)
	} else if w.interval != interval {
		w.ticker.Reset(interval)
	}
	w.interval = interval
}

func (w *updateNoticeWake) stopTimer() {
	if w.timer == nil {
		return
	}
	if !w.timer.Stop() {
		select {
		case <-w.timer.C:
		default:
		}
	}
	w.timer = nil
}

func (w *updateNoticeWake) stopTicker() {
	if w.ticker != nil {
		w.ticker.Stop()
		w.ticker = nil
	}
	w.interval = 0
}

func (w *updateNoticeWake) stop() {
	w.stopTimer()
	w.stopTicker()
}
