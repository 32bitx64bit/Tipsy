// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// Synthetic-image layout for the read-only MouseBehavior authority. The
// offsets mirror the live client (object +0xb00, behavior +0x88) but nothing
// here is pinned to it: every constant is re-derived by the decoder from the
// bytes these helpers lay down, which is exactly what the decode is for.
const (
	fakeGetterOff   = 0x1000
	fakeAccessorOff = 0x2000
	fakeSingletonA  = 0x3000
	fakeSingletonB  = 0x4000
	fakeObjectOff   = 0x5000
	fakeImageSize   = 0x8000
)

// fakeGetterShape is the getter body the decoder expects, as data.
type fakeGetterShape struct {
	index      uint32 // the getSingleton index in `mov $imm,%edi`
	objOffset  uint32 // `mov disp32(%rbx),%rax`
	enumOffset uint32 // `cmpl $imm8,disp32(%rax)`
	locked     uint8  // the immediate the boolean compares against
}

var fakeLiveShape = fakeGetterShape{
	index:      4,
	objOffset:  0xb00,
	enumOffset: 0x88,
	locked:     uint8(MouseBehaviorLockCenter),
}

// fakeEngineImage is one readable allocation standing in for the engine's
// mapped .text and .bss. It is a real address in this process, so
// engineReadable's /proc/self/maps guard accepts it exactly as it accepts the
// live client's mappings, and every decode and fail-closed path is exercised
// without a live engine.
type fakeEngineImage struct {
	base unsafe.Pointer
	mem  []byte
}

func newFakeEngineImage(t *testing.T) *fakeEngineImage {
	t.Helper()
	mem := make([]byte, fakeImageSize)
	// img.mem holds the backing array for the life of the image, so the
	// addresses img.addr returns stay valid and readable.
	return &fakeEngineImage{base: unsafe.Pointer(&mem[0]), mem: mem}
}

func (img *fakeEngineImage) addr(off int) uintptr {
	return uintptr(img.base) + uintptr(off)
}

func (img *fakeEngineImage) offset(addr uintptr) (int, bool) {
	off := int(addr) - int(uintptr(img.base))
	if off < 0 || off > len(img.mem) {
		return 0, false
	}
	return off, true
}

func (img *fakeEngineImage) bytes(addr uintptr, n int) ([]byte, bool) {
	off, ok := img.offset(addr)
	if !ok || n <= 0 || off+n > len(img.mem) {
		return nil, false
	}
	out := make([]byte, n)
	copy(out, img.mem[off:off+n])
	return out, true
}

func (img *fakeEngineImage) u64(addr uintptr) (uint64, bool) {
	if addr%8 != 0 {
		return 0, false
	}
	b, ok := img.bytes(addr, 8)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b), true
}

func (img *fakeEngineImage) u32(addr uintptr) (uint32, bool) {
	if addr%4 != 0 {
		return 0, false
	}
	b, ok := img.bytes(addr, 4)
	if !ok {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b), true
}

// writeGetter lays down the clang shape byte for byte.
func (img *fakeEngineImage) writeGetter(s fakeGetterShape) {
	g := img.mem[fakeGetterOff:]
	g[0] = 0xBF // mov $imm32,%edi
	binary.LittleEndian.PutUint32(g[1:], s.index)
	g[5] = 0xE8 // call rel32
	binary.LittleEndian.PutUint32(g[6:], uint32(int32(fakeAccessorOff-(fakeGetterOff+10))))
	copy(g[10:], []byte{0x48, 0x8B, 0x83}) // mov disp32(%rbx),%rax
	binary.LittleEndian.PutUint32(g[13:], s.objOffset)
	copy(g[17:], []byte{0x83, 0xB8}) // cmpl $imm8,disp32(%rax)
	binary.LittleEndian.PutUint32(g[19:], s.enumOffset)
	g[23] = s.locked
}

