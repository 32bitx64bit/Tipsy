// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

type LaunchOptions struct {
	Probe   bool
	Width   int
	Height  int
	Started func()
}

// launchStartedAck gives in-process frontends one deterministic handoff from
// launcher chrome to the live client. The sync.Once guard makes the API safe
// if the loop setup is refactored to have more than one entry edge later.
type launchStartedAck struct {
	once sync.Once
	fn   func()
}

type gameActivitySession struct {
	resize           *surfaceResize
	call             func(name, sig string, extra ...uintptr)
	forceExit        func(int)
	shutdownDeadline time.Duration
	shutdownOnce     sync.Once
	shutdownDuration time.Duration
}

const gracefulShutdownDeadline = 2 * time.Second

// closeClientModuleBeforeStart releases a client image only when no
// GameActivity session was established. Once initializeNativeCode succeeds,
// Roblox can retain official worker threads beyond terminateNativeCode (the
// HttpClient thread is one observed example). Unmapping libroblox beneath
// those threads is unsafe; the process teardown that immediately follows a
// completed launch is the owner of that mapping instead.
func closeClientModuleBeforeStart(clientStarted bool, closeFn func() error) error {
	if clientStarted || closeFn == nil {
		return nil
	}
	return closeFn()
}

// shutdown follows the official GameActivity Java lifecycle already declared
// by this APK: focus loss, pause, surface destruction, stop, then
// terminateNativeCode. The final call posts APP_CMD_DESTROY and joins the
// native app thread before Launch's deferred module unmap can run. sync.Once
// prevents a context cancellation racing a WM close from double-destroying
// the native handle.
func (s *gameActivitySession) shutdown(reason string) time.Duration {
	if s == nil || s.call == nil {
		return 0
	}
	s.shutdownOnce.Do(func() {
		started := time.Now()
		logging.Logger(logging.CatGameActivity).Info("graceful shutdown started", "reason", reason)
		// The runtime is an in-process host: returning and unmapping libroblox
		// while terminateNativeCode is still executing is unsafe. Give the
		// official lifecycle/join path a generous deadline, then terminate the
		// already-dismissed host process as a last resort so an engine teardown
		// stall cannot leave a hidden Tipsy process indefinitely. Normal closes
		// stop this watchdog hundreds of times before it can fire.
		var watchdog *time.Timer
		if s.forceExit != nil {
			deadline := s.shutdownDeadline
			if deadline <= 0 {
				deadline = gracefulShutdownDeadline
			}
			watchdog = time.AfterFunc(deadline, func() {
				logging.Logger(logging.CatGameActivity).Error("graceful shutdown deadline exceeded; exiting host",
					"reason", reason, "deadline", deadline)
				s.forceExit(0)
			})
		}
		s.call("onWindowFocusChangedNative", "(JZ)V", 0)
		s.call("onPauseNative", "(J)V")
		s.call("onSurfaceDestroyedNative", "(J)V")
		s.call("onStopNative", "(J)V")
		s.call("terminateNativeCode", "(J)V")
		if watchdog != nil {
			watchdog.Stop()
		}
		s.shutdownDuration = time.Since(started)
		logging.Logger(logging.CatGameActivity).Info("graceful shutdown completed",
			"reason", reason, "duration", s.shutdownDuration)
	})
	return s.shutdownDuration
}

func (a *launchStartedAck) signal() {
	if a == nil || a.fn == nil {
		return
	}
	a.once.Do(a.fn)
}

type xidHandle struct{ xid uintptr }

func (h xidHandle) NativeHandle() uintptr { return h.xid }

