// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

type gameActivitySession struct {
	resize           *surfaceResize
	call             func(name, sig string, extra ...uintptr)
	forceExit        func(int)
	shutdownDeadline time.Duration
	shutdownOnce     sync.Once
	shutdownDuration time.Duration
}

const gracefulShutdownDeadline = 2 * time.Second

const (
	initNativeSym             = "Java_com_google_androidgamesdk_GameActivity_initializeNativeCode"
	setAssetPathSym           = "Java_com_roblox_client_startup_MainGameActivity_nativeSetAssetPath"
	setCacheDirSym            = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetCacheDirectory"
	setFilesDirSym            = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetFilesDirectory"
	setPreferencesFileSym     = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetPreferencesFile"
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
	directMouseLockedSym = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGetMainWindowIsMouseLockedCenter"
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
	nativePassTextSym        = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassText"
	nativePassTextSig        = "(JLjava/lang/String;ZI)V"
	nativeReturnPressedSym   = "Java_com_roblox_engine_jni_NativeGLInterface_nativeReturnPressedFromOnScreenKeyboard"
	syncTextboxSelectionSym  = "Java_com_roblox_engine_jni_NativeGLInterface_syncTextboxTextAndCursorPosition2"
	nativeGetTextBoxInfoSym  = "Java_com_roblox_engine_jni_NativeGLInterface_nativeGetTextBoxInfo"
	appCmdInitWindow         = 1
	appCmdWindowResized      = 3
	appCmdWindowRedraw       = 4
	appCmdContentRectChanged = 5
	appCmdGainedFocus        = 7
	appCmdStart              = 11
	appCmdResume             = 12
)

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

func startGameActivity(ctx context.Context, vm *jni.VM, mod *loader.Module, aw *android.Window, files, cache, preferences, obb, assets, version string, width, height int, currentRefreshHz float32, supportedRefreshHz []float32, req rbxuri.Request) (*gameActivitySession, error) {
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
	writeCommand, _ := mod.Lookup(android.GameActivityWriteCommandSymbol)
	commands, err := android.NewGameActivityCommandWriter(uintptr(handle), writeCommand)
	if err != nil {
		logging.Logger(logging.CatGameActivity).Error("GameActivity command bridge unavailable", "err", err)
		// initializeNativeCode owns a native app thread. Let its registered
		// termination method destroy and join that thread before the caller
		// closes the authenticated module after this compatibility failure.
		callGameActivityNative(vm, env, activity, uintptr(handle), "terminateNativeCode", "(J)V")
		return nil, fmt.Errorf("GameActivity command bridge: %w", err)
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
	return dispatchGameActivityLifecycle(ctx, vm, mod, env, activity, uintptr(handle), commands, files, cache, preferences, assets, version,
		width, height, currentRefreshHz, supportedRefreshHz, aw, req), nil
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

// wireRobloxDirectInput resolves only the public static native methods used by
// the official 2.734.917 mouse listeners. `nativePassMouse` is absent and must
// not be guessed. The ()Z getter is the authority for host pointer capture.
func wireRobloxDirectInput(mod *loader.Module, env *jni.Env) {
	buttonFn, buttonErr := mod.Lookup(directMouseButtonSym)
	moveFn, moveErr := mod.Lookup(directMouseMoveSym)
	wheelFn, wheelErr := mod.Lookup(directMouseWheelSym)
	lockedFn, lockedErr := mod.Lookup(directMouseLockedSym)
	if buttonErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseButtonSym, "err", buttonErr)
	}
	if moveErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseMoveSym, "err", moveErr)
	}
	if wheelErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseWheelSym, "err", wheelErr)
	}
	if lockedErr != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct input export", "sym", directMouseLockedSym, "err", lockedErr)
	}
	class := env.FindClass("com/roblox/engine/jni/NativeInputInterface")
	jni.SetRobloxDirectInputTarget(env.Raw(), class, buttonFn, moveFn, wheelFn, lockedFn)
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
	getInfoFn, getInfoErr := mod.Lookup(nativeGetTextBoxInfoSym)
	for _, missing := range []struct {
		sym string
		err error
	}{
		{nativePassTextSym, passErr},
		{nativeReturnPressedSym, returnErr},
		{syncTextboxSelectionSym, syncErr},
		{nativeGetTextBoxInfoSym, getInfoErr},
	} {
		if missing.err != nil {
			logging.Logger(logging.CatJNI).Info("[jni] missing text input export", "sym", missing.sym, "err", missing.err)
		}
	}
	class := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	jni.SetRobloxTextInputTarget(env, class, passFn, returnFn, syncFn, getInfoFn, loader.CallP8)
}