// writeAccessor lays down clang's `cmp/lea/lea/cmove` static pair.
func (img *fakeEngineImage) writeAccessor(pivot uint8) {
	a := img.mem[fakeAccessorOff:]
	copy(a[0:], []byte{0x83, 0xFB, pivot}) // cmp $imm8,%ebx
	copy(a[3:], []byte{0x48, 0x8D, 0x0D})  // lea A(%rip),%rcx
	binary.LittleEndian.PutUint32(a[6:], uint32(int32(fakeSingletonA-(fakeAccessorOff+10))))
	copy(a[10:], []byte{0x48, 0x8D, 0x05}) // lea B(%rip),%rax
	binary.LittleEndian.PutUint32(a[13:], uint32(int32(fakeSingletonB-(fakeAccessorOff+17))))
	copy(a[17:], []byte{0x48, 0x0F, 0x44, 0xC1}) // cmove %rcx,%rax
}

// setSingleton writes the object pointer the chosen static holds. The static
// itself is a .bss slot, so the pointer lives at static + objOffset.
func (img *fakeEngineImage) setSingleton(which int, obj uintptr) {
	binary.LittleEndian.PutUint64(img.mem[which+int(fakeLiveShape.objOffset):], uint64(obj))
}

// setBehavior writes the MouseBehavior word inside the object.
func (img *fakeEngineImage) setBehavior(value uint32) {
	binary.LittleEndian.PutUint32(img.mem[fakeObjectOff+fakeLiveShape.enumOffset:], value)
}

// writeWildAccessor lays down the static-pair shape but points both statics
// far outside the image, so the decoded singleton address is not inside any
// readable mapping of this process.
func (img *fakeEngineImage) writeWildAccessor(pivot uint8) {
	a := img.mem[fakeAccessorOff:]
	copy(a[0:], []byte{0x83, 0xFB, pivot})
	copy(a[3:], []byte{0x48, 0x8D, 0x0D})
	binary.LittleEndian.PutUint32(a[6:], uint32(int32(0x40000000)))
	copy(a[10:], []byte{0x48, 0x8D, 0x05})
	binary.LittleEndian.PutUint32(a[13:], uint32(int32(0x40000000)))
	copy(a[17:], []byte{0x48, 0x0F, 0x44, 0xC1})
}

// useFakeEngineMemory substitutes the synthetic image for the live engine
// memory for one test.
func useFakeEngineMemory(t *testing.T, img *fakeEngineImage) {
	t.Helper()
	old := engineMem
	engineMem = img
	t.Cleanup(func() { engineMem = old })
}

// wireFakeEngineTarget points the direct target's exported lock getter at the
// synthetic image, which is the only symbol EngineMouseBehavior needs. Only
// that address is ever dereferenced: no recording native is involved, so
// RobloxMainWindowMouseLocked must not be called on this target.
func wireFakeEngineTarget(t *testing.T, img *fakeEngineImage) {
	t.Helper()
	installEngineMouseBehaviorProbe(t, EngineMouseBehavior)
	resetEngineMouseBehavior()
	getter := img.addr(fakeGetterOff)
	if !SetRobloxDirectInputTarget(0x1234, 0x5678, getter, getter, getter, getter) {
		t.Fatal("fake engine target did not wire")
	}
	t.Cleanup(func() {
		ClearRobloxDirectInputTarget()
		resetEngineMouseBehavior()
	})
}

// buildLiveShape lays down the live client's measured shape and returns the
// image ready for a decode.
func buildLiveShape(t *testing.T, pivot uint8) *fakeEngineImage {
	t.Helper()
	img := newFakeEngineImage(t)
	img.writeGetter(fakeLiveShape)
	img.writeAccessor(pivot)
	return img
}

