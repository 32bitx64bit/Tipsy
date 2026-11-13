// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// loadCopiedEngineImage maps a copied libroblox.so's PT_LOAD segments into one
// arena at their virtual addresses. The library is read as bytes only; no
// library code or constructor runs.
func loadCopiedEngineImage(t *testing.T, path string) (*fakeEngineImage, *elf.File) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
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
	return &fakeEngineImage{base: unsafe.Pointer(&data[0]), mem: data}, f
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// What each known build decodes to. Other builds only have to decode.
var engineCameraKnownBuilds = map[string]engineCameraLayout{
	"d29de17f": {cframe: 0x18, focus: 0x48, kind: 0x78, cameraAdjust: 0xa0, cameraSlot: 0x30, workspaceAdjust: 0x428,
		gameOffset: 0x1120, controlOffset: 0x1128, providerOffset: 0x1f0, vectorOffset: 0x98},
	"4892d894": {cframe: 0x18, focus: 0x48, kind: 0x78, cameraAdjust: 0xa8, cameraSlot: 0x30, workspaceAdjust: 0x430,
		gameOffset: 0xa58, controlOffset: 0xa60, providerOffset: 0x1f0, vectorOffset: 0xa0},
}

// Optional offline check against copied, untrusted libraries: a list of
// libroblox.so paths separated by the OS path-list separator.
func TestEngineCameraCopiedLibrary(t *testing.T) {
	list := os.Getenv("TIPSY_TEST_CAMERA_LIBRARY")
	if list == "" {
		t.Skip("no copied library supplied")
	}
	for _, path := range filepath.SplitList(list) {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path)))), func(t *testing.T) {
			img, f := loadCopiedEngineImage(t, path)
			useFakeEngineMemory(t, img)
			var getter uintptr
			syms, _ := f.DynamicSymbols()
			for _, s := range syms {
				if s.Name == "Java_com_roblox_engine_jni_NativeInputInterface_nativeGetMainWindowIsMouseLockedCenter" {
					getter = img.addr(int(s.Value))
				}
			}
			if getter == 0 {
				t.Fatal("no exported MouseBehavior getter")
			}
			resetEngineMouseBehavior()
			if !SetRobloxDirectInputTarget(0x1234, 0x5678, getter, getter, getter, getter) {
				t.Fatal("exported getter did not wire")
			}
			t.Cleanup(func() {
				ClearRobloxDirectInputTarget()
				resetEngineMouseBehavior()
			})
			base, end := img.addr(0), img.addr(len(img.mem))
			l, err := decodeEngineCamera([]engineAddrRange{{base, end}}, base, end)
			if err != nil {
				t.Fatal(err)
			}
			l.singleton -= base
			t.Logf("sha256 %s layout %+v", fileSHA256(t, path), l)
			want, known := engineCameraKnownBuilds[fileSHA256(t, path)[:8]]
			if !known {
				return
			}
			l.singleton = 0
			if l != want {
				t.Fatalf("layout %+v want %+v", l, want)
			}
		})
	}
}

// camAsm assembles the few instruction forms the accessor decode reads.
type camAsm struct {
	addr uintptr
	b    []byte
}

func (a *camAsm) raw(bs ...byte) { a.b = append(a.b, bs...) }

// lea emits `lea reg,[rip+rel32]` targeting target.
func (a *camAsm) lea(reg int, target uintptr) {
	rex := byte(0x48)
	if reg >= 8 {
		rex = 0x4c
	}
	next := a.addr + uintptr(len(a.b)) + 7
	a.raw(rex, 0x8d, byte(0x05|(reg&7)<<3))
	a.b = binary.LittleEndian.AppendUint32(a.b, uint32(int32(int64(target)-int64(next))))
}

// store emits `mov reg,off(%rax)`.
func (a *camAsm) store(off, reg int) {
	rex := byte(0x48)
	if reg >= 8 {
		rex = 0x4c
	}
	if off == 0 {
		a.raw(rex, 0x89, byte((reg&7)<<3))
		return
	}
	a.raw(rex, 0x89, byte(0x40|(reg&7)<<3), byte(off))
}

const (
	regRCX = 1
	regRDX = 2
	regR12 = 12
)