const (
	initNativeSym             = "Java_com_google_androidgamesdk_GameActivity_initializeNativeCode"
	setAssetPathSym           = "Java_com_roblox_client_startup_MainGameActivity_nativeSetAssetPath"
	setCacheDirSym            = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetCacheDirectory"
	setFilesDirSym            = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetFilesDirectory"
	assetManagerInitNativeSym = "Java_com_roblox_client_JNIAAssetManagerSetup_initNative"
	initStorageManagerV3Sym   = "Java_com_roblox_client_LocalStorageManager_initStorageManagerNativeV3"
	initStorageManagerV3Sig   = "(Landroid/content/res/AssetManager;Ljava/lang/String;Ljava/lang/String;)V"
	appStartSym               = "Java_com_roblox_engine_jni_NativeAppBridgeInterface_nativeAppBridgeAppStart__Ljava_lang_String_2Ljava_lang_String_2ZLjava_lang_String_2Ljava_lang_String_2Ljava_lang_String_2"
	// updateSurfaceSym is the APK-declared, non-overloaded static native
	// NativeGLInterface.nativeAppBridgeV2UpdateSurfaceAppWithPlatformParams(
	// Surface, PlatformParams). It is the public app bridge Android invokes
	// after a real surface-size change; unlike the registered GameActivity
	// onSurfaceChangedNative callback, it does not re-enter surface creation.
	updateSurfaceSym     = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2UpdateSurfaceAppWithPlatformParams"
	directMouseButtonSym = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseButton"
	directMouseMoveSym   = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseMove"
	directMouseWheelSym  = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseWheel"
	directKeyEventSym    = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassKeyEvent"
	// setInputConnectionName/Sig is the exact Java→native handshake the
	// engine registered (DEX ground truth, classes2.dex method table,
	// 2.734.917 — descriptors only, never vendored):
	// com/google/androidgamesdk/GameActivity.setInputConnectionNative(
	//   J, Lcom/google/androidgamesdk/gametextinput/InputConnection;)V
	// Direction is Tipsy→engine: Tipsy CALLs it once after
	// initializeNativeCode with the Tipsy-owned InputConnection object so
	// the engine has somewhere to send State deltas. Resolved via the same
	// RegisterNatives path as other GameActivity natives (callGameActivityNative),
	// never a version-pinned file vaddr.
	setInputConnectionName = "setInputConnectionNative"
	setInputConnectionSig  = "(JLcom/google/androidgamesdk/gametextinput/InputConnection;)V"
	// nativePassTextSym/Sig is the named commit route the engine exports
	// (named dynsym, no vaddrs):
	// com/roblox/engine/jni/NativeGLInterface.nativePassText(
	//   J, Ljava/lang/String;, Z, I)V
	// The caller is focus-gated by the engine's own showKeyboard textbox
	// handle. Its text comes only from the X11 input method's committed UTF-8.
	nativePassTextSym       = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassText"
	nativePassTextSig       = "(JLjava/lang/String;ZI)V"
	nativeReturnPressedSym  = "Java_com_roblox_engine_jni_NativeGLInterface_nativeReturnPressedFromOnScreenKeyboard"
	syncTextboxSelectionSym = "Java_com_roblox_engine_jni_NativeGLInterface_syncTextboxTextAndCursorPosition2"
	lsmSingletonVA          = 0x74d74f0

	appCmdInitWindow         = 1
	appCmdWindowResized      = 3
	appCmdWindowRedraw       = 4
	appCmdContentRectChanged = 5
	appCmdGainedFocus        = 7
	appCmdStart              = 11
	appCmdResume             = 12

	// inputDispatchInterval bounds the Go-side half of X11-to-engine input
	// delivery. The C background pump captures at 2 ms; 4 ms avoids the
	// previous 16 ms + 16 ms cursor lag while retaining bounded coalescing.
	inputDispatchInterval = 4 * time.Millisecond
)

