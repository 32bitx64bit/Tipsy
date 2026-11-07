// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

// Engine camera reader.
//
// Camera.CFrame, Camera.Focus and Camera.CameraType are plain fields of the
// engine's Camera object, and Workspace.CurrentCamera is a plain pointer field
// of Workspace. Their reflection descriptors name getters that are bare
// `mov/lea disp(%rdi)` bodies, and the matching setters carry the receiver
// adjustment (`lea -adjust(%rdi)`) from the Instance to the reflection
// receiver. Every offset is decoded from the loaded image; none is a
// per-build constant.
//
// The Camera is reached the way the engine reaches a service: singleton ->
// Game weak_ptr -> ServiceProvider -> service vector. The vector index of
// Workspace is not decoded; each entry is probed and accepted only when its
// CurrentCamera slot points at an object whose CFrame and Focus are
// orthonormal.
//
// A goroutine samples the camera for the first-person capture detector. With
// TIPSY_TRACE_CAMERA=1 it also logs CameraType and the camera-to-focus distance,
// or the exact reason there is no answer. No engine memory is written, no
// engine code runs, and a miss never stops the sampling.
package jni

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// engineCameraTypeMax bounds a plausible Enum.CameraType word.
const engineCameraTypeMax = 15

func engineCameraTypeName(v uint32) string {
	names := [...]string{"Fixed", "Attach", "Watch", "Track", "Follow", "Custom", "Scriptable", "Orbital"}
	if int(v) < len(names) {
		return fmt.Sprintf("%s(%d)", names[v], v)
	}
	return fmt.Sprintf("CameraType(%d)", v)
}

// cameraFieldKind is what a bare reflection getter does with its receiver.
type cameraFieldKind uint8

const (
	cameraFieldNone cameraFieldKind = iota
	cameraFieldAddr                 // lea  disp(%rdi),%rax: returns a reference to the field
	cameraFieldU32                  // mov  disp(%rdi),%eax: returns a 32-bit field
	cameraFieldU64                  // mov  disp(%rdi),%rax: returns a pointer field
)

// decodeCameraFieldGetter decodes a bare getter: one load or lea from the
// receiver in %rdi, then ret.
func decodeCameraFieldGetter(code []byte) (kind cameraFieldKind, disp uint32, ok bool) {
	var op []byte
	switch {
	case bytes.HasPrefix(code, []byte{0x48, 0x8d}):
		kind, op = cameraFieldAddr, code[2:]
	case bytes.HasPrefix(code, []byte{0x48, 0x8b}):
		kind, op = cameraFieldU64, code[2:]
	case bytes.HasPrefix(code, []byte{0x8b}):
		kind, op = cameraFieldU32, code[1:]
	default:
		return cameraFieldNone, 0, false
	}
	// modrm: 0x47 is disp8(%rdi) into %rax, 0x87 is disp32(%rdi) into %rax.
	switch {
	case len(op) >= 3 && op[0] == 0x47 && op[2] == 0xc3:
		disp = uint32(op[1])
	case len(op) >= 6 && op[0] == 0x87 && op[5] == 0xc3:
		disp = binary.LittleEndian.Uint32(op[1:])
	default:
		return cameraFieldNone, 0, false
	}
	align := uint32(4)
	if kind != cameraFieldU32 {
		align = 8
	}
	if disp == 0 || disp > mouseBehaviorMaxOffset || disp%align != 0 {
		return cameraFieldNone, 0, false
	}
	return kind, disp, true
}

// decodeCameraSetterAdjust finds the receiver adjustment in a reflection
// setter prologue: the first `lea -adjust(%rdi),%reg` turns the reflection
// receiver back into the Instance.
func decodeCameraSetterAdjust(code []byte) (adjust uint32, ok bool) {
	for i := 0; i+7 <= len(code); i++ {
		if (code[i] != 0x48 && code[i] != 0x4c) || code[i+1] != 0x8d || code[i+2]&0xc7 != 0x87 {
			continue
		}
		disp := int32(binary.LittleEndian.Uint32(code[i+3:]))
		if disp >= 0 || -int64(disp) > mouseBehaviorMaxOffset || disp%8 != 0 {
			continue
		}
		return uint32(-disp), true
	}
	return 0, false
}