// accessorPlain is the shape of a plain property in 2.746:
// lea rcx,vt; mov; lea rcx,get; mov; xor ecx,ecx; mov; lea rdx,set; mov; mov.
func accessorPlain(addr, vt, getter, setter uintptr) []byte {
	a := &camAsm{addr: addr}
	a.raw(0xbf, 0x28, 0, 0, 0, 0xe8, 0, 0, 0, 0)
	a.lea(regRCX, vt)
	a.store(0, regRCX)
	a.lea(regRCX, getter)
	a.store(8, regRCX)
	a.raw(0x31, 0xc9)
	a.store(0x10, regRCX)
	a.lea(regRDX, setter)
	a.store(0x18, regRDX)
	a.store(0x20, regRCX)
	return a.b
}

// accessorReused is the shape of 2.742 and of Workspace: one scratch register
// for every lea and a long-lived register for the two adjustments.
func accessorReused(addr, vt, getter, setter uintptr, adj int) []byte {
	a := &camAsm{addr: addr}
	a.raw(0xbf, 0x28, 0, 0, 0, 0xe8, 0, 0, 0, 0)
	a.lea(regRCX, vt)
	a.store(0, regRCX)
	a.lea(regRCX, getter)
	a.store(8, regRCX)
	a.store(0x10, adj)
	a.lea(regRCX, setter)
	a.store(0x18, regRCX)
	a.store(0x20, adj)
	return a.b
}

func TestDecodeCameraFieldGetter(t *testing.T) {
	for _, tc := range []struct {
		name string
		code []byte
		kind cameraFieldKind
		disp uint32
		ok   bool
	}{
		{"lea disp8", []byte{0x48, 0x8d, 0x47, 0x18, 0xc3}, cameraFieldAddr, 0x18, true},
		{"mov32 disp8", []byte{0x8b, 0x47, 0x78, 0xc3}, cameraFieldU32, 0x78, true},
		{"mov64 disp8", []byte{0x48, 0x8b, 0x47, 0x30, 0xc3}, cameraFieldU64, 0x30, true},
		{"lea disp32", []byte{0x48, 0x8d, 0x87, 0x48, 0x01, 0, 0, 0xc3}, cameraFieldAddr, 0x148, true},
		{"mov32 disp32", []byte{0x8b, 0x87, 0x80, 0x01, 0, 0, 0xc3}, cameraFieldU32, 0x180, true},
		{"mov64 disp32", []byte{0x48, 0x8b, 0x87, 0x30, 0x04, 0, 0, 0xc3}, cameraFieldU64, 0x430, true},
		{"no ret", []byte{0x48, 0x8d, 0x47, 0x18, 0x90}, cameraFieldNone, 0, false},
		{"zero disp", []byte{0x48, 0x8d, 0x47, 0x00, 0xc3}, cameraFieldNone, 0, false},
		{"misaligned word", []byte{0x8b, 0x47, 0x7a, 0xc3}, cameraFieldNone, 0, false},
		{"misaligned pointer", []byte{0x48, 0x8b, 0x47, 0x34, 0xc3}, cameraFieldNone, 0, false},
		{"other base register", []byte{0x48, 0x8d, 0x46, 0x18, 0xc3}, cameraFieldNone, 0, false},
		{"implausible disp32", []byte{0x48, 0x8d, 0x87, 0, 0, 0x10, 0, 0xc3}, cameraFieldNone, 0, false},
		{"mov rax,rdi", []byte{0x48, 0x89, 0xf8, 0xc3}, cameraFieldNone, 0, false},
		{"truncated", []byte{0x48, 0x8d, 0x47}, cameraFieldNone, 0, false},
		{"empty", nil, cameraFieldNone, 0, false},
	} {
		kind, disp, ok := decodeCameraFieldGetter(tc.code)
		if kind != tc.kind || disp != tc.disp || ok != tc.ok {
			t.Errorf("%s: got %v %#x %v, want %v %#x %v", tc.name, kind, disp, ok, tc.kind, tc.disp, tc.ok)
		}
	}
}

