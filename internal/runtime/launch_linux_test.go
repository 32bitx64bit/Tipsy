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
	"reflect"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

func TestGameActivityCommandConstants(t *testing.T) {
	if appCmdInitWindow != 1 || appCmdWindowResized != 3 || appCmdWindowRedraw != 4 || appCmdContentRectChanged != 5 || appCmdGainedFocus != 7 || appCmdStart != 11 || appCmdResume != 12 {
		t.Fatal("unexpected libroblox android_app command enum")
	}
}

func TestStutterDiagnosticsRequiresExactOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", "2"} {
		value := value
		if stutterDiagnosticsRequested(func(string) string { return value }) {
			t.Errorf("value %q enabled diagnostics", value)
		}
	}
	if !stutterDiagnosticsRequested(func(name string) string {
		if name != "TIPSY_STUTTER_DIAG" {
			t.Fatalf("queried unexpected environment key %q", name)
		}
		return "1"
	}) {
		t.Fatal("exact value 1 did not enable diagnostics")
	}
	if stutterDiagnosticsRequested(nil) {
		t.Fatal("nil environment reader enabled diagnostics")
	}
}

func TestInputDispatchInterval(t *testing.T) {
	// The launch loop waits on Window.InputReady rather than a 4ms Pump ticker.
	// Size() cannot change without InputResize on this X11 backend
	// (ConfigureNotify always enters the ring), so a poll fallback is not used.
	ch := (&x11.Window{}).InputReady()
	if ch == nil {
		t.Fatal("launch loop needs a selectable X11 InputReady channel")
	}
	select {
	case <-ch:
		t.Fatal("InputReady must start empty")
	default:
	}
}

func TestEGLPresentationPolicyHandoff(t *testing.T) {
	t.Cleanup(func() { android.SetEGLVSync(false) })
	for _, tt := range []struct {
		name     string
		settings clientsettings.Settings
		want     bool
	}{
		{name: "default off unthrottles auto fps", settings: clientsettings.Default(), want: true},
		{name: "off unthrottles low fixed fps", settings: clientsettings.Settings{FrameRate: clientsettings.FrameRate{Mode: clientsettings.FrameRateLimited, Limit: 30}}, want: true},
		{name: "on synchronizes auto fps", settings: clientsettings.Settings{FrameRate: clientsettings.FrameRate{Mode: clientsettings.FrameRateAuto}, VSync: true}, want: false},
		{name: "on synchronizes unlimited fps", settings: clientsettings.Settings{FrameRate: clientsettings.FrameRate{Mode: clientsettings.FrameRateUnlimited}, VSync: true}, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := configureEGLPresentationPolicy(tt.settings); got != tt.want {
				t.Fatalf("configureEGLPresentationPolicy()=%v want %v", got, tt.want)
			}
		})
	}
}

func TestMesaVBlankModeTracksIndependentVSync(t *testing.T) {
	t.Setenv("vblank_mode", "inherited")
	if err := configureMesaVBlankMode(false); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("vblank_mode"); got != "0" {
		t.Fatalf("VSync off vblank_mode=%q want 0", got)
	}
	if err := configureMesaVBlankMode(true); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("vblank_mode"); got != "1" {
		t.Fatalf("VSync on vblank_mode=%q want 1", got)
	}
}

func TestDisplayRefreshExportNamesAndABI(t *testing.T) {
	if currentDisplayRefreshRateSym != "Java_com_roblox_engine_jni_NativeGLInterface_nativePassCurrentDisplayRefreshRate" {
		t.Fatalf("current display refresh export = %q", currentDisplayRefreshRateSym)
	}
	if supportedRefreshRatesSym != "Java_com_roblox_engine_jni_NativeGLInterface_nativePassSupportedRefreshRates" {
		t.Fatalf("supported refresh rates export = %q", supportedRefreshRatesSym)
	}
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	class := env.FindClass("com/roblox/engine/jni/NativeGLInterface")
	currentFn, supportedFn := testDisplayRefreshExportFunctions()
	wantCurrent := float32(164.9577)
	wantSupported := []float32{59.94, 60, 120, 144, wantCurrent}
	if err := callDisplayRefreshRateExports(env, class, currentFn, supportedFn, wantCurrent, wantSupported); err != nil {
		t.Fatal(err)
	}
	gotCurrent, gotSupported := testDisplayRefreshValues()
	if gotCurrent != wantCurrent || !reflect.DeepEqual(gotSupported, wantSupported) {
		t.Fatalf("published current=%v supported=%v, want current=%v supported=%v", gotCurrent, gotSupported, wantCurrent, wantSupported)
	}
}