// decodeCameraAccessor decodes the property accessor object a registration
// builds: `mov $0x28,%edi ; call operator new`, then stores of the vtable, the
// getter, an adjustment, the setter and an adjustment at +0, +8, +0x10, +0x18
// and +0x20. Registers are tracked only as far as their last RIP-relative
// `lea`, because the compiler picks different registers per build.
func decodeCameraAccessor(code []byte, addr uintptr) (getter, setter uintptr, ok bool) {
	if len(code) < 10 || !bytes.Equal(code[:6], []byte{0xbf, 0x28, 0x00, 0x00, 0x00, 0xe8}) {
		return 0, 0, false
	}
	var leaTarget [16]uintptr
	var stored [5]bool
	var value [5]uintptr
	count := 0
	for i := 10; i+4 <= len(code) && count < len(stored); {
		b := code[i:]
		switch {
		case (b[0] == 0x48 || b[0] == 0x4c) && b[1] == 0x8d && b[2]&0xc7 == 0x05 && len(b) >= 7:
			reg := int(b[2]>>3&7) | int(b[0]&4)<<1
			leaTarget[reg] = cursorRelative(code, addr, i)
			i += 7
		case (b[0] == 0x48 || b[0] == 0x4c) && b[1] == 0x89 && (b[2]&0xc7 == 0x00 || b[2]&0xc7 == 0x40):
			reg := int(b[2]>>3&7) | int(b[0]&4)<<1
			off, n := 0, 3
			if b[2]&0xc0 == 0x40 {
				off, n = int(b[3]), 4
			}
			if off%8 != 0 || off/8 >= len(stored) {
				return 0, 0, false
			}
			if !stored[off/8] {
				stored[off/8] = true
				value[off/8] = leaTarget[reg]
				count++
			}
			i += n
		case b[0] == 0x31 && b[1]&0xc0 == 0xc0 && b[1]&7 == b[1]>>3&7: // xor r32,r32
			leaTarget[b[1]&7] = 0
			i += 2
		case b[0] == 0x45 && b[1] == 0x31 && b[2]&0xc0 == 0xc0 && b[2]&7 == b[2]>>3&7: // xor r8d..r15d
			leaTarget[8|b[2]&7] = 0
			i += 3
		default:
			return 0, 0, false
		}
	}
	if count != len(stored) || value[0] == 0 || value[1] == 0 || value[3] == 0 {
		return 0, 0, false
	}
	// A receiver adjustment held in a register that was just `lea`ed would
	// be a code pointer, not an offset: not this shape.
	if value[2] != 0 || value[4] != 0 {
		return 0, 0, false
	}
	return value[1], value[3], true
}

// cameraProp is one decoded plain-field property.
type cameraProp struct {
	ref       uintptr
	getter    uintptr
	setter    uintptr
	kind      cameraFieldKind
	disp      uint32
	adjust    uint32 // receiver adjustment from the setter prologue
	hasAdjust bool
}

const (
	cameraRefBefore = 0x60 // accessor windows around a name reference
	cameraRefAfter  = 0x70
)

