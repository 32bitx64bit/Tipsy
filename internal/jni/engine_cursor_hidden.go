// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

// Engine cursor-hidden (MouseIconEnabled) reader.
//
// The property's reflection descriptor names its getter. InitEngineCursorHidden
// finds that getter and the MouseBehavior reflection getter in the loaded image.
// Both getters must call the same resolver. When their MouseService object is
// not decodeMouseBehavior's MainWindow object, a second identity proof decodes
// the engine's own ServiceProvider path from the same singleton to MouseService:
// the resolver, ServiceProvider::create, its index and vector helpers, getGame,
// and a bridge whose caller reads both reflection displacements must all agree.
//
// Every pointer hop is read-only and guarded by engineMem. Runtime reads gate
// MouseIconEnabled on a valid MouseBehavior enum and boolean byte. Any shape or
// identity mismatch leaves resolve nil, so EngineCursorHidden reports ok=false
// and the cursor stays free.
package jni

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// cursorHiddenCandidate is a getter found through its reflection descriptor,
// the object resolver it calls, and the field offset it reads from that object.
type cursorHiddenCandidate struct {
	getter   uintptr
	resolver uintptr
	offset   uint32
}

// cursorScanOverlap keeps an instruction sequence that straddles two scan
// chunks whole in at least one of them.
const cursorScanOverlap = 87

func cursorRelative(code []byte, addr uintptr, off int) uintptr {
	return uintptr(int64(addr) + int64(off+7) + int64(int32(binary.LittleEndian.Uint32(code[off+3:]))))
}

// cursorGetterDecode decodes a reflection property getter body: adjust the
// receiver, call the object resolver, branch on NULL, and read disp32(%rax)
// (byte mov 0x8a for boolean getters, or dword mov 0x8b when word is true).
func cursorGetterDecode(code []byte, getter uintptr, word bool) (resolver uintptr, offset uint32, ok bool) {
	prefix := []byte{0x55, 0x48, 0x89, 0xe5, 0x48, 0x81, 0xc7}
	if len(code) < 33 || !bytes.Equal(code[:7], prefix) || code[11] != 0xe8 ||
		!bytes.Equal(code[16:21], []byte{0x48, 0x85, 0xc0, 0x74, 0x08}) ||
		code[22] != 0x80 ||
		!bytes.Equal(code[27:33], []byte{0xeb, 0x02, 0x31, 0xc0, 0x5d, 0xc3}) {
		return 0, 0, false
	}
	if code[21] != 0x8a && (!word || code[21] != 0x8b) {
		return 0, 0, false
	}
	rel := int32(binary.LittleEndian.Uint32(code[12:16]))
	resolver = uintptr(int64(getter) + 16 + int64(rel))
	offset = binary.LittleEndian.Uint32(code[23:27])
	if offset == 0 || offset > mouseBehaviorMaxOffset || (code[21] == 0x8b && offset%4 != 0) {
		return 0, 0, false
	}
	return resolver, offset, true
}