func TestDisplayRefreshRatesChanged(t *testing.T) {
	current := float32(60)
	supported := []float32{50, 59.94, 60}
	if displayRefreshRatesChanged(current, supported, current, append([]float32(nil), supported...)) {
		t.Fatal("identical defensive-copy snapshot reported a change")
	}
	if !displayRefreshRatesChanged(current, supported, 164.9577, []float32{60, 120, 164.9577}) {
		t.Fatal("active-output move did not report a change")
	}
	if !displayRefreshRatesChanged(current, supported, current, []float32{50, 60}) {
		t.Fatal("supported mode-list change did not report a change")
	}
}

func TestLaunchStartedAcknowledgementIsOnceAndNilSafe(t *testing.T) {
	var calls int
	ack := &launchStartedAck{fn: func() { calls++ }}
	ack.signal()
	ack.signal()
	if calls != 1 {
		t.Fatalf("Started callback calls = %d, want exactly 1", calls)
	}
	(&launchStartedAck{}).signal()
	var nilAck *launchStartedAck
	nilAck.signal()
}

func TestLaunchDoesNotAcknowledgeImmediateFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	var calls int
	err := Launch(context.Background(), LaunchOptions{Started: func() { calls++ }})
	if err == nil {
		t.Fatal("Launch without an installed runtime unexpectedly succeeded")
	}
	if calls != 0 {
		t.Fatalf("Started callback calls = %d, want 0 before client-loop readiness", calls)
	}
}

func TestAssetContentDir(t *testing.T) {
	if got, want := assetContentDir(), filepath.Join(RuntimeDir(), "assets", "content"); got != want {
		t.Fatalf("assetContentDir()=%q want %q", got, want)
	}
}

func TestInitialContentRectArgs(t *testing.T) {
	if appCmdContentRectChanged != 5 {
		t.Fatal("content rect must use APP_CMD 5")
	}
}

func TestLoggedOutAppStartExport(t *testing.T) {
	const want = "Java_com_roblox_engine_jni_NativeAppBridgeInterface_nativeAppBridgeAppStart__Ljava_lang_String_2Ljava_lang_String_2ZLjava_lang_String_2Ljava_lang_String_2Ljava_lang_String_2"
	if appStartSym != want {
		t.Fatalf("appStartSym=%q want %q", appStartSym, want)
	}
}

func TestSurfaceUpdateExportName(t *testing.T) {
	const want = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2UpdateSurfaceAppWithPlatformParams"
	if updateSurfaceSym != want {
		t.Fatalf("updateSurfaceSym=%q want %q", updateSurfaceSym, want)
	}
}

func TestDirectKeyExportName(t *testing.T) {
	const want = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassKeyEvent"
	if directKeyEventSym != want {
		t.Fatalf("directKeyEventSym=%q want %q", directKeyEventSym, want)
	}
}

func TestDirectWheelExportName(t *testing.T) {
	const want = "Java_com_roblox_engine_jni_NativeInputInterface_nativePassMouseWheel"
	if directMouseWheelSym != want {
		t.Fatalf("directMouseWheelSym=%q want %q", directMouseWheelSym, want)
	}
}

func TestDirectMouseLockGetterExportName(t *testing.T) {
	const want = "Java_com_roblox_engine_jni_NativeInputInterface_nativeGetMainWindowIsMouseLockedCenter"
	if directMouseLockedSym != want {
		t.Fatalf("directMouseLockedSym=%q want %q", directMouseLockedSym, want)
	}
}

