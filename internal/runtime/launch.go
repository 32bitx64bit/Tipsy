// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
	"github.com/tipsy-linux/tipsy/internal/integrity"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/rbxuri"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

type LaunchOptions struct {
	Probe bool
	// StartFullscreen asks the host X11 window manager for standard EWMH
	// fullscreen immediately after the window maps. It is a Tipsy-owned launch
	// policy, not a guessed Roblox Android preference.
	StartFullscreen      bool
	Width                int
	Height               int
	Started              func()
	Request              rbxuri.Request
	AuthorizedGeneration AuthorizedGeneration
}

// AuthorizedRuntimeFiles is the same-generation, already authenticated file
// view used by Android assets and launch metadata. Paths are usable only while
// the AuthorizedGeneration owner remains open.
type AuthorizedRuntimeFiles struct {
	GenerationID string
	RootDir      string
	AssetsDir    string
	BaseAPKPath  string
	VersionName  string
}

// AuthorizedGeneration is the narrow launch-side view implemented by
// setupsvc.AuthorizedGeneration. Keeping the interface here avoids a package
// cycle: setupsvc owns installation and currently depends on runtime setup
// helpers. Official callers must keep the generation open until Launch returns.
type AuthorizedGeneration interface {
	NativeDescriptorSet(context.Context) (*integrity.NativeDescriptorSet, error)
	AuthorizedRuntimeFiles(context.Context) (AuthorizedRuntimeFiles, error)
}

var ErrAuthorizedGenerationRequired = errors.New("runtime: authenticated runtime generation is required")

func authorizedNativeDescriptorSet(ctx context.Context, generation AuthorizedGeneration) (*integrity.NativeDescriptorSet, error) {
	if generation == nil {
		return nil, ErrAuthorizedGenerationRequired
	}
	set, err := generation.NativeDescriptorSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime: authenticated runtime generation: %w", err)
	}
	if set == nil {
		return nil, fmt.Errorf("runtime: authenticated runtime generation has no native descriptor set")
	}
	if err := set.Validate(); err != nil {
		return nil, fmt.Errorf("runtime: authenticated runtime generation descriptor set: %w", err)
	}
	return set, nil
}