// TestDecodeMouseBehaviorGetterBothSingletonBranches pins the accessor's
// cmove: index == pivot selects A, anything else selects B. The live client
// passes index 4 against pivot 3, so the second branch is the one that
// matters, but both are part of the shape and a decoder that only ever
// returned one of them would be guessing.
func TestDecodeMouseBehaviorGetterBothSingletonBranches(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index uint32
		pivot uint8
		want  int
	}{
		{"index equals pivot selects A", 3, 3, fakeSingletonA},
		{"index differs from pivot selects B", 4, 3, fakeSingletonB},
		{"index zero differs from pivot selects B", 0, 3, fakeSingletonB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := buildLiveShape(t, tc.pivot)
			img.writeGetter(fakeGetterShape{
				index: tc.index, objOffset: fakeLiveShape.objOffset,
				enumOffset: fakeLiveShape.enumOffset, locked: fakeLiveShape.locked,
			})
			useFakeEngineMemory(t, img)
			layout, err := decodeMouseBehavior(img.addr(fakeGetterOff))
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			if layout.singleton != img.addr(tc.want) {
				t.Fatalf("singleton=%#x, want %#x", layout.singleton, img.addr(tc.want))
			}
			if layout.pivot != tc.pivot {
				t.Fatalf("pivot=%d, want %d", layout.pivot, tc.pivot)
			}
			if layout.index != tc.index {
				t.Fatalf("index=%d, want %d", layout.index, tc.index)
			}
			if layout.objOffset != fakeLiveShape.objOffset ||
				layout.enumOffset != fakeLiveShape.enumOffset ||
				layout.lockedValue != fakeLiveShape.locked {
				t.Fatalf("layout=%+v, want the shape it was built from", layout)
			}
			if layout.accessor != img.addr(fakeAccessorOff) {
				t.Fatalf("accessor=%#x, want %#x", layout.accessor, img.addr(fakeAccessorOff))
			}
		})
	}
}

// TestDecodeMouseBehaviorGetterFailsClosed pins every rejection. A decode
// that guesses instead of failing would read the wrong field of the right
// object and return plausible garbage, which is worse than no answer at all.
func TestDecodeMouseBehaviorGetterFailsClosed(t *testing.T) {
	cases := []struct {
		name  string
		shape fakeGetterShape
	}{
		{"misaligned object offset", fakeGetterShape{4, 0xb01, 0x88, 1}},
		{"misaligned behavior offset", fakeGetterShape{4, 0xb00, 0x86, 1}},
		{"implausible object offset", fakeGetterShape{4, mouseBehaviorMaxOffset + 8, 0x88, 1}},
		{"implausible behavior offset", fakeGetterShape{4, 0xb00, mouseBehaviorMaxOffset + 4, 1}},
		{"zero object offset", fakeGetterShape{4, 0, 0x88, 1}},
		{"zero behavior offset", fakeGetterShape{4, 0xb00, 0, 1}},
		{"compare is not LockCenter", fakeGetterShape{4, 0xb00, 0x88, 2}},
		{"compare is zero", fakeGetterShape{4, 0xb00, 0x88, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := buildLiveShape(t, 3)
			img.writeGetter(tc.shape)
			useFakeEngineMemory(t, img)
			if _, err := decodeMouseBehavior(img.addr(fakeGetterOff)); err == nil {
				t.Fatal("decode accepted a shape it must reject")
			}
		})
	}
}

// TestDecodeMouseBehaviorRejectsMissingInstructions pins the three "no
// instruction found" failures: without them a body that merely resembles the
// getter would decode into offsets that are not the getter's.
func TestDecodeMouseBehaviorRejectsMissingInstructions(t *testing.T) {
	t.Run("no singleton fetch", func(t *testing.T) {
		img := newFakeEngineImage(t)
		copy(img.mem[fakeGetterOff:], []byte{0x48, 0x8B, 0x83, 0, 0x0b, 0, 0, 0x83, 0xB8})
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehaviorGetter(img.mem[fakeGetterOff:fakeGetterOff+0x80], img.addr(fakeGetterOff)); err == nil {
			t.Fatal("accepted a body with no `mov $imm,edi ; call rel32`")
		}
	})
	t.Run("no object load", func(t *testing.T) {
		img := newFakeEngineImage(t)
		copy(img.mem[fakeGetterOff:], []byte{0xBF, 4, 0, 0, 0, 0xE8, 0, 0, 0, 0, 0x83, 0xB8})
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehaviorGetter(img.mem[fakeGetterOff:fakeGetterOff+0x80], img.addr(fakeGetterOff)); err == nil {
			t.Fatal("accepted a body with no `mov disp32(%%rbx),%%rax`")
		}
	})
	t.Run("no behavior compare", func(t *testing.T) {
		img := newFakeEngineImage(t)
		copy(img.mem[fakeGetterOff:], []byte{0xBF, 4, 0, 0, 0, 0xE8, 0, 0, 0, 0, 0x48, 0x8B, 0x83, 0, 0x0b, 0, 0})
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehaviorGetter(img.mem[fakeGetterOff:fakeGetterOff+0x80], img.addr(fakeGetterOff)); err == nil {
			t.Fatal("accepted a body with no `cmpl $imm8,disp32(%%rax)`")
		}
	})
}

