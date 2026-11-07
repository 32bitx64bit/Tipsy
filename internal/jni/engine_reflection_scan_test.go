// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"debug/elf"
	"encoding/binary"
	"io"
	"os"
	"testing"
	"unsafe"
)

// Fixture layout inside the fake engine image.
const (
	cursorCodeOff   = 0x1000 // reflection constructor call sequence
	cursorGetterOff = 0x2000 // property getter body
	cursorNameOff   = 0x3000 // "MouseIconEnabled"
	cursorDescOff   = 0x4000 // reflection descriptor
	cursorObjectOff = 0x6000 // object the getter reads its byte from
	cursorByteOff   = 0x209  // byte offset the getter decodes
)

var cursorGetterBody = []byte{0x55, 0x48, 0x89, 0xe5, 0x48, 0x81, 0xc7, 0x40, 0xff, 0xff, 0xff, 0xe8, 0, 0, 0, 0, 0x48, 0x85, 0xc0, 0x74, 0x08, 0x8a, 0x80, 0x09, 0x02, 0, 0, 0xeb, 0x02, 0x31, 0xc0, 0x5d, 0xc3}

// writeCursorRegistration lays down the constructor call sequence at code,
// pointing at name, descriptor and getter. stack selects the setter-on-stack
// form of the sequence.
func writeCursorRegistration(img *fakeEngineImage, code, getter int, stack bool) {
	p := img.mem[code:]
	copy(p, []byte{0x48, 0x8d, 0x3d, 0, 0, 0, 0, 0x48, 0x8d, 0x35, 0, 0, 0, 0, 0x48, 0x8d, 0x15, 0, 0, 0, 0, 0x48, 0x8d, 0x0d, 0, 0, 0, 0, 0x4c, 0x8d, 0x05, 0, 0, 0, 0, 0x45, 0x31, 0xc9, 0xe8, 0, 0, 0, 0})
	for _, ref := range []struct{ off, target int }{{0, cursorDescOff}, {7, cursorNameOff}, {14, cursorNameOff}, {21, getter}, {28, getter}} {
		binary.LittleEndian.PutUint32(p[ref.off+3:], uint32(int32(ref.target-(code+ref.off+7))))
	}
	if stack {
		copy(p[28:], []byte{0x48, 0x8d, 0x9d, 0xf0, 0xfc, 0xff, 0xff, 0x49, 0x89, 0xd8, 0x45, 0x31, 0xc9, 0xe8, 0, 0, 0, 0})
	}
}

// writeCursorRegistrationBypass lays down the 2.742 call sequence, which keeps
// the category in rbx and moves it into rdx before the call.
func writeCursorRegistrationBypass(img *fakeEngineImage, code, getter int) {
	p := img.mem[code:]
	copy(p, []byte{0x48, 0x8d, 0x3d, 0, 0, 0, 0, 0x48, 0x8d, 0x35, 0, 0, 0, 0, 0x48, 0x8d, 0x1d, 0, 0, 0, 0, 0x48, 0x8d, 0x0d, 0, 0, 0, 0, 0x48, 0x89, 0xda, 0x4d, 0x89, 0xf8, 0x45, 0x31, 0xc9, 0xe8, 0, 0, 0, 0})
	for _, ref := range []struct{ off, target int }{{0, cursorDescOff}, {7, cursorNameOff}, {14, cursorDescOff + 0x10}, {21, getter}} {
		binary.LittleEndian.PutUint32(p[ref.off+3:], uint32(int32(ref.target-(code+ref.off+7))))
	}
}

// cursorFixture is an image holding one MouseIconEnabled registration.
func cursorFixture(t *testing.T) (*fakeEngineImage, []engineAddrRange) {
	t.Helper()
	img := newFakeEngineImage(t)
	useFakeEngineMemory(t, img)
	copy(img.mem[cursorNameOff:], "MouseIconEnabled\x00")
	copy(img.mem[cursorGetterOff:], cursorGetterBody)
	writeCursorRegistration(img, cursorCodeOff, cursorGetterOff, false)
	return img, []engineAddrRange{{img.addr(0), img.addr(fakeImageSize)}}
}

