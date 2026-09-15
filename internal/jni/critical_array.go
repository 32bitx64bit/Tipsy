// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"runtime"
	"sync"
	"unsafe"
)

// A JNI array identity and a returned data address identify one outstanding
// borrow group. Different acquisitions of the same array may return the same
// address. The backing slice must stay pinned until the last such borrow ends.
type criticalArrayKey struct {
	array int64
	data  unsafe.Pointer
}

type criticalArrayBorrow struct {
	owner  *Object // keep the Go owner reachable; do not expose it to C
	pinner runtime.Pinner
	refs   int
	copied bool
}

type criticalArrayRegistry struct {
	mu      sync.Mutex
	borrows map[criticalArrayKey]*criticalArrayBorrow
}

var criticalArrays criticalArrayRegistry

func (r *criticalArrayRegistry) pin(o *Object) unsafe.Pointer {
	if o == nil || len(o.bytes) == 0 {
		return nil
	}
	p := unsafe.Pointer(unsafe.SliceData(o.bytes))
	key := criticalArrayKey{array: o.id, data: p}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.borrows == nil {
		r.borrows = make(map[criticalArrayKey]*criticalArrayBorrow)
	}
	if b := r.borrows[key]; b != nil {
		b.refs++
		return p
	}
	b := &criticalArrayBorrow{owner: o, refs: 1}
	// Only the pointer-free backing allocation crosses into C. Pinning the
	// Object itself neither pins its reachable data nor makes it C-compatible.
	b.pinner.Pin(unsafe.SliceData(o.bytes))
	r.borrows[key] = b
	return p
}

// registerCopy is used only for the C-owned, one-byte empty-array sentinel.
func (r *criticalArrayRegistry) registerCopy(o *Object, p unsafe.Pointer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.borrows == nil {
		r.borrows = make(map[criticalArrayKey]*criticalArrayBorrow)
	}
	r.borrows[criticalArrayKey{array: o.id, data: p}] = &criticalArrayBorrow{
		owner: o, refs: 1, copied: true,
	}
}

// release returns C memory that the caller must free, or nil. Never infer
// malloc ownership from absence in the registry: that could free a Go pointer.
// JNI's mode is ignored for a direct/pinned array. JNI_COMMIT retains copies.
func (r *criticalArrayRegistry) release(array int64, p unsafe.Pointer, commit bool) unsafe.Pointer {
	if p == nil {
		return nil
	}
	key := criticalArrayKey{array: array, data: p}
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.borrows[key]
	if b == nil {
		return nil
	}
	if b.copied && commit {
		return nil
	}
	b.refs--
	if b.refs != 0 {
		return nil
	}
	delete(r.borrows, key)
	if b.copied {
		return p
	}
	b.pinner.Unpin()
	return nil
}
