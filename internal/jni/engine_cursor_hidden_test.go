// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"debug/elf"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func TestCursorHiddenLocator(t *testing.T) {
	img, ranges := cursorFixture(t)
	base, end := img.addr(0), img.addr(fakeImageSize)
	p := img.mem[cursorCodeOff:]
	found := cursorCandidates(p[:64], img.addr(cursorCodeOff), base, end)
	if len(found) != 1 || found[0].getter != img.addr(cursorGetterOff) || found[0].offset != cursorByteOff {
		t.Fatalf("candidate = %+v", found)
	}
	if got := locateCursorGetter(ranges, base, end); len(got) != 1 || got[0] != found[0] {
		t.Fatalf("locate = %+v, want %+v", got, found)
	}
	// Two overlapping ranges see the same registration twice; that is one getter.
	overlap := []engineAddrRange{ranges[0], {img.addr(0x800), img.addr(fakeImageSize)}}
	if got := locateCursorGetter(overlap, base, end); len(got) != 1 {
		t.Fatalf("overlapping scan = %+v, want one candidate", got)
	}
	writeCursorRegistration(img, cursorCodeOff, cursorGetterOff, true)
	if got := cursorCandidates(p[:64], img.addr(cursorCodeOff), base, end); len(got) != 1 || got[0].getter != img.addr(cursorGetterOff) {
		t.Fatalf("stack setter candidate: %+v", got)
	}
	writeCursorRegistrationBypass(img, cursorCodeOff, cursorGetterOff)
	if got := cursorCandidates(p[:64], img.addr(cursorCodeOff), base, end); len(got) != 1 || got[0].getter != img.addr(cursorGetterOff) || got[0].offset != cursorByteOff {
		t.Fatalf("rbx registration candidate: %+v", got)
	}
	if got := locateCursorGetter(ranges, base, end); len(got) != 1 || got[0].getter != img.addr(cursorGetterOff) {
		t.Fatalf("rbx registration locate = %+v", got)
	}
}

