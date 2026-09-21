// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"errors"
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
	closeLifecycle   func()
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
	// updateSurfaceSym is the public app bridge Android invokes after a real
	// surface-size change; unlike the GameActivity onSurfaceChangedNative callback,
	// it does not re-enter surface creation.
	updateSurfaceSym     = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2UpdateSurfaceAppWithPlatformParams"
	directMouseButtonSym = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseButton"
	directMouseMoveSym   = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseMove"
	directMouseWheelSym  = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseWheel"
	directMouseLockedSym = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGetMainWindowIsMouseLockedCenter"
	directKeyEventSym    = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassKeyEvent"
	// Direct gamepad family: only these six names are resolved; other
	// gamepad-shaped names must never be guessed.
	directGamepadAxisSym      = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGamepadAxisEvent"
	directGamepadButtonSym    = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGamepadButtonEvent"
	directGamepadConnectSym   = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGamepadConnectEventWithGamepadType"
	directGamepadDisconnSym   = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGamepadDisconnectEvent"
	directGamepadSetKeySym    = "Java_com_roblox_engine_jni_NativeInputInterface_nativeSetGamepadSupportedKeyWithGamepadType"
	directGamepadSetMotionSym = "Java_com_roblox_engine_jni_NativeInputInterface_nativeSetGamepadSupportedMotionWithGamepadType"
	// MessageBus exports the APK's PermissionsProtocol registration; Tipsy plays
	// that role. These are Java→native symbols resolved by name, not hooks.
	messageBusSetRequestHandlerRawSym = "Java_com_roblox_universalapp_messagebus_MessageBus_setRequestHandlerRaw"
	messageBusSubscribeRequestRawSym  = "Java_com_roblox_universalapp_messagebus_MessageBus_doSubscribeProtocolMethodRequestRaw"
	messageBusPublishResponseRawSym   = "Java_com_roblox_universalapp_messagebus_MessageBus_publishProtocolMethodResponseRaw"
	messageBusDoSubscribeRawSym       = "Java_com_roblox_universalapp_messagebus_MessageBus_doSubscribeRaw"
	messageBusGetMessageIdSym         = "Java_com_roblox_universalapp_messagebus_MessageBus_getMessageId"
	messageBusPublishRawSym           = "Java_com_roblox_universalapp_messagebus_MessageBus_publishRaw"
	webViewInitializeSym              = "Java_com_roblox_protocols_webview_WebViewProtocol_initializeAndroidWebViewProtocol"
	webViewSignalJavascriptSym        = "Java_com_roblox_protocols_webview_WebViewProtocol_signalJavascriptCallback"
	// setInputConnectionName/Sig is the Java→native handshake the engine
	// registered. Tipsy calls it once after initializeNativeCode with the
	// Tipsy-owned InputConnection so the engine can send State deltas.
	setInputConnectionName = "setInputConnectionNative"
	setInputConnectionSig  = "(JLcom/google/androidgamesdk/gametextinput/InputConnection;)V"
	// nativePassTextSym/Sig is the engine's named commit route. It is focus-gated
	// by the engine's showKeyboard textbox handle; its text comes only from the X11
	// input method's committed UTF-8.
	nativePassTextSym       = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassText"
	nativePassTextSig       = "(JLjava/lang/String;ZI)V"
	nativeReturnPressedSym  = "Java_com_roblox_engine_jni_NativeGLInterface_nativeReturnPressedFromOnScreenKeyboard"
	syncTextboxSelectionSym = "Java_com_roblox_engine_jni_NativeGLInterface_syncTextboxTextAndCursorPosition2"
	nativeGetTextBoxInfoSym = "Java_com_roblox_engine_jni_NativeGLInterface_nativeGetTextBoxInfo"
	// postExitForegroundSym is the static native invoked after an experience ends;
	// its Focused/AppInput event resumes the retained app, preserving its page.
	postExitForegroundSym = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2SendAppEventOnGameLoaded"
	// postExitLeaveGameSym is the static native invoked by the APK's session
	// cleanup path after gameDidLeave. It is session cleanup, not a second
	// destination request or process shutdown.
	postExitLeaveGameSym     = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2LeaveGame"
	appCmdInitWindow         = 1
	appCmdWindowResized      = 3
	appCmdWindowRedraw       = 4
	appCmdContentRectChanged = 5
	appCmdGainedFocus        = 7
	appCmdStart              = 11
	appCmdResume             = 12
)