// TestDecodeSingletonAccessorRejectsUnknownShape pins the accessor half: the
// two function-local statics are the whole reason index 4 needs no call and
// no breakpoint, so a body that is not that shape must not be walked.
func TestDecodeSingletonAccessorRejectsUnknownShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"empty", nil},
		{"cmp only", []byte{0x83, 0xFB, 3}},
		{"cmp and one lea", []byte{0x83, 0xFB, 3, 0x48, 0x8D, 0x0D, 0, 0, 0, 0}},
		{"wrong cmove", []byte{0x83, 0xFB, 3, 0x48, 0x8D, 0x0D, 0, 0, 0, 0,
			0x48, 0x8D, 0x05, 0, 0, 0, 0, 0x48, 0x0F, 0x45, 0xC1}},
		{"lea order swapped", []byte{0x83, 0xFB, 3, 0x48, 0x8D, 0x05, 0, 0, 0, 0,
			0x48, 0x8D, 0x0D, 0, 0, 0, 0, 0x48, 0x0F, 0x44, 0xC1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code := make([]byte, mouseBehaviorAccessorWindow)
			copy(code, tc.body)
			if _, _, err := decodeSingletonAccessor(code, 0x1000, 4); err == nil {
				t.Fatal("accepted an accessor body that is not the static pair")
			}
		})
	}
}

// TestDecodeMouseBehaviorRejectsUnreadableBodies pins the guard that keeps a
// mis-decode from dereferencing a wild address: an unreadable body is a
// decode error, never a crash.
func TestDecodeMouseBehaviorRejectsUnreadableBodies(t *testing.T) {
	t.Run("getter body unreadable", func(t *testing.T) {
		img := buildLiveShape(t, 3)
		useFakeEngineMemory(t, img)
		// An address outside the image has no readable bytes.
		if _, err := decodeMouseBehavior(img.addr(fakeImageSize + 0x1000)); err == nil {
			t.Fatal("decoded a getter body that is not readable")
		}
	})
	t.Run("accessor body unreadable", func(t *testing.T) {
		img := newFakeEngineImage(t)
		img.writeGetter(fakeLiveShape)
		// Point the call at an address whose 0x60-byte window falls outside
		// the image, so the getter decodes but the accessor cannot be read.
		outside := int32(fakeImageSize + 0x800)
		g := img.mem[fakeGetterOff:]
		binary.LittleEndian.PutUint32(g[6:], uint32(outside-(fakeGetterOff+10)))
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehavior(img.addr(fakeGetterOff)); err == nil {
			t.Fatal("decoded an accessor body that is not readable")
		}
	})
	t.Run("singleton slot outside a mapping", func(t *testing.T) {
		img := buildLiveShape(t, 3)
		// Point both statics far outside the image, so the decoded singleton
		// address is not inside any readable mapping of this process.
		img.writeWildAccessor(3)
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehavior(img.addr(fakeGetterOff)); err == nil {
			t.Fatal("accepted a singleton slot that is not in a readable mapping")
		}
	})
}

// TestEngineMouseBehaviorReadsTheEnumWord is the whole point of the
// authority: the value the exported boolean cannot see.
func TestEngineMouseBehaviorReadsTheEnumWord(t *testing.T) {
	for _, value := range []uint32{0, 1, 2} {
		t.Run(MouseBehavior(value).String(), func(t *testing.T) {
			img := buildLiveShape(t, 3)
			img.setSingleton(fakeSingletonB, img.addr(fakeObjectOff))
			img.setBehavior(value)
			useFakeEngineMemory(t, img)
			wireFakeEngineTarget(t, img)

			got, ok := EngineMouseBehavior()
			if !ok {
				t.Fatal("EngineMouseBehavior reported unavailable on a readable image")
			}
			if got != MouseBehavior(value) {
				t.Fatalf("behavior=%v, want %v", got, MouseBehavior(value))
			}
			if !engineBehaviorAuthoritative() {
				t.Fatal("a valid read did not make the authority authoritative")
			}
		})
	}
}