func TestCursorHiddenLocatorNotFound(t *testing.T) {
	img, ranges := cursorFixture(t)
	base, end := img.addr(0), img.addr(fakeImageSize)
	check := func(why string) {
		t.Helper()
		if got := locateCursorGetter(ranges, base, end); len(got) != 0 {
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
	if got := locateCursorGetter(ranges, base, end); len(got) != 1 {
		t.Fatalf("restored fixture: %+v", got)
	}
	// A target outside the module is not a candidate.
	if got := locateCursorGetter(ranges, base, img.addr(cursorGetterOff)); len(got) != 0 {
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

func TestCursorGetterCacheHitSkipsFullScan(t *testing.T) {
	img, ranges := cursorMouseServiceChainFixture(t)
	counter := &cursorReadCounter{img: img}
	engineMem = counter
	cachePath := filepath.Join(t.TempDir(), "cursor-getter.json")
	base := img.addr(0)

	first := &cursorHiddenReader{}
	first.initCached(ranges, base, fakeImageSize, cachePath, "build-a")
	if first.disabled || first.resolve == nil || first.bridgeCandidate.bridge != img.addr(chainBridgeOff) || counter.fullChunks == 0 {
		t.Fatalf("initial scan: disabled=%v candidate=%+v full chunks=%d", first.disabled, first.candidate, counter.fullChunks)
	}
	counter.fullChunks = 0
	second := &cursorHiddenReader{}
	second.initCached(ranges, base, fakeImageSize, cachePath, "build-a")
	if second.disabled || second.resolve == nil || second.candidate != first.candidate || second.behaviorCandidate != first.behaviorCandidate || second.bridgeCandidate != first.bridgeCandidate {
		t.Fatalf("cache hit candidate=%+v, want %+v (disabled=%v)", second.candidate, first.candidate, second.disabled)
	}
	if counter.fullChunks != 0 {
		t.Fatalf("cache hit performed %d full-chunk reads", counter.fullChunks)
	}
	if hidden, ok := second.read(); !hidden || !ok {
		t.Fatalf("cached MouseService read: hidden=%v ok=%v", hidden, ok)
	}
}

func TestCursorGetterCacheMismatchFallsBackToFullScan(t *testing.T) {
	tests := []string{"stale build", "corrupt json", "icon", "behavior", "bridge", "chain"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			img, ranges := cursorMouseServiceChainFixture(t)
			counter := &cursorReadCounter{img: img}
			engineMem = counter
			path := filepath.Join(t.TempDir(), "cursor-getter.json")
			base := img.addr(0)
			first := &cursorHiddenReader{}
			first.initCached(ranges, base, fakeImageSize, path, "build-a")
			if first.resolve == nil {
				t.Fatal("initial proof failed")
			}
			build := "build-a"
			switch name {
			case "stale build":
				build = "build-b"
			case "corrupt json":
				if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "icon":
				img.mem[proofCursorGetOff+7] ^= 1
			case "behavior":
				img.mem[proofBehaviorGetOff+7] ^= 1
			case "bridge":
				// A valid changed bridge is still rejected and rediscovered.
				binary.LittleEndian.PutUint32(img.mem[chainBridgeOff+29:], chainProviderOffset+8)
			case "chain":
				// A damaged proof must scan rather than accept cached addresses.
				img.mem[proofResolverOff+22] ^= 1
			}
			counter.fullChunks = 0
			got := &cursorHiddenReader{}
			got.initCached(ranges, base, fakeImageSize, path, build)
			if counter.fullChunks == 0 {
				t.Fatal("cache mismatch did not scan")
			}
			if name == "chain" {
				if got.resolve != nil {
					t.Fatal("invalid chain accepted")
				}
				return
			}
			if got.resolve == nil || got.candidate != first.candidate || got.behaviorCandidate != first.behaviorCandidate || got.bridgeCandidate.bridge != first.bridgeCandidate.bridge {
				t.Fatalf("fallback differs: %+v", got)
			}
			counter.fullChunks = 0
			again := &cursorHiddenReader{}
			again.initCached(ranges, base, fakeImageSize, path, build)
			if again.resolve == nil || counter.fullChunks != 0 {
				t.Fatalf("refreshed cache: resolve=%v full reads=%d", again.resolve != nil, counter.fullChunks)
			}
		})
	}
}

// locateCursorGetterPerChunkCopy is the superseded locator, retained here to
// measure the allocation regression prevented by the reusable-buffer scan.
func locateCursorGetterPerChunkCopy(ranges []engineAddrRange, base, end uintptr) []cursorHiddenCandidate {
	var found []cursorHiddenCandidate
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
				found = append(found, cursorCandidates(b, lo, base, end)...)
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
		if got := locateCursorGetter(ranges, base, end); len(got) != 1 {
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
func TestCursorHiddenCopiedLibrary(t *testing.T) {
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
	found := cursorCandidates(data, img.addr(0), img.addr(0), img.addr(len(data)))
	if len(found) != 1 {
		t.Fatalf("expected one getter, got %+v", found)
	}
	t.Logf("MouseIconEnabled getter RVA %#x, resolver RVA %#x, offset %#x",
		found[0].getter-img.addr(0), found[0].resolver-img.addr(0), found[0].offset)
	behavior := cursorCandidates(data, img.addr(0), img.addr(0), img.addr(len(data)), "MouseBehavior")
	if len(behavior) != 1 {
		t.Fatalf("expected one MouseBehavior getter, got %+v", behavior)
	}
	t.Logf("MouseBehavior getter RVA %#x, resolver RVA %#x, offset %#x",
		behavior[0].getter-img.addr(0), behavior[0].resolver-img.addr(0), behavior[0].offset)
}

func TestCursorHiddenValidation(t *testing.T) {
	for _, tc := range []struct {
		value      byte
		hidden, ok bool
	}{{0, true, true}, {1, false, true}, {2, false, false}, {255, false, false}} {
		hidden, ok := cursorHiddenValue(tc.value)
		if hidden != tc.hidden || ok != tc.ok {
			t.Fatalf("value %d: got %v, %v", tc.value, hidden, ok)
		}
	}
	// The process-wide reader has no proven object resolver.
	if hidden, ok := EngineCursorHidden(); hidden || ok {
		t.Fatalf("unresolved object must remain disabled: %v %v", hidden, ok)
	}
}

// newCursorReader returns an initialised reader whose resolver yields obj.
func newCursorReader(t *testing.T, img *fakeEngineImage, ranges []engineAddrRange, obj *uintptr, lines *[]string) *cursorHiddenReader {
	t.Helper()
	r := &cursorHiddenReader{
		resolve: func() (uintptr, bool) { return *obj, *obj != 0 },
		logf:    func(msg string, _ ...any) { *lines = append(*lines, msg) },
	}
	r.init(ranges, img.addr(0), fakeImageSize)
	return r
}

func TestCursorHiddenReader(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "")
	img, ranges := cursorFixture(t)
	obj := img.addr(cursorObjectOff)
	var lines []string
	r := newCursorReader(t, img, ranges, &obj, &lines)
	if r.disabled || r.candidate.getter != img.addr(cursorGetterOff) || r.candidate.offset != cursorByteOff {
		t.Fatalf("reader after init: %+v", r)
	}
	read := func(b byte) (bool, bool) {
		img.mem[cursorObjectOff+cursorByteOff] = b
		return r.read()
	}
	// MouseIconEnabled is the inverse of hidden: 0 hides the cursor.
	if hidden, ok := read(0); !hidden || !ok {
		t.Fatalf("byte 0: hidden=%v ok=%v", hidden, ok)
	}
	if hidden, ok := read(1); hidden || !ok {
		t.Fatalf("byte 1: hidden=%v ok=%v", hidden, ok)
	}
	// No object (before a game loads, or in Home) is unavailable but not latched.
	obj = 0
	if hidden, ok := read(0); hidden || ok {
		t.Fatalf("NULL object: hidden=%v ok=%v", hidden, ok)
	}
	if r.disabled {
		t.Fatal("NULL object latched the reader off")
	}
	obj = img.addr(cursorObjectOff)
	if hidden, ok := read(0); !hidden || !ok {
		t.Fatalf("recovery after NULL object: hidden=%v ok=%v", hidden, ok)
	}
	// An out-of-domain byte latches the reader off for good.
	if hidden, ok := read(2); hidden || ok {
		t.Fatalf("byte 2: hidden=%v ok=%v", hidden, ok)
	}
	if hidden, ok := read(0); hidden || ok {
		t.Fatalf("latched reader answered: hidden=%v ok=%v", hidden, ok)
	}
	if len(lines) != 0 {
		t.Fatalf("trace unset but logged: %q", lines)
	}
}

func TestCursorHiddenReaderUnreadableObject(t *testing.T) {
	img, ranges := cursorFixture(t)
	obj := img.addr(fakeImageSize + 0x1000) // outside the readable image
	var lines []string
	r := newCursorReader(t, img, ranges, &obj, &lines)
	if hidden, ok := r.read(); hidden || ok || !r.disabled {
		t.Fatalf("unreadable object: hidden=%v ok=%v disabled=%v", hidden, ok, r.disabled)
	}
}

func TestCursorHiddenReaderOffWithoutResolverOrGetter(t *testing.T) {
	img, ranges := cursorFixture(t)
	// A getter was found but no object resolver is proven: always unavailable.
	r := &cursorHiddenReader{}
	r.init(ranges, img.addr(0), fakeImageSize)
	if r.disabled || r.candidate.getter == 0 {
		t.Fatalf("locator should still find the getter: %+v", r)
	}
	if hidden, ok := r.read(); hidden || ok {
		t.Fatalf("no resolver: hidden=%v ok=%v", hidden, ok)
	}
	// No getter in the image: the reader latches off even with a resolver.
	img.mem[cursorNameOff] = 'X'
	obj := img.addr(cursorObjectOff)
	var lines []string
	r = newCursorReader(t, img, ranges, &obj, &lines)
	if !r.disabled {
		t.Fatal("missing getter did not latch the reader off")
	}
	if hidden, ok := r.read(); hidden || ok {
		t.Fatalf("missing getter: hidden=%v ok=%v", hidden, ok)
	}
	// Two different getters are ambiguous.
	img.mem[cursorNameOff] = 'M'
	copy(img.mem[cursorGetterOff+0x100:], cursorGetterBody)
	binary.LittleEndian.PutUint32(img.mem[cursorGetterOff+0x100+23:], 0x300)
	writeCursorRegistration(img, cursorCodeOff+0x100, cursorGetterOff+0x100, false)
	r = newCursorReader(t, img, ranges, &obj, &lines)
	if !r.disabled {
		t.Fatal("ambiguous getters did not latch the reader off")
	}
	// An empty image bound is rejected.
	r = &cursorHiddenReader{}
	r.init(ranges, 0, 0)
	if !r.disabled {
		t.Fatal("bad bounds did not latch the reader off")
	}
}

func TestCursorHiddenTrace(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "1")
	img, ranges := cursorFixture(t)
	obj := img.addr(cursorObjectOff)
	var lines []string
	r := newCursorReader(t, img, ranges, &obj, &lines)
	read := func(b byte) {
		img.mem[cursorObjectOff+cursorByteOff] = b
		r.read()
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "getter found") {
		t.Fatalf("init trace = %q", lines)
	}
	read(0)
	read(0) // repeat: no new line
	read(1)
	obj = 0
	read(1)
	if len(lines) != 4 {
		t.Fatalf("want init + 3 transitions, got %d: %q", len(lines), lines)
	}
	for i, want := range []string{"none -> hidden", "hidden -> visible", "visible -> unavailable"} {
		if !strings.Contains(lines[i+1], want) {
			t.Fatalf("line %d = %q, want %q", i+1, lines[i+1], want)
		}
	}
	obj = img.addr(cursorObjectOff)
	read(9)
	if len(lines) != 5 || !strings.Contains(lines[4], "not a boolean") {
		t.Fatalf("latch trace = %q", lines)
	}
	read(1)
	if len(lines) != 5 {
		t.Fatalf("latched reader logged again: %q", lines)
	}
}

const (
	proofCursorRegOff    = 0x0400
	proofBehaviorRegOff  = 0x0500
	proofCursorGetOff    = 0x0600
	proofBehaviorGetOff  = 0x0700
	proofResolverOff     = 0x0800
	proofAltResolverOff  = 0x0840
	proofCursorNameOff   = 0x0900
	proofBehaviorNameOff = 0x0940
	proofCursorDescOff   = 0x0a00
	proofBehaviorDescOff = 0x0b00
)

func writeResolverGetterBody(img *fakeEngineImage, getter, resolver int, opcode byte, disp uint32) {
	p := img.mem[getter:]
	copy(p, cursorGetterBody)
	p[21] = opcode
	binary.LittleEndian.PutUint32(p[12:], uint32(int32(resolver-(getter+16))))
	binary.LittleEndian.PutUint32(p[23:], disp)
}

func writeEnumPropertyRegistration(img *fakeEngineImage, code, desc, name, getter int) {
	p := img.mem[code:]
	copy(p, []byte{
		0x48, 0x8d, 0x3d, 0, 0, 0, 0,
		0x48, 0x8d, 0x0d, 0, 0, 0, 0,
		0x4c, 0x8d, 0x05, 0, 0, 0, 0,
		0x48, 0x89, 0xde, 0x4c, 0x89, 0xf2, 0xe8, 0, 0, 0, 0,
		0x4c, 0x89, 0x35, 0, 0, 0, 0,
		0x48, 0x8d, 0x05, 0, 0, 0, 0,
		0x48, 0x89, 0x05, 0, 0, 0, 0,
		0xbf, 0x28, 0x00, 0x00, 0x00, 0xe8, 0, 0, 0, 0,
		0x48, 0x8d, 0x0d, 0, 0, 0, 0,
		0x48, 0x89, 0x08,
		0x48, 0x8d, 0x0d, 0, 0, 0, 0,
		0x48, 0x89, 0x48, 0x08,
	})
	for _, ref := range []struct{ off, target int }{{0, desc}, {7, name}, {14, name}, {73, getter}} {
		binary.LittleEndian.PutUint32(p[ref.off+3:], uint32(int32(ref.target-(code+ref.off+7))))
	}
}

func writeFourLeaPropertyRegistration(img *fakeEngineImage, code, desc, name, getter int) {
	p := img.mem[code:]
	copy(p, []byte{0x48, 0x8d, 0x3d, 0, 0, 0, 0, 0x48, 0x8d, 0x35, 0, 0, 0, 0, 0x48, 0x8d, 0x15, 0, 0, 0, 0, 0x48, 0x8d, 0x0d, 0, 0, 0, 0, 0x4c, 0x8d, 0x05, 0, 0, 0, 0, 0x45, 0x31, 0xc9, 0xe8, 0, 0, 0, 0})
	for _, ref := range []struct{ off, target int }{{0, desc}, {7, name}, {14, name}, {21, getter}, {28, getter}} {
		binary.LittleEndian.PutUint32(p[ref.off+3:], uint32(int32(ref.target-(code+ref.off+7))))
	}
}

func cursorProofFixture(t *testing.T, behaviorResolver int, behaviorDisp uint32, enumReg bool) (*fakeEngineImage, []engineAddrRange) {
	t.Helper()
	img := newFakeEngineImage(t)
	useFakeEngineMemory(t, img)
	img.writeDelegatingGetter(0xe8)
	img.writeAccessor(3)
	wireFakeEngineTarget(t, img)

	copy(img.mem[proofCursorNameOff:], "MouseIconEnabled\x00")
	copy(img.mem[proofBehaviorNameOff:], "MouseBehavior\x00")
	writeResolverGetterBody(img, proofCursorGetOff, proofResolverOff, 0x8a, cursorByteOff)
	writeResolverGetterBody(img, proofBehaviorGetOff, behaviorResolver, 0x8b, behaviorDisp)
	writeFourLeaPropertyRegistration(img, proofCursorRegOff, proofCursorDescOff, proofCursorNameOff, proofCursorGetOff)
	if enumReg {
		writeEnumPropertyRegistration(img, proofBehaviorRegOff, proofBehaviorDescOff, proofBehaviorNameOff, proofBehaviorGetOff)
	} else {
		writeFourLeaPropertyRegistration(img, proofBehaviorRegOff, proofBehaviorDescOff, proofBehaviorNameOff, proofBehaviorGetOff)
	}
	return img, []engineAddrRange{{img.addr(0), img.addr(fakeImageSize)}}
}

func TestCursorHiddenSharedResolverProofPass(t *testing.T) {
	for _, enumReg := range []bool{true, false} {
		img, ranges := cursorProofFixture(t, proofResolverOff, 0x88, enumReg)
		r := &cursorHiddenReader{}
		r.init(ranges, img.addr(0), fakeImageSize)
		if r.disabled || r.resolve == nil {
			t.Fatalf("enumReg=%v: expected proven resolver, got disabled=%v resolve=%v", enumReg, r.disabled, r.resolve != nil)
		}
		if r.candidate.resolver != img.addr(proofResolverOff) || r.behaviorCandidate.resolver != img.addr(proofResolverOff) {
			t.Fatalf("enumReg=%v: resolvers = %#x vs %#x", enumReg, r.candidate.resolver, r.behaviorCandidate.resolver)
		}
		if r.enumOffset != 0x88 || r.candidate.offset != cursorByteOff {
			t.Fatalf("enumReg=%v: offsets cursor=%#x behavior=%#x", enumReg, r.candidate.offset, r.enumOffset)
		}
	}
}

// writeCursorBuildID adds a mapped ELF GNU build-id note to a fake image.
func writeCursorBuildID(img *fakeEngineImage, id []byte) {
	header := img.mem[:64]
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	binary.LittleEndian.PutUint64(header[32:], 64)
	binary.LittleEndian.PutUint16(header[54:], 56)
	binary.LittleEndian.PutUint16(header[56:], 1)
	ph := img.mem[64:120]
	binary.LittleEndian.PutUint32(ph, 4) // PT_NOTE
	binary.LittleEndian.PutUint64(ph[16:], 0x80)
	noteSize := 16 + (len(id)+3)&^3
	binary.LittleEndian.PutUint64(ph[40:], uint64(noteSize))
	note := img.mem[0x80 : 0x80+noteSize]
	binary.LittleEndian.PutUint32(note, 4)
	binary.LittleEndian.PutUint32(note[4:], uint32(len(id)))
	binary.LittleEndian.PutUint32(note[8:], 3) // NT_GNU_BUILD_ID
	copy(note[12:], "GNU\x00")
	copy(note[16:], id)
}

type cursorChunkCountingMemory struct {
	img    *fakeEngineImage
	chunks int
}

func (m *cursorChunkCountingMemory) bytes(addr uintptr, n int) ([]byte, bool) {
	return m.img.bytes(addr, n)
}
func (m *cursorChunkCountingMemory) u64(addr uintptr) (uint64, bool) {
	return m.img.u64(addr)
}
func (m *cursorChunkCountingMemory) u32(addr uintptr) (uint32, bool) {
	return m.img.u32(addr)
}
func (m *cursorChunkCountingMemory) read(addr uintptr, out []byte) bool {
	off, ok := m.img.offset(addr)
	if !ok || len(out) == 0 || off+len(out) > len(m.img.mem) {
		return false
	}
	m.chunks++
	copy(out, m.img.mem[off:off+len(out)])
	return true
}

func useCursorChunkCounter(t *testing.T, img *fakeEngineImage) *cursorChunkCountingMemory {
	t.Helper()
	m := &cursorChunkCountingMemory{img: img}
	old := engineMem
	engineMem = m
	t.Cleanup(func() { engineMem = old })
	return m
}

func TestCursorHiddenSharedResolverMismatch(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "1")
	img, ranges := cursorProofFixture(t, proofAltResolverOff, 0x88, true)
	binary.LittleEndian.PutUint64(img.mem[fakeSingletonB+0x10b8:], uint64(img.addr(cursorObjectOff)))
	img.mem[cursorObjectOff+cursorByteOff] = 0
	var lines []string
	r := &cursorHiddenReader{logf: func(msg string, _ ...any) { lines = append(lines, msg) }}
	r.init(ranges, img.addr(0), fakeImageSize)
	if r.resolve != nil {
		t.Fatal("mismatched resolver targets must leave resolve nil")
	}
	if hidden, ok := r.read(); hidden || ok {
		t.Fatalf("resolver mismatch must return ok=false: hidden=%v ok=%v", hidden, ok)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "resolver mismatch") {
		t.Fatalf("unexpected trace: %q", lines)
	}
}

func TestCursorHiddenMouseServiceProofRequired(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "1")
	// Different objects require the MouseService identity proof; matching only
	// the shared resolver is not sufficient.
	img, ranges := cursorProofFixture(t, proofResolverOff, 0x1f4, true)
	binary.LittleEndian.PutUint64(img.mem[fakeSingletonB+0x10b8:], uint64(img.addr(cursorObjectOff)))
	img.mem[cursorObjectOff+cursorByteOff] = 0
	var lines []string
	r := &cursorHiddenReader{logf: func(msg string, _ ...any) { lines = append(lines, msg) }}
	r.init(ranges, img.addr(0), fakeImageSize)
	if r.resolve != nil {
		t.Fatal("missing MouseService identity proof must leave resolve nil")
	}
	if hidden, ok := r.read(); hidden || ok {
		t.Fatalf("unproven MouseService chain must return ok=false: hidden=%v ok=%v", hidden, ok)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "MouseService identity proof failed") {
		t.Fatalf("unexpected trace: %q", lines)
	}
}

