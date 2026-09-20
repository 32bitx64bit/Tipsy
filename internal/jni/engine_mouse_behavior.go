// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

// Engine MouseBehavior authority.
//
// Roblox exports exactly one lock-state native,
// NativeInputInterface.nativeGetMainWindowIsMouseLockedCenter, and its body
// is literally
//
//	s   = getSingleton(4)
//	obj = *(void**)(s + OFF_PTR)
//	return *(int32_t*)(obj + OFF_ENUM) == 1
//
// Enum.MouseBehavior is Default = 0, LockCenter = 1, LockCurrentPosition = 2,
// so that `cmpl $0x1` reports a game sitting in LockCurrentPosition as "not
// locked". Place 79966250354565 ("Project 12 [BODY CAM!]") holds 2 for the
// whole time it is in first person -- 1,545 measured polls, LockCenter never
// once -- while the exported boolean was false on all 548 of Tipsy's probes.
// That is the measured bug; see
// .tipsy-private/docs/investigations/mouse-behavior-enum-measurement-2026-09-20.md.
//
// This file reads that one word, and nothing else.
//
// # ADR 0010 exception (owner-granted, reads only)
//
// ADR 0010 bans engine hooks. The owner granted a single narrow exception for
// "read-only engine state, purely read only, and very targeted", with an
// explicit worry about anti-cheat. The boundaries this file keeps:
//
//   - Zero writes anywhere in Roblox's address space: no hooks, trampolines,
//     patches or NOPs. Every access here is a load.
//   - No new threads and no polling loop. The read happens only at the call
//     sites that already invoke RobloxMainWindowMouseLocked(), at exactly the
//     cadence they already had, so the process's behavioural footprint is
//     unchanged.
//   - No ptrace, no /proc/self/mem, no mprotect, no signal handlers, no
//     mapping or permission changes. internal/loader maps libroblox into this
//     process, so the word is already mapped and readable: a plain aligned
//     load is the entire mechanism. /proc/self/maps is read (read-only, and
//     only when an address is seen for the first time) purely as a guard so a
//     mis-decode can never dereference a wild pointer.
//   - Every constant is decoded at runtime from the bytes of the exported
//     symbol and cached once. Nothing is hardcoded: the live client's object
//     offset is +0xb00 while the stale tree in ~/.local/share/tipsy/runtime/
//     uses +0xa10, so a pinned offset would read the wrong field of the right
//     object and return plausible garbage.
//   - Fail closed. A byte pattern that does not match, a NULL object, an
//     out-of-range value or a read that disagrees with the engine's own
//     exported boolean disables this authority and hands the decision back to
//     the boolean getter, loudly.
package jni

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// MouseBehavior is Roblox's Enum.MouseBehavior, the property the exported
// getter collapses into a boolean.
type MouseBehavior uint8

const (
	// MouseBehaviorDefault is a free pointer: the engine wants no lock at
	// all and the host pointer may leave the window.
	MouseBehaviorDefault MouseBehavior = 0
	// MouseBehaviorLockCenter is first person / shift lock: the engine
	// freezes its cursor at the viewport center.
	MouseBehaviorLockCenter MouseBehavior = 1
	// MouseBehaviorLockCurrentPosition freezes the engine cursor exactly
	// where it already was. The exported boolean cannot see it.
	MouseBehaviorLockCurrentPosition MouseBehavior = 2
)

func (b MouseBehavior) String() string {
	switch b {
	case MouseBehaviorDefault:
		return "Default"
	case MouseBehaviorLockCenter:
		return "LockCenter"
	case MouseBehaviorLockCurrentPosition:
		return "LockCurrentPosition"
	default:
		return "MouseBehavior(" + strconv.Itoa(int(b)) + ")"
	}
}

// mouseBehaviorLayout is everything the decode recovers from the exported
// getter. Addresses are live process addresses, never file vaddrs.
type mouseBehaviorLayout struct {
	getter      uintptr // the exported symbol the decode started from
	accessor    uintptr // getSingleton
	singleton   uintptr // the .bss static the accessor selects for index
	index       uint32  // the accessor index the getter passes
	pivot       uint8   // the accessor's cmp immediate
	objOffset   uint32  // singleton + objOffset holds the object pointer
	enumOffset  uint32  // object + enumOffset holds MouseBehavior
	lockedValue uint8   // the value the exported boolean compares against
}