// cameraPropAt decodes the property whose name is referenced at ref. The
// accessor object is built within a short window of the name reference: before
// it for plain properties, after it for enum properties.
func cameraPropAt(ref, base, end uintptr) (cameraProp, bool) {
	var prop cameraProp
	if ref < base+cameraRefBefore || ref+cameraRefAfter+0x20 > end {
		return prop, false
	}
	start := ref - cameraRefBefore
	win, ok := engineMem.bytes(start, cameraRefBefore+cameraRefAfter+0x20)
	if !ok {
		return prop, false
	}
	var found []cameraProp
	for off := 0; off+10 <= len(win); off++ {
		i := bytes.Index(win[off:], []byte{0xbf, 0x28, 0x00, 0x00, 0x00, 0xe8})
		if i < 0 {
			break
		}
		off += i
		if off > cameraRefBefore+cameraRefAfter {
			break
		}
		getter, setter, ok := decodeCameraAccessor(win[off:], start+uintptr(off))
		if !ok || getter < base || getter >= end || setter < base || setter >= end {
			continue
		}
		found = append(found, cameraProp{ref: ref, getter: getter, setter: setter})
	}
	if len(found) != 1 {
		return prop, false
	}
	prop = found[0]
	if body, ok := engineMem.bytes(prop.getter, 8); ok {
		prop.kind, prop.disp, _ = decodeCameraFieldGetter(body)
	}
	if prop.kind == cameraFieldNone {
		return prop, false
	}
	if body, ok := engineMem.bytes(prop.setter, 0x60); ok {
		prop.adjust, prop.hasAdjust = decodeCameraSetterAdjust(body)
	}
	return prop, true
}

// locateCameraProps finds, for each name, every registration whose getter is a
// bare field getter. Names are matched exactly like locateCursorGetter does:
// the string first, then the RIP-relative references to it.
func locateCameraProps(ranges []engineAddrRange, base, end uintptr, names ...string) map[string][]cameraProp {
	buf := make([]byte, cursorScanChunk)
	type nameAddr struct {
		addr uintptr
		name string
	}
	var strs []nameAddr
	for _, name := range names {
		want := []byte(name + "\x00")
		scanCursorChunks(ranges, base, end, buf, uintptr(len(want)-1), func(code []byte, addr uintptr) {
			for off := 0; off < len(code); {
				i := bytes.Index(code[off:], want)
				if i < 0 {
					break
				}
				strs = append(strs, nameAddr{addr + uintptr(off+i), name})
				off += i + len(want)
			}
		})
	}
	out := map[string][]cameraProp{}
	if len(strs) == 0 {
		return out
	}
	seen := map[uintptr]bool{}
	scanCursorChunks(ranges, base, end, buf, cursorScanOverlap, func(code []byte, addr uintptr) {
		for i := 0; i+7 <= len(code); {
			next := bytes.IndexByte(code[i+1:], 0x8d)
			if next < 0 {
				break
			}
			i += next // index of the byte before 0x8d
			if i+7 > len(code) {
				break
			}
			if (code[i] == 0x48 || code[i] == 0x4c) && code[i+2]&0xc7 == 0x05 {
				target := cursorRelative(code, addr, i)
				ref := addr + uintptr(i)
				for _, s := range strs {
					if s.addr != target || seen[ref] {
						continue
					}
					seen[ref] = true
					if prop, ok := cameraPropAt(ref, base, end); ok {
						out[s.name] = append(out[s.name], prop)
					}
				}
			}
			i++
		}
	})
	return out
}

// engineCameraLayout is everything the decode recovers. Offsets are
// displacements; addresses are live process addresses.
type engineCameraLayout struct {
	// Camera: fields are read at instance + cameraAdjust + field.
	cframe, focus, kind uint32
	cameraAdjust        uint32
	// Workspace: the CurrentCamera pointer is read at
	// instance + workspaceAdjust + cameraSlot.
	cameraSlot      uint32
	workspaceAdjust uint32
	// Service chain: singleton + gameOffset holds the Game weak_ptr object
	// pointer, controlOffset its control block, and the ServiceProvider's
	// vector of 16-byte shared_ptr entries lives at
	// game + providerOffset + vectorOffset.
	singleton      uintptr
	gameOffset     uint32
	controlOffset  uint32
	providerOffset uint32
	vectorOffset   uint32
}

var errEngineCameraShape = errors.New("engine camera does not match the decoded shape")