func dispatchGameActivityLifecycle(ctx context.Context, vm *jni.VM, mod *loader.Module, env *jni.Env, activity, handle uintptr, commands appCommandWriter, files, cache, preferences, assets, version string, width, height int, currentRefreshHz float32, supportedRefreshHz []float32, aw *android.Window, req rbxuri.Request) *gameActivitySession {
	call := func(name, sig string, extra ...uintptr) {
		callGameActivityNative(vm, env, activity, handle, name, sig, extra...)
	}
	call("onStartNative", "(J)V")
	setRobloxCacheAndFiles(mod, env, activity, files, cache)
	content := assetContentDir(assets)
	setRobloxAssetPath(mod, env, activity, content)
	handleColdStartProtocolLaunch(mod, env, activity, req)
	startRobloxApp(mod, env, activity, files, version)
	// Official MainScreenController ON_CREATE publishes Display 0's current
	// and supported refresh rates after native/client-settings initialization
	// and before resume/surface/V2Start. Reproduce that named JNI boundary
	// with the XRandR modes of this window's active output.
	if err := publishDisplayRefreshRates(mod, env, currentRefreshHz, supportedRefreshHz); err != nil {
		logging.Logger(logging.CatGraphics).Error("Android display refresh publication failed", "err", err)
	}
	call("onResumeNative", "(J)V")
	surface := env.AllocObject(env.FindClass("android/view/Surface"))
	call("onSurfaceCreatedNative", "(JLandroid/view/Surface;)V", surface)
	call("onWindowFocusChangedNative", "(JZ)V", 1)

	platform := makePlatformParams(env, content, width, height)
	device := makeDeviceParams(env, width, height, version)
	initParams := makeInitParams(env, activity, platform, device, version)
	callRobloxJNI(mod, env.Raw(), activity, "Java_com_roblox_client_startup_MainGameActivity_nativeAppBridgeSetInitParams", initParams)
	gl := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	if gl == 0 {
		gl = activity
	}
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2InitWithParams", initParams)
	startParams := makeStartAppParams(env, activity, platform, surface, req)
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2StartAppWithParams", startParams)
	startWebsiteGame(mod, env, gl, activity, platform, device, surface, req)
	for _, cmd := range []byte{appCmdInitWindow, appCmdStart, appCmdResume, appCmdGainedFocus, appCmdWindowResized, appCmdWindowRedraw} {
		postAndroidAppCmd(commands, cmd)
	}
	// X11 supplies the first real surface geometry. Deliver it once via the
	// registered GameActivity contract after lifecycle and surface setup.
	deliverInitialContentRect(mod, vm, env, activity, handle, commands, width, height)
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
				mod: mod, vm: vm, env: env, activity: activity, handle: handle, commands: commands, aw: aw,
				gl: gl, surface: surface, platform: platform,
			},
			seeded: true, width: width, height: height,
			minWidth: x11.RobloxMinimumWidth, minHeight: x11.RobloxMinimumHeight,
		},
	}
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