func TestCursorHiddenSharedResolverRuntimeGateAndTransitions(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "1")
	img, ranges := cursorProofFixture(t, proofResolverOff, 0x88, true)
	slot := fakeSingletonB + 0x10b8
	setObj := func(addr uintptr) {
		binary.LittleEndian.PutUint64(img.mem[slot:], uint64(addr))
	}
	setBehavior := func(v uint32) {
		binary.LittleEndian.PutUint32(img.mem[cursorObjectOff+0x88:], v)
	}
	setIcon := func(v byte) {
		img.mem[cursorObjectOff+cursorByteOff] = v
	}

	var lines []string
	r := &cursorHiddenReader{logf: func(msg string, _ ...any) { lines = append(lines, msg) }}
	r.init(ranges, img.addr(0), fakeImageSize)
	if r.resolve == nil || r.disabled {
		t.Fatalf("proof failed: %+v", r)
	}

	// NULL object in Home / pre-game returns ok=false without latching.
	setObj(0)
	if hidden, ok := r.read(); hidden || ok || r.disabled {
		t.Fatalf("NULL object: hidden=%v ok=%v disabled=%v", hidden, ok, r.disabled)
	}

	// Object present, MouseBehavior in domain, icon byte 0 -> hidden.
	setObj(img.addr(cursorObjectOff))
	setBehavior(uint32(MouseBehaviorLockCurrentPosition))
	setIcon(0)
	if hidden, ok := r.read(); !hidden || !ok {
		t.Fatalf("icon 0: hidden=%v ok=%v", hidden, ok)
	}

	// Transition to visible (icon byte 1).
	setIcon(1)
	if hidden, ok := r.read(); hidden || !ok {
		t.Fatalf("icon 1: hidden=%v ok=%v", hidden, ok)
	}

	// Out-of-domain MouseBehavior value latches the reader off.
	setBehavior(3)
	if hidden, ok := r.read(); hidden || ok || !r.disabled {
		t.Fatalf("out-of-domain MouseBehavior: hidden=%v ok=%v disabled=%v", hidden, ok, r.disabled)
	}
	// Restoring valid values cannot unlatch the reader.
	setBehavior(0)
	setIcon(0)
	if hidden, ok := r.read(); hidden || ok {
		t.Fatalf("latched reader answered: hidden=%v ok=%v", hidden, ok)
	}

	wantSubstrings := []string{
		"resolver proven",
		"none -> unavailable",
		"unavailable -> hidden (MouseIconEnabled byte 0)",
		"hidden -> visible (MouseIconEnabled byte 1)",
		"not an Enum.MouseBehavior value",
	}
	if len(lines) != len(wantSubstrings) {
		t.Fatalf("got %d trace lines %q, want %d", len(lines), lines, len(wantSubstrings))
	}
	for i, want := range wantSubstrings {
		if !strings.Contains(lines[i], want) {
			t.Fatalf("trace[%d] = %q, want %q", i, lines[i], want)
		}
	}
}