func authorizedRuntimeFiles(ctx context.Context, generation AuthorizedGeneration, generationID string) (AuthorizedRuntimeFiles, error) {
	if generation == nil {
		return AuthorizedRuntimeFiles{}, ErrAuthorizedGenerationRequired
	}
	files, err := generation.AuthorizedRuntimeFiles(ctx)
	if err != nil {
		return AuthorizedRuntimeFiles{}, fmt.Errorf("runtime: authenticated runtime files: %w", err)
	}
	if files.GenerationID == "" || files.GenerationID != generationID || files.RootDir == "" ||
		files.AssetsDir == "" || files.BaseAPKPath == "" || strings.TrimSpace(files.VersionName) == "" {
		return AuthorizedRuntimeFiles{}, fmt.Errorf("runtime: authenticated runtime files are incomplete or from a different generation")
	}
	if !filepath.IsAbs(files.RootDir) || files.AssetsDir != filepath.Join(files.RootDir, "assets") ||
		files.BaseAPKPath != filepath.Join(files.RootDir, "apk", "base.apk") {
		return AuthorizedRuntimeFiles{}, fmt.Errorf("runtime: authenticated runtime file layout is not canonical")
	}
	return files, nil
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

// fullscreenWindow gives the host launch policy a narrow unit-test seam while
// retaining X11 as the sole owner of the EWMH implementation.
type fullscreenWindow interface {
	SetFullscreen(enabled bool) error
}

// requestStartFullscreen runs after OpenOnDisplay has mapped the X11 window
// and before any presenter, JNI, or Roblox lifecycle work can interact with
// it. A disabled policy deliberately sends no remove request, preserving the
// window manager's normal startup behavior.
func requestStartFullscreen(w fullscreenWindow, enabled bool) error {
	if !enabled {
		return nil
	}
	if w == nil {
		return x11.ErrClosed
	}
	return w.SetFullscreen(true)
}

// startFullscreenRequested resolves the explicit launch policy together with
// the persisted, Tipsy-owned setting. Either is an affirmative host request;
// a default LaunchOptions and a default settings document remain windowed.
func startFullscreenRequested(opt LaunchOptions, settings clientsettings.Settings) bool {
	return opt.StartFullscreen || settings.StartFullscreen
}

func stutterDiagnosticsRequested(getenv func(string) string) bool {
	return getenv != nil && getenv("TIPSY_STUTTER_DIAG") == "1"
}

// Present timing can run without the synchronization wrappers so millions of
// guest mutex calls cannot perturb a frame-timing control capture.
func vulkanPresentTimingRequested(getenv func(string) string) bool {
	return getenv != nil && (getenv("TIPSY_PRESENT_TIMING") == "1" || stutterDiagnosticsRequested(getenv))
}

func logVulkanPresentTiming(batch android.VulkanPresentTimingBatch) {
	if len(batch.Samples) == 0 && batch.Overwritten == 0 {
		return
	}
	timestamps := make([]uint64, len(batch.Samples))
	for i, sample := range batch.Samples {
		timestamps[i] = sample.MonotonicNS
	}
	logging.Logger(logging.CatGraphics).Info("Vulkan present timing",
		"cursor", batch.Cursor, "overwritten", batch.Overwritten,
		"monotonicNS", timestamps)
}

func logStutterDiagnostics(wait android.StutterWaitStats, calls jni.JNIStutterStats, bionic android.BionicSyncStats) {
	workerMutex := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncMutexLock)
	workerTryMutex := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncMutexTryLock)
	workerTimedMutex := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncMutexTimedLock)
	workerUnlock := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncMutexUnlock)
	workerSignal := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncCondSignal)
	workerBroadcast := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncCondBroadcast)
	workerYield := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncSchedYield)
	workerAffinity := bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleRoblox, android.BionicSyncPthreadSetAffinity)
	mainMutex := bionic.Path(android.BionicSyncThreadMain, android.BionicSyncModuleRoblox, android.BionicSyncMutexLock)
	logging.Logger(logging.CatRuntime).Info("shared-wait diagnostic aggregate",
		"condCalls", wait.Cond.Calls, "condSlices", wait.Cond.Slices,
		"condSamples", wait.Cond.Samples, "condSampled", wait.Cond.SampledDuration,
		"condMax", wait.Cond.MaxDuration,
		"timedCondCalls", wait.TimedCond.Calls, "timedCondSlices", wait.TimedCond.Slices,
		"timedCondSamples", wait.TimedCond.Samples, "timedCondSampled", wait.TimedCond.SampledDuration,
		"timedCondMax", wait.TimedCond.MaxDuration,
		"futexPumpCalls", wait.FutexPump.Calls, "futexPumpSlices", wait.FutexPump.Slices,
		"futexPumpSamples", wait.FutexPump.Samples, "futexPumpSampled", wait.FutexPump.SampledDuration,
		"futexPumpMax", wait.FutexPump.MaxDuration,
		"jniWorkerInstance", calls.Calls[jni.JNIThreadRBXWorker][jni.JNICALLInstance],
		"jniWorkerStatic", calls.Calls[jni.JNIThreadRBXWorker][jni.JNICALLStatic],
		"jniWorkerNonvirtual", calls.Calls[jni.JNIThreadRBXWorker][jni.JNICALLNonvirtual],
		"jniMainInstance", calls.Calls[jni.JNIThreadMain][jni.JNICALLInstance],
		"jniMainStatic", calls.Calls[jni.JNIThreadMain][jni.JNICALLStatic],
		"jniMainNonvirtual", calls.Calls[jni.JNIThreadMain][jni.JNICALLNonvirtual],
		"jniOtherInstance", calls.Calls[jni.JNIThreadOther][jni.JNICALLInstance],
		"jniOtherStatic", calls.Calls[jni.JNIThreadOther][jni.JNICALLStatic],
		"jniOtherNonvirtual", calls.Calls[jni.JNIThreadOther][jni.JNICALLNonvirtual],
		"bionicWorkerMutexLock", workerMutex.Calls, "bionicWorkerMutexLockSamples", workerMutex.Samples,
		"bionicWorkerMutexLockSampled", workerMutex.SampledDuration, "bionicWorkerMutexLockMax", workerMutex.MaxDuration,
		"bionicWorkerMutexTry", workerTryMutex.Calls, "bionicWorkerMutexTryContended", workerTryMutex.Contention,
		"bionicWorkerMutexTryErrors", workerTryMutex.Errors, "bionicWorkerMutexTimed", workerTimedMutex.Calls,
		"bionicWorkerMutexTimedContended", workerTimedMutex.Contention, "bionicWorkerMutexTimedErrors", workerTimedMutex.Errors,
		"bionicWorkerMutexUnlock", workerUnlock.Calls, "bionicWorkerCondSignal", workerSignal.Calls,
		"bionicWorkerCondBroadcast", workerBroadcast.Calls, "bionicWorkerYield", workerYield.Calls,
		"bionicWorkerPthreadSetAffinity", workerAffinity.Calls, "bionicMainMutexLock", mainMutex.Calls,
		"bionicMainMutexLockSampled", mainMutex.SampledDuration, "bionicMainMutexLockMax", mainMutex.MaxDuration,
		"bionicAllMutexLock", bionic.Aggregate(android.BionicSyncMutexLock).Calls,
		"bionicAllMutexLockMax", bionic.Aggregate(android.BionicSyncMutexLock).MaxDuration,
		"bionicAllMutexUnlock", bionic.Aggregate(android.BionicSyncMutexUnlock).Calls,
		"bionicAllCondSignal", bionic.Aggregate(android.BionicSyncCondSignal).Calls,
		"bionicAllCondBroadcast", bionic.Aggregate(android.BionicSyncCondBroadcast).Calls,
		"bionicAllYield", bionic.Aggregate(android.BionicSyncSchedYield).Calls,
		"bionicWorkerUnknownMutexLock", bionic.Path(android.BionicSyncThreadRBXWorker, android.BionicSyncModuleUnknown, android.BionicSyncMutexLock).Calls)
}

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