// cursorRegistrationAddresses matches either the 4-lea property constructor
// call sequence or the inlined EnumPropertyDescriptor registration sequence.
func cursorRegistrationAddresses(p []byte, a uintptr) (descriptor, name, getter uintptr, ok bool) {
	if len(p) >= 42 &&
		bytes.Equal(p[:3], []byte{0x48, 0x8d, 0x3d}) &&
		bytes.Equal(p[7:10], []byte{0x48, 0x8d, 0x35}) &&
		bytes.Equal(p[14:17], []byte{0x48, 0x8d, 0x15}) &&
		bytes.Equal(p[21:24], []byte{0x48, 0x8d, 0x0d}) {
		direct := bytes.Equal(p[28:31], []byte{0x4c, 0x8d, 0x05}) && bytes.Equal(p[35:38], []byte{0x45, 0x31, 0xc9}) && p[38] == 0xe8
		stack := len(p) >= 46 && bytes.Equal(p[28:31], []byte{0x48, 0x8d, 0x9d}) && bytes.Equal(p[35:42], []byte{0x49, 0x89, 0xd8, 0x45, 0x31, 0xc9, 0xe8})
		if direct || stack {
			return cursorRelative(p, a, 0), cursorRelative(p, a, 7), cursorRelative(p, a, 21), true
		}
	}
	if len(p) >= 84 &&
		bytes.Equal(p[:3], []byte{0x48, 0x8d, 0x3d}) &&
		bytes.Equal(p[7:10], []byte{0x48, 0x8d, 0x0d}) {
		allocOff := 0
		if bytes.Equal(p[14:17], []byte{0x4c, 0x8d, 0x05}) &&
			bytes.Equal(p[21:24], []byte{0x48, 0x89, 0xde}) &&
			(bytes.Equal(p[24:27], []byte{0x4c, 0x89, 0xf2}) || bytes.Equal(p[24:27], []byte{0x48, 0x89, 0xc2})) &&
			p[27] == 0xe8 &&
			bytes.Equal(p[32:35], []byte{0x4c, 0x89, 0x35}) &&
			bytes.Equal(p[39:42], []byte{0x48, 0x8d, 0x05}) &&
			bytes.Equal(p[46:49], []byte{0x48, 0x89, 0x05}) {
			allocOff = 53
		} else if len(p) >= 87 &&
			bytes.Equal(p[14:17], []byte{0x4c, 0x8d, 0x8d}) &&
			bytes.Equal(p[21:31], []byte{0x4c, 0x89, 0xfe, 0x4c, 0x89, 0xe2, 0x49, 0x89, 0xd8, 0xe8}) &&
			bytes.Equal(p[35:38], []byte{0x4c, 0x89, 0x25}) &&
			bytes.Equal(p[42:45], []byte{0x48, 0x8d, 0x05}) &&
			bytes.Equal(p[49:52], []byte{0x48, 0x89, 0x05}) {
			allocOff = 56
		}
		if allocOff > 0 &&
			bytes.Equal(p[allocOff:allocOff+6], []byte{0xbf, 0x28, 0x00, 0x00, 0x00, 0xe8}) &&
			bytes.Equal(p[allocOff+10:allocOff+13], []byte{0x48, 0x8d, 0x0d}) &&
			bytes.Equal(p[allocOff+17:allocOff+23], []byte{0x48, 0x89, 0x08, 0x48, 0x8d, 0x0d}) &&
			bytes.Equal(p[allocOff+27:allocOff+31], []byte{0x48, 0x89, 0x48, 0x08}) {
			return cursorRelative(p, a, 0), cursorRelative(p, a, 7), cursorRelative(p, a, allocOff+20), true
		}
	}
	return 0, 0, 0, false
}

// cursorCandidates scans one mapped chunk for the reflection registration of
// prop (default "MouseIconEnabled"). The whole instruction sequence must
// match, not just a reference to the name. Addresses must fall inside
// [base, end).
func cursorCandidates(code []byte, addr, base, end uintptr, prop ...string) []cursorHiddenCandidate {
	propName := "MouseIconEnabled"
	if len(prop) > 0 && prop[0] != "" {
		propName = prop[0]
	}
	want := propName + "\x00"
	wantLen := uintptr(len(want))
	word := propName != "MouseIconEnabled"

	var out []cursorHiddenCandidate
	for i := 0; i+42 <= len(code); i++ {
		p := code[i:]
		a := addr + uintptr(i)
		descriptor, name, getter, ok := cursorRegistrationAddresses(p, a)
		if !ok {
			continue
		}
		if descriptor < base || descriptor >= end || name < base || name >= end || getter < base || getter >= end || end-name < wantLen || end-getter < 33 {
			continue
		}
		s, ok := engineMem.bytes(name, len(want))
		if !ok || string(s) != want {
			continue
		}
		body, ok := engineMem.bytes(getter, 33)
		if !ok {
			continue
		}
		if resolver, offset, ok := cursorGetterDecode(body, getter, word); ok && resolver >= base && resolver < end {
			out = append(out, cursorHiddenCandidate{getter: getter, resolver: resolver, offset: offset})
		}
	}
	return out
}