const (
	chainCreateOff       = 0x0c00
	chainIndexWrapOff    = 0x0d00
	chainIndexFnOff      = 0x0e00
	chainVectorFnOff     = 0x0f00
	chainGetGameFnOff    = 0x1100
	chainBridgeOff       = 0x1200
	chainCallerOff       = chainBridgeOff - 70
	chainGameOffset      = 0x80
	chainControlOffset   = chainGameOffset + 8
	chainProviderOffset  = 0x100
	chainVectorOffset    = 0x18
	chainGameObjectOff   = 0x6800
	chainControlBlockOff = 0x6a00
	chainIndexGuardOff   = 0x6a10
	chainServiceIndexOff = 0x6a18
	chainVectorOff       = 0x6b00
	chainBehaviorOffset  = 0x1f4
)

func writeCursorRel32(img *fakeEngineImage, at, target int, opcode byte) {
	img.mem[at] = opcode
	binary.LittleEndian.PutUint32(img.mem[at+1:], uint32(int32(target-(at+5))))
}

func writeCursorRIP32(img *fakeEngineImage, at, instructionLen, target int) {
	binary.LittleEndian.PutUint32(img.mem[at+instructionLen-4:], uint32(int32(target-(at+instructionLen))))
}

// writeCursorMouseServiceChain lays down every instruction shape and runtime
// pointer in the proven UserInputService -> MouseService path.
func writeCursorMouseServiceChain(img *fakeEngineImage) {
	resolver := make([]byte, 31)
	copy(resolver, []byte{0x55, 0x48, 0x89, 0xe5, 0xe8, 0, 0, 0, 0,
		0x48, 0x85, 0xc0, 0x74, 0x0d, 0x48, 0x89, 0xc7, 0xe8, 0, 0, 0, 0,
		0x48, 0x8b, 0x00, 0xeb, 0x02, 0x31, 0xc0, 0x5d, 0xc3})
	copy(img.mem[proofResolverOff:], resolver)
	writeCursorRel32(img, proofResolverOff+17, chainCreateOff, 0xe8)

	create := make([]byte, 49)
	copy(img.mem[chainCreateOff:], create)
	writeCursorRel32(img, chainCreateOff+33, chainIndexWrapOff, 0xe8)
	writeCursorRel32(img, chainCreateOff+44, chainVectorFnOff, 0xe8)

	wrapper := make([]byte, 27)
	copy(img.mem[chainIndexWrapOff:], wrapper)
	writeCursorRel32(img, chainIndexWrapOff+22, chainIndexFnOff, 0xe9)

	index := make([]byte, 0x60)
	copy(index[4:], []byte{0x8a, 0x05})
	copy(index[16:], []byte{0x48, 0x8b, 0x05})
	copy(img.mem[chainIndexFnOff:], index)
	writeCursorRIP32(img, chainIndexFnOff+4, 6, chainIndexGuardOff)
	writeCursorRIP32(img, chainIndexFnOff+16, 7, chainServiceIndexOff)

	vector := make([]byte, 47)
	copy(vector[33:], []byte{0x48, 0x8b, 0x87})
	binary.LittleEndian.PutUint32(vector[36:], chainVectorOffset)
	copy(vector[40:], []byte{0x48, 0x8b, 0x8f})
	binary.LittleEndian.PutUint32(vector[43:], chainVectorOffset+8)
	copy(img.mem[chainVectorFnOff:], vector)

	getGame := make([]byte, 42)
	copy(getGame[10:], []byte{0x48, 0x8b, 0xbf})
	binary.LittleEndian.PutUint32(getGame[13:], chainControlOffset)
	copy(getGame[35:], []byte{0x48, 0x8b, 0x9b})
	binary.LittleEndian.PutUint32(getGame[38:], chainGameOffset)
	copy(img.mem[chainGetGameFnOff:], getGame)

	bridge := make([]byte, 50)
	copy(bridge, []byte{0x55, 0x48, 0x89, 0xe5, 0xbf, 4, 0, 0, 0, 0xe8,
		0, 0, 0, 0, 0x48, 0x89, 0xc7, 0xe8, 0, 0, 0, 0,
		0x48, 0x85, 0xc0, 0x74, 0x13, 0x48, 0x05, 0, 0, 0, 0,
		0x48, 0x89, 0xc7, 0xe8, 0, 0, 0, 0,
		0x48, 0x8b, 0x00, 0xeb, 0x02, 0x31, 0xc0, 0x5d, 0xc3})
	binary.LittleEndian.PutUint32(bridge[29:], chainProviderOffset)
	copy(img.mem[chainBridgeOff:], bridge)
	writeCursorRel32(img, chainBridgeOff+9, fakeAccessorOff, 0xe8)
	writeCursorRel32(img, chainBridgeOff+17, chainGetGameFnOff, 0xe8)
	writeCursorRel32(img, chainBridgeOff+36, chainCreateOff, 0xe8)

	caller := make([]byte, 70)
	copy(caller[14:], []byte{0x8b, 0x88})
	binary.LittleEndian.PutUint32(caller[16:], chainBehaviorOffset)
	copy(caller[42:], []byte{0x0f, 0xb6, 0x80})
	binary.LittleEndian.PutUint32(caller[45:], cursorByteOff)
	copy(img.mem[chainCallerOff:], caller)
	writeCursorRel32(img, chainCallerOff+4, chainBridgeOff, 0xe8)

	put64 := func(off int, value uintptr) {
		binary.LittleEndian.PutUint64(img.mem[off:], uint64(value))
	}
	put64(fakeSingletonB+chainGameOffset, img.addr(chainGameObjectOff))
	put64(fakeSingletonB+chainControlOffset, img.addr(chainControlBlockOff))
	put64(chainControlBlockOff+8, 1)
	img.mem[chainIndexGuardOff] = 1
	put64(chainServiceIndexOff, 0)
	provider := chainGameObjectOff + chainProviderOffset
	put64(provider+chainVectorOffset, img.addr(chainVectorOff))
	put64(provider+chainVectorOffset+8, img.addr(chainVectorOff+16))
	put64(chainVectorOff, img.addr(cursorObjectOff))
	binary.LittleEndian.PutUint32(img.mem[cursorObjectOff+chainBehaviorOffset:], uint32(MouseBehaviorDefault))
	img.mem[cursorObjectOff+cursorByteOff] = 0
}