// Decode windows: the getter body and the accessor body, exactly the sizes
// the measurement harness proved sufficient on the live client.
const (
	mouseBehaviorGetterWindow   = 0x80
	mouseBehaviorAccessorWindow = 0x60
	// Offsets inside one object are small; anything larger is a mis-decode.
	mouseBehaviorMaxOffset = 0x10000
)

var errMouseBehaviorShape = errors.New("engine getter does not match the decoded shape")

// decodeMouseBehaviorGetter decodes the exported getter body.
//
// The shape clang emits for this function is
//
//	mov   $imm32,%edi        bf imm32          the getSingleton index
//	call  rel32              e8 rel32          getSingleton
//	mov   disp32(%rbx),%rax  48 8b 83 disp32   the object pointer
//	cmpl  $imm8,disp32(%rax) 83 b8 disp32 imm8 MouseBehavior == LockCenter
//
// addr is the live address the code was read from, so the call target comes
// out as a live address too.
func decodeMouseBehaviorGetter(code []byte, addr uintptr) (mouseBehaviorLayout, error) {
	out := mouseBehaviorLayout{getter: addr}

	found := false
	for i := 0; i+10 <= len(code); i++ {
		if code[i] != 0xBF || code[i+5] != 0xE8 {
			continue
		}
		out.index = binary.LittleEndian.Uint32(code[i+1:])
		rel := int32(binary.LittleEndian.Uint32(code[i+6:]))
		out.accessor = addr + uintptr(i) + 10 + uintptr(int64(rel))
		found = true
		break
	}
	if !found {
		return out, fmt.Errorf("%w: no `mov $imm,%%edi ; call rel32` singleton fetch", errMouseBehaviorShape)
	}

	found = false
	for i := 0; i+7 <= len(code); i++ {
		if code[i] != 0x48 || code[i+1] != 0x8B || code[i+2] != 0x83 {
			continue
		}
		out.objOffset = binary.LittleEndian.Uint32(code[i+3:])
		found = true
		break
	}
	if !found {
		return out, fmt.Errorf("%w: no `mov disp32(%%rbx),%%rax` object load", errMouseBehaviorShape)
	}

	found = false
	for i := 0; i+7 <= len(code); i++ {
		if code[i] != 0x83 || code[i+1] != 0xB8 {
			continue
		}
		out.enumOffset = binary.LittleEndian.Uint32(code[i+2:])
		out.lockedValue = code[i+6]
		found = true
		break
	}
	if !found {
		return out, fmt.Errorf("%w: no `cmpl $imm8,disp32(%%rax)` MouseBehavior compare", errMouseBehaviorShape)
	}

	if out.lockedValue != uint8(MouseBehaviorLockCenter) {
		return out, fmt.Errorf("%w: the boolean compares against %d, not LockCenter",
			errMouseBehaviorShape, out.lockedValue)
	}
	if out.objOffset == 0 || out.objOffset > mouseBehaviorMaxOffset ||
		out.enumOffset == 0 || out.enumOffset > mouseBehaviorMaxOffset {
		return out, fmt.Errorf("%w: implausible offsets object=%#x behavior=%#x",
			errMouseBehaviorShape, out.objOffset, out.enumOffset)
	}
	if out.objOffset%8 != 0 || out.enumOffset%4 != 0 {
		return out, fmt.Errorf("%w: misaligned offsets object=%#x behavior=%#x",
			errMouseBehaviorShape, out.objOffset, out.enumOffset)
	}
	return out, nil
}

// decodeSingletonAccessor decodes getSingleton(index).
//
// The two singletons are clang function-local statics, so the accessor is a
// register select over two fixed .bss addresses rather than a heap call:
//
//	cmp   $imm8,%ebx     83 fb imm8
//	lea   A(%rip),%rcx   48 8d 0d rel32
//	lea   B(%rip),%rax   48 8d 05 rel32
//	cmove %rcx,%rax      48 0f 44 c1
//
// The result is A when the caller's index equals the pivot and B otherwise,
// which is why index 4 needs no call and no breakpoint.
func decodeSingletonAccessor(code []byte, addr uintptr, index uint32) (singleton uintptr, pivot uint8, err error) {
	for i := 0; i+24 <= len(code); i++ {
		if code[i] != 0x83 || code[i+1] != 0xFB {
			continue
		}
		pivot = code[i+2]
		j := i + 3
		if code[j] != 0x48 || code[j+1] != 0x8D || code[j+2] != 0x0D {
			continue
		}
		relA := int32(binary.LittleEndian.Uint32(code[j+3:]))
		a := addr + uintptr(j) + 7 + uintptr(int64(relA))
		j += 7
		if code[j] != 0x48 || code[j+1] != 0x8D || code[j+2] != 0x05 {
			continue
		}
		relB := int32(binary.LittleEndian.Uint32(code[j+3:]))
		b := addr + uintptr(j) + 7 + uintptr(int64(relB))
		j += 7
		if code[j] != 0x48 || code[j+1] != 0x0F || code[j+2] != 0x44 || code[j+3] != 0xC1 {
			continue
		}
		if index == uint32(pivot) {
			return a, pivot, nil
		}
		return b, pivot, nil
	}
	return 0, 0, fmt.Errorf("%w: getSingleton is not the `cmp/lea/lea/cmove` static pair",
		errMouseBehaviorShape)
}