// uniqueCursorCandidates drops repeats: the overlap between chunks can find
// one registration twice, and that is not ambiguity.
func uniqueCursorCandidates(in []cursorHiddenCandidate) []cursorHiddenCandidate {
	var out []cursorHiddenCandidate
next:
	for _, c := range in {
		for _, seen := range out {
			if seen == c {
				continue next
			}
		}
		out = append(out, c)
	}
	return out
}

// locateCursorGetter scans the readable parts of [base, end) in chunks for
// prop (default "MouseIconEnabled").
func locateCursorGetter(ranges []engineAddrRange, base, end uintptr, prop ...string) []cursorHiddenCandidate {
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
				found = append(found, cursorCandidates(b, lo, base, end, prop...)...)
			}
			if n == hi-lo {
				break
			}
			lo += n - cursorScanOverlap
		}
	}
	return uniqueCursorCandidates(found)
}

// cursorMouseServiceChain is the load-time-decoded, read-only path from the
// singleton selected by decodeMouseBehavior to MouseService. Every field comes
// from an instruction in the loaded image; none is a build-specific constant.
type cursorMouseServiceChain struct {
	singleton      uintptr
	gameOffset     uint32
	controlOffset  uint32
	providerOffset uint32
	indexGuard     uintptr
	serviceIndex   uintptr
	vectorOffset   uint32
}

// cursorBranchTarget decodes a direct call or jump rel32 at off.
func cursorBranchTarget(code []byte, addr uintptr, off int) uintptr {
	return uintptr(int64(addr) + int64(off+5) + int64(int32(binary.LittleEndian.Uint32(code[off+1:]))))
}

// cursorDecodeResolver proves the reflection resolver calls
// ServiceProvider::create<MouseService> and dereferences the returned shared_ptr.
func cursorDecodeResolver(code []byte, addr uintptr) (create uintptr, ok bool) {
	if len(code) < 31 ||
		!bytes.Equal(code[:5], []byte{0x55, 0x48, 0x89, 0xe5, 0xe8}) ||
		!bytes.Equal(code[9:17], []byte{0x48, 0x85, 0xc0, 0x74, 0x0d, 0x48, 0x89, 0xc7}) ||
		code[17] != 0xe8 ||
		!bytes.Equal(code[22:31], []byte{0x48, 0x8b, 0x00, 0xeb, 0x02, 0x31, 0xc0, 0x5d, 0xc3}) {
		return 0, false
	}
	return cursorBranchTarget(code, addr, 17), true
}

// cursorDecodeCreate finds the service-index wrapper and service-vector helper
// at their validated call sites in ServiceProvider::create<MouseService>.
func cursorDecodeCreate(code []byte, addr uintptr) (indexWrapper, vector uintptr, ok bool) {
	if len(code) < 49 || code[33] != 0xe8 || code[44] != 0xe8 {
		return 0, 0, false
	}
	return cursorBranchTarget(code, addr, 33), cursorBranchTarget(code, addr, 44), true
}

func cursorDecodeIndexWrapper(code []byte, addr uintptr) (index uintptr, ok bool) {
	if len(code) < 27 || code[22] != 0xe9 {
		return 0, false
	}
	return cursorBranchTarget(code, addr, 22), true
}

// cursorDecodeIndex recovers the RIP-relative initialization guard and service
// index. Requiring one of each prevents an ambiguous decode.
func cursorDecodeIndex(code []byte, addr uintptr) (guard, index uintptr, ok bool) {
	guardAt, indexAt := -1, -1
	for i := 0; i+6 <= len(code); i++ {
		if code[i] == 0x8a && code[i+1] == 0x05 {
			if guard != 0 {
				return 0, 0, false
			}
			guardAt = i
			guard = uintptr(int64(addr) + int64(i+6) + int64(int32(binary.LittleEndian.Uint32(code[i+2:]))))
		}
		if i+7 <= len(code) && code[i] == 0x48 && code[i+1] == 0x8b && code[i+2] == 0x05 {
			if index != 0 {
				return 0, 0, false
			}
			indexAt = i
			index = uintptr(int64(addr) + int64(i+7) + int64(int32(binary.LittleEndian.Uint32(code[i+3:]))))
		}
	}
	if guard == 0 || index == 0 || guardAt >= indexAt {
		return 0, 0, false
	}
	return guard, index, true
}