func cursorMouseServiceChainFixture(t *testing.T) (*fakeEngineImage, []engineAddrRange) {
	t.Helper()
	img, ranges := cursorProofFixture(t, proofResolverOff, chainBehaviorOffset, true)
	writeCursorMouseServiceChain(img)
	return img, ranges
}

func TestCursorHiddenMouseServiceChain(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CURSOR_STATE", "")
	img, ranges := cursorMouseServiceChainFixture(t)
	r := &cursorHiddenReader{}
	r.init(ranges, img.addr(0), fakeImageSize)
	if r.disabled || r.resolve == nil {
		t.Fatalf("MouseService proof failed: disabled=%v resolve=%v", r.disabled, r.resolve != nil)
	}
	if r.enumOffset != chainBehaviorOffset {
		t.Fatalf("MouseBehavior offset = %#x, want reflection offset %#x", r.enumOffset, chainBehaviorOffset)
	}
	if hidden, ok := r.read(); !hidden || !ok {
		t.Fatalf("MouseIconEnabled 0: hidden=%v ok=%v", hidden, ok)
	}
	img.mem[cursorObjectOff+cursorByteOff] = 1
	if hidden, ok := r.read(); hidden || !ok {
		t.Fatalf("MouseIconEnabled 1: hidden=%v ok=%v", hidden, ok)
	}
	binary.LittleEndian.PutUint32(img.mem[cursorObjectOff+chainBehaviorOffset:], 3)
	if hidden, ok := r.read(); hidden || ok || !r.disabled {
		t.Fatalf("invalid MouseBehavior: hidden=%v ok=%v disabled=%v", hidden, ok, r.disabled)
	}
}