// engineMemory is the read-only window onto the already-mapped engine image.
// Production reads this process's own memory; tests substitute an arena, so
// every decode and fail-closed path is exercised without a live client.
type engineMemory interface {
	bytes(addr uintptr, n int) ([]byte, bool)
	u64(addr uintptr) (uint64, bool)
	u32(addr uintptr) (uint32, bool)
}

var engineMem engineMemory = liveEngineMemory{}

// liveEngineMemory loads from this process. internal/loader mapped libroblox
// here (file-backed MAP_FIXED, internal/loader/mmap.go), so the engine's
// .text and .bss are ordinary readable pages of this address space. Nothing
// is written, no mapping or protection is touched, and every access is
// guarded by engineReadable so a mis-decode fails closed instead of taking
// the process down.
type liveEngineMemory struct{}

func (liveEngineMemory) bytes(addr uintptr, n int) ([]byte, bool) {
	if addr == 0 || n <= 0 || !engineReadable(addr, uintptr(n)) {
		return nil, false
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(addr)), n)) //nolint:govet // validated engine mapping, read-only
	return out, true
}

func (liveEngineMemory) u64(addr uintptr) (uint64, bool) {
	if addr%8 != 0 || !engineReadable(addr, 8) {
		return 0, false
	}
	return *(*uint64)(unsafe.Pointer(addr)), true //nolint:govet // validated engine mapping, read-only
}

func (liveEngineMemory) u32(addr uintptr) (uint32, bool) {
	if addr%4 != 0 || !engineReadable(addr, 4) {
		return 0, false
	}
	return *(*uint32)(unsafe.Pointer(addr)), true //nolint:govet // validated engine mapping, read-only
}

type engineAddrRange struct{ lo, hi uintptr }

// engineMaps caches this process's readable mappings. It is the guard that
// keeps a mis-decoded address from being dereferenced: an address outside
// every readable mapping is reported unavailable rather than loaded. The
// snapshot is refreshed only when an address misses, i.e. once per new
// object pointer -- roughly once per session -- and never on the hot path.
var engineMaps struct {
	mu     sync.Mutex
	ranges []engineAddrRange
	reads  int
}

// engineMapsReadLimit bounds the refreshes so a permanently bad address
// cannot turn every probe into a /proc read.
const engineMapsReadLimit = 64

func engineReadable(addr, size uintptr) bool {
	if addr == 0 || size == 0 || addr+size < addr {
		return false
	}
	engineMaps.mu.Lock()
	defer engineMaps.mu.Unlock()
	if engineRangesContain(engineMaps.ranges, addr, size) {
		return true
	}
	if engineMaps.reads >= engineMapsReadLimit {
		return false
	}
	engineMaps.reads++
	if ranges, err := readProcSelfMaps(); err == nil {
		engineMaps.ranges = ranges
	}
	return engineRangesContain(engineMaps.ranges, addr, size)
}

func engineRangesContain(ranges []engineAddrRange, addr, size uintptr) bool {
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i].hi > addr })
	return i < len(ranges) && ranges[i].lo <= addr && addr+size <= ranges[i].hi
}

// readProcSelfMaps returns this process's readable mappings, sorted. It is a
// read of /proc/self/maps -- never /proc/self/mem -- and writes nothing.
func readProcSelfMaps() ([]engineAddrRange, error) {
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []engineAddrRange
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || len(fields[1]) < 1 || fields[1][0] != 'r' {
			continue
		}
		dash := strings.IndexByte(fields[0], '-')
		if dash <= 0 {
			continue
		}
		lo, err := strconv.ParseUint(fields[0][:dash], 16, 64)
		if err != nil {
			continue
		}
		hi, err := strconv.ParseUint(fields[0][dash+1:], 16, 64)
		if err != nil || hi <= lo {
			continue
		}
		out = append(out, engineAddrRange{lo: uintptr(lo), hi: uintptr(hi)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].lo < out[j].lo })
	return out, nil
}