// pickCameraProp returns the one candidate of kind that keep accepts.
func pickCameraProp(name string, in []cameraProp, kind cameraFieldKind, keep func(cameraProp) bool) (cameraProp, error) {
	var out []cameraProp
	for _, p := range in {
		if p.kind == kind && keep(p) {
			out = append(out, p)
		}
	}
	if len(out) != 1 {
		return cameraProp{}, fmt.Errorf("%w: %s has %d plain-field registrations, want 1", errEngineCameraShape, name, len(out))
	}
	return out[0], nil
}

// decodeEngineCameraProps recovers the Camera and Workspace field layout.
func decodeEngineCameraProps(ranges []engineAddrRange, base, end uintptr, l *engineCameraLayout) error {
	props := locateCameraProps(ranges, base, end, "CameraType", "Focus", "CFrame", "CurrentCamera")
	anyProp := func(cameraProp) bool { return true }

	kind, err := pickCameraProp("Camera.CameraType", props["CameraType"], cameraFieldU32, anyProp)
	if err != nil {
		return err
	}
	focus, err := pickCameraProp("Camera.Focus", props["Focus"], cameraFieldAddr, func(p cameraProp) bool { return p.hasAdjust })
	if err != nil {
		return err
	}
	// CFrame is registered by many classes; Camera's is the one whose setter
	// adjusts the receiver exactly as Camera.Focus's does.
	cframe, err := pickCameraProp("Camera.CFrame", props["CFrame"], cameraFieldAddr,
		func(p cameraProp) bool { return p.hasAdjust && p.adjust == focus.adjust })
	if err != nil {
		return err
	}
	ws, err := pickCameraProp("Workspace.CurrentCamera", props["CurrentCamera"], cameraFieldU64,
		func(p cameraProp) bool { return p.hasAdjust })
	if err != nil {
		return err
	}
	l.cframe, l.focus, l.kind = cframe.disp, focus.disp, kind.disp
	l.cameraAdjust = focus.adjust
	l.cameraSlot, l.workspaceAdjust = ws.disp, ws.adjust
	return nil
}

// decodeEngineCameraChain recovers the singleton -> Game -> ServiceProvider ->
// service vector path from the same loaded image the MouseBehavior authority
// decodes. The MouseBehavior reflection getter shares the UserInputService
// resolver that calls ServiceProvider::create, which reads the vector.
func decodeEngineCameraChain(ranges []engineAddrRange, base, end uintptr, l *engineCameraLayout) error {
	lockGetter := robloxDirectMouseLockedFn()
	if lockGetter == 0 {
		return fmt.Errorf("%w: no exported MouseBehavior getter", errEngineCameraShape)
	}
	layout, err := decodeMouseBehavior(lockGetter)
	if err != nil {
		return fmt.Errorf("MouseBehavior decode: %w", err)
	}
	behavior := locateCursorGetter(ranges, base, end, "MouseBehavior")
	if len(behavior) != 1 {
		return fmt.Errorf("%w: MouseBehavior getter lookup found %d candidates", errEngineCameraShape, len(behavior))
	}
	inImage := func(addr uintptr) bool { return addr >= base && addr < end }

	resolver, ok := engineMem.bytes(behavior[0].resolver, 31)
	if !ok {
		return fmt.Errorf("%w: unreadable resolver", errEngineCameraShape)
	}
	create, ok := cursorDecodeResolver(resolver, behavior[0].resolver)
	if !ok || !inImage(create) {
		return fmt.Errorf("%w: resolver shape", errEngineCameraShape)
	}
	createBody, ok := engineMem.bytes(create, 49)
	if !ok {
		return fmt.Errorf("%w: unreadable create", errEngineCameraShape)
	}
	_, vector, ok := cursorDecodeCreate(createBody, create)
	if !ok || !inImage(vector) {
		return fmt.Errorf("%w: create shape", errEngineCameraShape)
	}
	vectorBody, ok := engineMem.bytes(vector, 47)
	if !ok {
		return fmt.Errorf("%w: unreadable service vector helper", errEngineCameraShape)
	}
	vectorOffset, _, ok := cursorDecodeVector(vectorBody)
	if !ok {
		return fmt.Errorf("%w: service vector shape", errEngineCameraShape)
	}
	bridges := locateCursorBridge(ranges, base, end, layout, create)
	if len(bridges) != 1 {
		return fmt.Errorf("%w: bridge lookup found %d candidates", errEngineCameraShape, len(bridges))
	}
	gameBody, ok := engineMem.bytes(bridges[0].getGame, 42)
	if !ok {
		return fmt.Errorf("%w: unreadable getGame", errEngineCameraShape)
	}
	gameOffset, controlOffset, ok := cursorDecodeGame(gameBody)
	if !ok {
		return fmt.Errorf("%w: getGame shape", errEngineCameraShape)
	}
	l.singleton = layout.singleton
	l.gameOffset, l.controlOffset = gameOffset, controlOffset
	l.providerOffset, l.vectorOffset = bridges[0].providerOffset, vectorOffset
	return nil
}