// cursorDecodeVector recovers ServiceProvider's shared_ptr vector bounds.
func cursorDecodeVector(code []byte) (begin, end uint32, ok bool) {
	if len(code) < 47 ||
		!bytes.Equal(code[33:36], []byte{0x48, 0x8b, 0x87}) ||
		!bytes.Equal(code[40:43], []byte{0x48, 0x8b, 0x8f}) {
		return 0, 0, false
	}
	begin = binary.LittleEndian.Uint32(code[36:40])
	end = binary.LittleEndian.Uint32(code[43:47])
	if begin == 0 || begin > mouseBehaviorMaxOffset || begin%8 != 0 || end != begin+8 {
		return 0, 0, false
	}
	return begin, end, true
}

// cursorDecodeGame recovers the Game weak_ptr's object and control-block
// displacements. The control block immediately follows the object pointer.
func cursorDecodeGame(code []byte) (game, control uint32, ok bool) {
	if len(code) < 42 ||
		!bytes.Equal(code[10:13], []byte{0x48, 0x8b, 0xbf}) ||
		!bytes.Equal(code[35:38], []byte{0x48, 0x8b, 0x9b}) {
		return 0, 0, false
	}
	control = binary.LittleEndian.Uint32(code[13:17])
	game = binary.LittleEndian.Uint32(code[38:42])
	if game == 0 || game > mouseBehaviorMaxOffset || game%8 != 0 || control != game+8 {
		return 0, 0, false
	}
	return game, control, true
}

// cursorBridgeCandidate identifies the engine bridge from getSingleton(index)
// through getGame and Game's ServiceProvider to create<MouseService>.
type cursorBridgeCandidate struct {
	bridge         uintptr
	getGame        uintptr
	providerOffset uint32
}

func cursorDecodeBridge(code []byte, addr uintptr, layout mouseBehaviorLayout, create uintptr) (cursorBridgeCandidate, bool) {
	var out cursorBridgeCandidate
	if len(code) < 50 ||
		!bytes.Equal(code[:4], []byte{0x55, 0x48, 0x89, 0xe5}) || code[4] != 0xbf ||
		binary.LittleEndian.Uint32(code[5:9]) != layout.index || code[9] != 0xe8 ||
		cursorBranchTarget(code, addr, 9) != layout.accessor ||
		!bytes.Equal(code[14:17], []byte{0x48, 0x89, 0xc7}) || code[17] != 0xe8 ||
		!bytes.Equal(code[22:27], []byte{0x48, 0x85, 0xc0, 0x74, 0x13}) ||
		!bytes.Equal(code[27:29], []byte{0x48, 0x05}) ||
		!bytes.Equal(code[33:36], []byte{0x48, 0x89, 0xc7}) || code[36] != 0xe8 ||
		cursorBranchTarget(code, addr, 36) != create ||
		!bytes.Equal(code[41:50], []byte{0x48, 0x8b, 0x00, 0xeb, 0x02, 0x31, 0xc0, 0x5d, 0xc3}) {
		return out, false
	}
	out = cursorBridgeCandidate{
		bridge:         addr,
		getGame:        cursorBranchTarget(code, addr, 17),
		providerOffset: binary.LittleEndian.Uint32(code[29:33]),
	}
	if out.providerOffset == 0 || out.providerOffset > mouseBehaviorMaxOffset || out.providerOffset%8 != 0 {
		return cursorBridgeCandidate{}, false
	}
	return out, true
}

// cursorBridgeCallerProvesFields ties the bridge to both reflection properties:
// its caller reads MouseBehavior and MouseIconEnabled at the decoded offsets.
func cursorBridgeCallerProvesFields(code []byte, caller, bridge uintptr, behaviorOffset, iconOffset uint32) bool {
	return len(code) >= 70 && code[4] == 0xe8 &&
		cursorBranchTarget(code, caller, 4) == bridge &&
		bytes.Equal(code[14:16], []byte{0x8b, 0x88}) &&
		binary.LittleEndian.Uint32(code[16:20]) == behaviorOffset &&
		bytes.Equal(code[42:45], []byte{0x0f, 0xb6, 0x80}) &&
		binary.LittleEndian.Uint32(code[45:49]) == iconOffset
}

