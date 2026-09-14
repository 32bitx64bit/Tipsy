// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "time"

const (
	optionalLookupFirstRetry = time.Second
	optionalLookupMaxRetry   = 30 * time.Second
)

// optionalLookup backs off only automatic, failure-prone diagnostic reads.
// An explicit Refresh remains prompt, and a success immediately restores the
// normal automatic path.
type optionalLookup struct {
	failures   int
	retryAfter time.Time
}

func (l *optionalLookup) allow(now time.Time, explicit bool) bool {
	return explicit || l.retryAfter.IsZero() || !now.Before(l.retryAfter)
}

func (l *optionalLookup) failed(now time.Time) {
	l.failures++
	delay := optionalLookupFirstRetry
	for i := 1; i < l.failures && delay < optionalLookupMaxRetry; i++ {
		delay *= 2
	}
	if delay > optionalLookupMaxRetry {
		delay = optionalLookupMaxRetry
	}
	l.retryAfter = now.Add(delay)
}

func (l *optionalLookup) succeeded() {
	l.failures = 0
	l.retryAfter = time.Time{}
}