// Launch starts the official extracted Android x86-64 client under native X11.
// It intentionally performs no writes to libroblox.so text.
func Launch(ctx context.Context, opt LaunchOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Android app-private files are owner-only by default. Keep that invariant
	// for files the unmodified client creates itself, not only files Tipsy
	// prepares directly.
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)
	if opt.Width <= 0 {
		opt.Width = 1280
	}
	if opt.Height <= 0 {
		opt.Height = 720
	}
	started := &launchStartedAck{fn: opt.Started}
	dir, err := filepath.Abs(RuntimeDir())
	if err != nil {
		return fmt.Errorf("runtime directory: %w", err)
	}
	lib := filepath.Join(dir, "lib", "x86_64", "libroblox.so")
	if _, err := os.Stat(lib); err != nil {
		return fmt.Errorf("runtime not set up; run: tipsy setup <official-apk-or-dir>")
	}
	releaseClientLock, err := clientsettings.AcquireClientLock()
	if err != nil {
		return err
	}
	defer releaseClientLock()
	storage, migration, err := prepareAppStorage(dir)
	if err != nil {
		return fmt.Errorf("persistent app storage: %w", err)
	}
	logAppStorageMigration(migration)
	settingsService := clientsettings.New()
	if err := settingsService.ReconcileWhileClientLocked(ctx); err != nil {
		return fmt.Errorf("client settings: %w", err)
	}
	// Roblox may normalize experimental values while shutting down. Reapply an
	// explicit Tipsy-owned value after the client loop exits, while the launch
	// lock still excludes GUI settings writes. The next launch also reconciles.
	defer func() {
		_ = settingsService.ReconcileWhileClientLocked(context.Background())
	}()
	files := storage.FilesDir
	cache := storage.CacheDir
	assets := filepath.Join(dir, "assets")
	obb := filepath.Join(dir, "obb")
	for _, d := range []string{obb, filepath.Join(dir, "android")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := EnsureOfficialPatches(ctx, assets); err != nil {
		return fmt.Errorf("official ExtraContent: %w", err)
	}
	if err := EnsureOfficialFontViews(assets); err != nil {
		return fmt.Errorf("official fonts: %w", err)
	}
	// Install the APK's authoritative CA bundle under Android FilesDir before
	// native startup. Do not change the process CWD here: an otherwise
	// identical launch that entered FilesDir caused Roblox's HttpClient thread
	// to terminate itself during Startup. The remaining relative-open bridge is
	// a separate Android-ABI concern and must not be papered over with Chdir.
	if err := prepareRuntimeFiles(files, assets); err != nil {
		return fmt.Errorf("runtime TLS files: %w", err)
	}

	goruntime.LockOSThread()
	win, err := x11.Open("Roblox", opt.Width, opt.Height)
	if err != nil {
		return fmt.Errorf("x11: %w", err)
	}
	defer win.Close()
	eglSurf, err := graphics.BindEGL(win)
	if err != nil {
		return fmt.Errorf("egl: %w", err)
	}
	defer eglSurf.Close()
	_ = eglSurf.Swap()
	if err := eglSurf.ReleaseCurrent(); err != nil {
		return fmt.Errorf("egl release: %w", err)
	}
	if err := win.StartBackgroundPump(); err != nil {
		return fmt.Errorf("x11 pump thread: %w", err)
	}
	defer win.StopBackgroundPump()
	if err := eglSurf.StartSwapThread(); err != nil {
		logging.Logger(logging.CatRuntime).Info("EGL swap thread skipped", "err", err)
	}
	defer eglSurf.StopSwapThread()

	android.NewResolver(android.Config{AssetsDir: assets, APKPath: filepath.Join(dir, "apk", "base.apk"), Width: int32(opt.Width), Height: int32(opt.Height)})
	aw := android.NewWindow(opt.Width, opt.Height, xidHandle{xid: win.XID()})
	android.BindDefaultWindow(aw)
	vm, err := jni.NewVM()
	if err != nil {
		return fmt.Errorf("jni: %w", err)
	}
	vm.SetDirs(files, cache, obb, assets)
	vm.SetDisplaySize(opt.Width, opt.Height)
	if mmW, mmH := jni.X11DisplayPhysicalSizeMM(win.Display()); mmW > 0 && mmH > 0 {
		vm.SetDisplayPhysicalSizeMM(mmW, mmH)
	}
	loader.SetJNIFunctions(vm.NativeInterface())

	mod, err := loader.Open(lib, android.Provider())
	if err != nil {
		return err
	}
	clientStarted := false
	defer func() {
		if err := closeClientModuleBeforeStart(clientStarted, mod.Close); err != nil {
			logging.Logger(logging.CatRuntime).Info("client module close failed", "err", err)
		}
	}()
	android.Register("libroblox.so", func(sym string) (uintptr, error) { return mod.Lookup(sym) })
	android.RegisterImage(mod.Base, lib)
	if err := mod.Init(); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if os.Getenv("TIPSY_CRASH_DIAG") == "1" {
		if err := installCrashDiagHandler(); err != nil {
			logging.Logger(logging.CatRuntime).Info("crash diag", "err", err)
		}
	}
	onload, err := mod.Lookup("JNI_OnLoad")
	if err != nil {
		return fmt.Errorf("JNI_OnLoad: %w", err)
	}
	if ver := loader.CallJNIOnLoad(onload, vm.JavaVM(), 0); ver < 0 {
		return fmt.Errorf("JNI_OnLoad failed (%d)", ver)
	}
	if opt.Probe {
		return nil
	}
	session, err := startGameActivity(ctx, vm, mod, aw, files, cache, obb, opt.Width, opt.Height)
	if err != nil {
		return err
	}
	// A successful session means initializeNativeCode has entered the official
	// client and may have created engine-owned workers. Keep the image mapped
	// until the CLI/GUI host process exits; do not race those workers with
	// loader.Module.Close/rawMunmap after the visible window is dismissed.
	clientStarted = true
	resize := session.resize
	defer jni.ClearRobloxDirectInputTarget()
	defer jni.ClearRobloxDirectKeyTarget()
	defer jni.ClearRobloxTextInputTarget()
	defer jni.ClearGameActivityInputTarget()
	// ConfigureNotify enters the same ordered X11 stream as pointer events.
	// When a resize and a later pointer move are drained together, this
	// handler runs before the JNI bridge delivers that move to Roblox.
	cancelResizeInput := x11.OnInput(func(ev x11.InputEvent) {
		if ev.Kind == x11.InputResize {
			resize.observe(ev.Width, ev.Height)
		}
	})
	defer cancelResizeInput()

	ticker := time.NewTicker(inputDispatchInterval)
	defer ticker.Stop()
	stats := time.NewTicker(2 * time.Second)
	defer stats.Stop()
	// Everything needed by the in-process client loop is live: X11/EGL and
	// its pump were established above, GameActivity startup succeeded, input
	// targets are wired, and resize/input subscribers plus loop tickers are
	// installed. Immediate failures and --probe return before this boundary.
	started.signal()
	shutdownClient := func(reason string) {
		_ = win.Dismiss()
		_ = win.StopBackgroundPump()
		eglSurf.StopSwapThread()
		session.shutdown(reason)
	}
	for {
		select {
		case <-ctx.Done():
			shutdownClient("context-cancelled")
			return ctx.Err()
		case <-stats.C:
			s := jni.InputDeliveryStats()
			d := jni.RobloxDirectInputStats()
			textPass, textReturn, textSync, textDrop := jni.RbxTextDeliveryStats()
			logging.Logger(logging.CatRuntime).Info("input delivery",
				"path", jni.PointerInputPath().String(),
				"focus", s.FocusDelivered, "gameActivityKeys", s.KeyDelivered, "gameActivityPointers", s.PointerDelivered,
				"gameActivityConsumed", s.KeyConsumed+s.PointerConsumed, "gameActivityDropped", s.Dropped,
				"directKeys", d.KeyDelivered, "directButtons", d.ButtonDelivered, "directMoves", d.MoveDelivered, "directWheels", d.WheelDelivered, "directDropped", d.Dropped,
				"textPass", textPass, "textReturn", textReturn, "textSync", textSync, "textDropped", textDrop)
		case <-ticker.C:
			if err := win.Pump(); err != nil {
				if err == x11.ErrClosed {
					// Pump has already unmapped the window, bounding visible close
					// response independently from native teardown. Stop producers,
					// then let the official GameActivity destroy/join complete before
					// the deferred libroblox unmap.
					shutdownClient("wm-delete-window")
					return nil
				}
				return err
			}
			// X11 delivers the normal path above in exact event order. Keep a
			// deduplicated fallback for a future window backend that updates
			// Size() without emitting InputResize.
			w, h := win.Size()
			resize.observe(w, h)
		}
	}
}

