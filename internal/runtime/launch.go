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

// testRendererEnv is a process-only visual-compatibility control; the sole
// accepted value is `opengl`. It never writes the Tipsy settings document or
// Roblox's durable settings files.
const testRendererEnv = "TIPSY_TEST_RENDERER"

func testOpenGLRequested(getenv func(string) string) (bool, error) {
	if getenv == nil {
		return false, nil
	}
	switch getenv(testRendererEnv) {
	case "":
		return false, nil
	case "opengl":
		return true, nil
	default:
		return false, fmt.Errorf("%s only accepts opengl", testRendererEnv)
	}
}

// launchStartedAck gives in-process frontends one deterministic handoff from
// launcher chrome to the live client. The sync.Once makes the signal idempotent.
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

// fullscreenWindow is implemented by the X11 window, the sole owner of the
// EWMH fullscreen implementation.
type fullscreenWindow interface {
	SetFullscreen(enabled bool) error
}

// requestStartFullscreen must run after the X11 window maps and before any
// presenter, JNI, or Roblox lifecycle work. A disabled policy sends no request,
// preserving the window manager's normal startup behavior.
func requestStartFullscreen(w fullscreenWindow, enabled bool) error {
	if !enabled {
		return nil
	}
	if w == nil {
		return x11.ErrClosed
	}
	return w.SetFullscreen(true)
}

// startFullscreenRequested is true when the explicit launch policy or the
// persisted Tipsy-owned setting requests fullscreen; both default to windowed.
func startFullscreenRequested(opt LaunchOptions, settings clientsettings.Settings) bool {
	return opt.StartFullscreen || settings.StartFullscreen
}

// closeClientModuleBeforeStart releases the client image only when no
// GameActivity session was established. Once initializeNativeCode succeeds,
// Roblox can retain worker threads beyond terminateNativeCode, so unmapping
// libroblox beneath them is unsafe; process teardown owns that mapping instead.
func closeClientModuleBeforeStart(clientStarted bool, closeFn func() error) error {
	if clientStarted || closeFn == nil {
		return nil
	}
	return closeFn()
}