// appForegroundEvent carries no destination, place, or account payload.
type appForegroundEvent struct {
	Protocol string
	Payload  string
	Topic    string
}

var postExitForegroundEvent = appForegroundEvent{
	Protocol: "AppInput",
	Payload:  "",
	Topic:    "Focused",
}

// postExitForegroundUnavailableError is the typed, fail-closed diagnostic for a
// client whose foreground export is unavailable; the caller registers no
// lifecycle consumer in that case.
type postExitForegroundUnavailableError struct {
	Symbol string
	Cause  error
}

func (e *postExitForegroundUnavailableError) Error() string {
	if e == nil {
		return "post-exit app foreground export unavailable"
	}
	if e.Cause == nil {
		return fmt.Sprintf("post-exit app foreground export unavailable: %s", e.Symbol)
	}
	return fmt.Sprintf("post-exit app foreground export unavailable: %s: %v", e.Symbol, e.Cause)
}

func (e *postExitForegroundUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// resolvePostExitForeground resolves one named native export, keeping the
// unavailable case from silently subscribing a consumer with nowhere to send
// the event.
func resolvePostExitForeground(lookup func(string) (uintptr, error)) (uintptr, error) {
	if lookup == nil {
		return 0, &postExitForegroundUnavailableError{
			Symbol: postExitForegroundSym,
			Cause:  errors.New("module lookup is unavailable"),
		}
	}
	fn, err := lookup(postExitForegroundSym)
	if err != nil {
		return 0, &postExitForegroundUnavailableError{Symbol: postExitForegroundSym, Cause: err}
	}
	if fn == 0 {
		return 0, &postExitForegroundUnavailableError{
			Symbol: postExitForegroundSym,
			Cause:  errors.New("export resolved to address zero"),
		}
	}
	return fn, nil
}

// postExitGameRestorationUnavailableError is the typed, fail-closed diagnostic
// for a client missing the named session-cleanup export.
type postExitGameRestorationUnavailableError struct {
	Symbol string
	Cause  error
}

func (e *postExitGameRestorationUnavailableError) Error() string {
	if e == nil {
		return "post-exit game restoration export unavailable"
	}
	if e.Cause == nil {
		return fmt.Sprintf("post-exit game restoration export unavailable: %s", e.Symbol)
	}
	return fmt.Sprintf("post-exit game restoration export unavailable: %s: %v", e.Symbol, e.Cause)
}

func (e *postExitGameRestorationUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func resolvePostExitLeaveGame(lookup func(string) (uintptr, error)) (uintptr, error) {
	if lookup == nil {
		return 0, &postExitGameRestorationUnavailableError{
			Symbol: postExitLeaveGameSym,
			Cause:  errors.New("module lookup is unavailable"),
		}
	}
	fn, err := lookup(postExitLeaveGameSym)
	if err != nil {
		return 0, &postExitGameRestorationUnavailableError{Symbol: postExitLeaveGameSym, Cause: err}
	}
	if fn == 0 {
		return 0, &postExitGameRestorationUnavailableError{
			Symbol: postExitLeaveGameSym,
			Cause:  errors.New("export resolved to address zero"),
		}
	}
	return fn, nil
}

type postExitAppState uint8

const (
	postExitAppIdle postExitAppState = iota
	postExitAppStarted
	postExitAppLoadedExperience
	postExitAppStopped
)

// postExitAppResume owns one GameActivity session's observed lifecycle state. A
// non-zero onGameLoaded proves an experience DataModel loaded, so the matching
// later onGameLoaded(0) is a sufficient return-to-Home sequence; the explicit
// start -> stop -> Home sequence is also accepted. Startup Home, Lua
// notifications, and incomplete sequences are no-ops, and a later real start or
// non-zero load rearms an independent experience.
type postExitAppResume struct {
	mu             sync.Mutex
	state          postExitAppState
	closed         bool
	resumeInFlight bool
	leaveConsumed  bool
	leavePending   bool
	inflight       sync.WaitGroup
	invokeResume   func(appForegroundEvent)
	invokeLeave    func()
}

func newPostExitAppResume(invokeResume func(appForegroundEvent), invokeLeave func()) *postExitAppResume {
	return &postExitAppResume{invokeResume: invokeResume, invokeLeave: invokeLeave}
}

func (r *postExitAppResume) observe(event jni.NativeHelperLifecycleEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	var invoke func(appForegroundEvent)
	eventName := "unknown"
	idClass := "none"
	gate := "ignored"
	action := "none"
	switch event.Kind {
	case jni.NativeHelperExperienceStarted:
		eventName = "experience_start"
		r.state = postExitAppStarted
		r.leaveConsumed = false
		gate = "armed"
	case jni.NativeHelperExperienceStopped:
		eventName = "experience_stop"
		if r.state == postExitAppStarted || r.state == postExitAppLoadedExperience {
			r.state = postExitAppStopped
			gate = "awaiting_home"
		} else {
			// A second stop, or a stop without a start, is not the required ordered
			// pair; clear stale state rather than letting a following Home complete it.
			r.state = postExitAppIdle
			gate = "disarmed"
		}
	case jni.NativeHelperGameLoadedEvent:
		eventName = "game_loaded"
		idClass = "nonzero"
		if event.PlaceID != 0 {
			// This join/exit path does not emit NativeHelper start/stop; its non-zero
			// DataModel load is the evidence needed to arm the later Home-zero return.
			r.state = postExitAppLoadedExperience
			r.leaveConsumed = false
			gate = "armed"
			break
		}
		idClass = "zero"
		switch {
		case r.state != postExitAppStopped && r.state != postExitAppLoadedExperience:
			// A Home DataModel at launch, or any unordered callback, is not
			// evidence of a completed experience.
		case r.invokeResume == nil:
			gate = "unavailable"
		default:
			// Consume before invoking so duplicate Home notifications cannot
			// issue duplicate foreground events.
			r.state = postExitAppIdle
			invoke = r.invokeResume
			r.resumeInFlight = true
			r.inflight.Add(1)
			gate = "consumed"
			action = "resume_app"
		}
	case jni.NativeHelperLuaAppDidReturn:
		eventName = "lua_return"
		// This callback is used for Android orientation restoration; it is observed
		// but is neither an ordering substitute nor a route.
	}
	r.mu.Unlock()
	// This trace contains fixed lifecycle classes only; it never records the engine
	// duration or a place identifier.
	logging.Logger(logging.CatGameActivity).Info("post-exit app lifecycle",
		"event", eventName, "id_class", idClass, "gate", gate, "action", action)
	if invoke == nil {
		return
	}
	invoke(postExitForegroundEvent)
	r.mu.Lock()
	r.resumeInFlight = false
	var invokeLeave func()
	if !r.closed && r.leavePending && r.invokeLeave != nil {
		r.leavePending = false
		invokeLeave = r.invokeLeave
	}
	r.mu.Unlock()
	// The APK does not call LeaveGame re-entrantly from the gameDidLeave wrapper;
	// the deferred cleanup runs only after the foreground native returns. Flush at
	// that boundary while retaining this route's in-flight teardown ownership.
	if invokeLeave != nil {
		logging.Logger(logging.CatGameActivity).Info("post-exit game session restoration",
			"operation", "leave_game", "gate", "flushed")
		invokeLeave()
	}
	r.inflight.Done()
}

// gameDidLeave synchronously consumes the callback and defers the engine-facing
// operation until the outer foreground native returns. Consumption precedes the
// later call so native re-entry cannot recurse; it never emits a destination request.
func (r *postExitAppResume) gameDidLeave() {
	if r == nil {
		return
	}
	r.mu.Lock()
	active := r.state == postExitAppStarted ||
		r.state == postExitAppLoadedExperience ||
		r.state == postExitAppStopped ||
		r.resumeInFlight
	if r.closed || r.invokeLeave == nil || r.leaveConsumed || !active {
		r.mu.Unlock()
		return
	}
	r.leaveConsumed = true
	r.leavePending = true
	r.mu.Unlock()

	logging.Logger(logging.CatGameActivity).Info("post-exit game session restoration",
		"operation", "leave_game", "gate", "deferred")
}

// close blocks until an already-dispatched route call has returned. Combined
// with unsubscribing first, it prevents a dispatcher snapshot from calling an
// unloaded client module during teardown.
func (r *postExitAppResume) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	r.leavePending = false
	r.invokeResume = nil
	r.invokeLeave = nil
	r.mu.Unlock()
	r.inflight.Wait()
}

// bindPostExitAppObservers owns both subscriptions and the shared in-flight
// barrier. Cancellation precedes close so new dispatcher snapshots stop while
// already-copied callbacks finish safely.
func bindPostExitAppObservers(
	route *postExitAppResume,
	subscribeLifecycle func(jni.NativeHelperLifecycleListener) func(),
	subscribeLeave func(jni.NativeGLGameDidLeaveListener) func(),
) func() {
	if route == nil || subscribeLifecycle == nil || subscribeLeave == nil {
		if route == nil {
			return func() {}
		}
		return route.close
	}
	unsubLifecycle := subscribeLifecycle(route.observe)
	unsubLeave := subscribeLeave(route.gameDidLeave)
	var once sync.Once
	return func() {
		once.Do(func() {
			unsubLeave()
			unsubLifecycle()
			route.close()
		})
	}
}

// subscribePostExitAppResume ties the lifecycle consumer to this GameActivity
// session. Missing exports are recorded as a typed capability failure and leave
// navigation untouched.
func subscribePostExitAppResume(mod *loader.Module, env *jni.Env, gl uintptr) func() {
	fn, err := resolvePostExitForeground(func(sym string) (uintptr, error) {
		if mod == nil {
			return 0, errors.New("module is unavailable")
		}
		return mod.Lookup(sym)
	})
	if err != nil {
		logging.Logger(logging.CatGameActivity).Error("post-exit app foreground unavailable",
			"diagnostic", "app_foreground_export_unavailable", "sym", postExitForegroundSym, "err", err)
		return func() {}
	}
	leaveFn, err := resolvePostExitLeaveGame(func(sym string) (uintptr, error) {
		if mod == nil {
			return 0, errors.New("module is unavailable")
		}
		return mod.Lookup(sym)
	})
	if err != nil {
		logging.Logger(logging.CatGameActivity).Error("post-exit game restoration unavailable",
			"diagnostic", "leave_game_export_unavailable", "sym", postExitLeaveGameSym, "err", err)
		return func() {}
	}
	if env == nil || gl == 0 {
		logging.Logger(logging.CatGameActivity).Error("post-exit app foreground unavailable",
			"diagnostic", "app_foreground_target_unavailable", "sym", postExitForegroundSym)
		return func() {}
	}
	route := newPostExitAppResume(func(event appForegroundEvent) {
		loader.CallP8(fn, env.Raw(), gl,
			env.NewStringUTF(event.Protocol), env.NewStringUTF(event.Payload), env.NewStringUTF(event.Topic),
			0, 0, 0)
		logging.Logger(logging.CatGameActivity).Info("post-exit app foreground requested",
			"protocol", event.Protocol, "topic", event.Topic)
	}, func() {
		loader.CallP8(leaveFn, env.Raw(), gl, 0, 0, 0, 0, 0, 0)
		logging.Logger(logging.CatGameActivity).Info("post-exit game session restored",
			"operation", "leave_game")
	})
	return bindPostExitAppObservers(route, jni.SubscribeNativeHelperLifecycle, jni.SubscribeNativeGLGameDidLeave)
}

// shutdown follows the GameActivity lifecycle: focus loss, pause, surface
// destruction, stop, then terminateNativeCode, which joins the native app
// thread before Launch's deferred module unmap. sync.Once prevents a context
// cancellation racing a WM close from double-destroying the native handle.
func (s *gameActivitySession) shutdown(reason string) time.Duration {
	if s == nil {
		return 0
	}
	s.shutdownOnce.Do(func() {
		// Remove the session-owned NativeHelper observer before teardown; its close
		// waits for an in-flight dispatcher callback, so no post-exit foreground
		// call crosses module unmap.
		if s.closeLifecycle != nil {
			s.closeLifecycle()
		}
		if s.call == nil {
			return
		}
		started := time.Now()
		logging.Logger(logging.CatGameActivity).Info("graceful shutdown started", "reason", reason)
		// Park the evdev pad pump with the session it feeds: after terminateNativeCode
		// no gamepad target is live, so further polls only count parked drops.
		jni.StopRobloxDirectGamepadPump()
		// The runtime is an in-process host: unmapping libroblox while
		// terminateNativeCode still runs is unsafe. Give the join path a deadline, then
		// terminate the dismissed host as a last resort so a teardown stall cannot
		// leave a hidden process indefinitely.
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
		x11.SetWebViewStartGame(nil)
		x11.SetWebViewAssetsDir("")
		x11.CloseWebViewOverlay()
		if watchdog != nil {
			watchdog.Stop()
		}
		s.shutdownDuration = time.Since(started)
		logging.Logger(logging.CatGameActivity).Info("graceful shutdown completed",
			"reason", reason, "duration", s.shutdownDuration)
	})
	return s.shutdownDuration
}

func startGameActivity(ctx context.Context, vm *jni.VM, mod *loader.Module, aw *android.Window, files, cache, preferences, obb, assets, version string, width, height int, currentRefreshHz float32, supportedRefreshHz []float32, req rbxuri.Request, testOpenGL bool) (*gameActivitySession, error) {
	env := vm.Env()
	activity := env.AllocObject(env.FindClass("com/roblox/client/startup/MainGameActivity"))
	if activity == 0 {
		return nil, fmt.Errorf("AllocObject MainGameActivity failed")
	}
	overrides, overrideErr := loadAndroidAppOverrides(ctx, filepath.Join(files, "ClientAppSettings.json"), testOpenGL)
	assetsObj := env.AllocObject(env.FindClass("android/content/res/AssetManager"))
	cfg := env.AllocObject(env.FindClass("android/content/res/Configuration"))
	// FMOD init runs before the engine initializes; FMOD's output selection later
	// reads that Context. Java-side role, jni-owned.
	vm.FmodInit(activity)
	initJNIAAssetManager(mod, env, assetsObj)
	fn, err := mod.Lookup(initNativeSym)
	if err != nil {
		return nil, fmt.Errorf("initializeNativeCode: %w", err)
	}
	var handle int64
	runMainGameActivityOnCreateBoundary(overrides.renderer, func(payload string) {
		callRobloxJNI(mod, env.Raw(), activity, "Java_com_roblox_client_startup_MainGameActivity_nativePreloadFlagOverrides", env.NewStringUTF(payload))
	}, func() {
		handle = loader.CallP8(fn, env.Raw(), activity, env.NewStringUTF(files), env.NewStringUTF(obb), env.NewStringUTF(files), assetsObj, 0, cfg)
	})
	if handle == 0 {
		return nil, fmt.Errorf("initializeNativeCode returned 0")
	}
	writeCommand, _ := mod.Lookup(android.GameActivityWriteCommandSymbol)
	commands, err := android.NewGameActivityCommandWriter(uintptr(handle), writeCommand)
	if err != nil {
		logging.Logger(logging.CatGameActivity).Error("GameActivity command bridge unavailable", "err", err)
		// initializeNativeCode owns a native app thread; let its termination method
		// destroy and join that thread before the caller closes the module.
		callGameActivityNative(vm, env, activity, uintptr(handle), "terminateNativeCode", "(J)V")
		return nil, fmt.Errorf("GameActivity command bridge: %w", err)
	}
	jni.SetGameActivityInputTarget(env.Raw(), activity, uintptr(handle))
	// Hand the Tipsy-owned InputConnection to the engine right after the input
	// target, on the same Main-pthread stack. It is engine→Java state priming;
	// typed text uses the separate nativePassText path wired below.
	deliverTextInputConnection(vm, env, activity, uintptr(handle))
	wireRobloxDirectInput(mod, env)
	wireRobloxDirectKey(mod, env)
	wireRobloxDirectGamepad(mod, env)
	// The evdev pad pump runs its own goroutine into the JNI gamepad handler,
	// bypassing the X11 ring. It honors the TIPSY_GAMEPAD kill-switch and stays
	// silent with zero host pads.
	jni.StartRobloxDirectGamepadPump()
	wireRobloxTextInput(mod, env)
	wireRobloxPermissionsProtocol(mod, env)
	x11.SetWebViewAssetsDir(assets)
	wireRobloxWebViewProtocol(mod, env)
	return dispatchGameActivityLifecycle(ctx, vm, mod, env, activity, uintptr(handle), commands, files, cache, preferences, assets, version,
		width, height, currentRefreshHz, supportedRefreshHz, aw, req, overrides, overrideErr), nil
}

// deliverTextInputConnection delivers the Tipsy-owned InputConnection (a real
// VM object with an opaque handle, never an engine pointer) to the engine. A
// zero VM/env/activity/handle parks honestly; a missing registration logs and
// keeps the object for a later retry, never a stub success. No text content is
// handled or logged (opaque ids only); activation follows only the engine's
// own focus signals.
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

// wireRobloxDirectInput resolves only the public static native mouse methods.
// `nativePassMouse` is absent and must not be guessed; the ()Z getter is the
// authority for host pointer capture.
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

// wireRobloxDirectKey resolves the separate key route. NativeGLInterface owns
// nativePassKeyEvent; it must not be conflated with the NativeInputInterface
// mouse methods.
func wireRobloxDirectKey(mod *loader.Module, env *jni.Env) {
	fn, err := mod.Lookup(directKeyEventSym)
	if err != nil {
		logging.Logger(logging.CatJNI).Info("[jni] missing direct key export", "sym", directKeyEventSym, "err", err)
	}
	class := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	jni.SetRobloxDirectKeyTarget(env.Raw(), class, fn)
}

// wireRobloxDirectGamepad resolves only the six direct gamepad methods on
// NativeInputInterface. Missing exports log the missing line and park pad
// delivery (drops, never queues).
func wireRobloxDirectGamepad(mod *loader.Module, env *jni.Env) {
	axisFn, axisErr := mod.Lookup(directGamepadAxisSym)
	buttonFn, buttonErr := mod.Lookup(directGamepadButtonSym)
	connectFn, connectErr := mod.Lookup(directGamepadConnectSym)
	disconnectFn, disconnectErr := mod.Lookup(directGamepadDisconnSym)
	setKeyFn, setKeyErr := mod.Lookup(directGamepadSetKeySym)
	setMotionFn, setMotionErr := mod.Lookup(directGamepadSetMotionSym)
	for _, missing := range []struct {
		sym string
		err error
	}{
		{directGamepadAxisSym, axisErr},
		{directGamepadButtonSym, buttonErr},
		{directGamepadConnectSym, connectErr},
		{directGamepadDisconnSym, disconnectErr},
		{directGamepadSetKeySym, setKeyErr},
		{directGamepadSetMotionSym, setMotionErr},
	} {
		if missing.err != nil {
			logging.Logger(logging.CatJNI).Info("[jni] missing direct gamepad export", "sym", missing.sym, "err", missing.err)
		}
	}
	class := env.FindClass("com/roblox/engine/jni/NativeInputInterface")
	jni.SetRobloxDirectGamepadTarget(env.Raw(), class, axisFn, buttonFn, connectFn, disconnectFn, setKeyFn, setMotionFn)
}

// wireRobloxPermissionsProtocol registers Tipsy as the MessageBus answerer for
// the PermissionsProtocol the engine's voice stack uses. It runs at the same
// phase as the APK's Java protocol, right after initializeNativeCode. Missing
// exports log the missing line; each degrades independently inside jni.
func wireRobloxPermissionsProtocol(mod *loader.Module, env *jni.Env) {
	var exports jni.PermissionsProtocolExports
	for _, want := range []struct {
		sym string
		dst *uintptr
	}{
		{messageBusSetRequestHandlerRawSym, &exports.SetRequestHandlerRaw},
		{messageBusSubscribeRequestRawSym, &exports.DoSubscribeProtocolMethodRequestRaw},
		{messageBusPublishResponseRawSym, &exports.PublishProtocolMethodResponseRaw},
	} {
		fn, err := mod.Lookup(want.sym)
		if err != nil || fn == 0 {
			logging.Logger(logging.CatJNI).Info("[jni] missing MessageBus export", "sym", want.sym, "err", err)
			continue
		}
		*want.dst = fn
	}
	env.RegisterPermissionsProtocol(exports)
}

// wireRobloxWebViewProtocol registers Tipsy as the MessageBus answerer for the
// WebView protocol (Servers and similar in-app listings), at the same phase as
// the APK's Java WebViewProtocol.
func wireRobloxWebViewProtocol(mod *loader.Module, env *jni.Env) {
	var exports jni.WebViewProtocolExports
	for _, want := range []struct {
		sym string
		dst *uintptr
	}{
		{messageBusSetRequestHandlerRawSym, &exports.SetRequestHandlerRaw},
		{messageBusPublishResponseRawSym, &exports.PublishProtocolMethodResponseRaw},
		{messageBusDoSubscribeRawSym, &exports.DoSubscribeRaw},
		{messageBusGetMessageIdSym, &exports.GetMessageId},
		{messageBusPublishRawSym, &exports.PublishRaw},
		{webViewInitializeSym, &exports.InitializeAndroidWebViewProtocol},
		{webViewSignalJavascriptSym, &exports.SignalJavascriptCallback},
	} {
		fn, err := mod.Lookup(want.sym)
		if err != nil || fn == 0 {
			logging.Logger(logging.CatJNI).Info("[jni] missing MessageBus export", "sym", want.sym, "err", err)
			continue
		}
		*want.dst = fn
	}
	env.RegisterWebViewProtocol(exports)
}

// wireRobloxTextInput resolves the public static natives for text input.
// nativePassText is the required typing route; editor action and cursor
// selection are optional companions.
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

func dispatchGameActivityLifecycle(ctx context.Context, vm *jni.VM, mod *loader.Module, env *jni.Env, activity, handle uintptr, commands appCommandWriter, files, cache, preferences, assets, version string, width, height int, currentRefreshHz float32, supportedRefreshHz []float32, aw *android.Window, req rbxuri.Request, overrides androidAppOverrides, overrideErr error) *gameActivitySession {
	call := func(name, sig string, extra ...uintptr) {
		callGameActivityNative(vm, env, activity, handle, name, sig, extra...)
	}
	call("onStartNative", "(J)V")
	setRobloxCacheAndFiles(mod, env, activity, files, cache)
	content := assetContentDir(assets)
	setRobloxAssetPath(mod, env, activity, content)
	handleColdStartProtocolLaunch(mod, env, activity, req)
	startRobloxApp(ctx, mod, env, activity, files, version, overrides, overrideErr)
	// Publish Display's current and supported refresh rates after client-settings
	// init and before resume/surface/V2Start, matching the named JNI boundary,
	// using the XRandR modes of this window's active output.
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
	closeLifecycle := subscribePostExitAppResume(mod, env, gl)
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2InitWithParams", initParams)
	startParams := makeStartAppParams(env, activity, platform, surface, req)
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2StartAppWithParams", startParams)
	startWebsiteGame(mod, env, gl, activity, platform, device, surface, req)
	x11.SetWebViewStartGame(func(join rbxuri.Request) {
		startWebsiteGame(mod, env, gl, activity, platform, device, surface, join)
	})
	for _, cmd := range []byte{appCmdInitWindow, appCmdStart, appCmdResume, appCmdGainedFocus, appCmdWindowResized, appCmdWindowRedraw} {
		postAndroidAppCmd(commands, cmd)
	}
	// X11 supplies the first real surface geometry; deliver it once via the
	// GameActivity contract after lifecycle and surface setup.
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
		closeLifecycle:   closeLifecycle,
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
	absFiles, absCache := absExistingDir(files), absExistingDir(cache)
	if absFiles == "" || absCache == "" {
		// Honest structured failure instead of a silent skip: the cache/files setters
		// and LocalStorageManager stay unavailable, surfacing later as missing-API
		// failures. Paths are not secrets.
		logging.Logger(logging.CatGameActivity).Error("Android cache/files directories unavailable",
			"files_unavailable", absFiles == "", "cache_unavailable", absCache == "")
		return
	}
	files, cache = absFiles, absCache
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

const androidAppSettingsGroup = "GoogleAndroidApp"

func androidClientSettingsInitArgs(settings, rendererOverrides string) [3]string {
	return [3]string{settings, rendererOverrides, androidAppSettingsGroup}
}

func newClientSettingsInitLocals(env *jni.Env, args [3]string) [3]uintptr {
	if env == nil {
		return [3]uintptr{}
	}
	return [3]uintptr{env.NewString(args[0]), env.NewString(args[1]), env.NewString(args[2])}
}

func releaseClientSettingsInitLocals(env *jni.Env, locals [3]uintptr) {
	if env == nil {
		return
	}
	for _, obj := range locals {
		env.DeleteLocalRef(obj)
	}
}

// runMainGameActivityOnCreateBoundary preserves MainGameActivity's order: a
// non-empty external override is preloaded before the super.onCreate equivalent
// initializes native code. Ordinary launches make no synthetic empty preload.
func runMainGameActivityOnCreateBoundary(rendererOverrides string, preload func(string), superCreate func()) {
	if rendererOverrides != "" {
		preload(rendererOverrides)
	}
	superCreate()
}

func startRobloxApp(ctx context.Context, mod *loader.Module, env *jni.Env, activity uintptr, files, version string, overrides androidAppOverrides, overrideErr error) {
	if overrideErr != nil {
		logging.Logger(logging.CatGameActivity).Info("client settings unavailable", "err", overrideErr)
		return
	}
	flags, _, err := loadAndroidAppSettings(ctx, filepath.Join(files, "ClientAppSettings.json"), version, overrides)
	if err != nil || flags == "" {
		logging.Logger(logging.CatGameActivity).Info("client settings unavailable", "err", err)
		return
	}
	gl := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	if gl == 0 {
		gl = activity
	}
	initArgs := androidClientSettingsInitArgs(flags, overrides.renderer)
	initLocals := newClientSettingsInitLocals(env, initArgs)
	settingsStatus := callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativeInitClientSettings", initLocals[0], initLocals[1], initLocals[2])
	callRobloxJNI(mod, env.Raw(), gl, "Java_com_roblox_engine_jni_NativeGLInterface_nativePostClientSettingsLoadedInitialization3", env.NewArrayList())
	// nativeInitClientSettings copies the dual-key JSON into the C++ store. Inverted
	// JNI does not auto-drop the NewString locals a Java caller would release, which
	// would pin the string backing for the whole Main thread.
	releaseClientSettingsInitLocals(env, initLocals)
	// Complete the Java setup phase that owns CookieProtocol construction.
	if int32(settingsStatus) == 0 {
		env.CompleteAuthCookieInitialization()
	} else {
		logging.Logger(logging.CatFilesystem).Error("official cookie initialization unavailable after client-settings failure")
	}
	logNativeCookieRestoreState(mod, env, "after-native-init")
	startLoggedOutAppBridge(mod, env)
	logNativeCookieRestoreState(mod, env, "after-app-start")
}

// startLoggedOutAppBridge follows the Android bridge order for app startup. The
// first argument is the production base URL, not account identity; NativeUser
// owns user state.
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
		env.NewStringUTF(robloxBaseURL), env.NewStringUTF(""), 0,
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
	// One coherent pointer identity shared with the input dispatchers. Native X11
	// launches default to a mouse; TIPSY_INPUT_DEVICE=touch keeps the Android
	// phone identity.
	touch := jni.PointerDeviceIsTouch()
	env.PutField(p, "isKeyboardDevice", !touch)
	env.PutField(p, "isMouseDevice", !touch)
	env.PutField(p, "isTouchDevice", touch)
	setPlatformViewport(env, p, width, height)
	return p
}

// setPlatformViewport refreshes the two geometry fields on PlatformParams.
// Width/height stay X11 density-1 pixels; the 160 dpi conversion is preserved
// from startup.
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