// configureEGLPresentationPolicy applies the independent VSync choice at the
// Android EGL compatibility boundary. FPS mode and display refresh do not
// participate: off always requests interval zero; on always requests one.
func configureEGLPresentationPolicy(settings clientsettings.Settings) bool {
	unthrottled := settings.NeedsUnthrottledPresentation()
	android.SetEGLVSync(settings.VSync)
	interval := 0
	if settings.VSync {
		interval = 1
	}
	logging.Logger(logging.CatGraphics).Info("VSync presentation policy",
		"vsync", settings.VSync, "effectiveInterval", interval,
		"fpsMode", settings.FrameRate.Mode, "fpsLimit", settings.FrameRate.Limit,
		"unthrottledPresentation", unthrottled)
	return unthrottled
}

// configureMesaVBlankMode must run before EGLDisplay/context creation. Mesa's
// application-default mode (1) respects the interval-one VSync policy, while
// mode 0 removes the driver-level vblank wait that can otherwise retain a
// refresh-rate cap after EGL accepts interval zero.
func configureMesaVBlankMode(vsync bool) error {
	mode := "0"
	if vsync {
		mode = "1"
	}
	if err := os.Setenv("vblank_mode", mode); err != nil {
		return fmt.Errorf("set Mesa vblank_mode=%s: %w", mode, err)
	}
	logging.Logger(logging.CatGraphics).Info("Mesa VSync policy configured",
		"vsync", vsync, "vblankMode", mode)
	return nil
}

type clientPresenter struct {
	vulkan bool
	egl    *graphics.EGL
	xdpy   uintptr
	xid    uintptr
}

func (p *clientPresenter) refreshRates() (float32, []float32) {
	if p == nil {
		return 0, nil
	}
	// A raw combined snapshot preserves unknown-on-failure for retry. The EGL
	// policy accessor intentionally retains stale rates, so cannot do that.
	current, supported := graphics.WindowRefreshRates(p.xdpy, p.xid)
	return float32(current), supported
}

// displayRefreshPublication tracks the X11 generation whose complete rate pair
// the client has accepted. An unchanged window must do no X server round trips.
type displayRefreshPublication struct {
	version   uint64
	current   float32
	supported []float32
}

func (p *displayRefreshPublication) update(version uint64, query func() (float32, []float32), publish func(float32, []float32) error) error {
	if version == 0 || version == p.version {
		return nil
	}
	current, supported := query()
	if current <= 0 {
		// Transient XRandR failure keeps this generation pending for the next
		// bounded stats tick; it must not publish a guessed monitor rate.
		return nil
	}
	if displayRefreshRatesChanged(p.current, p.supported, current, supported) {
		if err := publish(current, supported); err != nil {
			return err
		}
		p.current, p.supported = current, supported
	}
	// The caller captured version before querying. Events arriving while the
	// server replies or JNI publishes therefore remain pending.
	p.version = version
	return nil
}

func (p *clientPresenter) stop() {
	if p != nil && p.egl != nil {
		p.egl.StopSwapThread()
	}
}

func (p *clientPresenter) close() {
	if p != nil && p.egl != nil {
		_ = p.egl.Close()
	}
}

func (p *clientPresenter) logPresentStats() {
	if p != nil && p.vulkan {
		stats := android.VulkanPresentStats()
		logging.Logger(logging.CatGraphics).Info("Android Vulkan presentation rate",
			"successfulPresents", stats.SuccessfulPresents,
			"observationWindow", stats.Elapsed,
			"observedFPS", stats.RateFPS)
		return
	}
	eglStats := android.EGLSwapStats()
	logging.Logger(logging.CatGraphics).Info("Android EGL presentation rate",
		"successfulSwaps", eglStats.SuccessfulSwaps,
		"observationWindow", eglStats.Elapsed,
		"observedFPS", eglStats.RateFPS)
}

func configureVulkanPresentationPolicy(settings clientsettings.Settings) {
	android.SetVulkanVSync(settings.VSync)
	presentMode := "immediate"
	if settings.VSync {
		presentMode = "fifo"
	}
	logging.Logger(logging.CatGraphics).Info("VSync presentation policy",
		"backend", "vulkan", "vsync", settings.VSync, "presentModePolicy", presentMode,
		"fpsMode", settings.FrameRate.Mode, "fpsLimit", settings.FrameRate.Limit,
		"unthrottledPresentation", settings.NeedsUnthrottledPresentation())
}

func bindClientPresenter(win *x11.Window, settings clientsettings.Settings) (*clientPresenter, error) {
	resolved, err := graphics.ProbeRendererCapabilities().Resolve(graphics.Renderer(settings.Renderer))
	if err != nil {
		return nil, err
	}
	presenter := &clientPresenter{xdpy: win.Display(), xid: win.XID(), vulkan: resolved == graphics.RendererVulkan}
	logging.Logger(logging.CatGraphics).Info("resolved client renderer",
		"choice", settings.Renderer, "resolved", resolved)
	if presenter.vulkan {
		if err := android.BindVulkanWSI(win.Display(), win.XID()); err != nil {
			return nil, err
		}
		configureVulkanPresentationPolicy(settings)
		return presenter, nil
	}
	if err := configureMesaVBlankMode(settings.VSync); err != nil {
		return nil, err
	}
	eglSurf, err := graphics.BindEGL(win)
	if err != nil {
		return nil, fmt.Errorf("egl: %w", err)
	}
	presenter.egl = eglSurf
	configureEGLPresentationPolicy(settings)
	_ = eglSurf.Swap()
	if err := eglSurf.ReleaseCurrent(); err != nil {
		_ = eglSurf.Close()
		return nil, fmt.Errorf("egl release: %w", err)
	}
	if err := eglSurf.StartSwapThread(); err != nil {
		logging.Logger(logging.CatRuntime).Info("EGL swap thread skipped", "err", err)
	}
	return presenter, nil
}

