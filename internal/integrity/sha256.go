// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later
package integrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"sync"
)

// sha256Scratch is the hash state and 1 MiB read buffer reused by SHA256Stream.
type sha256Scratch struct {
	h   hash.Hash
	buf []byte
}

var sha256Pool = sync.Pool{New: func() any {
	return &sha256Scratch{h: sha256.New(), buf: make([]byte, 1<<20)}
}}

// SHA256Stream reads r to EOF and returns its lowercase hex SHA-256 and byte count.
// Callers bound r themselves (io.SectionReader, io.LimitReader). ctx is checked
// before every read; a nil ctx means context.Background.
func SHA256Stream(ctx context.Context, r io.Reader) (string, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := sha256Pool.Get().(*sha256Scratch)
	defer sha256Pool.Put(s)
	s.h.Reset()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		n, err := r.Read(s.buf)
		if n > 0 {
			total += int64(n)
			_, _ = s.h.Write(s.buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", total, err
		}
	}
	return hex.EncodeToString(s.h.Sum(nil)), total, nil
}