// locateCursorBridge finds the unique bridge matching the already-decoded
// singleton accessor, index and create<MouseService> target.
func locateCursorBridge(ranges []engineAddrRange, base, end uintptr, layout mouseBehaviorLayout, create uintptr) []cursorBridgeCandidate {
	var found []cursorBridgeCandidate
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
			if code, ok := engineMem.bytes(lo, int(n)); ok {
				for i := 0; i+50 <= len(code); i++ {
					candidate, ok := cursorDecodeBridge(code[i:], lo+uintptr(i), layout, create)
					if !ok || candidate.getGame < base || candidate.getGame >= end {
						continue
					}
					seen := false
					for _, previous := range found {
						if previous == candidate {
							seen = true
							break
						}
					}
					if !seen {
						found = append(found, candidate)
					}
				}
			}
			if n == hi-lo {
				break
			}
			lo += n - cursorScanOverlap
		}
	}
	return found
}

// cursorHiddenValue interprets the MouseIconEnabled byte. The property is the
// inverse of "cursor hidden". Anything but 0 or 1 is not a boolean and is
// rejected; the caller latches the reader off.
func cursorHiddenValue(value byte) (hidden, ok bool) {
	if value > 1 {
		return false, false
	}
	return value == 0, true
}

// cursorTrace is the last value TIPSY_TRACE_CURSOR_STATE reported.
type cursorTrace uint8

const (
	cursorTraceNone cursorTrace = iota
	cursorTraceUnavailable
	cursorTraceVisible
	cursorTraceHidden
)

func (c cursorTrace) String() string {
	switch c {
	case cursorTraceUnavailable:
		return "unavailable"
	case cursorTraceVisible:
		return "visible"
	case cursorTraceHidden:
		return "hidden"
	default:
		return "none"
	}
}

// cursorHiddenReader owns the locator result and the fail-closed latch.
type cursorHiddenReader struct {
	mu        sync.Mutex
	attempted bool
	// disabled latches the reader off for the life of the process: lookup,
	// decode or validation failed. A latched reader never reports ok.
	disabled          bool
	candidate         cursorHiddenCandidate
	behaviorCandidate cursorHiddenCandidate
	enumOffset        uint32
	// resolve returns the object the getter reads its byte from, or false when
	// there is none yet (before a game loads, or in Home). nil means no
	// resolver is proven safe, which keeps the reader off.
	resolve func() (uintptr, bool)

	trace bool
	last  cursorTrace
	logf  func(msg string, args ...any)
}

// cursorHiddenAuthority is the process-wide reader.
var cursorHiddenAuthority cursorHiddenReader

func (r *cursorHiddenReader) note(format string, args ...any) {
	if !r.trace {
		return
	}
	logf := r.logf
	if logf == nil {
		logf = logging.Logger(logging.CatJNI).Info
	}
	logf("[jni] cursor state: " + fmt.Sprintf(format, args...))
}

func (r *cursorHiddenReader) latchOff(reason string) {
	if r.disabled {
		return
	}
	r.disabled = true
	r.note("reader off (%s); cursor stays free", reason)
}

// transition logs a change of state, never a repeat.
func (r *cursorHiddenReader) transition(to cursorTrace, raw int) {
	if to == r.last {
		return
	}
	from := r.last
	r.last = to
	if raw >= 0 {
		r.note("%s -> %s (MouseIconEnabled byte %d)", from, to, raw)
		return
	}
	r.note("%s -> %s", from, to)
}