func TestDecodeCameraSetterAdjust(t *testing.T) {
	// The Camera.Focus setter prologue of 2.746, relocations zeroed.
	prologue := []byte{0x55, 0x48, 0x89, 0xe5, 0x41, 0x57, 0x41, 0x56, 0x41, 0x55, 0x41, 0x54, 0x53,
		0x48, 0x83, 0xec, 0x48, 0x48, 0x89, 0xfb, 0x4c, 0x8b, 0x2d, 0, 0, 0, 0, 0x49, 0x8b, 0x45, 0x00,
		0x48, 0x89, 0x45, 0xd0, 0x48, 0x8d, 0x05, 0, 0, 0, 0, 0x44, 0x8a, 0x20,
		0x48, 0x8d, 0x05, 0, 0, 0, 0, 0x4c, 0x8d, 0xb7, 0x60, 0xff, 0xff, 0xff, 0x4c, 0x89, 0xf7, 0xff, 0x10}
	if adj, ok := decodeCameraSetterAdjust(prologue); !ok || adj != 0xa0 {
		t.Fatalf("adjust %#x %v, want 0xa0", adj, ok)
	}
	rax := bytes.Replace(prologue, []byte{0x4c, 0x8d, 0xb7, 0x60, 0xff, 0xff, 0xff}, []byte{0x48, 0x8d, 0x87, 0x58, 0xff, 0xff, 0xff}, 1)
	if adj, ok := decodeCameraSetterAdjust(rax); !ok || adj != 0xa8 {
		t.Fatalf("adjust %#x %v, want 0xa8", adj, ok)
	}
	// A positive displacement is a field address, not a receiver adjustment.
	positive := bytes.Replace(prologue, []byte{0x60, 0xff, 0xff, 0xff}, []byte{0x60, 0, 0, 0}, 1)
	if _, ok := decodeCameraSetterAdjust(positive); ok {
		t.Fatal("positive displacement accepted as an adjustment")
	}
	if _, ok := decodeCameraSetterAdjust(prologue[:30]); ok {
		t.Fatal("prologue without the adjustment accepted")
	}
}

func TestDecodeCameraAccessor(t *testing.T) {
	const addr = uintptr(0x100000)
	const vt, getter, setter = uintptr(0x200000), uintptr(0x300000), uintptr(0x310000)
	for name, code := range map[string][]byte{
		"plain":        accessorPlain(addr, vt, getter, setter),
		"reused rcx":   accessorReused(addr, vt, getter, setter, 14),
		"reused r12":   accessorReused(addr, vt, getter, setter, regR12),
		"trailing mov": append(accessorPlain(addr, vt, getter, setter), 0x4c, 0x8d, 0x45, 0xa0),
	} {
		g, s, ok := decodeCameraAccessor(code, addr)
		if !ok || g != getter || s != setter {
			t.Errorf("%s: got %#x %#x %v, want %#x %#x", name, g, s, ok, getter, setter)
		}
	}

	// An adjustment register that holds a code address is not this shape.
	a := &camAsm{addr: addr}
	a.raw(0xbf, 0x28, 0, 0, 0, 0xe8, 0, 0, 0, 0)
	a.lea(regRCX, vt)
	a.store(0, regRCX)
	a.lea(regRCX, getter)
	a.store(8, regRCX)
	a.store(0x10, regRCX) // adjustment is the getter address
	a.lea(regRDX, setter)
	a.store(0x18, regRDX)
	a.store(0x20, regRCX)
	if _, _, ok := decodeCameraAccessor(a.b, addr); ok {
		t.Error("code-address adjustment accepted")
	}

	plain := accessorPlain(addr, vt, getter, setter)
	if _, _, ok := decodeCameraAccessor(plain[:len(plain)-4], addr); ok {
		t.Error("accessor missing its last store accepted")
	}
	broken := append([]byte{}, plain...)
	broken[10] = 0x90 // an instruction the decode does not model
	if _, _, ok := decodeCameraAccessor(broken, addr); ok {
		t.Error("unknown instruction accepted")
	}
	wrongSize := append([]byte{}, plain...)
	wrongSize[1] = 0x30
	if _, _, ok := decodeCameraAccessor(wrongSize, addr); ok {
		t.Error("object of another size accepted")
	}
	if _, _, ok := decodeCameraAccessor(nil, addr); ok {
		t.Error("empty accepted")
	}
}

// Synthetic engine: fixed offsets for one Camera and one Workspace.
const (
	camSingletonOff = 0x1000
	camControlOff   = 0x1800
	camGameOff      = 0x2000
	camVectorOff    = 0x3000
	camJunkOff      = 0x3800
	camWorkspaceOff = 0x4000
	camCameraOff    = 0x5000
)

