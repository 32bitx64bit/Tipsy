// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"os"
	"sync"
	"sync/atomic"
	"unsafe"
)

const JNIVersion16 = 0x00010006

// VM is a process-local JavaVM large enough for JNI_OnLoad.
type VM struct {
	mu sync.RWMutex

	javaVM    unsafe.Pointer
	envRaw    unsafe.Pointer
	functions unsafe.Pointer

	classes map[string]*Class
	objects map[int64]*Object
	nextID  int64

	pending int64 // throwable object id, 0 = none

	filesDir   string
	cacheDir   string
	obbDir     string
	assetsDir  string
	appVersion string
	dispW     int32
	dispH     int32
	dispMmW   int32
	dispMmH   int32

	natives  map[string]uintptr // class + "." + name + sig → fnPtr
	monitors map[int64]*sync.Mutex

	// localFrames tracks JNI local-ref membership for the pinned JNIEnv.
	// frames[0] lives for the env; PushLocalFrame appends, PopLocalFrame pops.
	localFrames []localFrame

	// Immortal JNI objects reused across calls for stable Android paths and
	// identity strings. Cleared when SetDirs changes the matching path.
	immortalPackageName *Object
	immortalAppVersion  *Object
	immortalLocale      *Object
	immortalFilesDir    *Object
	immortalFilesDirStr *Object
	immortalCacheDir    *Object
	immortalObbDir      *Object
	immortalServices    map[string]*Object
}

// Env is the JNIEnv bound to a VM.
type Env struct {
	vm  *VM
	raw unsafe.Pointer
}

var globalVM atomic.Pointer[VM]

func vmFromEnv(_ unsafe.Pointer) *VM {
	return globalVM.Load()
}

// NewVM constructs a JNI 1.6 JavaVM with the seeded Android/GameActivity classes.
func NewVM() (*VM, error) {
	vm := &VM{
		classes:     make(map[string]*Class),
		objects:     make(map[int64]*Object),
		nextID:      1,
		filesDir:    os.TempDir(),
		cacheDir:    os.TempDir(),
		obbDir:      os.TempDir(),
		assetsDir:   "",
		dispW:       1280,
		dispH:       720,
		natives:     make(map[string]uintptr),
		monitors:    make(map[int64]*sync.Mutex),
		localFrames: []localFrame{{refs: make(map[int64]int)}},
	}
	vm.seedClasses()

	if err := initCBridge(); err != nil {
		return nil, err
	}
	vm.javaVM = javaVMPtr()
	vm.envRaw = envPtr()
	vm.functions = nativeInterfacePtr()

	// Deliver real X11 window input through the engine-registered
	// GameActivity input natives once an input target is wired; until
	// then events are counted as dropped, never synthesized.
	bindX11InputBridge()

	globalVM.Store(vm)
	return vm, nil
}

// Env returns the JNIEnv for this VM.
func (vm *VM) Env() *Env {
	if vm == nil {
		return nil
	}
	return &Env{vm: vm, raw: vm.envRaw}
}

// JavaVM returns the JavaVM* for JNI_OnLoad(JavaVM*, void*).
func (vm *VM) JavaVM() uintptr {
	if vm == nil {
		return 0
	}
	return uintptr(vm.javaVM)
}

// NativeInterface returns the original JNINativeInterface* (not JNIEnv*).
// Roblox wraps env->functions; callers must snapshot this before GetEnv.
func (vm *VM) NativeInterface() uintptr {
	if vm == nil {
		return 0
	}
	return uintptr(vm.functions)
}

// Handle is an alias for JavaVM().
func (vm *VM) Handle() uintptr {
	return vm.JavaVM()
}

// SetAppVersion sets the extracted APK versionName used by getAppVersion
// and DeviceStaticParams.appVersion. Launch reads it from runtime/meta.json.
func (vm *VM) SetAppVersion(version string) {
	if vm == nil {
		return
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if version == vm.appVersion {
		return
	}
	vm.appVersion = version
	vm.immortalAppVersion = nil
}

// SetDirs sets Android-style app directories used by getFilesDir / getCacheDir.
func (vm *VM) SetDirs(files, cache, obb, assets string) {
	if vm == nil {
		return
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if files != "" && files != vm.filesDir {
		vm.filesDir = files
		vm.immortalFilesDir = nil
		vm.immortalFilesDirStr = nil
	}
	if cache != "" && cache != vm.cacheDir {
		vm.cacheDir = cache
		vm.immortalCacheDir = nil
	}
	if obb != "" && obb != vm.obbDir {
		vm.obbDir = obb
		vm.immortalObbDir = nil
	}
	if assets != "" {
		vm.assetsDir = assets
	}
}

// SetDisplaySize is used by DisplayMetrics / getMetrics.
func (vm *VM) SetDisplaySize(w, h int) {
	if vm == nil {
		return
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if w > 0 {
		vm.dispW = int32(w)
	}
	if h > 0 {
		vm.dispH = int32(h)
	}
}

// SetDisplayPhysicalSizeMM sets the host screen's physical size in
// millimeters, used by getScreenPhysicalSizeInMillimeters. Zero values
// leave the 96-DPI fallback in place.
func (vm *VM) SetDisplayPhysicalSizeMM(w, h int) {
	if vm == nil {
		return
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if w > 0 {
		vm.dispMmW = int32(w)
	}
	if h > 0 {
		vm.dispMmH = int32(h)
	}
}

// screenPhysicalSizeMM returns the physical screen size in millimeters:
// the X-server-reported size when the launcher wired one, otherwise the
// standard 96-DPI derivation from the pixel size.
func (vm *VM) screenPhysicalSizeMM() (int32, int32) {
	vm.mu.RLock()
	w, h := vm.dispMmW, vm.dispMmH
	fallback := w <= 0 || h <= 0
	if fallback {
		w = vm.dispW
		h = vm.dispH
	}
	vm.mu.RUnlock()
	if fallback {
		logf("[jni] getScreenPhysicalSizeInMillimeters: no X11 physical size; 96-DPI fallback")
		return int32((int64(w)*254 + 480) / 960), int32((int64(h)*254 + 480) / 960)
	}
	return w, h
}

var onBootstrap func(env, activity uintptr)

// SetOnBootstrap is called from Java MainGameActivity.bootstrapTheApp().
func SetOnBootstrap(fn func(env, activity uintptr)) {
	onBootstrap = fn
}

func (vm *VM) fillDisplayMetricsLocked(m *Object) {
	if m == nil {
		return
	}
	w := vm.dispW
	h := vm.dispH
	if w <= 0 {
		w = 1280
	}
	if h <= 0 {
		h = 720
	}
	if m.fields == nil {
		m.fields = make(map[string]any)
	}
	m.fields["widthPixels"] = w
	m.fields["heightPixels"] = h
	m.fields["density"] = float32(1)
	m.fields["scaledDensity"] = float32(1)
	m.fields["densityDpi"] = int32(160)
	m.fields["xdpi"] = float32(160)
	m.fields["ydpi"] = float32(160)
}

func methodLogName(class, name, sig string) string {
	return class + "." + name + sig
}

// NativeMethod returns a RegisterNatives function pointer, or 0.
func (vm *VM) NativeMethod(class, name, sig string) uintptr {
	if vm == nil {
		return 0
	}
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	return vm.natives[methodLogName(class, name, sig)]
}