// mouseBehaviorAuthority caches the decode and carries the fail-closed
// latches. The decode runs once per resolved getter address and is reused;
// nothing here is re-derived per probe.
var mouseBehaviorAuthority struct {
	mu        sync.Mutex
	attempted bool
	getter    uintptr
	layout    *mouseBehaviorLayout

	// disabled latches the authority off: a decode failure, an out-of-range
	// value, or the engine's own exported boolean disagreeing with the read.
	disabled atomic.Bool
	// seen latches on the first valid read. It is what retires the zoom
	// heuristic: once the engine has answered for real, guessing is over.
	seen     atomic.Bool
	mismatch atomic.Int32
}

// engineMouseBehaviorMismatchLimit is how many consecutive disagreements
// with the exported boolean count as a racy sample rather than a broken
// decode. The word is read and the getter called microseconds apart without
// the engine's own lock, so a single straddled transition is expected; three
// in a row is not, and disables the read.
const engineMouseBehaviorMismatchLimit = 3

// EngineMouseBehavior reads the engine's live UserInputService.MouseBehavior.
//
// It is a read and only a read: the constants come from decoding the bytes of
// the exported getter once, and each probe is two aligned loads of memory
// internal/loader already mapped into this process. ok is false when the
// authority is unavailable for any reason -- not yet wired, decode failed,
// the object pointer is still NULL (it appears ~0.2 s after the pointer lock
// handshake), the value is out of range, or the validator has disabled it.
// An unavailable read is never reported as Default: a dead read and a game
// that wants a free pointer are different facts.
func EngineMouseBehavior() (MouseBehavior, bool) {
	if mouseBehaviorAuthority.disabled.Load() {
		return MouseBehaviorDefault, false
	}
	getter := robloxDirectMouseLockedFn()
	if getter == 0 {
		return MouseBehaviorDefault, false
	}
	layout := mouseBehaviorLayoutFor(getter)
	if layout == nil {
		return MouseBehaviorDefault, false
	}
	obj, ok := engineMem.u64(layout.singleton + uintptr(layout.objOffset))
	if !ok || obj == 0 {
		// NULL is the pre-onGameLoaded window, not a state.
		return MouseBehaviorDefault, false
	}
	value, ok := engineMem.u32(uintptr(obj) + uintptr(layout.enumOffset))
	if !ok {
		return MouseBehaviorDefault, false
	}
	if value > uint32(MouseBehaviorLockCurrentPosition) {
		disableEngineMouseBehavior(fmt.Sprintf("read %d, which is not an Enum.MouseBehavior value", value))
		return MouseBehaviorDefault, false
	}
	mouseBehaviorAuthority.seen.Store(true)
	return MouseBehavior(value), true
}

// engineMouseBehaviorProbe is the production read behind a test seam.
// Production never replaces it.
var engineMouseBehaviorProbe = EngineMouseBehavior

// mouseBehaviorLayoutFor returns the cached decode for getter, decoding once
// on first use. nil means the authority is unavailable.
func mouseBehaviorLayoutFor(getter uintptr) *mouseBehaviorLayout {
	mouseBehaviorAuthority.mu.Lock()
	defer mouseBehaviorAuthority.mu.Unlock()
	if mouseBehaviorAuthority.attempted && mouseBehaviorAuthority.getter == getter {
		return mouseBehaviorAuthority.layout
	}
	mouseBehaviorAuthority.attempted = true
	mouseBehaviorAuthority.getter = getter
	mouseBehaviorAuthority.layout = nil

	layout, err := decodeMouseBehavior(getter)
	if err != nil {
		logging.Logger(logging.CatJNI).Error(
			"[jni] engine MouseBehavior decode failed; falling back to the exported boolean",
			"getter", fmt.Sprintf("%#x", getter), "err", err)
		return nil
	}
	mouseBehaviorAuthority.layout = &layout
	logging.Logger(logging.CatJNI).Info("[jni] engine MouseBehavior authority resolved",
		"getter", fmt.Sprintf("%#x", layout.getter),
		"accessor", fmt.Sprintf("%#x", layout.accessor),
		"index", layout.index,
		"singleton", fmt.Sprintf("%#x", layout.singleton),
		"object", fmt.Sprintf("+%#x", layout.objOffset),
		"behavior", fmt.Sprintf("+%#x", layout.enumOffset))
	return mouseBehaviorAuthority.layout
}

