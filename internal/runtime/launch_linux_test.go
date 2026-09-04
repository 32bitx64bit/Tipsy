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
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
)

func TestGameActivityCommandConstants(t *testing.T) {
	if appCmdInitWindow != 1 || appCmdWindowResized != 3 || appCmdWindowRedraw != 4 || appCmdContentRectChanged != 5 || appCmdGainedFocus != 7 || appCmdStart != 11 || appCmdResume != 12 {
		t.Fatal("unexpected libroblox android_app command enum")
	}
}

func TestInputDispatchInterval(t *testing.T) {
	if inputDispatchInterval != 4*time.Millisecond {
		t.Fatalf("input dispatch interval = %s, want 4ms", inputDispatchInterval)
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
// dispatchers present — isTouchDevice == isMouseDevice's negation, driven
// by the same source of truth (TIPSY_INPUT_DEVICE). X11 defaults to mouse;
// touch remains an explicit Android-identity A/B control.
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
		if got := env.BoolField(p, "isKeyboardDevice"); !got {
			t.Fatal("isKeyboardDevice = false, want true (keys are unaffected by the pointer identity)")
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
	engine := base + 0x1000
	app := base + 0x2000
	*(*uintptr)(unsafe.Pointer(base + 0x6fedc78)) = engine
	*(*uintptr)(unsafe.Pointer(engine + 0x10)) = app

	var pipeFDs [2]int
	if err := syscall.Pipe2(pipeFDs[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	rfd, wfd := os.NewFile(uintptr(pipeFDs[0]), "cmd-r"), os.NewFile(uintptr(pipeFDs[1]), "cmd-w")
	t.Cleanup(func() { rfd.Close(); wfd.Close() })
	*(*int32)(unsafe.Pointer(app + 0x124)) = int32(pipeFDs[1])

	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	aw := android.NewWindow(1280, 720, nil)
	s := &surfaceResize{
		sink:   &engineResizeSink{mod: &loader.Module{Base: base}, vm: vm, aw: aw},
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
