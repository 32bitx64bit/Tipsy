// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

// Read-only reflection scan helpers: locate a property getter through its
// reflection registration, and decode the ServiceProvider path to a service.
// The camera reader uses them. No engine memory is written and no engine code
// runs.
package jni

import (
	"bytes"
	"encoding/binary"
)

// reflectionGetterCandidate is a getter found through its reflection descriptor,
// the object resolver it calls, and the field offset it reads from that object.
type reflectionGetterCandidate struct {
	getter   uintptr
	resolver uintptr
	offset   uint32
}

// cursorScanOverlap keeps an instruction sequence that straddles two scan
// chunks whole in at least one of them.
const (
	cursorScanOverlap = 87
	cursorScanChunk   = 1 << 20
)

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
		bytes.Equal(p[21:24], []byte{0x48, 0x8d, 0x0d}) {
		direct := bytes.Equal(p[14:17], []byte{0x48, 0x8d, 0x15}) &&
			bytes.Equal(p[28:31], []byte{0x4c, 0x8d, 0x05}) && bytes.Equal(p[35:38], []byte{0x45, 0x31, 0xc9}) && p[38] == 0xe8
		stack := len(p) >= 46 && bytes.Equal(p[14:17], []byte{0x48, 0x8d, 0x15}) &&
			bytes.Equal(p[28:31], []byte{0x48, 0x8d, 0x9d}) && bytes.Equal(p[35:42], []byte{0x49, 0x89, 0xd8, 0x45, 0x31, 0xc9, 0xe8})
		// 2.742 passes the category in rbx: lea rbx(category), then after the
		// getter lea it copies mov rbx,rdx and mov r15,r8 before xor r9d,r9d; call.
		bypass := bytes.Equal(p[14:17], []byte{0x48, 0x8d, 0x1d}) &&
			bytes.Equal(p[28:31], []byte{0x48, 0x89, 0xda}) &&
			bytes.Equal(p[34:38], []byte{0x45, 0x31, 0xc9, 0xe8})
		if direct || stack || bypass {
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
// prop. The whole instruction sequence must
// match, not just a reference to the name. Addresses must fall inside
// [base, end).
func cursorCandidates(code []byte, addr, base, end uintptr, prop string) []reflectionGetterCandidate {
	want := prop + "\x00"
	wantLen := uintptr(len(want))

	var out []reflectionGetterCandidate
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
		if resolver, offset, ok := cursorGetterDecode(body, getter, true); ok && resolver >= base && resolver < end {
			out = append(out, reflectionGetterCandidate{getter: getter, resolver: resolver, offset: offset})
		}
	}
	return out
}

// uniqueCursorCandidates drops repeats: the overlap between chunks can find
// one registration twice, and that is not ambiguity.
func uniqueCursorCandidates(in []reflectionGetterCandidate) []reflectionGetterCandidate {
	var out []reflectionGetterCandidate
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

// scanCursorChunks visits readable image chunks through one reusable buffer.
// overlap keeps strings or instructions split by a chunk boundary visible.
func scanCursorChunks(ranges []engineAddrRange, base, end uintptr, buf []byte, overlap uintptr, visit func([]byte, uintptr)) {
	chunk := uintptr(len(buf))
	if chunk == 0 || overlap >= chunk {
		return
	}
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
			if engineMem.read(lo, buf[:int(n)]) {
				visit(buf[:int(n)], lo)
			}
			if n == hi-lo {
				break
			}
			lo += n - overlap
		}
	}
}

// locateCursorGetter finds the property-name strings first, then decodes only
// registration-shaped code whose RIP-relative name references target one of
// those strings. This avoids running the full instruction decoder at every
// byte while retaining the complete registration and getter validation.
func locateCursorGetter(ranges []engineAddrRange, base, end uintptr, prop string) []reflectionGetterCandidate {
	want := []byte(prop + "\x00")
	buf := make([]byte, cursorScanChunk)

	var names []uintptr
	scanCursorChunks(ranges, base, end, buf, uintptr(len(want)-1), func(code []byte, addr uintptr) {
		for off := 0; off < len(code); {
			i := bytes.Index(code[off:], want)
			if i < 0 {
				break
			}
			names = append(names, addr+uintptr(off+i))
			off += i + len(want)
		}
	})
	if len(names) == 0 {
		return nil
	}

	nameTarget := func(addr uintptr) bool {
		for _, name := range names {
			if addr == name {
				return true
			}
		}
		return false
	}
	leaPrefix := []byte{0x48, 0x8d}
	var found []reflectionGetterCandidate
	scanCursorChunks(ranges, base, end, buf, cursorScanOverlap, func(code []byte, addr uintptr) {
		for i := 0; i+7 <= len(code); {
			next := bytes.Index(code[i:], leaPrefix)
			if next < 0 {
				break
			}
			i += next
			if i+7 > len(code) {
				break
			}
			back := 0
			switch code[i+2] {
			case 0x35: // name argument in the four-lea registration
				back = 7
			case 0x0d: // name argument in the inlined registration
				back = 7
			case 0x15: // second name reference in the four-lea registration
				back = 14
			}
			if back != 0 && nameTarget(cursorRelative(code, addr, i)) && i >= back {
				start := i - back
				stop := start + cursorScanOverlap
				if stop > len(code) {
					stop = len(code)
				}
				found = append(found, cursorCandidates(code[start:stop], addr+uintptr(start), base, end, prop)...)
			}
			i += 2
		}
	})
	return uniqueCursorCandidates(found)
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