var camTestLayout = engineCameraLayout{
	cframe: 0x18, focus: 0x48, kind: 0x78, cameraAdjust: 0xa0,
	cameraSlot: 0x30, workspaceAdjust: 0x428,
	gameOffset: 0x100, controlOffset: 0x108, providerOffset: 0x1f0, vectorOffset: 0x98,
}

type camFixture struct {
	img *fakeEngineImage
	r   *engineCameraReader
}

func putU64(img *fakeEngineImage, off int, v uint64) {
	binary.LittleEndian.PutUint64(img.mem[off:], v)
}

func putFloats(img *fakeEngineImage, off int, fs ...float32) {
	for i, f := range fs {
		binary.LittleEndian.PutUint32(img.mem[off+4*i:], math.Float32bits(f))
	}
}

// cframeAt builds a rotation-first CoordinateFrame with an identity rotation.
func cframeAt(x, y, z float32) []float32 {
	return []float32{1, 0, 0, 0, 1, 0, 0, 0, 1, x, y, z}
}

// newCamFixture lays out singleton -> Game -> ServiceProvider -> three
// services, the second of which is Workspace with a Custom camera three
// studs from its focus.
func newCamFixture(t *testing.T) *camFixture {
	t.Helper()
	img := newFakeEngineImage(t)
	useFakeEngineMemory(t, img)
	l := camTestLayout
	l.singleton = img.addr(camSingletonOff)
	putU64(img, camSingletonOff+int(l.gameOffset), uint64(img.addr(camGameOff)))
	putU64(img, camSingletonOff+int(l.controlOffset), uint64(img.addr(camControlOff)))
	putU64(img, camControlOff+8, 1)
	vec := camGameOff + int(l.providerOffset) + int(l.vectorOffset)
	putU64(img, vec, uint64(img.addr(camVectorOff)))
	putU64(img, vec+8, uint64(img.addr(camVectorOff+3*16)))
	putU64(img, camVectorOff, uint64(img.addr(camJunkOff)))
	putU64(img, camVectorOff+16, uint64(img.addr(camWorkspaceOff)))
	// The third service is NULL: a service not created yet.
	putU64(img, camWorkspaceOff+int(l.workspaceAdjust)+int(l.cameraSlot), uint64(img.addr(camCameraOff)))
	recv := camCameraOff + int(l.cameraAdjust)
	putFloats(img, recv+int(l.cframe), cframeAt(10, 5, 0)...)
	putFloats(img, recv+int(l.focus), cframeAt(10, 5, -3)...)
	binary.LittleEndian.PutUint32(img.mem[recv+int(l.kind):], 5)
	// Junk that is not a camera: non-zero, unaligned-looking bytes.
	for i := 0; i < 0x200; i++ {
		img.mem[camJunkOff+i] = byte(0x40 + i%7)
	}
	return &camFixture{img: img, r: &engineCameraReader{layout: l, hint: -1, mem: img}}
}

func TestEngineCameraSample(t *testing.T) {
	f := newCamFixture(t)
	before := append([]byte{}, f.img.mem...)
	s, reason := f.r.sample()
	if reason != "" {
		t.Fatalf("unresolved: %s", reason)
	}
	if s.cameraType != 5 || math.Abs(s.distance-3) > 1e-6 || s.index != 1 {
		t.Fatalf("sample %+v, want Custom at distance 3 from service 1", s)
	}
	if s.camera != [3]float64{10, 5, 0} || s.focus != [3]float64{10, 5, -3} {
		t.Fatalf("positions camera=%v focus=%v", s.camera, s.focus)
	}
	if got := s.describe(); !strings.Contains(got, "Custom(5)") || !strings.Contains(got, "distance=3.00") {
		t.Fatalf("describe %q must name the CameraType and the distance", got)
	}
	if f.r.hint != 1 {
		t.Fatalf("hint %d, want 1", f.r.hint)
	}
	// Sampling again takes the hinted path and sees a moved camera.
	recv := camCameraOff + int(f.r.layout.cameraAdjust)
	putFloats(f.img, recv+int(f.r.layout.cframe), cframeAt(10, 5, -2.5)...)
	if s, reason := f.r.sample(); reason != "" || math.Abs(s.distance-0.5) > 1e-6 {
		t.Fatalf("moved camera: %+v %q", s, reason)
	}
	// Reading never writes.
	putFloats(f.img, recv+int(f.r.layout.cframe), cframeAt(10, 5, 0)...)
	if !bytes.Equal(before, f.img.mem) {
		t.Fatal("sampling changed engine memory")
	}
}