// proveMouseServiceChain decodes and cross-checks every hop in the
// UserInputService reflection resolver's path to MouseService.
func (r *cursorHiddenReader) proveMouseServiceChain(ranges []engineAddrRange, base, end uintptr, layout mouseBehaviorLayout) (cursorMouseServiceChain, bool) {
	var chain cursorMouseServiceChain
	fail := func(stage string) {
		r.note("getter found, reader off: MouseService identity proof failed at %s resolver_rva=%#x cursor_offset=%#x behavior_offset=%#x",
			stage, r.candidate.resolver-base, r.candidate.offset, r.behaviorCandidate.offset)
	}
	inImage := func(addr uintptr) bool { return addr >= base && addr < end }

	resolver, ok := engineMem.bytes(r.candidate.resolver, 31)
	if !ok {
		fail("unreadable resolver")
		return chain, false
	}
	create, ok := cursorDecodeResolver(resolver, r.candidate.resolver)
	if !ok || !inImage(create) {
		fail("resolver shape")
		return chain, false
	}
	createBody, ok := engineMem.bytes(create, 49)
	if !ok {
		fail("unreadable create")
		return chain, false
	}
	indexWrapper, vector, ok := cursorDecodeCreate(createBody, create)
	if !ok || !inImage(indexWrapper) || !inImage(vector) {
		fail("create shape")
		return chain, false
	}
	wrapperBody, ok := engineMem.bytes(indexWrapper, 27)
	if !ok {
		fail("unreadable index wrapper")
		return chain, false
	}
	indexFn, ok := cursorDecodeIndexWrapper(wrapperBody, indexWrapper)
	if !ok || !inImage(indexFn) {
		fail("index wrapper shape")
		return chain, false
	}
	indexBody, ok := engineMem.bytes(indexFn, 0x60)
	if !ok {
		fail("unreadable index")
		return chain, false
	}
	guard, serviceIndex, ok := cursorDecodeIndex(indexBody, indexFn)
	if !ok || !inImage(guard) || !inImage(serviceIndex) || serviceIndex%8 != 0 {
		fail("index shape")
		return chain, false
	}
	vectorBody, ok := engineMem.bytes(vector, 47)
	if !ok {
		fail("unreadable service vector")
		return chain, false
	}
	vectorOffset, _, ok := cursorDecodeVector(vectorBody)
	if !ok {
		fail("service vector shape")
		return chain, false
	}

	bridges := locateCursorBridge(ranges, base, end, layout, create)
	if len(bridges) != 1 {
		fail(fmt.Sprintf("bridge lookup found %d candidates", len(bridges)))
		return chain, false
	}
	bridge := bridges[0]
	if bridge.bridge-base < 70 {
		fail("bridge caller bounds")
		return chain, false
	}
	caller := bridge.bridge - 70
	callerBody, ok := engineMem.bytes(caller, 70)
	if !ok || !cursorBridgeCallerProvesFields(callerBody, caller, bridge.bridge, r.behaviorCandidate.offset, r.candidate.offset) {
		fail("bridge caller fields")
		return chain, false
	}
	gameBody, ok := engineMem.bytes(bridge.getGame, 42)
	if !ok {
		fail("unreadable getGame")
		return chain, false
	}
	gameOffset, controlOffset, ok := cursorDecodeGame(gameBody)
	if !ok {
		fail("getGame shape")
		return chain, false
	}
	chain = cursorMouseServiceChain{
		singleton:      layout.singleton,
		gameOffset:     gameOffset,
		controlOffset:  controlOffset,
		providerOffset: bridge.providerOffset,
		indexGuard:     guard,
		serviceIndex:   serviceIndex,
		vectorOffset:   vectorOffset,
	}
	return chain, true
}