// installCrashDiagHandler remains opt-in. The stripped base deliberately does
// not alter process signals or Roblox memory unless diagnostics are explicitly
// requested; the loader's normal core-dump path remains intact.
func installCrashDiagHandler() error {
	logging.Logger(logging.CatRuntime).Info("crash diagnostics requested; using normal core-dump path")
	return nil
}

func startGameActivity(ctx context.Context, vm *jni.VM, mod *loader.Module, aw *android.Window, files, cache, obb string, width, height int) (*gameActivitySession, error) {
	env := vm.Env()
	activity := env.AllocObject(env.FindClass("com/roblox/client/startup/MainGameActivity"))
	if activity == 0 {
		return nil, fmt.Errorf("AllocObject MainGameActivity failed")
	}
	assetsObj := env.AllocObject(env.FindClass("android/content/res/AssetManager"))
	cfg := env.AllocObject(env.FindClass("android/content/res/Configuration"))
	initJNIAAssetManager(mod, env, assetsObj)
	fn, err := mod.Lookup(initNativeSym)
	if err != nil {
		return nil, fmt.Errorf("initializeNativeCode: %w", err)
	}
	handle := loader.CallP8(fn, env.Raw(), activity, env.NewStringUTF(files), env.NewStringUTF(obb), env.NewStringUTF(files), assetsObj, 0, cfg)
	if handle == 0 {
		return nil, fmt.Errorf("initializeNativeCode returned 0")
	}
	jni.SetGameActivityInputTarget(env.Raw(), activity, uintptr(handle))
	// Text-input handshake (§88): hand the Tipsy-owned InputConnection to
	// the engine immediately after the input target, on this same
	// Main-pthread stack (CallP8 hops there like every other GameActivity
	// native — never a goroutine hop). The dormant commit gate runs right
	// after. The InputConnection is engine→Java state priming; typed text
	// takes the APK's separate RbxKeyboard nativePassText path wired below.
	deliverTextInputConnection(vm, env, activity, uintptr(handle))
	wireRobloxDirectInput(mod, env)
	wireRobloxDirectKey(mod, env)
	wireRobloxTextInput(mod, env)
	return dispatchGameActivityLifecycle(ctx, vm, mod, env, activity, uintptr(handle), files, cache, width, height, aw), nil
}

// deliverTextInputConnection ensures the Tipsy-owned InputConnection object
// (jni.EnsureTextInputConnection, a real VM object — opaque handle, never an
// engine pointer) and delivers it through the engine-registered
// setInputConnectionNative(J, InputConnection)V via the same
// RegisterNatives-resolved path as other GameActivity natives. The jobject
// encoding in this VM is the object id itself (cgo idToJobject/jobjectToID;
// Env.NewString does uintptr(idToJobject(id))), so uintptr(conn) IS the
// honest Java object — no wrapper fabrication, no fake engine pointers. A
// zero VM/env/activity/handle parks honestly (returns 0, no call). A missing
// engine registration logs the honest missing-native line and keeps the
// object for a later focus-driven retry — never a stub success. No text
// content is handled or logged here (opaque ids only); activation still
// follows only the engine's own showKeyboard/hideKeyboard focus signals
// inside internal/jni.
func deliverTextInputConnection(vm *jni.VM, env *jni.Env, activity, handle uintptr) int64 {
	if vm == nil || env == nil || activity == 0 || handle == 0 {
		return 0
	}
	conn := vm.EnsureTextInputConnection()
	if conn == 0 {
		logging.Logger(logging.CatGameActivity).Info("text input connection unavailable")
		return 0
	}
	callGameActivityNative(vm, env, activity, handle, setInputConnectionName, setInputConnectionSig, uintptr(conn))
	logging.Logger(logging.CatGameActivity).Info("text input connection delivered", "conn", conn)
	return conn
}

// wireRobloxDirectInput resolves only the three public static native methods
// used by the official 2.734.917 mouse listener. `nativePassMouse` is absent
// and must not be guessed. Delivery remains A/B-gated inside internal/jni.
func wireRobloxDirectInput(mod *loader.Module, env *jni.Env) {
	buttonFn, buttonErr := mod.Lookup(directMouseButtonSym)
	moveFn, moveErr := mod.Lookup(directMouseMoveSym)
	wheelFn, wheelErr := mod.Lookup(directMouseWheelSym)
	if buttonErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseButtonSym, "err", buttonErr)
	}
	if moveErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseMoveSym, "err", moveErr)
	}
	if wheelErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseWheelSym, "err", wheelErr)
	}
	class := env.FindClass("com/roblox/engine/jni/NativeInputInterface")
	jni.SetRobloxDirectInputTarget(env.Raw(), class, buttonFn, moveFn, wheelFn)
}

