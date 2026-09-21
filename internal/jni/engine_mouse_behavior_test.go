// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

const (
	fakeGetterOff   = 0x1000
	fakeAccessorOff = 0x2000
	fakeSingletonA  = 0x3000
	fakeSingletonB  = 0x4000
	fakeObjectOff   = 0x5000
	fakeImageSize   = 0x8000
)

type fakeGetterShape struct {
	index      uint32
	objOffset  uint32
	enumOffset uint32
	locked     uint8
}

var fakeLiveShape = fakeGetterShape{
	index:      4,
	objOffset:  0xb00,
	enumOffset: 0x88,
	locked:     uint8(MouseBehaviorLockCenter),
}

// fakeEngineImage is one readable allocation standing in for engine memory.
type fakeEngineImage struct {
	base unsafe.Pointer
	mem  []byte
}

func newFakeEngineImage(t *testing.T) *fakeEngineImage {
	t.Helper()
	mem := make([]byte, fakeImageSize)
	// img.mem holds the backing array for the image's life, keeping the addresses img.addr returns valid and readable.
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

func (img *fakeEngineImage) writeGetter(s fakeGetterShape) {
	g := img.mem[fakeGetterOff:]
	g[0] = 0xBF
	binary.LittleEndian.PutUint32(g[1:], s.index)
	g[5] = 0xE8
	binary.LittleEndian.PutUint32(g[6:], uint32(int32(fakeAccessorOff-(fakeGetterOff+10))))
	copy(g[10:], []byte{0x48, 0x8B, 0x83})
	binary.LittleEndian.PutUint32(g[13:], s.objOffset)
	copy(g[17:], []byte{0x83, 0xB8})
	binary.LittleEndian.PutUint32(g[19:], s.enumOffset)
	g[23] = s.locked
}

func (img *fakeEngineImage) writeAccessor(pivot uint8) {
	a := img.mem[fakeAccessorOff:]
	copy(a[0:], []byte{0x83, 0xFB, pivot})
	copy(a[3:], []byte{0x48, 0x8D, 0x0D})
	binary.LittleEndian.PutUint32(a[6:], uint32(int32(fakeSingletonA-(fakeAccessorOff+10))))
	copy(a[10:], []byte{0x48, 0x8D, 0x05})
	binary.LittleEndian.PutUint32(a[13:], uint32(int32(fakeSingletonB-(fakeAccessorOff+17))))
	copy(a[17:], []byte{0x48, 0x0F, 0x44, 0xC1})
}

func (img *fakeEngineImage) setSingleton(which int, obj uintptr) {
	binary.LittleEndian.PutUint64(img.mem[which+int(fakeLiveShape.objOffset):], uint64(obj))
}

func (img *fakeEngineImage) setBehavior(value uint32) {
	binary.LittleEndian.PutUint32(img.mem[fakeObjectOff+fakeLiveShape.enumOffset:], value)
}

// writeWildAccessor points both statics far outside the image, so the decoded singleton address is not in any readable mapping.
func (img *fakeEngineImage) writeWildAccessor(pivot uint8) {
	a := img.mem[fakeAccessorOff:]
	copy(a[0:], []byte{0x83, 0xFB, pivot})
	copy(a[3:], []byte{0x48, 0x8D, 0x0D})
	binary.LittleEndian.PutUint32(a[6:], uint32(int32(0x40000000)))
	copy(a[10:], []byte{0x48, 0x8D, 0x05})
	binary.LittleEndian.PutUint32(a[13:], uint32(int32(0x40000000)))
	copy(a[17:], []byte{0x48, 0x0F, 0x44, 0xC1})
}

func useFakeEngineMemory(t *testing.T, img *fakeEngineImage) {
	t.Helper()
	old := engineMem
	engineMem = img
	t.Cleanup(func() { engineMem = old })
}

// wireFakeEngineTarget points the target's exported lock getter at the synthetic image; only that address is dereferenced, so RobloxMainWindowMouseLocked must not be called.
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

// buildLiveShape lays down the getter and accessor and returns the image ready for a decode.
func buildLiveShape(t *testing.T, pivot uint8) *fakeEngineImage {
	t.Helper()
	img := newFakeEngineImage(t)
	img.writeGetter(fakeLiveShape)
	img.writeAccessor(pivot)
	return img
}

// TestDecodeMouseBehaviorGetterBothSingletonBranches pins the accessor's cmove: index == pivot selects A, anything else selects B.
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

// TestDecodeMouseBehaviorGetterFailsClosed pins every rejection: a decode that guesses would return plausible garbage, which is worse than no answer.
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

// TestDecodeMouseBehaviorRejectsMissingInstructions pins the missing-instruction failures: without them a resembling body would decode into wrong offsets.
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

// TestDecodeSingletonAccessorRejectsUnknownShape pins that a body which is not the static-pair shape must not be walked.
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

// TestDecodeMouseBehaviorRejectsUnreadableBodies pins that an unreadable body is a decode error, never a wild dereference or crash.
func TestDecodeMouseBehaviorRejectsUnreadableBodies(t *testing.T) {
	t.Run("getter body unreadable", func(t *testing.T) {
		img := buildLiveShape(t, 3)
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehavior(img.addr(fakeImageSize + 0x1000)); err == nil {
			t.Fatal("decoded a getter body that is not readable")
		}
	})
	t.Run("accessor body unreadable", func(t *testing.T) {
		img := newFakeEngineImage(t)
		img.writeGetter(fakeLiveShape)
		// Point the call outside the image so the getter decodes but the accessor cannot be read.
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
		// Point both statics far outside the image so the decoded singleton address is not in a readable mapping.
		img.writeWildAccessor(3)
		useFakeEngineMemory(t, img)
		if _, err := decodeMouseBehavior(img.addr(fakeGetterOff)); err == nil {
			t.Fatal("accepted a singleton slot that is not in a readable mapping")
		}
	})
}

// TestEngineMouseBehaviorReadsTheEnumWord covers the value the exported boolean cannot see.
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

// TestEngineMouseBehaviorNULLObjectIsUnavailable pins that a NULL object read is unavailable, never reported as Default.
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

// TestEngineMouseBehaviorOutOfRangeDisablesAuthority pins fail-closed: an out-of-range word disables the read rather than being reinterpreted.
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

// TestEngineMouseBehaviorValidatorIsPolicedByTheExportedBoolean pins the reference boolean: agreement resets the counter, three consecutive disagreements disable the authority, one straddled transition is tolerated.
func TestEngineMouseBehaviorValidatorIsPolicedByTheExportedBoolean(t *testing.T) {
	img := buildLiveShape(t, 3)
	img.setSingleton(fakeSingletonB, img.addr(fakeObjectOff))
	img.setBehavior(uint32(MouseBehaviorLockCurrentPosition))
	useFakeEngineMemory(t, img)
	wireFakeEngineTarget(t, img)

	if !engineMouseBehaviorAgrees(MouseBehaviorLockCurrentPosition, false) {
		t.Fatal("LockCurrentPosition disagreed with a false getter")
	}
	if n := mouseBehaviorAuthority.mismatch.Load(); n != 0 {
		t.Fatalf("mismatch=%d after agreement, want 0", n)
	}

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
	if engineMouseBehaviorAgrees(MouseBehaviorDefault, true) {
		t.Fatal("third disagreement was reported as agreement")
	}
	if !mouseBehaviorAuthority.disabled.Load() {
		t.Fatal("three consecutive disagreements did not disable the authority")
	}
	if engineBehaviorAuthoritative() {
		t.Fatal("a disabled authority still reported itself authoritative")
	}

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

// TestEngineMouseBehaviorAgreementResetsTheCounter pins that a disagreement followed by an agreement does not count toward the disable limit.
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

// TestResetEngineMouseBehaviorClearsEveryLatch pins the teardown contract: a re-wired target must decode against the bytes it actually has.
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

// TestEngineMouseBehaviorDisabledByDefaultFailsClosed pins that an unwired process reports no authority.
func TestEngineMouseBehaviorDisabledByDefaultFailsClosed(t *testing.T) {
	resetEngineMouseBehavior()
	if got, ok := EngineMouseBehavior(); ok || got != MouseBehaviorDefault {
		t.Fatalf("behavior=(%v,%t) with no target, want (Default,false)", got, ok)
	}
	if engineBehaviorAuthoritative() {
		t.Fatal("an unwired process reported the authority as authoritative")
	}
}

// TestEngineMouseBehaviorUnwiredTargetIsUnavailable pins that a zero getter address means no decode.
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
