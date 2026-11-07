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
