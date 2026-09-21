// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

// Engine MouseBehavior authority.
//
// The exported lock-state boolean collapses Enum.MouseBehavior into a single
// "centered" answer, so a cursor locked at its current position reports as
// unlocked. This file reads that enum directly.
//
// The access is strictly read-only, adds no threads and no polling, and fails
// closed: any shape mismatch, NULL object, out-of-range value, or disagreement
// with the exported boolean disables this authority and returns the decision
// to the boolean getter.
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

// MouseBehavior is the engine's Enum.MouseBehavior, the property the exported
// getter collapses into a boolean.
type MouseBehavior uint8

const (
	// MouseBehaviorDefault is a free pointer; the host pointer may leave the
	// window.
	MouseBehaviorDefault MouseBehavior = 0
	// MouseBehaviorLockCenter freezes the cursor at the viewport center.
	MouseBehaviorLockCenter MouseBehavior = 1
	// MouseBehaviorLockCurrentPosition freezes the cursor where it already
	// was; the exported boolean cannot see it.
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
	accessor    uintptr // the singleton accessor
	singleton   uintptr // the static the accessor selects for index
	index       uint32  // the accessor index the getter passes
	pivot       uint8   // the accessor's compare immediate
	objOffset   uint32  // singleton + objOffset holds the object pointer
	enumOffset  uint32  // object + enumOffset holds MouseBehavior
	lockedValue uint8   // the value the exported boolean compares against
}

// Decode windows: the getter body and the accessor body.
const (
	mouseBehaviorGetterWindow   = 0x80
	mouseBehaviorAccessorWindow = 0x60
	// Offsets inside one object are small; anything larger is a mis-decode.
	mouseBehaviorMaxOffset = 0x10000
)

var errMouseBehaviorShape = errors.New("engine getter does not match the decoded shape")

// decodeMouseBehaviorGetter decodes the exported getter body.
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

// decodeSingletonAccessor decodes the singleton accessor. It returns the
// selected static address for index; pivot is the accessor's compare
// immediate.
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
// Tests substitute an arena so every decode and fail-closed path is exercised
// without a live client.
type engineMemory interface {
	bytes(addr uintptr, n int) ([]byte, bool)
	u64(addr uintptr) (uint64, bool)
	u32(addr uintptr) (uint32, bool)
}

var engineMem engineMemory = liveEngineMemory{}

// liveEngineMemory loads from this process. The engine image is already
// mapped here, so its .text and .bss are ordinary readable pages of this
// address space. Nothing is written and no mapping or protection is touched;
// every access is guarded by engineReadable so a mis-decode fails closed.
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

// engineMaps caches this process's readable mappings so a mis-decoded address
// is reported unavailable rather than dereferenced. The snapshot is refreshed
// only when an address misses, never on the hot path.
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
// latches. The decode runs once per resolved getter address and is reused.
var mouseBehaviorAuthority struct {
	mu        sync.Mutex
	attempted bool
	getter    uintptr
	layout    *mouseBehaviorLayout

	// disabled latches the authority off: a decode failure, an out-of-range
	// value, or the exported boolean disagreeing with the read.
	disabled atomic.Bool
	// seen latches on the first valid read and retires the zoom heuristic.
	seen     atomic.Bool
	mismatch atomic.Int32
}

// engineMouseBehaviorMismatchLimit is how many consecutive disagreements with
// the exported boolean count as a broken decode rather than a racy sample.
const engineMouseBehaviorMismatchLimit = 3

// EngineMouseBehavior reads the engine's live MouseBehavior.
//
// ok is false when the authority is unavailable for any reason -- not yet
// wired, decode failed, the object pointer is still NULL, the value is out of
// range, or the validator has disabled it. An unavailable read is never
// reported as Default: a dead read and a game that wants a free pointer are
// different facts.
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

// decodeMouseBehavior walks the exported symbol's own instructions in the live
// image. Nothing is read from disk, so the decode always matches the client
// that is actually loaded.
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

// engineMouseBehaviorAgrees is the validator: the exported boolean polices
// this read. The word and the boolean must agree on the LockCenter case; a
// disagreement is either a straddled transition (tolerated) or a decode that
// is not reading what the getter reads (fatal to the authority).
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
// and the heuristics it drives.
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
// at least one valid read in this process and has not been disabled. It gates
// the wheel-driven zoom heuristic.
func engineBehaviorAuthoritative() bool {
	return mouseBehaviorAuthority.seen.Load() && !mouseBehaviorAuthority.disabled.Load()
}

// resetEngineMouseBehavior drops the decode and every latch so a re-wired
// target re-decodes against the image it actually has.
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