// resolveMouseService follows the proven chain without calling engine code.
// NULL and not-yet-initialized values are normal Home/pre-load states. A broken
// readable pointer chain is a decode failure and latches the reader off.
func (r *cursorHiddenReader) resolveMouseService(chain cursorMouseServiceChain) (uintptr, bool) {
	if mouseBehaviorAuthority.disabled.Load() {
		r.latchOff("MouseBehavior authority disabled")
		return 0, false
	}
	game, ok := engineMem.u64(chain.singleton + uintptr(chain.gameOffset))
	if !ok {
		r.latchOff("Game slot is not readable")
		return 0, false
	}
	if game == 0 {
		return 0, false
	}
	control, ok := engineMem.u64(chain.singleton + uintptr(chain.controlOffset))
	if !ok {
		r.latchOff("Game control-block slot is not readable")
		return 0, false
	}
	if control == 0 {
		return 0, false
	}
	owners, ok := engineMem.u64(uintptr(control) + 8)
	if !ok {
		r.latchOff("Game control block is not readable")
		return 0, false
	}
	if owners == 0 {
		return 0, false
	}
	guard, ok := engineMem.bytes(chain.indexGuard, 1)
	if !ok {
		r.latchOff("MouseService index guard is not readable")
		return 0, false
	}
	if guard[0] == 0 {
		return 0, false
	}
	index, ok := engineMem.u64(chain.serviceIndex)
	if !ok {
		r.latchOff("MouseService index is not readable")
		return 0, false
	}
	provider := uintptr(game) + uintptr(chain.providerOffset)
	vectorBegin, ok := engineMem.u64(provider + uintptr(chain.vectorOffset))
	if !ok {
		r.latchOff("ServiceProvider vector begin is not readable")
		return 0, false
	}
	vectorEnd, ok := engineMem.u64(provider + uintptr(chain.vectorOffset) + 8)
	if !ok {
		r.latchOff("ServiceProvider vector end is not readable")
		return 0, false
	}
	if vectorEnd < vectorBegin || (vectorEnd-vectorBegin)%16 != 0 || (vectorBegin == 0) != (vectorEnd == 0) {
		r.latchOff("ServiceProvider vector bounds are implausible")
		return 0, false
	}
	if index >= (vectorEnd-vectorBegin)>>4 {
		return 0, false
	}
	service, ok := engineMem.u64(uintptr(vectorBegin) + uintptr(index<<4))
	if !ok {
		r.latchOff("MouseService vector entry is not readable")
		return 0, false
	}
	return uintptr(service), service != 0
}

// proveResolver verifies the shared reflection resolver. The old direct
// MainWindow shape remains valid when its MouseBehavior displacement agrees;
// otherwise the full MouseService identity proof must succeed.
func (r *cursorHiddenReader) proveResolver(ranges []engineAddrRange, base, end uintptr) {
	behavior := locateCursorGetter(ranges, base, end, "MouseBehavior")
	if len(behavior) != 1 {
		r.note("getter found, reader off: MouseBehavior getter lookup found %d candidates getter_rva=%#x resolver_rva=%#x offset=%#x",
			len(behavior), r.candidate.getter-base, r.candidate.resolver-base, r.candidate.offset)
		return
	}
	r.behaviorCandidate = behavior[0]
	if r.candidate.resolver != r.behaviorCandidate.resolver {
		r.note("getter found, reader off: resolver mismatch cursor_resolver_rva=%#x behavior_resolver_rva=%#x cursor_offset=%#x behavior_offset=%#x",
			r.candidate.resolver-base, r.behaviorCandidate.resolver-base, r.candidate.offset, r.behaviorCandidate.offset)
		return
	}
	lockGetter := robloxDirectMouseLockedFn()
	if lockGetter == 0 || mouseBehaviorAuthority.disabled.Load() {
		r.note("getter found, reader off: LockCenter getter unavailable resolver_rva=%#x cursor_offset=%#x behavior_offset=%#x",
			r.candidate.resolver-base, r.candidate.offset, r.behaviorCandidate.offset)
		return
	}
	layout, err := decodeMouseBehavior(lockGetter)
	if err != nil {
		r.note("getter found, reader off: LockCenter decode failed (%v) resolver_rva=%#x cursor_offset=%#x behavior_offset=%#x",
			err, r.candidate.resolver-base, r.candidate.offset, r.behaviorCandidate.offset)
		return
	}
	if r.behaviorCandidate.offset == layout.enumOffset {
		slot := layout.singleton + uintptr(layout.objOffset)
		r.enumOffset = layout.enumOffset
		r.resolve = func() (uintptr, bool) {
			if mouseBehaviorAuthority.disabled.Load() {
				r.latchOff("MouseBehavior authority disabled")
				return 0, false
			}
			obj, ok := engineMem.u64(slot)
			if !ok {
				r.latchOff("singleton object slot is not readable")
				return 0, false
			}
			return uintptr(obj), obj != 0
		}
	} else {
		chain, ok := r.proveMouseServiceChain(ranges, base, end, layout)
		if !ok {
			return
		}
		r.enumOffset = r.behaviorCandidate.offset
		r.resolve = func() (uintptr, bool) { return r.resolveMouseService(chain) }
	}
	r.note("getter found, resolver proven getter_rva=%#x resolver_rva=%#x offset=%#x behavior_offset=%#x",
		r.candidate.getter-base, r.candidate.resolver-base, r.candidate.offset, r.enumOffset)
}