// decodeEngineCamera runs both decodes over the readable mappings of [base, end).
func decodeEngineCamera(ranges []engineAddrRange, base, end uintptr) (engineCameraLayout, error) {
	var l engineCameraLayout
	if err := decodeEngineCameraProps(ranges, base, end, &l); err != nil {
		return l, err
	}
	if err := decodeEngineCameraChain(ranges, base, end, &l); err != nil {
		return l, err
	}
	return l, nil
}

// cframeFloats is a CoordinateFrame in memory: 12 floats.
type cframeFloats [12]float32

// orthonormalRows reports whether m is a rotation matrix: finite, unit rows,
// perpendicular rows. A zeroed or garbage object fails every one of these.
func orthonormalRows(m []float32) bool {
	const tol = 1e-2
	for _, v := range m[:9] {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
	}
	dot := func(a, b int) float64 {
		return float64(m[3*a])*float64(m[3*b]) + float64(m[3*a+1])*float64(m[3*b+1]) + float64(m[3*a+2])*float64(m[3*b+2])
	}
	for i := 0; i < 3; i++ {
		if math.Abs(dot(i, i)-1) > tol {
			return false
		}
		for j := i + 1; j < 3; j++ {
			if math.Abs(dot(i, j)) > tol {
				return false
			}
		}
	}
	return true
}

// position returns the translation of a CFrame. The rotation is whichever 9
// floats are orthonormal: rotation first (translation last) or the reverse.
func (c cframeFloats) position() (pos [3]float64, ok bool) {
	var t []float32
	switch {
	case orthonormalRows(c[:9]):
		t = c[9:]
	case orthonormalRows(c[3:]):
		t = c[:3]
	default:
		return pos, false
	}
	for i := range pos {
		v := float64(t[i])
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e7 {
			return pos, false
		}
		pos[i] = v
	}
	return pos, true
}

// engineCameraSample is one validated read of the engine camera.
type engineCameraSample struct {
	cameraType uint32
	camera     [3]float64 // Camera.CFrame position
	focus      [3]float64 // Camera.Focus position
	distance   float64    // |camera - focus|
	index      int        // service vector entry that is Workspace
}

// engineCameraReader owns the decoded layout and the Workspace index hint.
// The hint only orders the probe; it is never trusted without validation.
type engineCameraReader struct {
	layout engineCameraLayout
	hint   int // service vector index that last held Workspace, or -1
	mem    engineMemory
}

const (
	engineCameraMaxServices = 4096
	// The latest detector result is a roughly 100ms cache for input events.
	engineCameraInterval            = 100 * time.Millisecond
	engineCameraHeartbeat           = 5 * time.Second
	engineCameraFirstPersonEnter    = 0.75
	engineCameraFirstPersonExit     = 1.25
	engineCameraFirstPersonType     = 5 // Enum.CameraType.Custom
	engineCameraFirstPersonDebounce = 2
)