// read lets scanCursorChunks reuse its buffer with the fake engine arena too.
func (img *fakeEngineImage) read(addr uintptr, out []byte) bool {
	off, ok := img.offset(addr)
	if !ok || len(out) == 0 || off+len(out) > len(img.mem) {
		return false
	}
	copy(out, img.mem[off:off+len(out)])
	return true
}

func TestReflectionGetterLocator(t *testing.T) {
	img, ranges := cursorFixture(t)
	base, end := img.addr(0), img.addr(fakeImageSize)
	p := img.mem[cursorCodeOff:]
	found := cursorCandidates(p[:64], img.addr(cursorCodeOff), base, end, "MouseIconEnabled")
	if len(found) != 1 || found[0].getter != img.addr(cursorGetterOff) || found[0].offset != cursorByteOff {
		t.Fatalf("candidate = %+v", found)
	}
	if got := locateCursorGetter(ranges, base, end, "MouseIconEnabled"); len(got) != 1 || got[0] != found[0] {
		t.Fatalf("locate = %+v, want %+v", got, found)
	}
	// Two overlapping ranges see the same registration twice; that is one getter.
	overlap := []engineAddrRange{ranges[0], {img.addr(0x800), img.addr(fakeImageSize)}}
	if got := locateCursorGetter(overlap, base, end, "MouseIconEnabled"); len(got) != 1 {
		t.Fatalf("overlapping scan = %+v, want one candidate", got)
	}
	writeCursorRegistration(img, cursorCodeOff, cursorGetterOff, true)
	if got := cursorCandidates(p[:64], img.addr(cursorCodeOff), base, end, "MouseIconEnabled"); len(got) != 1 || got[0].getter != img.addr(cursorGetterOff) {
		t.Fatalf("stack setter candidate: %+v", got)
	}
	writeCursorRegistrationBypass(img, cursorCodeOff, cursorGetterOff)
	if got := cursorCandidates(p[:64], img.addr(cursorCodeOff), base, end, "MouseIconEnabled"); len(got) != 1 || got[0].getter != img.addr(cursorGetterOff) || got[0].offset != cursorByteOff {
		t.Fatalf("rbx registration candidate: %+v", got)
	}
	if got := locateCursorGetter(ranges, base, end, "MouseIconEnabled"); len(got) != 1 || got[0].getter != img.addr(cursorGetterOff) {
		t.Fatalf("rbx registration locate = %+v", got)
	}
}

func TestReflectionGetterLocatorNotFound(t *testing.T) {
	img, ranges := cursorFixture(t)
	base, end := img.addr(0), img.addr(fakeImageSize)
	check := func(why string) {
		t.Helper()
		if got := locateCursorGetter(ranges, base, end, "MouseIconEnabled"); len(got) != 0 {
			t.Fatalf("%s: unexpected candidates %+v", why, got)
		}
	}
	img.mem[cursorNameOff] = 'X'
	check("wrong name")
	img.mem[cursorNameOff] = 'M'
	img.mem[cursorGetterOff+21] = 0x8b // getter body no longer the decoded shape
	check("wrong getter shape")
	img.mem[cursorGetterOff+21] = 0x8a
	binary.LittleEndian.PutUint32(img.mem[cursorGetterOff+23:], 0x7fffffff) // offset beyond any plausible object
	check("implausible offset")
	binary.LittleEndian.PutUint32(img.mem[cursorGetterOff+23:], cursorByteOff)
	if got := locateCursorGetter(ranges, base, end, "MouseIconEnabled"); len(got) != 1 {
		t.Fatalf("restored fixture: %+v", got)
	}
	// A target outside the module is not a candidate.
	if got := locateCursorGetter(ranges, base, img.addr(cursorGetterOff), "MouseIconEnabled"); len(got) != 0 {
		t.Fatalf("getter outside module accepted: %+v", got)
	}
	copy(img.mem[cursorCodeOff:], make([]byte, 64))
	check("no registration")
}

type cursorReadCounter struct {
	img        *fakeEngineImage
	fullChunks int
}

func (c *cursorReadCounter) bytes(addr uintptr, n int) ([]byte, bool) {
	if n > mouseBehaviorGetterWindow {
		c.fullChunks++
	}
	return c.img.bytes(addr, n)
}