// wireRobloxDirectKey resolves the separate public static native key route
// proven by the official APK. NativeGLInterface owns nativePassKeyEvent;
// it must not be conflated with the mouse methods on NativeInputInterface.
func wireRobloxDirectKey(mod *loader.Module, env *jni.Env) {
	fn, err := mod.Lookup(directKeyEventSym)
	if err != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct key export", "sym", directKeyEventSym, "err", err)
	}
	class := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	jni.SetRobloxDirectKeyTarget(env.Raw(), class, fn)
}

// wireRobloxTextInput resolves the exact public static natives used by the
// APK's RbxKeyboard EditText. nativePassText is the required typing route;
// editor action and cursor selection are optional companions. All are named
// JNI exports, never version-pinned addresses or hooks.
func wireRobloxTextInput(mod *loader.Module, env *jni.Env) {
	passFn, passErr := mod.Lookup(nativePassTextSym)
	returnFn, returnErr := mod.Lookup(nativeReturnPressedSym)
	syncFn, syncErr := mod.Lookup(syncTextboxSelectionSym)
	for _, missing := range []struct {
		sym string
		err error
	}{
		{nativePassTextSym, passErr},
		{nativeReturnPressedSym, returnErr},
		{syncTextboxSelectionSym, syncErr},
	} {
		if missing.err != nil {
			logging.Logger(logging.CatJNI).Info("[jni] missing text input export", "sym", missing.sym, "err", missing.err)
		}
	}
	class := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	jni.SetRobloxTextInputTarget(env, class, passFn, returnFn, syncFn, loader.CallP8)
}

func dispatchGameActivityLifecycle(ctx context.Context, vm *jni.VM, mod *loader.Module, env *jni.Env, activity, handle uintptr, files, cache string, width, height int, aw *android.Window) *gameActivitySession {
	call := func(name, sig string, extra ...uintptr) {
		callGameActivityNative(vm, env, activity, handle, name, sig, extra...)
	}
	call("onStartNative", "(J)V")
	setRobloxCacheAndFiles(mod, env, activity, files, cache)
	setRobloxAssetPath(mod, env, activity)
	startRobloxApp(mod, env, activity, files)
	call("onResumeNative", "(J)V")
	surface := env.AllocObject(env.FindClass("android/view/Surface"))
	call("onSurfaceCreatedNative", "(JLandroid/view/Surface;)V", surface)
	call("onWindowFocusChangedNative", "(JZ)V", 1)

	platform := makePlatformParams(env, assetContentDir(), width, height)
	device := makeDeviceParams(env, width, height)
	initParams := makeInitParams(env, activity, platform, device)
	callRobloxJNI(mod, env.Raw(), activity, "Java_com_roblox_client_startup_MainGameActivity_nativeAppBridgeSetInitParams", initParams)
	gl := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	if gl == 0 {
		gl = activity
	}
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2InitWithParams", initParams)
	startParams := makeStartAppParams(env, activity, platform, surface)
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2StartAppWithParams", startParams)
	for _, cmd := range []byte{appCmdInitWindow, appCmdStart, appCmdResume, appCmdGainedFocus, appCmdWindowResized, appCmdWindowRedraw} {
		postAndroidAppCmd(mod, cmd)
	}
	// X11 supplies the first real surface geometry. Deliver it once via the
	// registered GameActivity contract after lifecycle and surface setup.
	deliverInitialContentRect(mod, vm, env, activity, handle, width, height)
	for _, n := range [][2]string{
		{"onWindowFocusChangedNative", "(JZ)V"},
		{"onKeyDownNative", "(JLandroid/view/KeyEvent;)Z"},
		{"onKeyUpNative", "(JLandroid/view/KeyEvent;)Z"},
		{"onTouchEventNative", "(JLandroid/view/MotionEvent;IIIIIJJIIIIIIFF)Z"},
	} {
		logging.Logger(logging.CatGameActivity).Info("input native lookup",
			"name", n[0], "fn", fmt.Sprintf("%#x", vm.NativeMethod("com/google/androidgamesdk/GameActivity", n[0], n[1])))
	}
	return &gameActivitySession{
		call:             call,
		forceExit:        os.Exit,
		shutdownDeadline: gracefulShutdownDeadline,
		resize: &surfaceResize{
			sink: &engineResizeSink{
				mod: mod, vm: vm, env: env, activity: activity, handle: handle, aw: aw,
				gl: gl, surface: surface, platform: platform,
			},
			seeded: true, width: width, height: height,
		},
	}
}