func TestCursorHiddenMouseServiceIdentityMismatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeEngineImage)
	}{
		{"resolver shape", func(img *fakeEngineImage) { img.mem[proofResolverOff+22] = 0 }},
		{"bridge create target", func(img *fakeEngineImage) {
			writeCursorRel32(img, chainBridgeOff+36, chainVectorFnOff, 0xe8)
		}},
		{"caller behavior field", func(img *fakeEngineImage) {
			binary.LittleEndian.PutUint32(img.mem[chainCallerOff+16:], chainBehaviorOffset+4)
		}},
		{"caller icon field", func(img *fakeEngineImage) {
			binary.LittleEndian.PutUint32(img.mem[chainCallerOff+45:], cursorByteOff+1)
		}},
		{"getGame weak pointer", func(img *fakeEngineImage) {
			binary.LittleEndian.PutUint32(img.mem[chainGetGameFnOff+13:], chainControlOffset+8)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TIPSY_TRACE_CURSOR_STATE", "")
			img, ranges := cursorMouseServiceChainFixture(t)
			tc.mutate(img)
			r := &cursorHiddenReader{}
			r.init(ranges, img.addr(0), fakeImageSize)
			if r.resolve != nil {
				t.Fatal("identity mismatch installed a resolver")
			}
			if hidden, ok := r.read(); hidden || ok {
				t.Fatalf("identity mismatch: hidden=%v ok=%v", hidden, ok)
			}
		})
	}
}