// init runs the locator once over the readable mappings of [base, end).
func (r *cursorHiddenReader) init(ranges []engineAddrRange, base uintptr, size uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempted {
		return
	}
	r.attempted = true
	r.trace = os.Getenv("TIPSY_TRACE_CURSOR_STATE") == "1"
	if base == 0 || size == 0 || uint64(uintptr(size)) != size || base+uintptr(size) < base {
		r.latchOff("bad image bounds")
		return
	}
	end := base + uintptr(size)
	found := locateCursorGetter(ranges, base, end, "MouseIconEnabled")
	if len(found) != 1 {
		r.latchOff(fmt.Sprintf("getter lookup found %d candidates", len(found)))
		return
	}
	r.candidate = found[0]
	if r.resolve == nil {
		r.proveResolver(ranges, base, end)
		return
	}
	r.note("getter found getter_rva=%#x resolver_rva=%#x offset=%#x",
		found[0].getter-base, found[0].resolver-base, found[0].offset)
}

// read returns the cursor-hidden state. ok is false whenever the reader is
// off, has no object yet, or fails validation; a free cursor is the answer
// then, never a guess.
func (r *cursorHiddenReader) read() (hidden, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled || r.candidate.getter == 0 || r.resolve == nil {
		return false, false
	}
	obj, have := r.resolve()
	if r.disabled {
		return false, false
	}
	if !have || obj == 0 {
		// No object is the pre-game or Home window, not a failure: stay
		// unavailable without latching.
		r.transition(cursorTraceUnavailable, -1)
		return false, false
	}
	if r.enumOffset != 0 {
		beh, readable := engineMem.u32(obj + uintptr(r.enumOffset))
		if !readable {
			r.latchOff("object MouseBehavior is not readable")
			return false, false
		}
		if beh > uint32(MouseBehaviorLockCurrentPosition) {
			r.latchOff(fmt.Sprintf("read MouseBehavior %d, which is not an Enum.MouseBehavior value", beh))
			return false, false
		}
	}
	b, readable := engineMem.bytes(obj+uintptr(r.candidate.offset), 1)
	if !readable {
		r.latchOff("object byte is not readable")
		return false, false
	}
	hidden, ok = cursorHiddenValue(b[0])
	if !ok {
		r.latchOff(fmt.Sprintf("read %d, which is not a boolean", b[0]))
		return false, false
	}
	if hidden {
		r.transition(cursorTraceHidden, int(b[0]))
	} else {
		r.transition(cursorTraceVisible, int(b[0]))
	}
	return hidden, true
}

func resetEngineCursorHidden() {
	cursorHiddenAuthority.mu.Lock()
	cursorHiddenAuthority.attempted = false
	cursorHiddenAuthority.disabled = false
	cursorHiddenAuthority.candidate = cursorHiddenCandidate{}
	cursorHiddenAuthority.behaviorCandidate = cursorHiddenCandidate{}
	cursorHiddenAuthority.enumOffset = 0
	cursorHiddenAuthority.resolve = nil
	cursorHiddenAuthority.trace = false
	cursorHiddenAuthority.last = cursorTraceNone
	cursorHiddenAuthority.logf = nil
	cursorHiddenAuthority.mu.Unlock()
}

// InitEngineCursorHidden locates the MouseIconEnabled getter in the loaded
// libroblox.so and runs the load-time shared-resolver proof. It never calls
// engine code and is idempotent.
func InitEngineCursorHidden(base uintptr, size uint64) {
	ranges, err := readProcSelfMaps()
	if err != nil {
		ranges = nil // the locator then finds nothing and latches the reader off
	}
	cursorHiddenAuthority.init(ranges, base, size)
}

// EngineCursorHidden reports whether the engine currently hides the mouse
// cursor (MouseIconEnabled false). ok is false when the answer is unavailable
// for any reason; callers must then treat the cursor as free.
func EngineCursorHidden() (hidden, ok bool) {
	return cursorHiddenAuthority.read()
}