// surfaceResize propagates genuine X11 size deltas into the surface-geometry
// holders the engine reads once at startup and otherwise never updates: the
// ANativeWindow buffer geometry, the JNI DisplayMetrics, and the GameActivity
// content-rect/insets contract. It runs only on the Launch ticker goroutine —
// the same thread that delivered the startup lifecycle callbacks and the X11
// input events — so callback lookups and native calls stay serialized with
// the rest of the engine-facing surface and need no extra synchronization.
type surfaceResize struct {
	sink resizeSink

	seeded        bool
	width, height int
}

// resizeSink is the engine-facing update surface, factored out so tests can
// pin ordering and dedupe deterministically without a mapped engine.
type resizeSink interface {
	resizeBuffers(width, height int) error
	setDisplaySize(width, height int)
	postAppCmd(cmd byte)
	updateSurface(width, height int)
	callNative(name, sig string, extra ...uintptr)
}

// engineResizeSink adapts the real engine-facing updates.
type engineResizeSink struct {
	mod      *loader.Module
	vm       *jni.VM
	env      *jni.Env
	activity uintptr
	handle   uintptr
	aw       *android.Window
	gl       uintptr
	surface  uintptr
	platform uintptr
}

func (s *engineResizeSink) resizeBuffers(width, height int) error { return s.aw.Resize(width, height) }

func (s *engineResizeSink) setDisplaySize(width, height int) { s.vm.SetDisplaySize(width, height) }

func (s *engineResizeSink) postAppCmd(cmd byte) { postAndroidAppCmd(s.mod, cmd) }

func (s *engineResizeSink) updateSurface(width, height int) {
	if s == nil || s.mod == nil || s.env == nil || s.gl == 0 || s.surface == 0 || s.platform == 0 {
		return
	}
	setPlatformViewport(s.env, s.platform, width, height)
	callRobloxJNI(s.mod, s.env.Raw(), s.gl, updateSurfaceSym, s.surface, s.platform)
}

func (s *engineResizeSink) callNative(name, sig string, extra ...uintptr) {
	callGameActivityNative(s.vm, s.env, s.activity, s.handle, name, sig, extra...)
}

// observe compares win.Size() after a Pump with the last delivered
// dimensions. Invalid sizes are ignored, unchanged sizes are deduplicated,
// and one genuine positive delta produces exactly one delivery, in the
// startup order: ANativeWindow geometry, DisplayMetrics, then the public
// GameActivity resize contract — APP_CMD_WINDOW_RESIZED (3) and
// APP_CMD_WINDOW_REDRAW_NEEDED (4). The current client consumes those
// commands but its NativeDM fallback may return before updating the render
// size, so follow them with the APK-declared V2 surface-update JNI bridge and
// refreshed PlatformParams. APP_CMD_CONTENT_RECT_CHANGED (5) plus the
// content-rect/insets callbacks remain last. A failed geometry update aborts
// the delivery honestly instead of delivering a surface size the native
// window does not have.
func (s *surfaceResize) observe(w, h int) {
	if s == nil || w <= 0 || h <= 0 {
		return
	}
	if s.seeded && s.width == w && s.height == h {
		return
	}
	if err := s.sink.resizeBuffers(w, h); err != nil {
		logging.Logger(logging.CatRuntime).Info("surface resize skipped", "err", err)
		return
	}
	s.sink.setDisplaySize(w, h)
	s.sink.postAppCmd(appCmdWindowResized)
	s.sink.postAppCmd(appCmdWindowRedraw)
	s.sink.updateSurface(w, h)
	s.sink.postAppCmd(appCmdContentRectChanged)
	s.sink.callNative("onContentRectChangedNative", "(JIIII)V", 0, 0, uintptr(w), uintptr(h))
	s.sink.callNative("onWindowInsetsChangedNative", "(J)V")
	s.seeded, s.width, s.height = true, w, h
	logging.Logger(logging.CatGameActivity).Info("surface resize delivered", "width", w, "height", h)
}

func initJNIAAssetManager(mod *loader.Module, env *jni.Env, assetsObj uintptr) {
	fn, err := mod.Lookup(assetManagerInitNativeSym)
	if err != nil || fn == 0 {
		return
	}
	cls := env.FindClass("com/roblox/client/JNIAAssetManagerSetup")
	loader.CallP3(fn, env.Raw(), cls, assetsObj)
}

func callRobloxJNI(mod *loader.Module, env, activity uintptr, sym string, extra ...uintptr) int64 {
	fn, err := mod.Lookup(sym)
	if err != nil || fn == 0 {
		logging.Logger(logging.CatGameActivity).Info("missing JNI export", "sym", sym)
		return 0
	}
	args := append([]uintptr{env, activity}, extra...)
	for len(args) < 8 {
		args = append(args, 0)
	}
	return loader.CallP8(fn, args[0], args[1], args[2], args[3], args[4], args[5], args[6], args[7])
}

func callGameActivityNative(vm *jni.VM, env *jni.Env, activity, handle uintptr, name, sig string, extra ...uintptr) {
	fn := vm.NativeMethod("com/google/androidgamesdk/GameActivity", name, sig)
	if fn == 0 {
		fn = vm.NativeMethod("com/roblox/client/startup/MainGameActivity", name, sig)
	}
	if fn == 0 {
		logging.Logger(logging.CatGameActivity).Info("missing GameActivity native", "name", name, "sig", sig)
		return
	}
	args := append([]uintptr{env.Raw(), activity, handle}, extra...)
	for len(args) < 8 {
		args = append(args, 0)
	}
	loader.CallP8(fn, args[0], args[1], args[2], args[3], args[4], args[5], args[6], args[7])
}