// TestEngineMouseBehaviorNULLObjectIsUnavailable pins the pre-onGameLoaded
// window: the object pointer appears about 0.2 s after the pointer lock
// handshake, and a dead read must never be reported as Default.
func TestEngineMouseBehaviorNULLObjectIsUnavailable(t *testing.T) {
	img := buildLiveShape(t, 3)
	// setSingleton is deliberately not called: the static holds NULL.
	useFakeEngineMemory(t, img)
	wireFakeEngineTarget(t, img)

	got, ok := EngineMouseBehavior()
	if ok {
		t.Fatalf("behavior=%v with ok=true, want unavailable", got)
	}
	if got != MouseBehaviorDefault {
		t.Fatalf("behavior=%v, want the Default zero value with ok=false", got)
	}
	if engineBehaviorAuthoritative() {
		t.Fatal("a NULL object must not make the authority authoritative")
	}
}

// TestEngineMouseBehaviorOutOfRangeDisablesAuthority pins fail-closed: a word
// that is not an Enum.MouseBehavior value disables the read for the rest of
// the process rather than being reinterpreted.
func TestEngineMouseBehaviorOutOfRangeDisablesAuthority(t *testing.T) {
	img := buildLiveShape(t, 3)
	img.setSingleton(fakeSingletonB, img.addr(fakeObjectOff))
	img.setBehavior(7)
	useFakeEngineMemory(t, img)
	wireFakeEngineTarget(t, img)

	if got, ok := EngineMouseBehavior(); ok || got != MouseBehaviorDefault {
		t.Fatalf("behavior=(%v,%t), want (Default,false)", got, ok)
	}
	if !mouseBehaviorAuthority.disabled.Load() {
		t.Fatal("an out-of-range value did not disable the authority")
	}
	// Disabled is latched: even a good word afterwards stays unavailable.
	img.setBehavior(uint32(MouseBehaviorLockCenter))
	if got, ok := EngineMouseBehavior(); ok {
		t.Fatalf("behavior=(%v,%t) after disable, want unavailable", got, ok)
	}
}

// TestEngineMouseBehaviorValidatorIsPolicedByTheExportedBoolean is the
// exception's safety argument. The engine's own boolean is the reference:
// agreement resets the counter, three consecutive disagreements disable the
// authority, and one straddled transition is tolerated.
func TestEngineMouseBehaviorValidatorIsPolicedByTheExportedBoolean(t *testing.T) {
	img := buildLiveShape(t, 3)
	img.setSingleton(fakeSingletonB, img.addr(fakeObjectOff))
	img.setBehavior(uint32(MouseBehaviorLockCurrentPosition))
	useFakeEngineMemory(t, img)
	wireFakeEngineTarget(t, img)

	// 2 against a false getter is the measured Project 12 case: it agrees.
	if !engineMouseBehaviorAgrees(MouseBehaviorLockCurrentPosition, false) {
		t.Fatal("LockCurrentPosition disagreed with a false getter")
	}
	if n := mouseBehaviorAuthority.mismatch.Load(); n != 0 {
		t.Fatalf("mismatch=%d after agreement, want 0", n)
	}

	// A straddled transition is tolerated once, then twice.
	for i := 1; i <= 2; i++ {
		if engineMouseBehaviorAgrees(MouseBehaviorDefault, true) {
			t.Fatalf("disagreement %d was reported as agreement", i)
		}
		if n := mouseBehaviorAuthority.mismatch.Load(); n != int32(i) {
			t.Fatalf("mismatch=%d, want %d", n, i)
		}
		if mouseBehaviorAuthority.disabled.Load() {
			t.Fatalf("disagreement %d disabled the authority early", i)
		}
	}
	// The third disables it.
	if engineMouseBehaviorAgrees(MouseBehaviorDefault, true) {
		t.Fatal("third disagreement was reported as agreement")
	}
	if !mouseBehaviorAuthority.disabled.Load() {
		t.Fatal("three consecutive disagreements did not disable the authority")
	}
	if engineBehaviorAuthoritative() {
		t.Fatal("a disabled authority still reported itself authoritative")
	}

	// Agreement after a disable would be meaningless, and reset proves it:
	// the counter, the seen latch and the disabled latch all clear.
	resetEngineMouseBehavior()
	if mouseBehaviorAuthority.disabled.Load() || mouseBehaviorAuthority.seen.Load() ||
		mouseBehaviorAuthority.mismatch.Load() != 0 ||
		mouseBehaviorAuthority.attempted || mouseBehaviorAuthority.layout != nil {
		t.Fatal("resetEngineMouseBehavior left a latch behind")
	}
	if _, ok := EngineMouseBehavior(); !ok {
		t.Fatal("the authority did not come back after a reset")
	}
}

