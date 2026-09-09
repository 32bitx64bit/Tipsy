// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"fmt"
	"os"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

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
