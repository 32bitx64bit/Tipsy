// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"io"
)

type Source interface {
	Availability(context.Context) SourceAvailability
	Acquire(context.Context, string, ProgressFunc) ([]string, error)
}

type UnavailableSource struct {
	Name     string
	Reason   string
	LegalURL string
}

func (s UnavailableSource) Availability(context.Context) SourceAvailability {
	reason := s.Reason
	if reason == "" {
		reason = "No official public direct APK source is available; choose APK files obtained through your own authorized account."
	}
	return SourceAvailability{Available: false, Name: s.Name, Reason: reason, LegalURL: s.LegalURL}
}

func (s UnavailableSource) Acquire(context.Context, string, ProgressFunc) ([]string, error) {
	a := s.Availability(context.Background())
	return nil, setupError(ErrSourceUnavailable, "automatic setup", a.Reason, nil)
}

type progressReader struct {
	ctx    context.Context
	r      io.Reader
	onRead func(int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if n > 0 && r.onRead != nil {
		r.onRead(int64(n))
	}
	return n, err
}