// TestEngineMouseBehaviorAgreementResetsTheCounter pins the other half: a
// disagreement followed by an agreement must not count toward the limit, so a
// session that straddles transitions often but never persistently never loses
// the authority.
func TestEngineMouseBehaviorAgreementResetsTheCounter(t *testing.T) {
	for i := 0; i < 8; i++ {
		if engineMouseBehaviorAgrees(MouseBehaviorDefault, true) {
			t.Fatal("disagreement reported as agreement")
		}
		if !engineMouseBehaviorAgrees(MouseBehaviorLockCenter, true) {
			t.Fatal("agreement reported as disagreement")
		}
	}
	if mouseBehaviorAuthority.disabled.Load() {
		t.Fatal("alternating agreement and disagreement disabled the authority")
	}
	if n := mouseBehaviorAuthority.mismatch.Load(); n != 0 {
		t.Fatalf("mismatch=%d, want 0", n)
	}
	resetEngineMouseBehavior()
}

// TestResetEngineMouseBehaviorClearsEveryLatch pins the teardown contract:
// the decoded layout belongs to the image that is about to be unmapped, so a
// re-wired target must decode against the bytes it actually has.
func TestResetEngineMouseBehaviorClearsEveryLatch(t *testing.T) {
	img := buildLiveShape(t, 3)
	img.setSingleton(fakeSingletonB, img.addr(fakeObjectOff))
	img.setBehavior(uint32(MouseBehaviorLockCurrentPosition))
	useFakeEngineMemory(t, img)
	wireFakeEngineTarget(t, img)

	if _, ok := EngineMouseBehavior(); !ok {
		t.Fatal("setup: the authority did not read")
	}
	disableEngineMouseBehavior("test")
	resetEngineMouseBehavior()
	if mouseBehaviorAuthority.attempted || mouseBehaviorAuthority.getter != 0 ||
		mouseBehaviorAuthority.layout != nil || mouseBehaviorAuthority.disabled.Load() ||
		mouseBehaviorAuthority.seen.Load() || mouseBehaviorAuthority.mismatch.Load() != 0 {
		t.Fatal("resetEngineMouseBehavior left a latch behind")
	}
}

// TestEngineMouseBehaviorDisabledByDefaultFailsClosed pins the starting
// state: an unwired process has no authority and must say so.
func TestEngineMouseBehaviorDisabledByDefaultFailsClosed(t *testing.T) {
	resetEngineMouseBehavior()
	if got, ok := EngineMouseBehavior(); ok || got != MouseBehaviorDefault {
		t.Fatalf("behavior=(%v,%t) with no target, want (Default,false)", got, ok)
	}
	if engineBehaviorAuthoritative() {
		t.Fatal("an unwired process reported the authority as authoritative")
	}
}

// TestEngineMouseBehaviorUnwiredTargetIsUnavailable pins the other fail-closed
// entry: the exported getter address is the only anchor, so a zero one means
// no decode at all.
func TestEngineMouseBehaviorUnwiredTargetIsUnavailable(t *testing.T) {
	resetEngineMouseBehavior()
	ClearRobloxDirectInputTarget()
	if got := robloxDirectMouseLockedFn(); got != 0 {
		t.Fatalf("lock getter=%#x after clear, want 0", got)
	}
	if got, ok := EngineMouseBehavior(); ok {
		t.Fatalf("behavior=(%v,%t) with no getter, want unavailable", got, ok)
	}
}