// Launch starts the official extracted Android x86-64 client under native X11.
// It performs no writes to libroblox.so text.
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
	var inputDrainDiag stutterInputDrainDiagnostics
	var stringDiag stutterJNIStringDiagnostics
	if stutterDiag {
		inputDrainDiag = stutterInputDrainDiagnostics{
			setEnabled: x11.SetInputDrainDiagnostics,
			snapshot:   x11.InputDrainSnapshot,
			log:        logging.Logger(logging.CatRuntime).Info,
		}
		stringDiag = stutterJNIStringDiagnostics{
			snapshot: jni.StringDiagnosticsSnapshot,
			log:      logging.Logger(logging.CatRuntime).Info,
		}
		android.SetStutterWaitDiagnostics(true)
		android.SetBionicSyncDiagnostics(true)
		jni.SetStutterDiagnostics(true)
		stringDiag.begin(true)
		inputDrainDiag.begin(true)
		_ = android.StutterWaitSnapshot(true)
		_ = android.BionicSyncSnapshot(true)
		_ = jni.StutterSnapshot(true)
		logging.Logger(logging.CatRuntime).Info("shared-wait diagnostics enabled",
			"sampleRate", "1/64", "interval", 2*time.Second,
			"x11InputDrain", true,
			"bionicResolverOnly", true)
		defer func() {
			logStutterDiagnostics(android.StutterWaitSnapshot(true), jni.StutterSnapshot(true), android.BionicSyncSnapshot(true))
			stringDiag.end(true)
			inputDrainDiag.end(true)
			android.SetStutterWaitDiagnostics(false)
			android.SetBionicSyncDiagnostics(false)
			jni.SetStutterDiagnostics(false)
		}()
	}
	// Android app-private files are owner-only by default. Keep that invariant for
	// files the unmodified client creates itself, not only files Tipsy prepares
	// directly, so the session-wide umask stays narrow.
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)
	if opt.Width <= 0 {
		opt.Width = 1280
	}
	if opt.Height <= 0 {
		opt.Height = 720
	}
	// The official client survives an isolated small rectangle, but a real resize
	// drag through sub-minimum rectangles crashes before GameActivity settles its
	// surface update. Keep every construction boundary on the same logical-pixel floor.
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
	// Establish the Android app's real-time privilege limit before loading guest
	// code or creating its threads; desktop RTPRIO allowances must not leak in.
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
	testOpenGL, err := testOpenGLRequested(os.Getenv)
	if err != nil {
		return fmt.Errorf("test renderer: %w", err)
	}
	if testOpenGL {
		// Keep this process-local: the deferred reconciliation rewrites the persisted
		// selection, never this visual-test choice.
		settings.Renderer = clientsettings.RendererOpenGL
		logging.Logger(logging.CatGraphics).Info("transient renderer override", "renderer", clientsettings.RendererOpenGL)
	}
	// Roblox may normalize experimental values on shutdown. Reapply the Tipsy-owned
	// value after the loop exits, while the launch lock still excludes GUI writes.
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
	// native startup. Do not change the process CWD here: entering FilesDir can
	// terminate the client's HttpClient thread during Startup.
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
	// Queue the EWMH fullscreen request before binding a presenter or starting any
	// Android/JNI/Roblox lifecycle work; later ConfigureNotify geometry flows
	// through the established resize path.
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
	startupMeasurement := newStartupMeasurementArm(os.Getenv, time.Now, logging.Logger(logging.CatRuntime).Info)
	defer startupMeasurement.teardown()
	if startupMeasurement != nil {
		startupMeasurement.attachX11(win)
	}
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
		opt.Width, opt.Height, currentRefreshHz, supportedRefreshHz, opt.Request, testOpenGL)
	if err != nil {
		return err
	}
	presence := startDiscordPresence(ctx, settingsService.Load, opt.Request.PlaceID, filepath.Join(files, "appData", "logs"))
	defer stopDiscordPresence(presence)
	// A successful session means initializeNativeCode entered the official client
	// and may have created engine-owned workers. Keep the image mapped until the
	// host process exits; do not race those workers with Close/rawMunmap.
	clientStarted = true
	refreshPublication := displayRefreshPublication{version: refreshVersion, current: currentRefreshHz, supported: supportedRefreshHz}
	if currentRefreshHz <= 0 {
		refreshPublication.version = 0
	}
	resize := session.resize
	// A window-manager drag emits a dense ConfigureNotify sequence; replaying
	// GameActivity's full surface lifecycle for every intermediate size re-enters
	// the client before its prior update settles. Keep only the latest positive
	// geometry until the drag quiesces; the timer exists only while one is pending.
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
			// Text and field identity never enter this diagnostic; a later version or
			// repaint retries the host View.
			logging.Logger(logging.CatX11).Error("focused text overlay unavailable", "err", err)
			textOverlayErrorLogged = true
		} else if updated && err == nil {
			textOverlayErrorLogged = false
			if focusedTextSync.active && focusedTextSync.seen != textOverlayDiagnosticsVersion && os.Getenv("TIPSY_DIAG") == "1" {
				diag := focusedTextOverlay.Diagnostics()
				// Aggregate ink booleans and raw color are safe to log; editor content
				// and glyph identities never enter diagnostics.
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
	// ConfigureNotify shares the ordered X11 stream with pointer events. Update the
	// X11-owned size immediately, but settle the engine-facing lifecycle after a
	// quiet period so one drag cannot re-enter V2 per intermediate rectangle.
	cancelResizeInput := x11.OnInput(func(ev x11.InputEvent) {
		if ev.Kind == x11.InputResize {
			scheduleSurfaceResize(ev.Width, ev.Height)
		}
	})
	defer cancelResizeInput()

	// Signal only after the loop's event sources are live; immediate failures and
	// --probe return before this point.
	started.signal()
	shutdownClient := func(reason string) {
		_ = win.Dismiss()
		_ = win.StopBackgroundPump()
		presenter.stop()
		session.shutdown(reason)
		// terminateNativeCode's join has completed while the client lock is still held.
		// Treat the payload as opaque: flush only the file and its directory.
		if err := syncPrivateOpaqueFile(preferences); err != nil {
			logging.Logger(logging.CatFilesystem).Info("official cookie storage sync failed", "err", err)
		} else {
			logging.Logger(logging.CatFilesystem).Info("official cookie storage synchronized")
		}
	}
	// Display-refresh republication is event-driven: the X11 reader wakes this loop
	// via RefreshReady when the refresh generation moves (move, resize, reparent,
	// or RandR), and refreshFallbackPeriod bounds how long a lost wake delays it.
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
				stringDiag.logInterval(true)
				inputDrainDiag.logInterval(true)
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
	// refreshFallbackPeriod bounds how long a missed X11 refresh wake can delay
	// republication. It is deliberately long so an idle client does not wake to
	// re-check an unchanged generation.
	refreshFallbackPeriod = 30 * time.Second
	// launchDiagnosticPeriod is the cadence for the opt-in present-timing,
	// shared-wait, and TIPSY_DIAG log lines.
	launchDiagnosticPeriod = 2 * time.Second
)

// launchLoopSources is the live client loop's event and callback surface.
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
	// diagnosticsTick is a test-only clock seam. Production leaves it nil and
	// uses diagnosticsPeriod's ticker; injecting a channel pins cadence without
	// adding another launch-loop event source.
	diagnosticsTick <-chan time.Time
}

// runLaunchLoop is the live client event loop: it republishes display refresh
// rates on refresh-generation change or the fallback period, logs diagnostics
// when enabled, and runs the focused-text, resize-settle, and input paths.
func runLaunchLoop(src launchLoopSources, shutdownClient func(reason string), refreshFallback, diagnosticsPeriod time.Duration) error {
	var refreshFallbackC <-chan time.Time
	if refreshFallback > 0 {
		fallback := time.NewTicker(refreshFallback)
		defer fallback.Stop()
		refreshFallbackC = fallback.C
	}
	var diagnosticsC <-chan time.Time
	if src.diagnostics != nil && diagnosticsPeriod > 0 {
		if src.diagnosticsTick != nil {
			diagnosticsC = src.diagnosticsTick
		} else {
			diagnostics := time.NewTicker(diagnosticsPeriod)
			defer diagnostics.Stop()
			diagnosticsC = diagnostics.C
		}
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
					// Pump already unmapped the window. Stop producers, then let
					// GameActivity's destroy/join complete before the deferred unmap.
					shutdownClient("wm-delete-window")
					return nil
				}
				// Every other pump failure uses the same orderly teardown:
				// terminateNativeCode must join before the cookie fsync boundary and
				// deferred module handoff. The pump error stays the returned error.
				shutdownClient("x11-pump-error")
				return err
			}
			// XIM commits and editor keys mutate the focused snapshot during Pump;
			// paint that version in the same turn rather than waiting for the repaint fallback.
			src.refreshFocusedText(time.Now())
		}
	}
}