type xidHandle struct{ xid uintptr }

func (h xidHandle) NativeHandle() uintptr { return h.xid }

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

// Launch starts the official extracted Android x86-64 client under native X11.
// It intentionally performs no writes to libroblox.so text.
func Launch(ctx context.Context, opt LaunchOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	nativeDescriptorSet, err := authorizedNativeDescriptorSet(ctx, opt.AuthorizedGeneration)
	if err != nil {
		return err
	}
	runtimeFiles, err := authorizedRuntimeFiles(ctx, opt.AuthorizedGeneration, nativeDescriptorSet.GenerationID())
	if err != nil {
		return err
	}
	presentTiming := vulkanPresentTimingRequested(os.Getenv)
	presentTimingCursor := android.SetVulkanPresentTiming(presentTiming)
	if presentTiming {
		logging.Logger(logging.CatGraphics).Info("Vulkan present timing enabled", "capacity", android.VulkanPresentTimingCapacity)
		defer func() {
			android.SetVulkanPresentTiming(false)
			logVulkanPresentTiming(android.VulkanPresentTimingSnapshot(presentTimingCursor))
		}()
	}
	stutterDiag := stutterDiagnosticsRequested(os.Getenv)
	if stutterDiag {
		android.SetStutterWaitDiagnostics(true)
		android.SetBionicSyncDiagnostics(true)
		jni.SetStutterDiagnostics(true)
		_ = android.StutterWaitSnapshot(true)
		_ = android.BionicSyncSnapshot(true)
		_ = jni.StutterSnapshot(true)
		logging.Logger(logging.CatRuntime).Info("shared-wait diagnostics enabled",
			"sampleRate", "1/64", "interval", 2*time.Second,
			"bionicResolverOnly", true)
		defer func() {
			logStutterDiagnostics(android.StutterWaitSnapshot(true), jni.StutterSnapshot(true), android.BionicSyncSnapshot(true))
			android.SetStutterWaitDiagnostics(false)
			android.SetBionicSyncDiagnostics(false)
			jni.SetStutterDiagnostics(false)
		}()
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
	// The official client can survive an isolated small rectangle, but a real
	// native X11/Vulkan resize drag through sub-720p rectangles reproducibly
	// crashes before GameActivity can settle its surface update. Keep every
	// construction boundary on the same X11 logical-pixel floor.
	opt.Width, opt.Height = clampRobloxSurfaceSize(opt.Width, opt.Height)
	started := &launchStartedAck{fn: opt.Started}
	dir, err := filepath.Abs(runtimeFiles.RootDir)
	if err != nil {
		return fmt.Errorf("authenticated runtime directory: %w", err)
	}
	if dir != runtimeFiles.RootDir {
		return fmt.Errorf("authenticated runtime directory is not canonical")
	}
	releaseClientLock, err := clientsettings.AcquireClientLock()
	if err != nil {
		return err
	}
	defer releaseClientLock()
	// Ordinary Android apps cannot promote native work to a real-time policy.
	// Establish that real kernel privilege limit before loading guest code or
	// creating its threads; desktop RTPRIO allowances must not leak into it.
	if err := initializeAndroidAppScheduling(); err != nil {
		return err
	}
	storage, migration, err := prepareAppStorage(dir)
	if err != nil {
		return fmt.Errorf("persistent app storage: %w", err)
	}
	logAppStorageMigration(migration)
	settingsService := clientsettings.New()
	if err := settingsService.ReconcileWhileClientLocked(ctx); err != nil {
		return fmt.Errorf("client settings: %w", err)
	}
	settings, err := settingsService.Load(ctx)
	if err != nil {
		return fmt.Errorf("load client settings: %w", err)
	}
	// Roblox may normalize experimental values while shutting down. Reapply an
	// explicit Tipsy-owned value after the client loop exits, while the launch
	// lock still excludes GUI settings writes. The next launch also reconciles.
	defer func() {
		_ = settingsService.ReconcileWhileClientLocked(context.Background())
	}()
	files := storage.FilesDir
	cache := storage.CacheDir
	preferences := storage.CookieFile
	assets := runtimeFiles.AssetsDir
	obb := filepath.Join(storage.DataRoot, "obb")
	if err := os.MkdirAll(obb, 0o700); err != nil {
		return err
	}
	// Install the APK's authoritative CA bundle under Android FilesDir before
	// native startup. Do not change the process CWD here: an otherwise
	// identical launch that entered FilesDir caused Roblox's HttpClient thread
	// to terminate itself during Startup. The remaining relative-open bridge is
	// a separate Android-ABI concern and must not be papered over with Chdir.
	if err := prepareRuntimeFiles(files, assets); err != nil {
		return fmt.Errorf("runtime TLS files: %w", err)
	}
	if !opt.Request.Empty() {
		logging.Logger(logging.CatRuntime).Info("website launch", "request", opt.Request.Summary())
	}
	if err := importWebsiteAuth(ctx, storage.CookieFile, &opt.Request); err != nil {
		return fmt.Errorf("website sign-in: %w", err)
	}

	goruntime.LockOSThread()
	win, err := x11.OpenOnDisplay("Roblox", opt.Width, opt.Height, settings.Display)
	if err != nil {
		return fmt.Errorf("x11: %w", err)
	}
	defer win.Close()
	// The mapped X11 window is the complete host-owned fullscreen boundary.
	// Queue the standard EWMH request before binding a presenter or starting
	// any Android/JNI/Roblox lifecycle work; later ConfigureNotify geometry
	// continues through the established resize path.
	if err := requestStartFullscreen(win, startFullscreenRequested(opt, settings)); err != nil {
		return fmt.Errorf("x11 start fullscreen: %w", err)
	}
	presenter, err := bindClientPresenter(win, settings)
	if err != nil {
		return fmt.Errorf("renderer: %w", err)
	}
	defer presenter.close()
	if err := win.StartBackgroundPump(); err != nil {
		return fmt.Errorf("x11 pump thread: %w", err)
	}
	defer win.StopBackgroundPump()
	defer presenter.stop()
	focusedTextOverlay, err := x11.NewFocusedTextOverlay(win)
	if err != nil {
		return fmt.Errorf("focused text overlay: %w", err)
	}
	defer focusedTextOverlay.Close()

	android.NewResolver(android.Config{AssetsDir: assets, APKPath: runtimeFiles.BaseAPKPath, Width: int32(opt.Width), Height: int32(opt.Height)})
	aw := android.NewWindow(opt.Width, opt.Height, xidHandle{xid: win.XID()})
	android.BindDefaultWindow(aw)
	vm, err := jni.NewVM()
	if err != nil {
		return fmt.Errorf("jni: %w", err)
	}
	vm.SetDirs(files, cache, obb, assets)
	ver := runtimeFiles.VersionName
	vm.SetAppVersion(ver)
	logging.Logger(logging.CatRuntime).Info("installed client", "version", ver)
	vm.SetDisplaySize(opt.Width, opt.Height)
	jni.SetRbxTextOverlayViewport(opt.Width, opt.Height, 1)
	if mmW, mmH := jni.X11DisplayPhysicalSizeMM(win.Display()); mmW > 0 && mmH > 0 {
		vm.SetDisplayPhysicalSizeMM(mmW, mmH)
	}
	mod, err := loader.OpenFD(ctx, "libroblox.so", nativeDescriptorSet, android.Provider())
	if err != nil {
		return err
	}
	clientStarted := false
	defer func() {
		if err := closeClientModuleBeforeStart(clientStarted, func() error {
			android.UnregisterImage(mod.Base)
			return mod.Close()
		}); err != nil {
			logging.Logger(logging.CatRuntime).Info("client module close failed", "err", err)
		}
	}()
	android.Register("libroblox.so", func(sym string) (uintptr, error) { return mod.Lookup(sym) })
	android.RegisterImage(mod.Base, mod.Path)
	if err := mod.Init(); err != nil {
		return fmt.Errorf("init: %w", err)
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
	if err := configureRobloxCookieBridge(vm, mod, vm.Env(), storage.CookieFile); err != nil {
		return err
	}
	refreshVersion := win.RefreshVersion()
	currentRefreshHz, supportedRefreshHz := presenter.refreshRates()
	session, err := startGameActivity(ctx, vm, mod, aw, files, cache, preferences, obb, assets, ver,
		opt.Width, opt.Height, currentRefreshHz, supportedRefreshHz, opt.Request)
	if err != nil {
		return err
	}
	presence := startDiscordPresence(ctx, settingsService.Load, opt.Request.PlaceID, filepath.Join(files, "appData", "logs"))
	defer stopDiscordPresence(presence)
	// A successful session means initializeNativeCode has entered the official
	// client and may have created engine-owned workers. Keep the image mapped
	// until the CLI/GUI host process exits; do not race those workers with
	// loader.Module.Close/rawMunmap after the visible window is dismissed.
	clientStarted = true
	refreshPublication := displayRefreshPublication{version: refreshVersion, current: currentRefreshHz, supported: supportedRefreshHz}
	if currentRefreshHz <= 0 {
		refreshPublication.version = 0
	}
	resize := session.resize
	// A window-manager drag can emit a dense ConfigureNotify sequence. Vulkan
	// observes its native X11 window directly, but replaying GameActivity's
	// full ANativeWindow/V2/content-rect lifecycle for every intermediate drag
	// size re-enters the official client before its prior surface update has
	// settled. Keep only the latest positive geometry until the drag quiesces.
	// This timer exists only while a resize is pending; the normal event-driven
	// input path remains timer-free.
	resizeDebouncer := surfaceResizeDebouncer{
		minWidth: x11.RobloxMinimumWidth, minHeight: x11.RobloxMinimumHeight,
	}
	var resizeSettleTimer *time.Timer
	var resizeSettleC <-chan time.Time
	scheduleSurfaceResize := func(w, h int) {
		if !resizeDebouncer.queue(w, h) {
			return
		}
		if resizeSettleTimer == nil {
			resizeSettleTimer = time.NewTimer(surfaceResizeSettleDelay)
			resizeSettleC = resizeSettleTimer.C
			return
		}
		if !resizeSettleTimer.Stop() {
			select {
			case <-resizeSettleTimer.C:
			default:
			}
		}
		resizeSettleTimer.Reset(surfaceResizeSettleDelay)
		resizeSettleC = resizeSettleTimer.C
	}
	flushSurfaceResize := func() {
		resizeSettleC = nil
		if w, h, ok := resizeDebouncer.take(); ok {
			resize.observe(w, h)
		}
	}
	defer func() {
		if resizeSettleTimer != nil {
			resizeSettleTimer.Stop()
		}
	}()
	focusedTextSync := newFocusedTextOverlaySync(focusedTextOverlay, assets)
	textOverlayTicker := time.NewTicker(focusedTextOverlayPollInterval)
	defer textOverlayTicker.Stop()
	textOverlayErrorLogged := false
	var textOverlayDiagnosticsVersion uint64
	refreshFocusedText := func(now time.Time) {
		updated, err := focusedTextSync.refresh(now)
		if err != nil && !textOverlayErrorLogged {
			// Text and field identity never enter this diagnostic. Continue the
			// client honestly; a later version/repaint retries the host View.
			logging.Logger(logging.CatX11).Error("focused text overlay unavailable", "err", err)
			textOverlayErrorLogged = true
		} else if updated && err == nil {
			textOverlayErrorLogged = false
			if focusedTextSync.active && focusedTextSync.seen != textOverlayDiagnosticsVersion && os.Getenv("TIPSY_DIAG") == "1" {
				diag := focusedTextOverlay.Diagnostics()
				// Aggregate ink booleans and raw Android color are safe to log;
				// editor content and glyph identities never enter diagnostics.
				logging.Logger(logging.CatX11).Info("focused text overlay paint",
					"mapped", diag.Mapped,
					"usesARGB", diag.UsesARGB,
					"textColorARGB", fmt.Sprintf("0x%08x", diag.TextColorARGB),
					"textAlpha", diag.TextAlpha,
					"glyphMask", diag.GlyphMaskPixels > 0,
					"brightGlyphInk", diag.BrightGlyphPixels > 0,
					"caretMask", diag.CaretMaskPixels > 0)
				textOverlayDiagnosticsVersion = focusedTextSync.seen
			}
		}
	}
	refreshFocusedText(time.Now())
	defer jni.ClearRobloxDirectInputTarget()
	defer jni.ClearRobloxDirectKeyTarget()
	defer jni.ClearRobloxTextInputTarget()
	defer jni.ClearGameActivityInputTarget()
	// ConfigureNotify enters the same ordered X11 stream as pointer events.
	// Update the X11-owned size immediately, but settle the engine-facing
	// lifecycle after a short quiet period so one manual drag cannot re-enter
	// V2 for every intermediate rectangle.
	cancelResizeInput := x11.OnInput(func(ev x11.InputEvent) {
		if ev.Kind == x11.InputResize {
			scheduleSurfaceResize(ev.Width, ev.Height)
		}
	})
	defer cancelResizeInput()

	stats := time.NewTicker(2 * time.Second)
	defer stats.Stop()
	// Everything needed by the in-process client loop is live: X11 and the
	// exclusive EGL or Vulkan presenter plus its pump were established above,
	// GameActivity startup succeeded, input targets are wired, and the launch
	// loop waits on the X11 input wake plus a 2s refresh-rate ticker. Immediate
	// failures and --probe return before this boundary.
	started.signal()
	shutdownClient := func(reason string) {
		_ = win.Dismiss()
		_ = win.StopBackgroundPump()
		presenter.stop()
		session.shutdown(reason)
		// The official terminateNativeCode join has completed, while the
		// package-private path and process-wide client lock are still live.
		// Treat the payload as opaque: flush file + containing directory only.
		if err := syncPrivateOpaqueFile(preferences); err != nil {
			logging.Logger(logging.CatFilesystem).Info("official cookie storage sync failed", "err", err)
		} else {
			logging.Logger(logging.CatFilesystem).Info("official cookie storage synchronized")
		}
	}
	for {
		select {
		case <-ctx.Done():
			shutdownClient("context-cancelled")
			return ctx.Err()
		case <-stats.C:
			if err := refreshPublication.update(win.RefreshVersion(), presenter.refreshRates, func(current float32, supported []float32) error {
				return publishDisplayRefreshRates(mod, vm.Env(), current, supported)
			}); err != nil {
				logging.Logger(logging.CatGraphics).Error("republish Android display refresh rates", "err", err)
			}
			if presentTiming {
				batch := android.VulkanPresentTimingSnapshot(presentTimingCursor)
				logVulkanPresentTiming(batch)
				presentTimingCursor = batch.Cursor
			}
			if stutterDiag {
				logStutterDiagnostics(android.StutterWaitSnapshot(true), jni.StutterSnapshot(true), android.BionicSyncSnapshot(true))
			}
			if os.Getenv("TIPSY_DIAG") == "1" {
				presenter.logPresentStats()
				s := jni.InputDeliveryStats()
				d := jni.RobloxDirectInputStats()
				textPass, textReturn, textSync, textDrop := jni.RbxTextDeliveryStats()
				textInfo := jni.RbxTextInfoRefreshStats()
				logging.Logger(logging.CatRuntime).Info("input delivery",
					"path", jni.PointerInputPath().String(),
					"focus", s.FocusDelivered, "gameActivityKeys", s.KeyDelivered, "gameActivityPointers", s.PointerDelivered,
					"gameActivityConsumed", s.KeyConsumed+s.PointerConsumed, "gameActivityDropped", s.Dropped,
					"directKeys", d.KeyDelivered, "directButtons", d.ButtonDelivered, "directMoves", d.MoveDelivered, "directWheels", d.WheelDelivered, "directDropped", d.Dropped,
					"pointerLockQueries", d.LockQueries, "pointerLockTrue", d.LockTrue,
					"textPass", textPass, "textReturn", textReturn, "textSync", textSync, "textDropped", textDrop,
					"textInfoRequested", textInfo.Requested, "textInfoAttempted", textInfo.Attempted,
					"textInfoApplied", textInfo.Applied, "textInfoMissing", textInfo.MissingTarget,
					"textInfoNull", textInfo.NullResult, "textInfoStale", textInfo.StaleSession)
			}
		case now := <-textOverlayTicker.C:
			refreshFocusedText(now)
		case <-resizeSettleC:
			flushSurfaceResize()
		case <-win.InputReady():
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
			// XIM commits and editor-navigation keys mutate the focused snapshot
			// while Pump notifies subscribers. Paint that version in the same
			// launch-loop turn rather than waiting for the bounded poll fallback.
			refreshFocusedText(time.Now())
		}
	}
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

// surfaceResizeSettleDelay bounds GameActivity/V2 surface-update re-entry
// during a manual X11 resize drag. It is deliberately short enough that a
// completed resize remains responsive, yet longer than the dense intermediate
// ConfigureNotify burst that otherwise reaches the official client as a chain
// of overlapping surface transitions.
const surfaceResizeSettleDelay = 75 * time.Millisecond

// clampRobloxSurfaceSize keeps GameActivity's initial display metrics aligned
// with the X11 Roblox window's WM minimum. These are X11 client pixels; the
// Android density remains one in this desktop adapter.
func clampRobloxSurfaceSize(width, height int) (int, int) {
	if width < x11.RobloxMinimumWidth {
		width = x11.RobloxMinimumWidth
	}
	if height < x11.RobloxMinimumHeight {
		height = x11.RobloxMinimumHeight
	}
	return width, height
}

// surfaceResizeDebouncer holds only the most recent positive X11 rectangle
// from a resize burst. The X11 Window continues to track each real
// ConfigureNotify for input and presentation; this type coalesces only the
// Android/GameActivity lifecycle work that must not be re-entered per pixel of
// a title-bar drag. It is owned by the Launch goroutine.
type surfaceResizeDebouncer struct {
	pending             bool
	width, height       int
	minWidth, minHeight int
}

func (d *surfaceResizeDebouncer) queue(w, h int) bool {
	if d == nil || w <= 0 || h <= 0 ||
		(d.minWidth > 0 && w < d.minWidth) ||
		(d.minHeight > 0 && h < d.minHeight) {
		return false
	}
	d.pending, d.width, d.height = true, w, h
	return true
}

func (d *surfaceResizeDebouncer) take() (w, h int, ok bool) {
	if d == nil || !d.pending {
		return 0, 0, false
	}
	w, h = d.width, d.height
	d.pending = false
	return w, h, true
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

	seeded              bool
	width, height       int
	minWidth, minHeight int
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
	commands appCommandWriter
	aw       *android.Window
	gl       uintptr
	surface  uintptr
	platform uintptr
}

func (s *engineResizeSink) resizeBuffers(width, height int) error { return s.aw.Resize(width, height) }

func (s *engineResizeSink) setDisplaySize(width, height int) {
	s.vm.SetDisplaySize(width, height)
	// DisplayMetrics.density remains 1 in the desktop Android contract. Keep
	// the transient editor's clipping metadata in lockstep with that same
	// surface resize; NativeTextBoxInfo bounds themselves are not rescaled.
	jni.SetRbxTextOverlayViewport(width, height, 1)
}

func (s *engineResizeSink) postAppCmd(cmd byte) { postAndroidAppCmd(s.commands, cmd) }

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
	if s == nil || w <= 0 || h <= 0 ||
		(s.minWidth > 0 && w < s.minWidth) ||
		(s.minHeight > 0 && h < s.minHeight) {
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

// setRobloxPreferencesFile follows NativeHelper.P -> el/y.f: the parameter is
// an Android SharedPreferences NAME, not a filesystem path. "rbx.prefs" is only
// the APK's logging tag. Cookie persistence is the separate CookieProtocol.
func setRobloxPreferencesFile(mod *loader.Module, env *jni.Env) {
	class := env.FindClass("com/roblox/engine/jni/NativeSettingsInterface")
	callRobloxJNI(mod, env.Raw(), class, setPreferencesFileSym, env.NewStringUTF(robloxPreferencesID))
}

// configureRobloxCookieBridge implements the APK's Java-owned lifecycle:
// MainGameActivity.onCreate restores scoped cookies before native creation;
// NativeHelper initializes CookieProtocol's official callback. Cookie content
// never enters diagnostics, flags, account stubs, or engine memory patches.
func configureRobloxCookieBridge(vm *jni.VM, mod *loader.Module, env *jni.Env, path string) error {
	const baseURL = "https://www.roblox.com/"
	const registerSym = "Java_com_roblox_universalapp_cookie_JNICookieProtocol_updateOnSetCookieHandler"
	register, err := mod.Lookup(registerSym)
	if err != nil || register == 0 {
		return fmt.Errorf("official cookie callback unavailable")
	}
	if err = vm.ConfigureAuthCookies(path, baseURL); err != nil {
		return fmt.Errorf("prepare official cookie storage: %w", err)
	}
	protocol := env.AllocObject(env.FindClass("com/roblox/universalapp/cookie/JNICookieProtocol"))
	handler := env.AllocObject(env.FindClass("com/roblox/universalapp/cookie/CookieProtocol$OnSetCookieHandlerImpl"))
	vm.SetAuthCookieRegistration(func() {
		setRobloxPreferencesFile(mod, env)
		loader.CallP8(register, env.Raw(), protocol, handler, 0, 0, 0, 0, 0)
		logging.Logger(logging.CatFilesystem).Info("official cookie callback registered")
	})
	header, err := vm.RestoreAuthCookies()
	if err != nil {
		return err
	}
	if err := restoreRobloxCookieHeader(env, header, func(symbol string, settings, first, second uintptr) error {
		fn, err := mod.Lookup(symbol)
		if err != nil || fn == 0 {
			return fmt.Errorf("official cookie startup API unavailable: %s", symbol)
		}
		loader.CallP8(fn, env.Raw(), settings, first, second, 0, 0, 0, 0)
		return nil
	}); err != nil {
		return err
	}
	logging.Logger(logging.CatFilesystem).Info("official cookie restore delivered", "stored_cookies", header != "")
	logNativeCookieRestoreState(mod, env, "before-native-init")
	return nil
}

// restoreRobloxCookieHeader preserves rh/w0.V0 -> R0's ordered JNI contract.
// The native cookie setter filters against its configured origin; calling
// restore before nativeSetBaseUrl silently discards valid saved cookies.
func restoreRobloxCookieHeader(env *jni.Env, header string, invoke func(symbol string, settings, first, second uintptr) error) error {
	const baseURL = "https://www.roblox.com/"
	settings := env.FindClass("com/roblox/engine/jni/NativeSettingsInterface")
	if err := invoke("Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetBaseUrl", settings, env.NewStringUTF(baseURL), env.NewStringUTF("https://api.roblox.com/")); err != nil {
		return err
	}
	return invoke("Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetMultipleCookies", settings, env.NewStringUTF(baseURL), env.NewStringUTF(header))
}

// logNativeCookieRestoreState is an opt-in, read-only check of the named APK
// cookie getter. It emits only record counts and auth-category presence, never
// cookie names, values, URLs, paths, account data, or raw native strings.
func logNativeCookieRestoreState(mod *loader.Module, env *jni.Env, phase string) {
	if os.Getenv("TIPSY_AUTH_RESTORE_DIAGNOSTICS") != "1" {
		return
	}
	const sym = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeGetCookiesInNetscapeFormat"
	fn, err := mod.Lookup(sym)
	if err != nil || fn == 0 {
		logging.Logger(logging.CatFilesystem).Info("official cookie readback unavailable")
		return
	}
	result := loader.CallP8(fn, env.Raw(), env.FindClass("com/roblox/engine/jni/NativeSettingsInterface"), env.NewStringUTF("https://www.roblox.com/"), 0, 0, 0, 0, 0)
	if result == 0 {
		logging.Logger(logging.CatFilesystem).Info("official cookie readback unavailable")
		return
	}
	snapshot, err := env.GetStringUTFChars(uintptr(result))
	if err != nil {
		logging.Logger(logging.CatFilesystem).Info("official cookie readback unavailable")
		return
	}
	count, authPresent := 0, false
	for _, record := range strings.Split(snapshot, ";") {
		fields := strings.Split(record, "\t")
		if len(fields) == 6 || len(fields) == 7 {
			count++
			if fields[5] == ".ROBLOSECURITY" && len(fields) == 7 && fields[6] != "" {
				authPresent = true
			}
		}
	}
	logging.Logger(logging.CatFilesystem).Info("official cookie readback", "phase", phase, "records", count, "auth_present", authPresent)
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

func assetContentDir(assetsDir string) string { return filepath.Join(assetsDir, "content") }

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
