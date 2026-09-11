// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"syscall"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// launchStartedAck gives in-process frontends one deterministic handoff from
// launcher chrome to the live client. The sync.Once guard makes the API safe
// if the loop setup is refactored to have more than one entry edge later.
type launchStartedAck struct {
	once sync.Once
	fn   func()
}

func (a *launchStartedAck) signal() {
	if a == nil || a.fn == nil {
		return
	}
	a.once.Do(a.fn)
}

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
	var presentDurationCursor uint64
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
	// prepares directly. Scoping this to the storage-prep/import window was
	// audited and rejected: the native client creates cache, log, and pref
	// files for the whole session, so save+restore around prep would silently
	// widen those defaults again.
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
	var textOverlayWake focusedTextOverlayWake
	defer textOverlayWake.stop()
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
		textOverlayWake.sync(focusedTextSync.active)
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

	// Everything needed by the in-process client loop is live: X11 and the
	// exclusive EGL or Vulkan presenter plus its pump were established above,
	// GameActivity startup succeeded, input targets are wired, and the launch
	// loop waits on the X11 input wake, the X11 refresh-change wake, and the
	// focused-text/resize event sources. Immediate failures and --probe return
	// before this boundary.
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
	// Display-refresh republication is event-driven. The X11 reader wakes this
	// loop through RefreshReady when the window's refresh generation moves
	// (move, resize, map/reparent, or RandR change), and refreshFallbackPeriod
	// bounds how long a lost wake can delay republication. The retired 2s
	// ticker woke an otherwise idle client twice per second to re-check a
	// version that only changes on those events. Opt-in diagnostics keep their
	// exact historical 2s logging cadence.
	var logDiagnostics func()
	if presentTiming || stutterDiag || os.Getenv("TIPSY_DIAG") == "1" {
		logDiagnostics = func() {
			if presentTiming {
				batch := android.VulkanPresentTimingSnapshot(presentTimingCursor)
				logVulkanPresentTiming(batch)
				presentTimingCursor = batch.Cursor
				durations, next := android.VulkanPresentCallDurations(presentDurationCursor)
				logVulkanPresentCallDurations(durations)
				presentDurationCursor = next
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
		}
	}
	return runLaunchLoop(launchLoopSources{
		ctx:                ctx,
		inputReady:         win.InputReady(),
		refreshReady:       win.RefreshReady(),
		textOverlayWake:    textOverlayWake.C,
		resizeSettle:       func() <-chan time.Time { return resizeSettleC },
		pump:               win.Pump,
		refreshFocusedText: refreshFocusedText,
		flushResize:        flushSurfaceResize,
		publishRefresh: func() {
			if err := refreshPublication.update(win.RefreshVersion(), presenter.refreshRates, func(current float32, supported []float32) error {
				return publishDisplayRefreshRates(mod, vm.Env(), current, supported)
			}); err != nil {
				logging.Logger(logging.CatGraphics).Error("republish Android display refresh rates", "err", err)
			}
		},
		diagnostics: logDiagnostics,
	}, shutdownClient, refreshFallbackPeriod, launchDiagnosticPeriod)
}

const (
	// refreshFallbackPeriod bounds how long a missed X11 refresh wake can
	// delay display-rate republication. The RefreshReady wake is the primary
	// path (it follows a move/resize/RandR change by the reader's notify), and
	// this fallback is deliberately long so an idle client does not wake to
	// re-check an unchanged generation. 30s cuts the retired 2s wake rate by
	// 15x while keeping the worst-case staleness of a lost wake bounded.
	refreshFallbackPeriod = 30 * time.Second
	// launchDiagnosticPeriod preserves the historical 2s cadence of the
	// opt-in present-timing, shared-wait, and TIPSY_DIAG log lines.
	launchDiagnosticPeriod = 2 * time.Second
)

// launchLoopSources is the live client loop's event and callback surface.
// Production values come from the mapped X11 window, the focused-text overlay,
// the resize debouncer, the display-refresh publication, and the requested
// diagnostics; tests substitute fakes and short periods so the wake policy can
// be pinned without launching Roblox.
type launchLoopSources struct {
	ctx                context.Context
	inputReady         <-chan struct{}
	refreshReady       <-chan struct{}
	textOverlayWake    func() <-chan time.Time
	resizeSettle       func() <-chan time.Time
	pump               func() error
	refreshFocusedText func(time.Time)
	flushResize        func()
	publishRefresh     func()
	diagnostics        func()
}

// runLaunchLoop is the live client event loop. It republishes Android display
// refresh rates when the X11 refresh generation moves or after the long
// fallback period, logs diagnostics at the requested cadence when enabled, and
// runs the focused-text, resize-settle, and input paths with the existing
// shutdown semantics.
func runLaunchLoop(src launchLoopSources, shutdownClient func(reason string), refreshFallback, diagnosticsPeriod time.Duration) error {
	var refreshFallbackC <-chan time.Time
	if refreshFallback > 0 {
		fallback := time.NewTicker(refreshFallback)
		defer fallback.Stop()
		refreshFallbackC = fallback.C
	}
	var diagnosticsC <-chan time.Time
	if src.diagnostics != nil && diagnosticsPeriod > 0 {
		diagnostics := time.NewTicker(diagnosticsPeriod)
		defer diagnostics.Stop()
		diagnosticsC = diagnostics.C
	}
	for {
		select {
		case <-src.ctx.Done():
			shutdownClient("context-cancelled")
			return src.ctx.Err()
		case <-diagnosticsC:
			src.diagnostics()
		case <-src.refreshReady:
			src.publishRefresh()
		case <-refreshFallbackC:
			src.publishRefresh()
		case now := <-src.textOverlayWake():
			src.refreshFocusedText(now)
		case <-src.resizeSettle():
			src.flushResize()
		case <-src.inputReady:
			if err := src.pump(); err != nil {
				if err == x11.ErrClosed {
					// Pump has already unmapped the window, bounding visible close
					// response independently from native teardown. Stop producers,
					// then let the official GameActivity destroy/join complete before
					// the deferred libroblox unmap.
					shutdownClient("wm-delete-window")
					return nil
				}
				// Every other pump failure takes the same orderly teardown:
				// terminateNativeCode must join before the cookie fsync boundary
				// and the deferred module handoff, exactly like the ctx-done and
				// WM-delete paths. The pump error stays the returned error.
				shutdownClient("x11-pump-error")
				return err
			}
			// XIM commits and editor-navigation keys mutate the focused snapshot
			// while Pump notifies subscribers. Paint that version in the same
			// launch-loop turn rather than waiting for the bounded poll fallback.
			src.refreshFocusedText(time.Now())
		}
	}
}