// decodeMouseBehavior walks the exported symbol's own instructions in the
// live image. Nothing is read from disk: the stale client in
// ~/.local/share/tipsy/runtime/ has a different object offset (+0xa10 against
// the live +0xb00), and a decode that trusted it would read the wrong field
// of the right object.
func decodeMouseBehavior(getter uintptr) (mouseBehaviorLayout, error) {
	code, ok := engineMem.bytes(getter, mouseBehaviorGetterWindow)
	if !ok {
		return mouseBehaviorLayout{}, fmt.Errorf("getter body at %#x is not readable", getter)
	}
	layout, err := decodeMouseBehaviorGetter(code, getter)
	if err != nil {
		return layout, err
	}
	accessor, ok := engineMem.bytes(layout.accessor, mouseBehaviorAccessorWindow)
	if !ok {
		return layout, fmt.Errorf("getSingleton body at %#x is not readable", layout.accessor)
	}
	singleton, pivot, err := decodeSingletonAccessor(accessor, layout.accessor, layout.index)
	if err != nil {
		return layout, err
	}
	layout.singleton, layout.pivot = singleton, pivot
	if !engineReadable(layout.singleton+uintptr(layout.objOffset), 8) {
		return layout, fmt.Errorf("%w: singleton slot %#x is not in a readable mapping",
			errMouseBehaviorShape, layout.singleton+uintptr(layout.objOffset))
	}
	return layout, nil
}

// engineMouseBehaviorAgrees is the validator that made this exception safe to
// take: the engine's own exported boolean polices Tipsy's read. The word and
// the boolean must agree on the `== LockCenter` case; a disagreement means
// either a straddled transition (rare, tolerated) or a decode that is not
// reading what the getter reads (fatal to the authority).
func engineMouseBehaviorAgrees(value MouseBehavior, locked bool) bool {
	if (value == MouseBehaviorLockCenter) == locked {
		mouseBehaviorAuthority.mismatch.Store(0)
		return true
	}
	n := mouseBehaviorAuthority.mismatch.Add(1)
	if n < engineMouseBehaviorMismatchLimit {
		logging.Logger(logging.CatJNI).Info(
			"[jni] engine MouseBehavior disagreed with the exported getter",
			"behavior", value.String(), "getter", locked, "consecutive", n)
		return false
	}
	disableEngineMouseBehavior(fmt.Sprintf(
		"%d consecutive disagreements with nativeGetMainWindowIsMouseLockedCenter (last read %s, getter %t)",
		n, value, locked))
	return false
}

// disableEngineMouseBehavior fails the authority closed for the rest of the
// process and says so loudly. Everything falls back to the exported boolean
// and the heuristics it drives; an honest, noisy loss of a capability beats a
// silent wrong answer about where the operator's cursor belongs.
func disableEngineMouseBehavior(reason string) {
	if mouseBehaviorAuthority.disabled.Swap(true) {
		return
	}
	mouseBehaviorAuthority.seen.Store(false)
	logging.Logger(logging.CatJNI).Error(
		"[jni] engine MouseBehavior authority disabled; falling back to the exported boolean",
		"reason", reason)
}

// engineBehaviorAuthoritative reports whether the enum authority has produced
// at least one valid read in this process and has not been disabled. It is
// the gate that retires the wheel-driven zoom heuristic: while the engine is
// answering for real, Tipsy never guesses.
func engineBehaviorAuthoritative() bool {
	return mouseBehaviorAuthority.seen.Load() && !mouseBehaviorAuthority.disabled.Load()
}

// resetEngineMouseBehavior drops the decode and every latch. Teardown uses it
// so a re-wired target re-decodes against the image it actually has; tests
// use it to start from a known state.
func resetEngineMouseBehavior() {
	mouseBehaviorAuthority.mu.Lock()
	mouseBehaviorAuthority.attempted = false
	mouseBehaviorAuthority.getter = 0
	mouseBehaviorAuthority.layout = nil
	mouseBehaviorAuthority.mu.Unlock()
	mouseBehaviorAuthority.disabled.Store(false)
	mouseBehaviorAuthority.seen.Store(false)
	mouseBehaviorAuthority.mismatch.Store(0)
}