func TestCursorHiddenMouseServiceUnavailableDoesNotLatch(t *testing.T) {
	put64 := func(img *fakeEngineImage, off int, value uint64) {
		binary.LittleEndian.PutUint64(img.mem[off:], value)
	}
	tests := []struct {
		name   string
		mutate func(*fakeEngineImage)
	}{
		{"NULL Game", func(img *fakeEngineImage) { put64(img, fakeSingletonB+chainGameOffset, 0) }},
		{"NULL control block", func(img *fakeEngineImage) { put64(img, fakeSingletonB+chainControlOffset, 0) }},
		{"expired Game", func(img *fakeEngineImage) { put64(img, chainControlBlockOff+8, 0) }},
		{"uninitialized service index", func(img *fakeEngineImage) { img.mem[chainIndexGuardOff] = 0 }},
		{"service index out of bounds", func(img *fakeEngineImage) { put64(img, chainServiceIndexOff, 1) }},
		{"NULL MouseService", func(img *fakeEngineImage) { put64(img, chainVectorOff, 0) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img, ranges := cursorMouseServiceChainFixture(t)
			tc.mutate(img)
			r := &cursorHiddenReader{}
			r.init(ranges, img.addr(0), fakeImageSize)
			if hidden, ok := r.read(); hidden || ok || r.disabled {
				t.Fatalf("hidden=%v ok=%v disabled=%v", hidden, ok, r.disabled)
			}
		})
	}
}

func TestCursorHiddenMouseServiceUnreadableHopLatchesOff(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fakeEngineImage)
	}{
		{"control block", func(img *fakeEngineImage) {
			binary.LittleEndian.PutUint64(img.mem[fakeSingletonB+chainControlOffset:], uint64(img.addr(fakeImageSize+0x1000)))
		}},
		{"MouseService object", func(img *fakeEngineImage) {
			binary.LittleEndian.PutUint64(img.mem[chainVectorOff:], uint64(img.addr(fakeImageSize+0x1000)))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img, ranges := cursorMouseServiceChainFixture(t)
			tc.mutate(img)
			r := &cursorHiddenReader{}
			r.init(ranges, img.addr(0), fakeImageSize)
			if hidden, ok := r.read(); hidden || ok || !r.disabled {
				t.Fatalf("hidden=%v ok=%v disabled=%v", hidden, ok, r.disabled)
			}
		})
	}
}