func setRobloxAssetPath(mod *loader.Module, env *jni.Env, activity uintptr) {
	callRobloxJNI(mod, env.Raw(), activity, setAssetPathSym, env.NewStringUTF(assetContentDir()))
}

func setRobloxCacheAndFiles(mod *loader.Module, env *jni.Env, activity uintptr, files, cache string) {
	files, cache = absExistingDir(files), absExistingDir(cache)
	if files == "" || cache == "" {
		return
	}
	callRobloxJNI(mod, env.Raw(), activity, setCacheDirSym, env.NewStringUTF(cache))
	callRobloxJNI(mod, env.Raw(), activity, setFilesDirSym, env.NewStringUTF(files))
	initRobloxLocalStorageManager(mod, env, files, cache)
}

func initRobloxLocalStorageManager(mod *loader.Module, env *jni.Env, files, cache string) {
	fn, err := mod.Lookup(initStorageManagerV3Sym)
	if err != nil || fn == 0 {
		return
	}
	thiz := env.AllocObject(env.FindClass("com/roblox/client/LocalStorageManager"))
	am := env.AllocObject(env.FindClass("android/content/res/AssetManager"))
	loader.CallP8(fn, env.Raw(), thiz, am, env.NewStringUTF(files), env.NewStringUTF(cache), 0, 0, 0)
}

func startRobloxApp(mod *loader.Module, env *jni.Env, activity uintptr, files string) {
	flags, _, err := loadAndroidAppSettings(filepath.Join(files, "ClientAppSettings.json"))
	if err != nil || flags == "" {
		logging.Logger(logging.CatGameActivity).Info("client settings unavailable", "err", err)
		return
	}
	gl := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	if gl == 0 {
		gl = activity
	}
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeInitClientSettings", env.NewString(flags), env.NewString(""), env.NewString(""))
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativePostClientSettingsLoadedInitialization3", env.NewArrayList())
	callRobloxJNI(mod, env.Raw(), activity, "Java_com_roblox_client_startup_MainGameActivity_nativePreloadFlagOverrides", env.NewStringUTF(""))
	startLoggedOutAppBridge(mod, env)
}

// startLoggedOutAppBridge follows the Android bridge order observed for a
// logged-out client. The six generic arguments are empty/false, not account
// identity values; NativeUser remains responsible for user state.
func startLoggedOutAppBridge(mod *loader.Module, env *jni.Env) {
	if mod == nil || env == nil {
		return
	}
	bridge := env.FindClass("com/roblox/engine/jni/NativeAppBridgeInterface")
	if bridge == 0 {
		return
	}
	fn, err := mod.Lookup(appStartSym)
	if err != nil || fn == 0 {
		logging.Logger(logging.CatGameActivity).Info("missing JNI export", "sym", appStartSym)
		return
	}
	logging.Logger(logging.CatGameActivity).Info("calling logged-out JNI bridge", "sym", appStartSym)
	loader.CallP8(fn, env.Raw(), bridge,
		env.NewStringUTF(""), env.NewStringUTF(""), 0,
		env.NewStringUTF(""), env.NewStringUTF(""), env.NewStringUTF(""))
}

func postAndroidAppCmd(mod *loader.Module, cmd byte) {
	const nativeEngineSingletonVA = 0x6fedc78
	const androidAppFromEngineOff = 0x10
	const androidAppMsgWriteOff = 0x124
	if mod == nil || mod.Base == 0 {
		return
	}
	// This is the official android_app command pipe established by
	// initializeNativeCode, not a libroblox text patch.
	engine := *(*uintptr)(unsafe.Pointer(mod.Base + nativeEngineSingletonVA))
	if engine < 0x10000 {
		return
	}
	app := *(*uintptr)(unsafe.Pointer(engine + androidAppFromEngineOff))
	if app < 0x10000 {
		return
	}
	fd := int(*(*int32)(unsafe.Pointer(app + androidAppMsgWriteOff)))
	if fd >= 3 {
		_, _ = syscall.Write(fd, []byte{cmd})
	}
}

func deliverInitialContentRect(mod *loader.Module, vm *jni.VM, env *jni.Env, activity, handle uintptr, width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	postAndroidAppCmd(mod, appCmdContentRectChanged)
	callGameActivityNative(vm, env, activity, handle, "onContentRectChangedNative", "(JIIII)V", 0, 0, uintptr(width), uintptr(height))
	callGameActivityNative(vm, env, activity, handle, "onWindowInsetsChangedNative", "(J)V")
}

func assetContentDir() string { return filepath.Join(RuntimeDir(), "assets", "content") }

func absExistingDir(path string) string {
	if strings.TrimSpace(path) == "" || os.MkdirAll(path, 0o700) != nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return abs
}