func setRobloxAssetPath(mod *loader.Module, env *jni.Env, activity uintptr, contentDir string) {
	callRobloxJNI(mod, env.Raw(), activity, setAssetPathSym, env.NewStringUTF(contentDir))
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

func startRobloxApp(mod *loader.Module, env *jni.Env, activity uintptr, files, version string) {
	flags, _, err := loadAndroidAppSettings(filepath.Join(files, "ClientAppSettings.json"), version)
	if err != nil || flags == "" {
		logging.Logger(logging.CatGameActivity).Info("client settings unavailable", "err", err)
		return
	}
	gl := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	if gl == 0 {
		gl = activity
	}
	settingsStatus := callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeInitClientSettings", env.NewString(flags), env.NewString(""), env.NewString(""))
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativePostClientSettingsLoadedInitialization3", env.NewArrayList())
	// Complete the APK Java setup phase which owns CookieProtocol construction.
	if int32(settingsStatus) == 0 {
		env.CompleteAuthCookieInitialization()
	} else {
		logging.Logger(logging.CatFilesystem).Error("official cookie initialization unavailable after client-settings failure")
	}
	logNativeCookieRestoreState(mod, env, "after-native-init")
	callRobloxJNI(mod, env.Raw(), activity, "Java_com_roblox_client_startup_MainGameActivity_nativePreloadFlagOverrides", env.NewStringUTF(""))
	startLoggedOutAppBridge(mod, env)
	logNativeCookieRestoreState(mod, env, "after-app-start")
}

// startLoggedOutAppBridge follows the Android bridge order for app startup.
// APK yk/l0 passes rh/w0.g() as the first argument: the production base URL,
// not account identity. NativeUser remains responsible for user state.
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
		env.NewStringUTF("https://www.roblox.com/"), env.NewStringUTF(""), 0,
		env.NewStringUTF(""), env.NewStringUTF(""), env.NewStringUTF(""))
}

type appCommandWriter interface {
	WriteCommand(byte) error
}

func postAndroidAppCmd(commands appCommandWriter, cmd byte) {
	if commands == nil {
		logging.Logger(logging.CatGameActivity).Error("GameActivity app command failed", "command", cmd, "err", "command bridge is unavailable")
		return
	}
	if err := commands.WriteCommand(cmd); err != nil {
		logging.Logger(logging.CatGameActivity).Error("GameActivity app command failed", "command", cmd, "err", err)
	}
}

func deliverInitialContentRect(mod *loader.Module, vm *jni.VM, env *jni.Env, activity, handle uintptr, commands appCommandWriter, width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	postAndroidAppCmd(commands, appCmdContentRectChanged)
	callGameActivityNative(vm, env, activity, handle, "onContentRectChangedNative", "(JIIII)V", 0, 0, uintptr(width), uintptr(height))
	callGameActivityNative(vm, env, activity, handle, "onWindowInsetsChangedNative", "(J)V")
}

func makePlatformParams(env *jni.Env, assets string, width, height int) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/model/PlatformParams"))
	env.PutField(p, "assetFolderPath", assets)
	env.PutField(p, "dpiScale", float32(1))
	// One coherent pointer identity shared with the input dispatchers. Native
	// X11 launches default to a mouse; TIPSY_INPUT_DEVICE=touch retains the
	// Android phone identity as an explicit A/B control.
	touch := jni.PointerDeviceIsTouch()
	env.PutField(p, "isKeyboardDevice", !touch)
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

func makeDeviceParams(env *jni.Env, width, height int, version string) uintptr {
	d := env.AllocObject(env.FindClass("com/roblox/engine/jni/model/DeviceParams"))
	for k, v := range map[string]any{
		"appBuildVariant": "GooglePlay", "appVersion": version, "country": "US", "cpu64Bit": true,
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

func makeInitParams(env *jni.Env, activity, platform, device uintptr, version string) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/InitParams"))
	for k, v := range map[string]any{
		"baseURL": "https://www.roblox.com", "buildVariant": "GooglePlay", "userAgent": robloxUserAgent(version),
		"deviceParams": device, "platformParams": platform, "isPotato": false, "isTablet": false, "isVrDevice": false, "vrContext": activity,
	} {
		env.PutField(p, k, v)
	}
	return p
}

func makeStartAppParams(env *jni.Env, activity, platform, surface uintptr, req rbxuri.Request) uintptr {
	p := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/StartAppParams"))
	for k, v := range map[string]any{
		"surface": surface, "username": "", "appUserId": int64(0), "isUnder13": false, "membershipType": int32(0),
		"selectedTheme": "", "appStarterPlace": appStarterPlace(req), "appStarterScript": "", "platformParams": platform, "vrContext": activity,
	} {
		env.PutField(p, k, v)
	}
	return p
}
