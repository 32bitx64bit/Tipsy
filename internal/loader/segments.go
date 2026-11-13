// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package loader

import (
	"sort"
	"syscall"
)

// SegmentRange is a live process address interval [Lo, Hi) belonging to one or
// more contiguous PT_LOAD segments of a mapped Module.
type SegmentRange struct {
	Lo uintptr
	Hi uintptr
}

func mergeSegmentRanges(in []SegmentRange) []SegmentRange {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Lo != in[j].Lo {
			return in[i].Lo < in[j].Lo
		}
		return in[i].Hi < in[j].Hi
	})
	out := make([]SegmentRange, 0, len(in))
	for _, r := range in {
		if r.Hi <= r.Lo {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Hi >= r.Lo {
			if r.Hi > out[n-1].Hi {
				out[n-1].Hi = r.Hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func segmentRangesContain(ranges []SegmentRange, addr, size uintptr) bool {
	if addr == 0 || size == 0 || addr > ^uintptr(0)-size {
		return false
	}
	end := addr + size
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].Hi > addr })
	return i < len(ranges) && ranges[i].Lo <= addr && end <= ranges[i].Hi
}

func segmentRangesOverlap(ranges []SegmentRange, addr, size uintptr) bool {
	if size == 0 || addr > ^uintptr(0)-size {
		return false
	}
	end := addr + size
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].Hi > addr })
	return i < len(ranges) && ranges[i].Lo < end
}

// ensureSegmentRanges builds sorted live [Lo, Hi) segment tables from m.segs.
// Caller must hold m.mu.
func (m *Module) ensureSegmentRanges() {
	if m.segRangesReady {
		return
	}
	m.readRanges = nil
	m.execRanges = nil
	m.codeRanges = nil
	m.dataRanges = nil
	if m.closed || m.bias == 0 {
		m.segRangesReady = true
		return
	}
	var read, exec, code, data []SegmentRange
	for _, s := range m.segs {
		if s.memsz == 0 {
			continue
		}
		vaddr := uintptr(s.vaddr)
		memsz := uintptr(s.memsz)
		if uint64(vaddr) != s.vaddr || uint64(memsz) != s.memsz {
			continue
		}
		if m.bias > ^uintptr(0)-vaddr {
			continue
		}
		lo := m.bias + vaddr
		if lo == 0 || lo > ^uintptr(0)-memsz {
			continue
		}
		r := SegmentRange{Lo: lo, Hi: lo + memsz}
		isRead := s.prot&syscall.PROT_READ != 0
		isExec := s.prot&syscall.PROT_EXEC != 0
		if isRead {
			read = append(read, r)
		}
		if isExec {
			exec = append(exec, r)
		}
		if isRead && isExec {
			code = append(code, r)
		}
		if isRead && !isExec {
			data = append(data, r)
		}
	}
	m.readRanges = mergeSegmentRanges(read)
	m.execRanges = mergeSegmentRanges(exec)
	m.codeRanges = mergeSegmentRanges(code)
	m.dataRanges = mergeSegmentRanges(data)
	m.segRangesReady = true
}

// IsOpen reports whether the module remains mapped with at least one readable
// PT_LOAD segment.
func (m *Module) IsOpen() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.bias == 0 {
		return false
	}
	m.ensureSegmentRanges()
	return len(m.readRanges) > 0
}

// ReadableSegments returns a copy of this module's readable PT_LOAD intervals,
// sorted by ascending address. A nil or closed module returns nil.
func (m *Module) ReadableSegments() []SegmentRange {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.ensureSegmentRanges()
	return append([]SegmentRange(nil), m.readRanges...)
}

// ExecutableSegments returns a copy of this module's executable PT_LOAD
// intervals, sorted by ascending address. A nil or closed module returns nil.
func (m *Module) ExecutableSegments() []SegmentRange {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.ensureSegmentRanges()
	return append([]SegmentRange(nil), m.execRanges...)
}

// DataSegments returns a copy of this module's readable non-executable PT_LOAD
// intervals (.rodata, RELRO, .data, .bss), sorted by ascending address.
func (m *Module) DataSegments() []SegmentRange {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.ensureSegmentRanges()
	return append([]SegmentRange(nil), m.dataRanges...)
}

// ContainsReadable reports whether [addr, addr+size) lies wholly within this
// module's readable PT_LOAD segments.
func (m *Module) ContainsReadable(addr, size uintptr) bool {
	if m == nil || addr == 0 || size == 0 || addr > ^uintptr(0)-size {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ensureSegmentRanges()
	return segmentRangesContain(m.readRanges, addr, size)
}

// ContainsExec reports whether [addr, addr+size) lies wholly within this
// module's readable executable PT_LOAD segments.
func (m *Module) ContainsExec(addr, size uintptr) bool {
	if m == nil || addr == 0 || size == 0 || addr > ^uintptr(0)-size {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ensureSegmentRanges()
	return segmentRangesContain(m.codeRanges, addr, size)
}

// ContainsData reports whether [addr, addr+size) lies wholly within this
// module's readable non-executable PT_LOAD segments.
func (m *Module) ContainsData(addr, size uintptr) bool {
	if m == nil || addr == 0 || size == 0 || addr > ^uintptr(0)-size {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ensureSegmentRanges()
	return segmentRangesContain(m.dataRanges, addr, size)
}

// OverlapsExec reports whether [addr, addr+size) overlaps any executable
// PT_LOAD segment of this module.
func (m *Module) OverlapsExec(addr, size uintptr) bool {
	if m == nil || size == 0 || addr > ^uintptr(0)-size {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ensureSegmentRanges()
	return segmentRangesOverlap(m.execRanges, addr, size)
}