func TestEngineCameraSampleMissesAreNotLatched(t *testing.T) {
	l := camTestLayout
	slot := camWorkspaceOff + int(l.workspaceAdjust) + int(l.cameraSlot)
	recv := camCameraOff + int(l.cameraAdjust)
	for _, tc := range []struct {
		name   string
		break_ func(f *camFixture)
		fix    func(f *camFixture)
		reason string
	}{
		{"no DataModel", func(f *camFixture) { putU64(f.img, camSingletonOff+int(l.gameOffset), 0) },
			func(f *camFixture) { putU64(f.img, camSingletonOff+int(l.gameOffset), uint64(f.img.addr(camGameOff))) },
			"no DataModel yet"},
		{"no control block", func(f *camFixture) { putU64(f.img, camSingletonOff+int(l.controlOffset), 0) },
			func(f *camFixture) {
				putU64(f.img, camSingletonOff+int(l.controlOffset), uint64(f.img.addr(camControlOff)))
			},
			"no DataModel yet"},
		{"expired DataModel", func(f *camFixture) { putU64(f.img, camControlOff+8, 0) },
			func(f *camFixture) { putU64(f.img, camControlOff+8, 1) },
			"DataModel expired"},
		{"unreadable game", func(f *camFixture) {
			putU64(f.img, camSingletonOff+int(l.gameOffset), uint64(f.img.addr(len(f.img.mem)+0x1000)))
		}, func(f *camFixture) { putU64(f.img, camSingletonOff+int(l.gameOffset), uint64(f.img.addr(camGameOff))) },
			"ServiceProvider vector is not readable"},
		{"empty vector", func(f *camFixture) {
			vec := camGameOff + int(l.providerOffset) + int(l.vectorOffset)
			putU64(f.img, vec+8, uint64(f.img.addr(camVectorOff)))
		}, func(f *camFixture) {
			vec := camGameOff + int(l.providerOffset) + int(l.vectorOffset)
			putU64(f.img, vec+8, uint64(f.img.addr(camVectorOff+3*16)))
		}, "no services yet"},
		{"torn vector", func(f *camFixture) {
			vec := camGameOff + int(l.providerOffset) + int(l.vectorOffset)
			putU64(f.img, vec+8, uint64(f.img.addr(camVectorOff+3*16+5)))
		}, func(f *camFixture) {
			vec := camGameOff + int(l.providerOffset) + int(l.vectorOffset)
			putU64(f.img, vec+8, uint64(f.img.addr(camVectorOff+3*16)))
		}, "bounds are implausible"},
		{"nil camera", func(f *camFixture) { putU64(f.img, slot, 0) },
			func(f *camFixture) { putU64(f.img, slot, uint64(f.img.addr(camCameraOff))) },
			"Workspace.CurrentCamera is nil"},
		{"camera outside memory", func(f *camFixture) { putU64(f.img, slot, uint64(f.img.addr(len(f.img.mem)+0x1000))) },
			func(f *camFixture) { putU64(f.img, slot, uint64(f.img.addr(camCameraOff))) },
			"no Workspace with a valid Camera"},
		{"CFrame not orthonormal", func(f *camFixture) { putFloats(f.img, recv+int(l.cframe), 2, 0, 0, 0, 2, 0, 0, 0, 2, 1, 1, 1) },
			func(f *camFixture) { putFloats(f.img, recv+int(l.cframe), cframeAt(10, 5, 0)...) },
			"no Workspace with a valid Camera"},
		{"Focus is NaN", func(f *camFixture) { putFloats(f.img, recv+int(l.focus), float32(math.NaN())) },
			func(f *camFixture) { putFloats(f.img, recv+int(l.focus), cframeAt(10, 5, -3)...) },
			"no Workspace with a valid Camera"},
		{"CameraType out of range", func(f *camFixture) {
			binary.LittleEndian.PutUint32(f.img.mem[recv+int(l.kind):], 0xdeadbeef)
		}, func(f *camFixture) { binary.LittleEndian.PutUint32(f.img.mem[recv+int(l.kind):], 5) },
			"no Workspace with a valid Camera"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCamFixture(t)
			// Resolve once so the nil-camera message has a hint to name.
			if _, reason := f.r.sample(); reason != "" {
				t.Fatalf("baseline: %s", reason)
			}
			tc.break_(f)
			for i := 0; i < 3; i++ {
				if _, reason := f.r.sample(); !strings.Contains(reason, tc.reason) {
					t.Fatalf("miss %d: reason %q, want %q", i, reason, tc.reason)
				}
			}
			tc.fix(f)
			if s, reason := f.r.sample(); reason != "" || s.cameraType != 5 {
				t.Fatalf("no recovery after the miss: %+v %q", s, reason)
			}
		})
	}
}

