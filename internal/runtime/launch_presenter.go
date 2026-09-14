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

// logVulkanPresentCallDurations reports the opt-in E2b host-call histogram.
// The caller runs it only inside the existing present-timing diagnostics
// block, so the default path neither reads nor logs anything.
func logVulkanPresentCallDurations(stats android.VulkanPresentCallDurationStatistics) {
	if stats.Count == 0 && stats.Overwritten == 0 {
		return
	}
	logging.Logger(logging.CatGraphics).Info("Vulkan present call durations",
		"count", stats.Count, "overwritten", stats.Overwritten,
		"p50NS", stats.P50NS, "p99NS", stats.P99NS, "maxNS", stats.MaxNS)
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

// stutterInputDrainDiagnostics keeps the X11 input-drain observer on the same
// explicitly opted-in lifecycle as the existing shared-wait diagnostics. The
// function fields make its logger and counter source deterministic in runtime
// tests; production supplies only aggregate-only X11 APIs.
type stutterInputDrainDiagnostics struct {
	setEnabled func(bool)
	snapshot   func(reset bool) x11.InputDrainStats
	log        func(msg string, args ...any)
}

// begin clears counters before the first measured interval. A disabled
// capture intentionally makes no source or logger call, preserving zero
// additional work on ordinary launches.
func (d stutterInputDrainDiagnostics) begin(enabled bool) {
	if !enabled || d.setEnabled == nil || d.snapshot == nil {
		return
	}
	d.setEnabled(true)
	_ = d.snapshot(true)
}

// logInterval emits only fixed-size, content-free aggregate counters. It is
// called by the existing diagnostics ticker, never from the input pump.
func (d stutterInputDrainDiagnostics) logInterval(enabled bool) {
	if !enabled || d.snapshot == nil || d.log == nil {
		return
	}
	stats := d.snapshot(true)
	d.log("X11 input-drain diagnostic aggregate",
		"drainCalls", stats.DrainCalls,
		"emptyDrains", stats.EmptyDrains,
		"nonEmptyDrains", stats.NonEmptyDrains,
		"events", stats.Events,
		"batchBuckets", stats.BatchBuckets,
		"inputRingLockSamples", stats.InputRingLockWait.Samples,
		"inputRingLockTotalNS", stats.InputRingLockWait.TotalNS,
		"inputRingLockMaxNS", stats.InputRingLockWait.MaxNS,
		"inputRingLockBuckets", stats.InputRingLockWait.Buckets,
		"cDrainSamples", stats.CDrain.Samples,
		"cDrainTotalNS", stats.CDrain.TotalNS,
		"cDrainMaxNS", stats.CDrain.MaxNS,
		"cDrainBuckets", stats.CDrain.Buckets,
		"pumpWaitCalls", stats.PumpWaitCalls,
		"pumpWaitSamples", stats.PumpWait.Samples,
		"pumpWaitTotalNS", stats.PumpWait.TotalNS,
		"pumpWaitMaxNS", stats.PumpWait.MaxNS,
		"pumpWaitBuckets", stats.PumpWait.Buckets,
		"pumpReady", stats.PumpReady,
		"pumpPipeWakes", stats.PumpPipeWakes,
		"pumpErrors", stats.PumpErrors,
		"goWindowLockWaitSamples", stats.GoWindowLockWait.Samples,
		"goWindowLockWaitTotalNS", stats.GoWindowLockWait.TotalNS,
		"goWindowLockWaitMaxNS", stats.GoWindowLockWait.MaxNS,
		"goWindowLockWaitBuckets", stats.GoWindowLockWait.Buckets,
		"goDrainSamples", stats.GoDrain.Samples,
		"goDrainTotalNS", stats.GoDrain.TotalNS,
		"goDrainMaxNS", stats.GoDrain.MaxNS,
		"goDrainBuckets", stats.GoDrain.Buckets)
}

// end reports the final partial interval before disabling the observer. This
// matches the established Android/JNI stutter teardown sequence.
func (d stutterInputDrainDiagnostics) end(enabled bool) {
	if !enabled {
		return
	}
	d.logInterval(true)
	if d.setEnabled != nil {
		d.setEnabled(false)
	}
}

// jniStringPathDiagnosticFields is the fixed, content-free subset Runtime
// emits for one JNI string boundary. Keeping the aggregate grouped by an
// explicit vtable path avoids object, Java-class, handle, or payload identity
// in a gameplay capture.
type jniStringPathDiagnosticFields struct {
	Calls                uint64
	Succeeded            uint64
	InputUTF8Bytes       uint64
	OutputUTF8Bytes      uint64
	OutputUTF16Bytes     uint64
	CAllocatedBytes      uint64
	CopiedBytes          uint64
	StringObjects        uint64
	DurationSamples      uint64
	SampledDurationNS    uint64
	MaxSampledDurationNS uint64
	DurationBuckets      [8]uint64
}

func jniStringDiagnosticFields(stats jni.JNIStringPathStats) jniStringPathDiagnosticFields {
	return jniStringPathDiagnosticFields{
		Calls:                stats.Calls,
		Succeeded:            stats.Succeeded,
		InputUTF8Bytes:       stats.InputUTF8Bytes,
		OutputUTF8Bytes:      stats.OutputUTF8Bytes,
		OutputUTF16Bytes:     stats.OutputUTF16Bytes,
		CAllocatedBytes:      stats.CAllocatedBytes,
		CopiedBytes:          stats.CopiedBytes,
		StringObjects:        stats.StringObjects,
		DurationSamples:      stats.DurationSamples,
		SampledDurationNS:    stats.SampledDurationNS,
		MaxSampledDurationNS: stats.MaxSampledDurationNS,
		DurationBuckets:      stats.DurationBuckets,
	}
}

// stutterJNIStringDiagnostics takes interval snapshots only inside the
// existing exact opt-in diagnostic lifecycle. JNI owns enabling its observer
// through SetStutterDiagnostics; Runtime only clears and logs aggregates.
type stutterJNIStringDiagnostics struct {
	snapshot func(reset bool) jni.JNIStringDiagnostics
	log      func(msg string, args ...any)
}

func (d stutterJNIStringDiagnostics) begin(enabled bool) {
	if enabled && d.snapshot != nil {
		_ = d.snapshot(true)
	}
}

func (d stutterJNIStringDiagnostics) logInterval(enabled bool) {
	if !enabled || d.snapshot == nil || d.log == nil {
		return
	}
	stats := d.snapshot(true)
	d.log("JNI string diagnostic aggregate",
		"getStringChars", jniStringDiagnosticFields(stats.Paths[jni.JNIStringGetStringChars]),
		"getStringUTFChars", jniStringDiagnosticFields(stats.Paths[jni.JNIStringGetStringUTFChars]),
		"getStringCritical", jniStringDiagnosticFields(stats.Paths[jni.JNIStringGetStringCritical]),
		"releaseStringChars", jniStringDiagnosticFields(stats.Paths[jni.JNIStringReleaseStringChars]),
		"releaseStringUTFChars", jniStringDiagnosticFields(stats.Paths[jni.JNIStringReleaseStringUTF]),
		"releaseStringCritical", jniStringDiagnosticFields(stats.Paths[jni.JNIStringReleaseStringCritical]),
		"newStringUTF", jniStringDiagnosticFields(stats.Paths[jni.JNIStringNewStringUTF]),
		"isInstanceOf", jniStringDiagnosticFields(stats.Paths[jni.JNIStringIsInstanceOf]),
		"fieldGetterString", jniStringDiagnosticFields(stats.Paths[jni.JNIStringFieldGetterString]))
}

func (d stutterJNIStringDiagnostics) end(enabled bool) {
	d.logInterval(enabled)
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
	// WindowRefreshRates performs a fresh combined XRandR query and reports
	// zero current / empty supported on failure, so the caller can keep the
	// generation pending for retry instead of publishing stale rates.
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
		if err := p.egl.StopSwapThread(); err != nil {
			logging.Logger(logging.CatGraphics).Error("EGL sentinel present failed", "err", err)
		}
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
	// The C sentinel thread owns the first present: it makes the context
	// current, clears the back buffer to opaque black, presents exactly one
	// frame, then retires once the client presenter is observed
	// (internal/graphics egl.c). Tipsy must not swap first here: an undefined
	// pre-clear present would flash garbage and race the sentinel's single
	// defined frame.
	if err := eglSurf.ReleaseCurrent(); err != nil {
		_ = eglSurf.Close()
		return nil, fmt.Errorf("egl release: %w", err)
	}
	if err := eglSurf.StartSwapThread(); err != nil {
		// The sentinel thread is best-effort observation/paint only: the
		// official client still owns its own presentation, so a failed start
		// stays non-fatal but must not be silent.
		logging.Logger(logging.CatGraphics).Error("EGL sentinel present unavailable", "err", err)
	}
	return presenter, nil
}

type xidHandle struct{ xid uintptr }

func (h xidHandle) NativeHandle() uintptr { return h.xid }