func EnsureOfficialFontViews(assetsDir string) error {
	if strings.TrimSpace(assetsDir) == "" {
		return nil
	}
	src := filepath.Join(assetsDir, "content", "fonts")
	st, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !st.IsDir() {
		return nil
	}
	src, err = filepath.Abs(src)
	if err != nil {
		return err
	}
	for _, dest := range []string{filepath.Join(assetsDir, "android", "fonts"), filepath.Join(assetsDir, "ExtraContent", "fonts")} {
		if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dest, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0o700)
			}
			if _, err := os.Lstat(target); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.Symlink(path, target)
		}); err != nil {
			return err
		}
	}
	return nil
}

// prepareRuntimeFiles installs the CA bundle the official APK shipped under
// Android FilesDir. It deliberately does not alter process-global CWD: normal
// X11 startup depends on the caller's existing relative-resource context.
func prepareRuntimeFiles(filesDir, assetsDir string) error {
	return ensureRobloxCABundle(filesDir, assetsDir)
}

// ensureRobloxCABundle prepares the exact relative path libroblox opens.
// The official APK asset is authoritative; an absent or unusable asset is a
// startup error.  Falling back to the host CA store could silently change the
// client's trust configuration, so it is deliberately not supported here.
func ensureRobloxCABundle(filesDir, assetsDir string) error {
	if strings.TrimSpace(filesDir) == "" {
		return fmt.Errorf("files directory is empty")
	}
	if strings.TrimSpace(assetsDir) == "" {
		return fmt.Errorf("official CA bundle assets directory is empty")
	}
	official := filepath.Join(assetsDir, "ssl", "cacert.pem")
	st, err := os.Stat(official)
	if err != nil {
		return fmt.Errorf("inspect official CA bundle: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 {
		return fmt.Errorf("official CA bundle is not a nonempty regular file: %s", official)
	}
	dest := filepath.Join(filesDir, "exe", "cacert.pem")
	if err := copyFileAtomically(official, dest); err != nil {
		return fmt.Errorf("install official CA bundle: %w", err)
	}
	return nil
}

func copyFileAtomically(src, dest string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return fmt.Errorf("source is empty: %s", src)
	}
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, b) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".cacert-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func makePlatformParams(env *jni.Env, assets string, width, height int) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/model/PlatformParams"))
	env.PutField(p, "assetFolderPath", assets)
	env.PutField(p, "dpiScale", float32(1))
	// One coherent pointer identity shared with the input dispatchers. Native
	// X11 launches default to a mouse; TIPSY_INPUT_DEVICE=touch retains the
	// Android phone identity as an explicit A/B control.
	touch := jni.PointerDeviceIsTouch()
	env.PutField(p, "isKeyboardDevice", true)
	env.PutField(p, "isMouseDevice", !touch)
	env.PutField(p, "isTouchDevice", touch)
	setPlatformViewport(env, p, width, height)
	return p
}

// setPlatformViewport refreshes the two geometry fields present in the
// official PlatformParams DEX class. Width/height stay X11 density-1 pixels;
// the existing 160 dpi conversion is preserved exactly from startup.
func setPlatformViewport(env *jni.Env, platform uintptr, width, height int) {
	if env == nil || platform == 0 || width <= 0 || height <= 0 {
		return
	}
	env.PutField(platform, "viewportWidthMm", int32(width*254/1600))
	env.PutField(platform, "viewportHeightMm", int32(height*254/1600))
}

func makeDeviceParams(env *jni.Env, width, height int) uintptr {
	d := env.AllocObject(env.FindClass("com/roblox/engine/jni/model/DeviceParams"))
	for k, v := range map[string]any{
		"appBuildVariant": "GooglePlay", "appVersion": "2.734.917", "country": "US", "cpu64Bit": true,
		"deviceName": "tipsy", "deviceSku": "tipsy", "deviceTotalMemoryMB": int32(8192),
		"displayPhysicalHeightPixels": int32(height), "displayPhysicalWidthPixels": int32(width),
		"displayResolution": fmt.Sprintf("%dx%d", width, height), "isChrome": false, "isLowRamDevice": false,
		"largeMemoryClass": int32(512), "manufacturer": "Tipsy", "memoryClass": int32(256), "networkType": "WIFI",
		"osVersion": "26", "socModel": "generic", "testDeviceName": "",
	} {
		env.PutField(d, k, v)
	}
	return d
}

func makeInitParams(env *jni.Env, activity, platform, device uintptr) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/InitParams"))
	for k, v := range map[string]any{
		"baseURL": "https://www.roblox.com", "buildVariant": "GooglePlay", "userAgent": "Roblox/2.734.917 (Linux; Android 8.0.0; tipsy)",
		"deviceParams": device, "platformParams": platform, "isPotato": false, "isTablet": false, "isVrDevice": false, "vrContext": activity,
	} {
		env.PutField(p, k, v)
	}
	return p
}

func makeStartAppParams(env *jni.Env, activity, platform, surface uintptr) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/StartAppParams"))
	for k, v := range map[string]any{
		"surface": surface, "username": "", "appUserId": int64(0), "isUnder13": false, "membershipType": int32(0),
		"selectedTheme": "", "appStarterPlace": "", "appStarterScript": "", "platformParams": platform, "vrContext": activity,
	} {
		env.PutField(p, k, v)
	}
	return p
}