// engineCameraFirstPersonDetector smooths validated camera samples. Entering
// and leaving first person take two matching samples; distances between the
// thresholds preserve the current state. An unresolved sample never captures,
// but only clears the decision: the reader continues sampling.
type engineCameraFirstPersonDetector struct {
	mu               sync.Mutex
	firstPerson      bool
	candidate        bool
	candidateSamples int
}

func (d *engineCameraFirstPersonDetector) update(s engineCameraSample, resolved bool) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !resolved {
		d.firstPerson = false
		d.candidate = false
		d.candidateSamples = 0
		return false
	}

	want := d.firstPerson
	if d.firstPerson {
		if s.cameraType != engineCameraFirstPersonType || s.distance >= engineCameraFirstPersonExit {
			want = false
		}
	} else if s.cameraType == engineCameraFirstPersonType && s.distance <= engineCameraFirstPersonEnter {
		want = true
	}
	if want == d.firstPerson {
		d.candidate = want
		d.candidateSamples = 0
		return d.firstPerson
	}
	if d.candidate != want || d.candidateSamples == 0 {
		d.candidate = want
		d.candidateSamples = 1
		return d.firstPerson
	}
	d.candidateSamples++
	if d.candidateSamples >= engineCameraFirstPersonDebounce {
		d.firstPerson = want
		d.candidateSamples = 0
	}
	return d.firstPerson
}

var engineCameraCapture engineCameraFirstPersonDetector

// EngineCameraFirstPerson returns the latest debounced camera decision.
func EngineCameraFirstPerson() bool {
	engineCameraCapture.mu.Lock()
	defer engineCameraCapture.mu.Unlock()
	return engineCameraCapture.firstPerson
}

// engineCameraMemory is the guarded window the sampler reads through. It
// takes its own /proc/self/maps snapshot on every sample, so probing heap
// pointers can never spend the refresh budget the cursor and MouseBehavior
// readers share through engineMaps.
type engineCameraMemory struct {
	ranges []engineAddrRange
}

func (m *engineCameraMemory) refresh() bool {
	ranges, err := readProcSelfMaps()
	if err != nil {
		return false
	}
	m.ranges = ranges
	return true
}

func (m *engineCameraMemory) bytes(addr uintptr, n int) ([]byte, bool) {
	if n <= 0 {
		return nil, false
	}
	out := make([]byte, n)
	if !m.read(addr, out) {
		return nil, false
	}
	return out, true
}

func (m *engineCameraMemory) read(addr uintptr, out []byte) bool {
	if addr == 0 || len(out) == 0 || addr+uintptr(len(out)) < addr || !engineRangesContain(m.ranges, addr, uintptr(len(out))) {
		return false
	}
	copy(out, unsafe.Slice((*byte)(unsafe.Pointer(addr)), len(out))) //nolint:govet // addr is inside a mapping from this sample's snapshot, read-only
	return true
}

func (m *engineCameraMemory) u64(addr uintptr) (uint64, bool) {
	var b [8]byte
	if addr%8 != 0 || !m.read(addr, b[:]) {
		return 0, false
	}
	return binary.LittleEndian.Uint64(b[:]), true
}

func (m *engineCameraMemory) u32(addr uintptr) (uint32, bool) {
	var b [4]byte
	if addr%4 != 0 || !m.read(addr, b[:]) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[:]), true
}