func TestGameActivitySessionShutdownOrderAndOnce(t *testing.T) {
	var calls []string
	s := &gameActivitySession{call: func(name, sig string, extra ...uintptr) {
		calls = append(calls, name+sig)
		if name == "onWindowFocusChangedNative" {
			if len(extra) != 1 || extra[0] != 0 {
				t.Fatalf("focus-loss args = %v, want [0]", extra)
			}
		}
	}}
	s.shutdown("test-wm-close")
	s.shutdown("test-second-close")
	want := []string{
		"onWindowFocusChangedNative(JZ)V",
		"onPauseNative(J)V",
		"onSurfaceDestroyedNative(J)V",
		"onStopNative(J)V",
		"terminateNativeCode(J)V",
	}
	if len(calls) != len(want) {
		t.Fatalf("shutdown calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("shutdown calls = %v, want %v", calls, want)
		}
	}
}

func TestGameActivitySessionShutdownHasBoundedLastResort(t *testing.T) {
	enteredTerminate := make(chan struct{})
	releaseTerminate := make(chan struct{})
	forced := make(chan int, 1)
	done := make(chan struct{})
	s := &gameActivitySession{
		shutdownDeadline: 20 * time.Millisecond,
		forceExit: func(code int) {
			forced <- code
		},
		call: func(name, sig string, extra ...uintptr) {
			if name == "terminateNativeCode" {
				close(enteredTerminate)
				<-releaseTerminate
			}
		},
	}
	go func() {
		s.shutdown("test-stalled-join")
		close(done)
	}()
	<-enteredTerminate
	select {
	case code := <-forced:
		if code != 0 {
			t.Fatalf("fallback exit code = %d, want 0 for user close", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stalled terminateNativeCode did not trigger bounded fallback")
	}
	close(releaseTerminate)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("shutdown did not return after releasing terminateNativeCode")
	}
}

func TestClientModuleLifetimeRetainsStartedImage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		clientStarted bool
		wantCloses    int
	}{
		{name: "pre-start failure closes", clientStarted: false, wantCloses: 1},
		{name: "started client retained", clientStarted: true, wantCloses: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closes := 0
			if err := closeClientModuleBeforeStart(tc.clientStarted, func() error {
				closes++
				return nil
			}); err != nil {
				t.Fatalf("closeClientModuleBeforeStart: %v", err)
			}
			if closes != tc.wantCloses {
				t.Fatalf("close count = %d, want %d", closes, tc.wantCloses)
			}
		})
	}
}

// TestTextInputHandshakeConstants pins the exact Java→native handshake
// identities (§88): the engine-registered setInputConnectionNative
// class+name+sig (DEX ground truth, classes2.dex method table) and the named
// nativePassText dynsym + ABI. Any drift from the official descriptors must
// fail loudly here, never silently rewire.
func TestTextInputHandshakeConstants(t *testing.T) {
	if setInputConnectionName != "setInputConnectionNative" {
		t.Fatalf("setInputConnectionName=%q", setInputConnectionName)
	}
	const wantSig = "(JLcom/google/androidgamesdk/gametextinput/InputConnection;)V"
	if setInputConnectionSig != wantSig {
		t.Fatalf("setInputConnectionSig=%q want %q", setInputConnectionSig, wantSig)
	}
	const wantSym = "Java_com_roblox_engine_jni_NativeGLInterface_nativePassText"
	if nativePassTextSym != wantSym {
		t.Fatalf("nativePassTextSym=%q want %q", nativePassTextSym, wantSym)
	}
	const wantPassSig = "(JLjava/lang/String;ZI)V"
	if nativePassTextSig != wantPassSig {
		t.Fatalf("nativePassTextSig=%q want %q", nativePassTextSig, wantPassSig)
	}
	if nativeReturnPressedSym != "Java_com_roblox_engine_jni_NativeGLInterface_nativeReturnPressedFromOnScreenKeyboard" {
		t.Fatalf("nativeReturnPressedSym=%q", nativeReturnPressedSym)
	}
	if syncTextboxSelectionSym != "Java_com_roblox_engine_jni_NativeGLInterface_syncTextboxTextAndCursorPosition2" {
		t.Fatalf("syncTextboxSelectionSym=%q", syncTextboxSelectionSym)
	}
}