func TestEngineCameraSampleReprobesAfterHintGoesStale(t *testing.T) {
	f := newCamFixture(t)
	if s, reason := f.r.sample(); reason != "" || s.index != 1 {
		t.Fatalf("baseline %+v %q", s, reason)
	}
	// Workspace moves to vector slot 2; the hint at slot 1 now holds a service
	// with no camera.
	l := f.r.layout
	putU64(f.img, camVectorOff+16, uint64(f.img.addr(camJunkOff)))
	putU64(f.img, camVectorOff+32, uint64(f.img.addr(camWorkspaceOff)))
	if s, reason := f.r.sample(); reason != "" || s.index != 2 {
		t.Fatalf("after the move: %+v %q", s, reason)
	}
	if f.r.hint != 2 {
		t.Fatalf("hint %d, want 2 (layout %+v)", f.r.hint, l)
	}
}

func TestCFramePosition(t *testing.T) {
	rotFirst := cframeFloats{1, 0, 0, 0, 1, 0, 0, 0, 1, 7, 8, 9}
	if pos, ok := rotFirst.position(); !ok || pos != [3]float64{7, 8, 9} {
		t.Fatalf("rotation first: %v %v", pos, ok)
	}
	transFirst := cframeFloats{7, 8, 9, 1, 0, 0, 0, 1, 0, 0, 0, 1}
	if pos, ok := transFirst.position(); !ok || pos != [3]float64{7, 8, 9} {
		t.Fatalf("translation first: %v %v", pos, ok)
	}
	// A 90 degree turn about Y is still a rotation.
	turned := cframeFloats{0, 0, -1, 0, 1, 0, 1, 0, 0, 1, 2, 3}
	if pos, ok := turned.position(); !ok || pos != [3]float64{1, 2, 3} {
		t.Fatalf("turned: %v %v", pos, ok)
	}
	for name, c := range map[string]cframeFloats{
		"zero":       {},
		"scaled":     {2, 0, 0, 0, 2, 0, 0, 0, 2, 1, 2, 3},
		"skewed":     {1, 1, 0, 0, 1, 0, 0, 0, 1, 1, 2, 3},
		"nan":        {float32(math.NaN()), 0, 0, 0, 1, 0, 0, 0, 1, 1, 2, 3},
		"inf pos":    {1, 0, 0, 0, 1, 0, 0, 0, 1, float32(math.Inf(1)), 0, 0},
		"huge pos":   {1, 0, 0, 0, 1, 0, 0, 0, 1, 1e9, 0, 0},
		"all ones":   {1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		"denormal":   {1e-30, 1e-30, 1e-30, 1e-30, 1e-30, 1e-30, 1e-30, 1e-30, 1e-30, 1, 2, 3},
		"pointerish": {3.0e-41, 4.6e-41, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	} {
		if _, ok := c.position(); ok {
			t.Errorf("%s accepted as a CFrame", name)
		}
	}
}

func TestEngineCameraTypeName(t *testing.T) {
	for v, want := range map[uint32]string{0: "Fixed(0)", 5: "Custom(5)", 6: "Scriptable(6)", 7: "Orbital(7)", 9: "CameraType(9)"} {
		if got := engineCameraTypeName(v); got != want {
			t.Errorf("%d: %q want %q", v, got, want)
		}
	}
}

// A synthetic registration: the accessor object, then the name reference.
func TestLocateCameraProps(t *testing.T) {
	img := newFakeEngineImage(t)
	useFakeEngineMemory(t, img)
	const (
		nameOff   = 0x2000
		codeOff   = 0x1000
		getterOff = 0x3000
		setterOff = 0x3100
		decoyOff  = 0x1800
	)
	copy(img.mem[nameOff:], "Focus\x00")
	copy(img.mem[getterOff:], []byte{0x48, 0x8d, 0x47, 0x48, 0xc3})
	copy(img.mem[setterOff:], []byte{0x55, 0x48, 0x89, 0xe5, 0x4c, 0x8d, 0xb7, 0x58, 0xff, 0xff, 0xff, 0x4c, 0x89, 0xf7})
	code := accessorPlain(img.addr(codeOff+0x20), img.addr(0x4000), img.addr(getterOff), img.addr(setterOff))
	copy(img.mem[codeOff+0x20:], code)
	// The name reference follows the accessor, as in a plain registration.
	ref := &camAsm{addr: img.addr(codeOff + 0x20 + len(code) + 0x10)}
	ref.lea(regRDX, img.addr(nameOff))
	copy(img.mem[codeOff+0x20+len(code)+0x10:], ref.b)
	// A second reference to the same name with no accessor near it.
	decoy := &camAsm{addr: img.addr(decoyOff)}
	decoy.lea(regRCX, img.addr(nameOff))
	copy(img.mem[decoyOff:], decoy.b)

	base, end := img.addr(0), img.addr(len(img.mem))
	got := locateCameraProps([]engineAddrRange{{base, end}}, base, end, "Focus", "CFrame")
	if len(got["CFrame"]) != 0 || len(got["Focus"]) != 1 {
		t.Fatalf("props %+v", got)
	}
	p := got["Focus"][0]
	if p.getter != img.addr(getterOff) || p.setter != img.addr(setterOff) ||
		p.kind != cameraFieldAddr || p.disp != 0x48 || !p.hasAdjust || p.adjust != 0xa8 {
		t.Fatalf("prop %+v", p)
	}
}

func TestEngineCameraMemoryIsIsolatedAndGuarded(t *testing.T) {
	data := make([]byte, 64)
	binary.LittleEndian.PutUint64(data[8:], 0x1122334455667788)
	binary.LittleEndian.PutUint32(data[16:], 0xcafef00d)
	base := uintptr(unsafe.Pointer(&data[0]))

	var m engineCameraMemory
	if _, ok := m.u64(base + 8); ok {
		t.Fatal("read before the first snapshot succeeded")
	}
	if !m.refresh() {
		t.Fatal("no /proc/self/maps snapshot")
	}
	engineMaps.mu.Lock()
	sharedReads := engineMaps.reads
	engineMaps.mu.Unlock()

	if v, ok := m.u64(base + 8); !ok || v != 0x1122334455667788 {
		t.Fatalf("u64 %#x %v", v, ok)
	}
	if v, ok := m.u32(base + 16); !ok || v != 0xcafef00d {
		t.Fatalf("u32 %#x %v", v, ok)
	}
	if _, ok := m.u64(base + 9); ok {
		t.Fatal("misaligned u64 read")
	}
	for i := 0; i < 200; i++ {
		if _, ok := m.u64(0x10 + uintptr(i)*8); ok {
			t.Fatalf("read of unmapped address %#x", 0x10+i*8)
		}
	}
	if _, ok := m.bytes(^uintptr(0)-3, 8); ok {
		t.Fatal("wrapping read succeeded")
	}
	if b, ok := m.bytes(base+8, 4); !ok || !bytes.Equal(b, data[8:12]) {
		t.Fatalf("bytes %x %v", b, ok)
	}
	engineMaps.mu.Lock()
	after := engineMaps.reads
	engineMaps.mu.Unlock()
	if after != sharedReads {
		t.Fatalf("camera reads spent the shared maps budget: %d -> %d", sharedReads, after)
	}
}

func TestEngineCameraFirstPersonDetector(t *testing.T) {
	var d engineCameraFirstPersonDetector
	sample := func(kind uint32, distance float64) engineCameraSample {
		return engineCameraSample{cameraType: kind, distance: distance}
	}
	steps := []struct {
		name     string
		sample   engineCameraSample
		resolved bool
		want     bool
	}{
		{"third person stays free", sample(5, 12.5), true, false},
		{"one close sample only arms", sample(5, 0.5), true, false},
		{"second close sample enters", sample(5, 0.75), true, true},
		{"hysteresis holds through middle band", sample(5, 1.0), true, true},
		{"one far sample only arms release", sample(5, 1.25), true, true},
		{"second far sample releases", sample(5, 1.5), true, false},
		{"middle band does not re-enter", sample(5, 1.0), true, false},
		{"first-person camera then unresolved", sample(5, 0.5), true, false},
		{"second close sample re-enters", sample(5, 0.5), true, true},
		{"unresolved releases without latching reader", engineCameraSample{}, false, false},
		{"recovery needs a fresh first sample", sample(5, 0.5), true, false},
		{"recovery completes on second sample", sample(5, 0.5), true, true},
		{"one Scriptable sample only arms release", sample(6, 6), true, true},
		{"Scriptable releases on second sample", sample(6, 6), true, false},
		{"Scriptable near zero never enters", sample(6, 0.5), true, false},
		{"Scriptable near zero still does not enter", sample(6, 0.5), true, false},
	}
	for _, tc := range steps {
		if got := d.update(tc.sample, tc.resolved); got != tc.want {
			t.Fatalf("%s: firstPerson=%t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestInitEngineCameraAlwaysStartsWithoutTrace(t *testing.T) {
	for _, v := range []string{"", "0", "true"} {
		t.Setenv("TIPSY_TRACE_CAMERA", v)
		engineCameraTrace.mu.Lock()
		engineCameraTrace.started = false
		engineCameraTrace.mu.Unlock()
		InitEngineCamera(0, 0)
		engineCameraTrace.mu.Lock()
		started := engineCameraTrace.started
		engineCameraTrace.mu.Unlock()
		if !started {
			t.Fatalf("TIPSY_TRACE_CAMERA=%q disabled camera sampling", v)
		}
	}
}

func TestInitEngineCameraBadBoundsLogsAndStartsNoSampler(t *testing.T) {
	t.Setenv("TIPSY_TRACE_CAMERA", "1")
	t.Cleanup(func() {
		engineCameraTrace.mu.Lock()
		engineCameraTrace.started = false
		engineCameraTrace.mu.Unlock()
	})
	InitEngineCamera(0, 0)
	engineCameraTrace.mu.Lock()
	started := engineCameraTrace.started
	engineCameraTrace.mu.Unlock()
	if !started {
		t.Fatal("camera reader did not record its initialization attempt")
	}
}

// run keeps sampling through misses, logs changes rather than repeats, and
// reports a decode failure instead of going quiet.
func TestEngineCameraRunNeverLatchesOff(t *testing.T) {
	f := newCamFixture(t)
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	l := f.r.layout
	steps := []func(){
		func() {}, // resolved
		func() {}, // resolved, unchanged: no new line
		func() { putU64(f.img, camSingletonOff+int(l.gameOffset), 0) }, // miss
		func() {}, // still missing: no new line
		func() { putU64(f.img, camSingletonOff+int(l.gameOffset), uint64(f.img.addr(camGameOff))) }, // back
	}
	stop := make(chan struct{})
	n := 0
	sleep := func(time.Duration) {
		if n < len(steps) {
			steps[n]()
		}
		n++
		if n > len(steps) {
			close(stop)
		}
	}
	f.r.run(nil, logf, sleep, stop)
	want := []string{
		"resolved: Custom(5) distance=3.00",
		"unresolved: no DataModel yet",
		"resolved: Custom(5) distance=3.00",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines %q, want %d lines", lines, len(want))
	}
	for i := range want {
		if !strings.HasPrefix(lines[i], want[i]) {
			t.Fatalf("line %d %q, want prefix %q", i, lines[i], want[i])
		}
	}

	lines, n, stop = nil, 0, make(chan struct{})
	(&engineCameraReader{hint: -1}).run(errEngineCameraShape, logf, func(time.Duration) {
		n++
		if n == 2 {
			close(stop)
		}
	}, stop)
	if len(lines) != 1 || !strings.Contains(lines[0], "unresolved: "+errEngineCameraShape.Error()) {
		t.Fatalf("decode failure lines %q", lines)
	}
}