// readCFrame reads and validates a 12-float CoordinateFrame.
func readCFrame(mem engineMemory, addr uintptr) (pos [3]float64, ok bool) {
	var raw [48]byte
	if !mem.read(addr, raw[:]) {
		return pos, false
	}
	var c cframeFloats
	for i := range c {
		c[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return c.position()
}

// probeWorkspace reads Workspace.CurrentCamera from one service and, when it
// points at a readable orthonormal Camera, returns the sample. stage names the
// furthest check reached, for the unresolved reason.
func (r *engineCameraReader) probeWorkspace(service uintptr) (s engineCameraSample, stage int) {
	l := r.layout
	cam, ok := r.mem.u64(service + uintptr(l.workspaceAdjust) + uintptr(l.cameraSlot))
	if !ok {
		return s, 0
	}
	if cam == 0 {
		return s, 1
	}
	recv := uintptr(cam) + uintptr(l.cameraAdjust)
	camPos, ok := readCFrame(r.mem, recv+uintptr(l.cframe))
	if !ok {
		return s, 2
	}
	focusPos, ok := readCFrame(r.mem, recv+uintptr(l.focus))
	if !ok {
		return s, 2
	}
	kind, ok := r.mem.u32(recv + uintptr(l.kind))
	if !ok || kind > engineCameraTypeMax {
		return s, 2
	}
	s.cameraType, s.camera, s.focus = kind, camPos, focusPos
	for i := range camPos {
		d := camPos[i] - focusPos[i]
		s.distance += d * d
	}
	s.distance = math.Sqrt(s.distance)
	return s, 3
}

// sample walks singleton -> Game -> ServiceProvider -> services and returns the
// camera of the service that is Workspace. reason is empty on success; it is
// otherwise the exact step that had no answer. No state is latched.
func (r *engineCameraReader) sample() (s engineCameraSample, reason string) {
	l := r.layout
	game, ok := r.mem.u64(l.singleton + uintptr(l.gameOffset))
	if !ok {
		return s, "Game slot is not readable"
	}
	if game == 0 {
		return s, "no DataModel yet"
	}
	control, ok := r.mem.u64(l.singleton + uintptr(l.controlOffset))
	if !ok {
		return s, "Game control-block slot is not readable"
	}
	if control == 0 {
		return s, "no DataModel yet (no control block)"
	}
	owners, ok := r.mem.u64(uintptr(control) + 8)
	if !ok {
		return s, "Game control block is not readable"
	}
	if owners == 0 {
		return s, "DataModel expired"
	}
	provider := uintptr(game) + uintptr(l.providerOffset)
	begin, ok := r.mem.u64(provider + uintptr(l.vectorOffset))
	if !ok {
		return s, "ServiceProvider vector is not readable"
	}
	end, ok := r.mem.u64(provider + uintptr(l.vectorOffset) + 8)
	if !ok {
		return s, "ServiceProvider vector end is not readable"
	}
	if end < begin || (end-begin)%16 != 0 || (begin == 0) != (end == 0) || (end-begin)>>4 > engineCameraMaxServices {
		return s, fmt.Sprintf("ServiceProvider vector bounds are implausible (begin=%#x end=%#x)", begin, end)
	}
	count := int((end - begin) >> 4)
	if count == 0 {
		return s, "ServiceProvider has no services yet"
	}

	var nonNull, withCamera int
	hintStage := -1
	try := func(i int) bool {
		service, ok := r.mem.u64(uintptr(begin) + uintptr(i)*16)
		if !ok || service == 0 {
			return false
		}
		nonNull++
		got, stage := r.probeWorkspace(uintptr(service))
		if i == r.hint {
			hintStage = stage
		}
		if stage >= 2 {
			withCamera++
		}
		if stage == 3 {
			got.index = i
			s = got
			return true
		}
		return false
	}
	if r.hint >= 0 && r.hint < count && try(r.hint) {
		return s, ""
	}
	nonNull, withCamera = 0, 0
	for i := 0; i < count; i++ {
		if try(i) {
			r.hint = i
			return s, ""
		}
	}
	if hintStage == 1 {
		return s, fmt.Sprintf("Workspace.CurrentCamera is nil (service %d)", r.hint)
	}
	return s, fmt.Sprintf("no Workspace with a valid Camera (%d services, %d non-null, %d with a CurrentCamera pointer)",
		count, nonNull, withCamera)
}

// engineCameraTrace is the process-wide trace sampler.
var engineCameraTrace struct {
	mu      sync.Mutex
	started bool
}

func engineCameraLog(format string, args ...any) {
	logging.Logger(logging.CatJNI).Info("[jni] camera trace: " + fmt.Sprintf(format, args...))
}

// describe formats a sample or an unresolved reason as one trace line.
func (s engineCameraSample) describe() string {
	return fmt.Sprintf("%s distance=%.2f camera=(%.1f,%.1f,%.1f) focus=(%.1f,%.1f,%.1f) service=%d",
		engineCameraTypeName(s.cameraType), s.distance,
		s.camera[0], s.camera[1], s.camera[2], s.focus[0], s.focus[1], s.focus[2], s.index)
}

// run samples forever. A decode failure is reported on every heartbeat and a
// sampling miss on every change or heartbeat; nothing ever stops the loop.
func (r *engineCameraReader) run(decodeErr error, logf func(string, ...any), sleep func(time.Duration), stop <-chan struct{}) {
	var live engineCameraMemory
	var last string
	var lastAt time.Time
	report := func(line string) {
		if line != last || time.Since(lastAt) >= engineCameraHeartbeat {
			last, lastAt = line, time.Now()
			logf("%s", line)
		}
	}
	for {
		select {
		case <-stop:
			return
		default:
		}
		switch {
		case decodeErr != nil:
			engineCameraCapture.update(engineCameraSample{}, false)
			report("unresolved: " + decodeErr.Error())
		default:
			if _, isLive := engineMem.(liveEngineMemory); isLive {
				if !live.refresh() {
					engineCameraCapture.update(engineCameraSample{}, false)
					report("unresolved: /proc/self/maps is not readable")
					sleep(engineCameraInterval)
					continue
				}
				r.mem = &live
			} else {
				r.mem = engineMem
			}
			if s, reason := r.sample(); reason != "" {
				engineCameraCapture.update(engineCameraSample{}, false)
				report("unresolved: " + reason)
			} else {
				engineCameraCapture.update(s, true)
				report("resolved: " + s.describe())
			}
		}
		sleep(engineCameraInterval)
	}
}

// InitEngineCamera starts the process-wide camera reader. The latest result
// feeds capture decisions through EngineCameraFirstPerson; trace logging alone
// is controlled by TIPSY_TRACE_CAMERA=1. Initialization is idempotent and a
// decode or sampling failure never latches the reader off.
func InitEngineCamera(base uintptr, size uint64) {
	engineCameraTrace.mu.Lock()
	defer engineCameraTrace.mu.Unlock()
	if engineCameraTrace.started {
		return
	}
	engineCameraTrace.started = true
	trace := os.Getenv("TIPSY_TRACE_CAMERA") == "1"
	logf := func(string, ...any) {}
	if trace {
		logf = engineCameraLog
	}
	if base == 0 || size == 0 || uint64(uintptr(size)) != size || base+uintptr(size) < base {
		engineCameraCapture.update(engineCameraSample{}, false)
		logf("unresolved: bad image bounds")
		return
	}
	end := base + uintptr(size)
	go func() {
		ranges, err := readProcSelfMaps()
		var layout engineCameraLayout
		if err == nil {
			layout, err = decodeEngineCamera(ranges, base, end)
		}
		if err != nil {
			logf("decode failed: %v", err)
		} else {
			logf("decoded Camera CFrame=+%#x Focus=+%#x CameraType=+%#x receiver=+%#x; Workspace CurrentCamera=+%#x receiver=+%#x; services=game+%#x+%#x",
				layout.cframe, layout.focus, layout.kind, layout.cameraAdjust,
				layout.cameraSlot, layout.workspaceAdjust, layout.providerOffset, layout.vectorOffset)
		}
		r := &engineCameraReader{layout: layout, hint: -1}
		r.run(err, logf, time.Sleep, nil)
	}()
}