func (c *cursorReadCounter) read(addr uintptr, out []byte) bool {
	if len(out) > mouseBehaviorGetterWindow {
		c.fullChunks++
	}
	return c.img.read(addr, out)
}

func (c *cursorReadCounter) u64(addr uintptr) (uint64, bool) { return c.img.u64(addr) }
func (c *cursorReadCounter) u32(addr uintptr) (uint32, bool) { return c.img.u32(addr) }

// locateCursorGetterPerChunkCopy is the superseded locator, retained here to
// measure the allocation regression prevented by the reusable-buffer scan.
func locateCursorGetterPerChunkCopy(ranges []engineAddrRange, base, end uintptr) []reflectionGetterCandidate {
	var found []reflectionGetterCandidate
	const chunk = uintptr(1 << 20)
	for _, r := range ranges {
		lo, hi := r.lo, r.hi
		if lo < base {
			lo = base
		}
		if hi > end {
			hi = end
		}
		for lo < hi {
			n := hi - lo
			if n > chunk {
				n = chunk
			}
			if b, ok := engineMem.bytes(lo, int(n)); ok {
				found = append(found, cursorCandidates(b, lo, base, end, "MouseIconEnabled")...)
			}
			if n == hi-lo {
				break
			}
			lo += n - cursorScanOverlap
		}
	}
	return uniqueCursorCandidates(found)
}

func TestCursorGetterScanUsesFewerAllocations(t *testing.T) {
	mem := make([]byte, 8<<20)
	img := &fakeEngineImage{base: unsafe.Pointer(&mem[0]), mem: mem}
	useFakeEngineMemory(t, img)
	copy(img.mem[cursorNameOff:], "MouseIconEnabled\x00")
	copy(img.mem[cursorGetterOff:], cursorGetterBody)
	writeCursorRegistration(img, cursorCodeOff, cursorGetterOff, false)
	ranges := []engineAddrRange{{img.addr(0), img.addr(len(mem))}}
	base, end := img.addr(0), img.addr(len(mem))

	oldAllocs := testing.AllocsPerRun(3, func() {
		if got := locateCursorGetterPerChunkCopy(ranges, base, end); len(got) != 1 {
			t.Fatalf("per-chunk copy found %d candidates", len(got))
		}
	})
	newAllocs := testing.AllocsPerRun(3, func() {
		if got := locateCursorGetter(ranges, base, end, "MouseIconEnabled"); len(got) != 1 {
			t.Fatalf("reused-buffer scan found %d candidates", len(got))
		}
	})
	if newAllocs >= oldAllocs {
		t.Fatalf("reused-buffer allocations %.1f, want fewer than per-chunk copies %.1f", newAllocs, oldAllocs)
	}
	t.Logf("full scan allocations: reused buffer %.1f, per-chunk copies %.1f", newAllocs, oldAllocs)
}

// Optional offline check against a copied, untrusted library. It is read as
// bytes only; no library code or constructors are loaded or executed.
func TestReflectionScanCopiedLibrary(t *testing.T) {
	path := os.Getenv("TIPSY_TEST_CURSOR_LIBRARY")
	if path == "" {
		t.Skip("no copied library supplied")
	}
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var size uint64
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD && p.Vaddr+p.Memsz > size {
			size = p.Vaddr + p.Memsz
		}
	}
	if size == 0 || size > 1<<30 {
		t.Fatalf("invalid image size %#x", size)
	}
	data := make([]byte, int(size))
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD {
			if _, err := io.ReadFull(p.Open(), data[p.Vaddr:p.Vaddr+p.Filesz]); err != nil {
				t.Fatal(err)
			}
		}
	}
	img := &fakeEngineImage{base: unsafe.Pointer(&data[0]), mem: data}
	useFakeEngineMemory(t, img)
	behavior := cursorCandidates(data, img.addr(0), img.addr(0), img.addr(len(data)), "MouseBehavior")
	if len(behavior) != 1 {
		t.Fatalf("expected one MouseBehavior getter, got %+v", behavior)
	}
	t.Logf("MouseBehavior getter RVA %#x, resolver RVA %#x, offset %#x",
		behavior[0].getter-img.addr(0), behavior[0].resolver-img.addr(0), behavior[0].offset)
}