// TestTextInputHandshakeOrder pins the production order
// (startGameActivity: input target → connection → lifecycle): a zero handle
// parks the connection handshake (no fabrication without the input-target
// handle), wiring the target then hands over one stable honest object, and
// no CWD change or input synthesis occurs during the handoff.
func TestTextInputHandshakeOrder(t *testing.T) {
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	activity := env.AllocObject(env.FindClass("com/roblox/client/startup/MainGameActivity"))
	if activity == 0 {
		t.Fatal("AllocObject MainGameActivity failed")
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	jni.ClearGameActivityInputTarget()

	// 1. Without the input-target handle the handshake parks (honest 0).
	if got := deliverTextInputConnection(vm, env, activity, 0); got != 0 {
		t.Fatalf("zero-handle deliver = %d, want 0 (parked, never fabricated)", got)
	}
	if deliverTextInputConnection(nil, env, activity, 77) != 0 {
		t.Fatal("nil-VM deliver fabricated a connection")
	}

	// 2. Wire the input target exactly like production, then hand over.
	keyBefore := jni.InputDeliveryStats().KeyDelivered
	ptrBefore := jni.InputDeliveryStats().PointerDelivered
	jni.SetGameActivityInputTarget(env.Raw(), activity, 77)
	t.Cleanup(jni.ClearGameActivityInputTarget)
	conn := deliverTextInputConnection(vm, env, activity, 77)
	if conn == 0 {
		t.Fatal("wired deliver returned 0, want the honest Tipsy-owned object")
	}
	if _, _, active := jni.TextInputConnectionState(); active {
		t.Fatal("deliver activated the connection without the engine's own showKeyboard focus signal")
	}
	// 3. Stable handover: the object survives a second delivery (same id).
	if got := deliverTextInputConnection(vm, env, activity, 77); got != conn {
		t.Fatalf("second deliver = %d, want stable %d", got, conn)
	}
	if got := vm.EnsureTextInputConnection(); got != conn {
		t.Fatalf("Ensure = %d, want stable %d", got, conn)
	}
	// 4. No input synthesis, no CWD change.
	if got := jni.InputDeliveryStats().KeyDelivered; got != keyBefore {
		t.Fatalf("KeyDelivered moved %d→%d (handshake must not synthesize keys)", keyBefore, got)
	}
	if got := jni.InputDeliveryStats().PointerDelivered; got != ptrBefore {
		t.Fatalf("PointerDelivered moved %d→%d (handshake must not synthesize pointers)", ptrBefore, got)
	}
	if got, err := os.Getwd(); err != nil || got != old {
		t.Fatalf("working directory = %q, %v; want unchanged %q", got, err, old)
	}
}

func TestEnsureOfficialFontViews(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "content", "fonts")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "test.ttf"), []byte("ttf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureOfficialFontViews(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "android", "fonts", "test.ttf")); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureRobloxCABundleCopiesOfficialAssetOverStaleDestination(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	files := filepath.Join(root, "files")
	official := filepath.Join(assets, "ssl", "cacert.pem")
	dest := filepath.Join(files, "exe", "cacert.pem")
	if err := os.MkdirAll(filepath.Dir(official), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(official, []byte("official APK CA bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale host CA bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureRobloxCABundle(files, assets); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := "official APK CA bundle"; string(got) != want {
		t.Fatalf("installed bundle = %q, want official APK asset %q", got, want)
	}
}

func TestEnsureRobloxCABundleRequiresOfficialAsset(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	files := filepath.Join(root, "files")
	dest := filepath.Join(files, "exe", "cacert.pem")
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale host CA bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureRobloxCABundle(files, assets); err == nil {
		t.Fatal("missing official APK CA bundle succeeded")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if want := "stale host CA bundle"; string(got) != want {
		t.Fatalf("missing official asset changed destination to %q, want %q", got, want)
	}
}

func TestPrepareRuntimeFilesDoesNotChangeCallerWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	assets := filepath.Join(root, "assets")
	files := filepath.Join(root, "files")
	official := filepath.Join(assets, "ssl", "cacert.pem")
	if err := os.MkdirAll(filepath.Dir(official), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(official, []byte("official APK CA bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := prepareRuntimeFiles(files, assets); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Getwd(); err != nil || got != old {
		t.Fatalf("working directory = %q, %v; want unchanged %q", got, err, old)
	}
	got, err := os.ReadFile(filepath.Join(files, "exe", "cacert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "official APK CA bundle"; string(got) != want {
		t.Fatalf("installed CA bundle = %q, want %q", got, want)
	}
}

func TestAbsExistingDir(t *testing.T) {
	if got := absExistingDir(""); got != "" {
		t.Fatalf("empty dir=%q", got)
	}
	if got := absExistingDir(filepath.Join(t.TempDir(), "cache")); got == "" {
		t.Fatal("directory was not created")
	}
}

func TestGameActivityInputTargetWiring(t *testing.T) {
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	jni.ClearGameActivityInputTarget()
	before := jni.InputDeliveryStats().Dropped
	if jni.DispatchGameActivityFocus(true) {
		t.Fatal("focus delivered without a wired target")
	}
	jni.SetGameActivityInputTarget(vm.Env().Raw(), 42, 0)
	if jni.DispatchGameActivityFocus(true) {
		t.Fatal("focus delivered with a zero-handle target")
	}
	jni.SetGameActivityInputTarget(vm.Env().Raw(), 42, 77)
	if jni.DispatchGameActivityFocus(true) {
		t.Fatal("focus delivered by a bare VM without registered engine natives")
	}
	jni.ClearGameActivityInputTarget()
	if jni.DispatchGameActivityFocus(true) {
		t.Fatal("focus delivered after teardown clear")
	}
	if got := jni.InputDeliveryStats().Dropped; got != before+4 {
		t.Fatalf("Dropped = %d, want %d (no event may be queued or synthesized)", got, before+4)
	}
}

// TestPlatformParamsMatchesPointerDeviceMode pins the coherence contract:
// PlatformParams must present exactly the pointer identity the input
// dispatchers present — the official APK derives both keyboard and mouse from
// android.hardware.type.pc, while touchscreen is independent. All three are
// driven by the same source of truth (TIPSY_INPUT_DEVICE). X11 defaults to the
// PC profile; touch remains an explicit Android-phone A/B control.
func TestPlatformParamsMatchesPointerDeviceMode(t *testing.T) {
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	check := func(t *testing.T, wantTouch bool) {
		t.Helper()
		p := makePlatformParams(env, "/data/assets", 1280, 720)
		if got := env.BoolField(p, "isTouchDevice"); got != wantTouch {
			t.Fatalf("isTouchDevice = %v, want %v", got, wantTouch)
		}
		if got := env.BoolField(p, "isMouseDevice"); got != !wantTouch {
			t.Fatalf("isMouseDevice = %v, want %v", got, !wantTouch)
		}
		if got := env.BoolField(p, "isKeyboardDevice"); got != !wantTouch {
			t.Fatalf("isKeyboardDevice = %v, want %v", got, !wantTouch)
		}
	}

	t.Run("default-mouse", func(t *testing.T) {
		t.Setenv("TIPSY_INPUT_DEVICE", "")
		t.Cleanup(jni.ResetPointerDeviceMode)
		jni.ResetPointerDeviceMode()
		if jni.PointerDeviceIsTouch() {
			t.Fatal("default device mode is not mouse")
		}
		check(t, false)
	})

	t.Run("touch-gate", func(t *testing.T) {
		t.Setenv("TIPSY_INPUT_DEVICE", "touch")
		t.Cleanup(jni.ResetPointerDeviceMode)
		jni.ResetPointerDeviceMode()
		if !jni.PointerDeviceIsTouch() {
			t.Fatal("TIPSY_INPUT_DEVICE=touch did not select the touch identity")
		}
		check(t, true)
	})
}

type recordingResizeSink struct {
	events     []string
	failResize bool
}

func (r *recordingResizeSink) resizeBuffers(width, height int) error {
	if r.failResize {
		return errors.New("setBuffersGeometry refused")
	}
	r.events = append(r.events, fmt.Sprintf("buffers %dx%d", width, height))
	return nil
}

func (r *recordingResizeSink) setDisplaySize(width, height int) {
	r.events = append(r.events, fmt.Sprintf("display %dx%d", width, height))
}

func (r *recordingResizeSink) postAppCmd(cmd byte) {
	r.events = append(r.events, fmt.Sprintf("cmd%d", cmd))
}

func (r *recordingResizeSink) updateSurface(width, height int) {
	r.events = append(r.events, fmt.Sprintf("v2surface %dx%d", width, height))
}

func (r *recordingResizeSink) callNative(name, sig string, extra ...uintptr) {
	if len(extra) >= 4 {
		r.events = append(r.events, fmt.Sprintf("%s %d,%d,%d,%d", name, extra[0], extra[1], extra[2], extra[3]))
		return
	}
	r.events = append(r.events, name)
}

func newSeededResize(w, h int) *surfaceResize {
	return &surfaceResize{sink: &recordingResizeSink{}, seeded: true, width: w, height: h}
}

func recordingSink(s *surfaceResize) *recordingResizeSink {
	return s.sink.(*recordingResizeSink)
}

// TestSurfaceResizeStartupSeedDoesNotDeliver pins the seed contract: the
// startup content-rect delivery seeds the bridge, so the first ticker tick
// on an unchanged size must not re-deliver the startup events.
func TestSurfaceResizeStartupSeedDoesNotDeliver(t *testing.T) {
	s := newSeededResize(1280, 720)
	s.observe(1280, 720)
	if got := len(recordingSink(s).events); got != 0 {
		t.Fatalf("unchanged size delivered %d events: %v", got, recordingSink(s).events)
	}
}

// TestSurfaceResizeIgnoresInvalidSizes pins that non-positive sizes are
// ignored: they never reach the engine and never replace the seeded state.
func TestSurfaceResizeIgnoresInvalidSizes(t *testing.T) {
	s := newSeededResize(1280, 720)
	for _, tc := range [][2]int{{0, 720}, {1280, 0}, {-1, 720}, {0, 0}} {
		s.observe(tc[0], tc[1])
	}
	if got := len(recordingSink(s).events); got != 0 {
		t.Fatalf("invalid sizes delivered events: %v", recordingSink(s).events)
	}
	if s.seeded != true || s.width != 1280 || s.height != 720 {
		t.Fatalf("invalid sizes mutated the bridge state: seeded=%v %dx%d", s.seeded, s.width, s.height)
	}
}

// TestSurfaceResizeDeliversOncePerDeltaInOrder pins the exact per-delta
// delivery: one genuine delta runs buffers → DisplayMetrics → cmds 3,4 →
// V2 surface bridge → cmd 5 → content-rect callback {0,0,w,h} →
// insets callback, exactly once; an
// unchanged size re-delivers nothing; the next genuine delta repeats the
// sequence once.
func TestSurfaceResizeDeliversOncePerDeltaInOrder(t *testing.T) {
	s := newSeededResize(1280, 720)
	s.observe(1920, 1080)
	want := []string{
		"buffers 1920x1080",
		"display 1920x1080",
		"cmd3",
		"cmd4",
		"v2surface 1920x1080",
		"cmd5",
		"onContentRectChangedNative 0,0,1920,1080",
		"onWindowInsetsChangedNative",
	}
	if got := recordingSink(s).events; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("first delta events = %v, want %v", got, want)
	}
	s.observe(1920, 1080)
	s.observe(0, 1080)
	if got := len(recordingSink(s).events); got != len(want) {
		t.Fatalf("unchanged/invalid size re-delivered: %v", recordingSink(s).events[len(want):])
	}
	s.observe(1024, 768)
	got := recordingSink(s).events[len(want):]
	want2 := []string{
		"buffers 1024x768",
		"display 1024x768",
		"cmd3",
		"cmd4",
		"v2surface 1024x768",
		"cmd5",
		"onContentRectChangedNative 0,0,1024,768",
		"onWindowInsetsChangedNative",
	}
	if fmt.Sprint(got) != fmt.Sprint(want2) {
		t.Fatalf("second delta events = %v, want %v", got, want2)
	}
}

// TestSurfaceResizeFailedGeometryAbortsDelivery pins the honest-failure
// path: when ANativeWindow geometry refuses the update, nothing else is
// delivered and the delta is retried on the next observation.
func TestSurfaceResizeFailedGeometryAbortsDelivery(t *testing.T) {
	s := newSeededResize(1280, 720)
	rec := recordingSink(s)
	rec.failResize = true
	s.observe(1600, 900)
	if got := len(rec.events); got != 0 {
		t.Fatalf("failed geometry delivered events: %v", rec.events)
	}
	if s.seeded != true || s.width != 1280 || s.height != 720 {
		t.Fatalf("failed geometry mutated the bridge state: %dx%d", s.width, s.height)
	}
	rec.failResize = false
	s.observe(1600, 900)
	if got := len(rec.events); got != 8 {
		t.Fatalf("retry after failed geometry delivered %d events, want 8: %v", got, rec.events)
	}
}

// TestSurfaceResizePipeDeliversCommandBytes runs the production sink's
// command path against a fake engine pipe: one delta writes exactly the
// three command bytes 3,4,5 once, an unchanged size writes nothing, and a
// second delta writes them again — through the real postAndroidAppCmd
// msgwrite and the real ANativeWindow resize.
func TestSurfaceResizePipeDeliversCommandBytes(t *testing.T) {
	page := 0x7000000
	buf, err := syscall.Mmap(-1, 0, page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(buf) })
	base := uintptr(unsafe.Pointer(&buf[0]))
	handle := base + 0x2000

	var pipeFDs [2]int
	if err := syscall.Pipe2(pipeFDs[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	rfd, wfd := os.NewFile(uintptr(pipeFDs[0]), "cmd-r"), os.NewFile(uintptr(pipeFDs[1]), "cmd-w")
	t.Cleanup(func() { rfd.Close(); wfd.Close() })
	*(*int32)(unsafe.Pointer(handle + 0x154)) = int32(pipeFDs[1])

	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	aw := android.NewWindow(1280, 720, nil)
	s := &surfaceResize{
		sink:   &engineResizeSink{handle: handle, vm: vm, aw: aw},
		seeded: true, width: 1280, height: 720,
	}

	readAll := func() []byte {
		var out []byte
		for len(out) < 3 {
			b := make([]byte, 3-len(out))
			n, _ := rfd.Read(b)
			if n == 0 {
				break
			}
			out = append(out, b[:n]...)
		}
		return out
	}
	readable := func(timeoutMS int) bool {
		var set syscall.FdSet
		fd := pipeFDs[0]
		set.Bits[fd/64] |= 1 << (uint(fd) % 64)
		tv := syscall.Timeval{Sec: int64(timeoutMS / 1000), Usec: int64((timeoutMS % 1000) * 1000)}
		n, err := syscall.Select(fd+1, &set, nil, nil, &tv)
		return err == nil && n > 0
	}

	s.observe(1600, 900)
	if got := readAll(); len(got) != 3 || got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("first delta pipe bytes = %v, want [3 4 5]", got)
	}
	if readable(100) {
		t.Fatal("first delta wrote more than the three command bytes")
	}
	if gw, gh := aw.Size(); gw != 1600 || gh != 900 {
		t.Fatalf("ANativeWindow size = %dx%d, want 1600x900", gw, gh)
	}
	s.observe(1600, 900)
	if readable(100) {
		t.Fatal("unchanged size wrote pipe bytes")
	}
	s.observe(1024, 768)
	if got := readAll(); len(got) != 3 || got[0] != 3 || got[1] != 4 || got[2] != 5 {
		t.Fatalf("second delta pipe bytes = %v, want [3 4 5]", got)
	}
	if readable(100) {
		t.Fatal("second delta wrote more than the three command bytes")
	}
}

func TestPostAndroidAppCmdWritesHandlePipe(t *testing.T) {
	buf, err := syscall.Mmap(-1, 0, 0x200, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(buf) })
	handle := uintptr(unsafe.Pointer(&buf[0]))
	var pipeFDs [2]int
	if err := syscall.Pipe2(pipeFDs[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(pipeFDs[0]); _ = syscall.Close(pipeFDs[1]) })
	*(*int32)(unsafe.Pointer(handle + 0x154)) = int32(pipeFDs[1])
	postAndroidAppCmd(handle, appCmdInitWindow)
	b := make([]byte, 1)
	n, err := syscall.Read(pipeFDs[0], b)
	if err != nil || n != 1 || b[0] != appCmdInitWindow {
		t.Fatalf("pipe read n=%d err=%v b=%v", n, err, b[:n])
	}
	postAndroidAppCmd(0, appCmdStart)
}

func TestDisplayRefreshPublicationInvalidationAndRetry(t *testing.T) {
	p := displayRefreshPublication{version: 1, current: 60, supported: []float32{60}}
	queries, publications := 0, 0
	current, supported := float32(60), []float32{60}
	var publishErr error
	query := func() (float32, []float32) {
		queries++
		return current, supported
	}
	publish := func(float32, []float32) error {
		publications++
		return publishErr
	}
	for i := 0; i < 1000; i++ {
		if err := p.update(1, query, publish); err != nil {
			t.Fatal(err)
		}
	}
	if queries != 0 || publications != 0 {
		t.Fatal("unchanged window performed display work")
	}
	// A move within one monitor consumes the event with no duplicate JNI call.
	if err := p.update(2, query, publish); err != nil {
		t.Fatal(err)
	}
	if queries != 1 || publications != 0 || p.version != 2 {
		t.Fatalf("unchanged rates: queries=%d publications=%d version=%d", queries, publications, p.version)
	}
	// Unknown XRandR data must retry on the same generation.
	current, supported = 0, nil
	if err := p.update(3, query, publish); err != nil || p.version != 2 {
		t.Fatalf("failed query consumed generation: %+v, %v", p, err)
	}
	current, supported = 165, []float32{60, 165}
	publishErr = errors.New("second JNI publication failed")
	if err := p.update(3, query, publish); !errors.Is(err, publishErr) {
		t.Fatalf("publish failure=%v", err)
	}
	if p.version != 2 || p.current != 60 {
		t.Fatalf("partial JNI publication consumed snapshot: %+v", p)
	}
	publishErr = nil
	if err := p.update(3, query, publish); err != nil || p.version != 3 || p.current != 165 {
		t.Fatalf("retry did not publish changed monitor: %+v, %v", p, err)
	}
	if queries != 4 || publications != 2 {
		t.Fatalf("queries=%d publications=%d", queries, publications)
	}
}

func TestDisplayRefreshPublicationRetainsConcurrentEvent(t *testing.T) {
	p := displayRefreshPublication{version: 1, current: 60, supported: []float32{60}}
	version := uint64(2)
	queries := 0
	query := func() (float32, []float32) {
		queries++
		version = 3 // A further X11 event arrives while the server is replying.
		return 165, []float32{60, 165}
	}
	publish := func(float32, []float32) error { return nil }
	if err := p.update(version, query, publish); err != nil {
		t.Fatal(err)
	}
	if p.version != 2 {
		t.Fatalf("consumed event arriving during query: version=%d", p.version)
	}
	if err := p.update(version, query, publish); err != nil || queries != 2 || p.version != 3 {
		t.Fatalf("pending event lost: queries=%d version=%d error=%v", queries, p.version, err)
	}
}

func BenchmarkDisplayRefreshPublicationUnchanged(b *testing.B) {
	p := displayRefreshPublication{version: 1}
	// Nil callbacks also ensure the skip path cannot accidentally query X11.
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = p.update(1, nil, nil)
	}
}

func TestVulkanPresentTimingRequiresExactOptIn(t *testing.T) {
	for _, tc := range []struct {
		timing, sync string
		want         bool
	}{
		{"", "", false}, {"0", "0", false}, {"true", "yes", false},
		{"1", "", true}, {"", "1", true}, {"1", "1", true},
	} {
		getenv := func(name string) string {
			if name == "TIPSY_PRESENT_TIMING" {
				return tc.timing
			}
			if name == "TIPSY_STUTTER_DIAG" {
				return tc.sync
			}
			return ""
		}
		if got := vulkanPresentTimingRequested(getenv); got != tc.want {
			t.Errorf("timing=%q sync=%q enabled=%v", tc.timing, tc.sync, got)
		}
		if tc.timing == "1" && tc.sync == "" && stutterDiagnosticsRequested(getenv) {
			t.Fatal("present-only timing enabled synchronization wrappers")
		}
	}
	if vulkanPresentTimingRequested(nil) {
		t.Fatal("nil environment enabled timing")
	}
}
