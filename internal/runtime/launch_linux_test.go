// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/integrity"
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

func TestTestOpenGLRequestIsExactAndProcessLocal(t *testing.T) {
	for _, value := range []string{"", "opengl", "vulkan", "auto", "opengl ", "OpenGL", "gl"} {
		value := value
		requested, err := testOpenGLRequested(func(string) string { return value })
		if value == "opengl" {
			if err != nil || !requested {
				t.Fatalf("opengl request = %t, %v", requested, err)
			}
			continue
		}
		if value == "" {
			if err != nil || requested {
				t.Fatalf("empty request = %t, %v", requested, err)
			}
			continue
		}
		if err == nil || requested {
			t.Errorf("invalid %q request = %t, %v", value, requested, err)
		}
	}
	if requested, err := testOpenGLRequested(nil); err != nil || requested {
		t.Fatalf("nil environment request = %t, %v", requested, err)
	}
}

func TestAndroidClientSettingsInitArgsMatchOfficialAPK(t *testing.T) {
	settings := `{"applicationSettings":{"Official":"kept"}}`
	overridePayload := `{"FFlagDebugGraphicsPreferOpenGL":"True"}`
	got := androidClientSettingsInitArgs(settings, overridePayload)
	want := [3]string{settings, overridePayload, "GoogleAndroidApp"}
	if got != want {
		t.Fatalf("nativeInitClientSettings args=%q want %q", got, want)
	}

	ordinary := androidClientSettingsInitArgs(settings, "")
	if ordinary != [3]string{settings, "", "GoogleAndroidApp"} {
		t.Fatalf("ordinary nativeInitClientSettings args=%q", ordinary)
	}
}

func TestClientSettingsInitLocalsReleaseAfterNativeInit(t *testing.T) {
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	args := androidClientSettingsInitArgs(`{"applicationSettings":{"Official":"kept"},"ClientAppSettings":{"Official":"kept"}}`, "")
	locals := newClientSettingsInitLocals(env, args)
	for i, obj := range locals {
		if obj == 0 {
			t.Fatalf("arg %d NewString returned NULL", i)
		}
		got, err := env.GetStringUTFChars(obj)
		if err != nil || got != args[i] {
			t.Fatalf("arg %d = %q err=%v want %q", i, got, err, args[i])
		}
	}
	releaseClientSettingsInitLocals(env, locals)
	releaseClientSettingsInitLocals(nil, locals)
	env.DeleteLocalRef(0)
	for i, obj := range locals {
		if args[i] == "" {
			continue
		}
		got, err := env.GetStringUTFChars(obj)
		if got != "" || err != nil {
			t.Fatalf("arg %d survived DeleteLocalRef: %q err=%v", i, got, err)
		}
	}
}

func TestRendererPreloadPrecedesGameActivitySuperAndOrdinaryIsInert(t *testing.T) {
	var order []string
	runMainGameActivityOnCreateBoundary(`{"renderer":"opengl"}`, func(payload string) {
		if payload != `{"renderer":"opengl"}` {
			t.Fatalf("preload payload=%q", payload)
		}
		order = append(order, "preload")
	}, func() {
		order = append(order, "super_create")
	})
	if !reflect.DeepEqual(order, []string{"preload", "super_create"}) {
		t.Fatalf("GameActivity order=%v", order)
	}

	order = nil
	runMainGameActivityOnCreateBoundary("", func(string) {
		order = append(order, "unexpected_preload")
	}, func() {
		order = append(order, "super_create")
	})
	if !reflect.DeepEqual(order, []string{"super_create"}) {
		t.Fatalf("ordinary GameActivity order=%v", order)
	}
}

func TestStutterInputDrainDiagnosticsDefaultOff(t *testing.T) {
	var setCalls []bool
	snapshotCalls := 0
	logCalls := 0
	diagnostics := stutterInputDrainDiagnostics{
		setEnabled: func(enabled bool) { setCalls = append(setCalls, enabled) },
		snapshot: func(bool) x11.InputDrainStats {
			snapshotCalls++
			return x11.InputDrainStats{}
		},
		log: func(string, ...any) { logCalls++ },
	}
	diagnostics.begin(false)
	diagnostics.logInterval(false)
	diagnostics.end(false)
	if len(setCalls) != 0 || snapshotCalls != 0 || logCalls != 0 {
		t.Fatalf("disabled input-drain diagnostics touched source: sets=%v snapshots=%d logs=%d", setCalls, snapshotCalls, logCalls)
	}
}

func diagnosticAttr(t *testing.T, args []any, name string) any {
	t.Helper()
	for i := 0; i+1 < len(args); i += 2 {
		key, ok := args[i].(string)
		if ok && key == name {
			return args[i+1]
		}
	}
	t.Fatalf("missing diagnostic attribute %q in %#v", name, args)
	return nil
}

func TestStutterInputDrainDiagnosticsAggregateAndTeardown(t *testing.T) {
	interval := x11.InputDrainStats{
		DrainCalls: 11, EmptyDrains: 3, NonEmptyDrains: 8, Events: 29,
		BatchBuckets:      [x11.InputDrainBatchBuckets]uint64{3, 4, 2, 1},
		InputRingLockWait: x11.InputDrainDurationStats{Samples: 2, TotalNS: 91, MaxNS: 70},
		CDrain:            x11.InputDrainDurationStats{Samples: 2, TotalNS: 222, MaxNS: 140},
		PumpWait:          x11.InputDrainDurationStats{Samples: 4, TotalNS: 600, MaxNS: 250},
		PumpWaitCalls:     5, PumpReady: 4, PumpPipeWakes: 1,
		GoWindowLockWait: x11.InputDrainDurationStats{Samples: 11, TotalNS: 88, MaxNS: 18},
		GoDrain:          x11.InputDrainDurationStats{Samples: 11, TotalNS: 777, MaxNS: 145},
	}
	final := x11.InputDrainStats{DrainCalls: 2, Events: 2, GoDrain: x11.InputDrainDurationStats{Samples: 2, TotalNS: 50, MaxNS: 30}}
	snapshots := []x11.InputDrainStats{{}, interval, final}
	var resets []bool
	var enabled []bool
	type entry struct {
		msg  string
		args []any
	}
	var entries []entry
	diagnostics := stutterInputDrainDiagnostics{
		setEnabled: func(value bool) { enabled = append(enabled, value) },
		snapshot: func(reset bool) x11.InputDrainStats {
			resets = append(resets, reset)
			if len(snapshots) == 0 {
				t.Fatal("unexpected snapshot")
			}
			out := snapshots[0]
			snapshots = snapshots[1:]
			return out
		},
		log: func(msg string, args ...any) {
			entries = append(entries, entry{msg: msg, args: append([]any(nil), args...)})
		},
	}
	diagnostics.begin(true)
	diagnostics.logInterval(true)
	diagnostics.end(true)

	if got, want := enabled, []bool{true, false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enable sequence = %v, want %v", got, want)
	}
	if got, want := resets, []bool{true, true, true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot reset sequence = %v, want %v", got, want)
	}
	if len(entries) != 2 {
		t.Fatalf("aggregate log count = %d, want 2", len(entries))
	}
	if entries[0].msg != "X11 input-drain diagnostic aggregate" || entries[1].msg != entries[0].msg {
		t.Fatalf("aggregate log messages = %#v", entries)
	}
	if got := diagnosticAttr(t, entries[0].args, "events"); got != interval.Events {
		t.Fatalf("interval events = %#v, want %d", got, interval.Events)
	}
	if got := diagnosticAttr(t, entries[0].args, "batchBuckets"); !reflect.DeepEqual(got, interval.BatchBuckets) {
		t.Fatalf("interval batches = %#v, want %#v", got, interval.BatchBuckets)
	}
	if got := diagnosticAttr(t, entries[0].args, "cDrainMaxNS"); got != interval.CDrain.MaxNS {
		t.Fatalf("interval C drain max = %#v, want %d", got, interval.CDrain.MaxNS)
	}
	if got := diagnosticAttr(t, entries[0].args, "pumpPipeWakes"); got != interval.PumpPipeWakes {
		t.Fatalf("interval pump pipe wakes = %#v, want %d", got, interval.PumpPipeWakes)
	}
	if got := diagnosticAttr(t, entries[0].args, "goDrainTotalNS"); got != interval.GoDrain.TotalNS {
		t.Fatalf("interval Go drain total = %#v, want %d", got, interval.GoDrain.TotalNS)
	}
	if got := diagnosticAttr(t, entries[1].args, "drainCalls"); got != final.DrainCalls {
		t.Fatalf("final drain calls = %#v, want %d", got, final.DrainCalls)
	}
}

func stringDiagnosticFixture(seed uint64) jni.JNIStringDiagnostics {
	var out jni.JNIStringDiagnostics
	for path := range out.Paths {
		value := seed + uint64(path+1)*100
		stats := jni.JNIStringPathStats{
			Calls: value + 1, Succeeded: value + 2,
			InputUTF8Bytes: value + 3, OutputUTF8Bytes: value + 4, OutputUTF16Bytes: value + 5,
			CAllocatedBytes: value + 6, CopiedBytes: value + 7, StringObjects: value + 8,
			DurationSamples: value + 9, SampledDurationNS: value + 10, MaxSampledDurationNS: value + 11,
		}
		for bucket := range stats.DurationBuckets {
			stats.DurationBuckets[bucket] = value + uint64(bucket+12)
		}
		out.Paths[path] = stats
	}
	return out
}

func TestStutterJNIStringDiagnosticsDefaultOff(t *testing.T) {
	setSnapshots := 0
	logs := 0
	diagnostics := stutterJNIStringDiagnostics{
		snapshot: func(bool) jni.JNIStringDiagnostics {
			setSnapshots++
			return jni.JNIStringDiagnostics{}
		},
		log: func(string, ...any) { logs++ },
	}
	diagnostics.begin(false)
	diagnostics.logInterval(false)
	diagnostics.end(false)
	if setSnapshots != 0 || logs != 0 {
		t.Fatalf("disabled JNI string diagnostics touched source: snapshots=%d logs=%d", setSnapshots, logs)
	}
}

func TestStutterJNIStringDiagnosticsAggregateAndTeardown(t *testing.T) {
	interval := stringDiagnosticFixture(1_000)
	final := stringDiagnosticFixture(2_000)
	snapshots := []jni.JNIStringDiagnostics{{}, interval, final}
	var resets []bool
	type entry struct {
		msg  string
		args []any
	}
	var entries []entry
	diagnostics := stutterJNIStringDiagnostics{
		snapshot: func(reset bool) jni.JNIStringDiagnostics {
			resets = append(resets, reset)
			if len(snapshots) == 0 {
				t.Fatal("unexpected JNI string snapshot")
			}
			out := snapshots[0]
			snapshots = snapshots[1:]
			return out
		},
		log: func(msg string, args ...any) {
			entries = append(entries, entry{msg: msg, args: append([]any(nil), args...)})
		},
	}
	diagnostics.begin(true)
	diagnostics.logInterval(true)
	diagnostics.end(true)
	if got, want := resets, []bool{true, true, true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot reset sequence = %v, want %v", got, want)
	}
	if len(entries) != 2 {
		t.Fatalf("aggregate log count = %d, want 2", len(entries))
	}
	if entries[0].msg != "JNI string diagnostic aggregate" || entries[1].msg != entries[0].msg {
		t.Fatalf("aggregate log messages = %#v", entries)
	}
	paths := []struct {
		name string
		path jni.JNIStringDiagnosticPath
	}{
		{"getStringChars", jni.JNIStringGetStringChars},
		{"getStringUTFChars", jni.JNIStringGetStringUTFChars},
		{"getStringCritical", jni.JNIStringGetStringCritical},
		{"releaseStringChars", jni.JNIStringReleaseStringChars},
		{"releaseStringUTFChars", jni.JNIStringReleaseStringUTF},
		{"releaseStringCritical", jni.JNIStringReleaseStringCritical},
		{"newStringUTF", jni.JNIStringNewStringUTF},
		{"isInstanceOf", jni.JNIStringIsInstanceOf},
		{"fieldGetterString", jni.JNIStringFieldGetterString},
	}
	for _, tc := range paths {
		got, ok := diagnosticAttr(t, entries[0].args, tc.name).(jniStringPathDiagnosticFields)
		if !ok {
			t.Fatalf("%s log value has unexpected type %T", tc.name, diagnosticAttr(t, entries[0].args, tc.name))
		}
		want := jniStringDiagnosticFields(interval.Paths[tc.path])
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s fields = %+v, want %+v", tc.name, got, want)
		}
		assertNumericAggregateType(t, reflect.TypeOf(got))
	}
	finalGetChars, ok := diagnosticAttr(t, entries[1].args, "getStringChars").(jniStringPathDiagnosticFields)
	if !ok || !reflect.DeepEqual(finalGetChars, jniStringDiagnosticFields(final.Paths[jni.JNIStringGetStringChars])) {
		t.Fatalf("final GetStringChars = %#v, want %+v", finalGetChars, jniStringDiagnosticFields(final.Paths[jni.JNIStringGetStringChars]))
	}
}

func assertNumericAggregateType(t *testing.T, typ reflect.Type) {
	t.Helper()
	switch typ.Kind() {
	case reflect.Uint64:
		return
	case reflect.Array:
		assertNumericAggregateType(t, typ.Elem())
	case reflect.Struct:
		for field := range typ.NumField() {
			assertNumericAggregateType(t, typ.Field(field).Type)
		}
	default:
		t.Fatalf("diagnostic aggregate exposes non-numeric type %v", typ)
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
	refreshCh := (&x11.Window{}).RefreshReady()
	if refreshCh == nil {
		t.Fatal("launch loop needs a selectable X11 RefreshReady channel")
	}
	select {
	case <-refreshCh:
		t.Fatal("RefreshReady must start empty")
	default:
	}
}

type recordingFullscreenWindow struct {
	requests []bool
	err      error
	order    *[]string
}

func (w *recordingFullscreenWindow) SetFullscreen(enabled bool) error {
	w.requests = append(w.requests, enabled)
	if w.order != nil {
		*w.order = append(*w.order, "ewmh-fullscreen")
	}
	return w.err
}

func TestStartFullscreenPolicyDefaultIsWindowed(t *testing.T) {
	if (LaunchOptions{}).StartFullscreen {
		t.Fatal("zero LaunchOptions unexpectedly requests fullscreen")
	}
	if startFullscreenRequested(LaunchOptions{}, clientsettings.Default()) {
		t.Fatal("default launch policy and settings unexpectedly request fullscreen")
	}
	w := &recordingFullscreenWindow{}
	if err := requestStartFullscreen(w, false); err != nil {
		t.Fatalf("disabled fullscreen policy: %v", err)
	}
	if len(w.requests) != 0 {
		t.Fatalf("disabled fullscreen policy sent requests %#v", w.requests)
	}
}

func TestStartFullscreenPolicyUsesExplicitOrPersistedHostSetting(t *testing.T) {
	settings := clientsettings.Default()
	settings.StartFullscreen = true
	if !startFullscreenRequested(LaunchOptions{}, settings) {
		t.Fatal("persisted host fullscreen setting was ignored")
	}
	if !startFullscreenRequested(LaunchOptions{StartFullscreen: true}, clientsettings.Default()) {
		t.Fatal("explicit host fullscreen launch policy was ignored")
	}
}

func TestStartFullscreenPolicyRequestsEWMHBeforeClientLifecycle(t *testing.T) {
	var order []string
	w := &recordingFullscreenWindow{order: &order}
	if err := requestStartFullscreen(w, true); err != nil {
		t.Fatalf("start fullscreen request: %v", err)
	}
	order = append(order, "client-lifecycle")
	if got, want := w.requests, []bool{true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fullscreen requests = %#v, want %#v", got, want)
	}
	if got, want := order, []string{"ewmh-fullscreen", "client-lifecycle"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("launch order = %#v, want %#v", got, want)
	}
}

func TestStartFullscreenPolicyPropagatesEWMHError(t *testing.T) {
	w := &recordingFullscreenWindow{err: x11.ErrFullscreen}
	if err := requestStartFullscreen(w, true); !errors.Is(err, x11.ErrFullscreen) {
		t.Fatalf("start fullscreen error = %v, want %v", err, x11.ErrFullscreen)
	}
}

type recordingFocusedTextOverlay struct {
	frames []x11.FocusedTextSnapshot
	err    error
}

func (r *recordingFocusedTextOverlay) Update(frame x11.FocusedTextSnapshot) error {
	r.frames = append(r.frames, frame)
	return r.err
}

func TestFocusedTextOverlaySyncCopiesOnlyForChangeOrActiveRepaint(t *testing.T) {
	sink := &recordingFocusedTextOverlay{}
	version := uint64(7)
	snapshotCalls := 0
	sync := &focusedTextOverlaySync{
		sink:    sink,
		version: func() uint64 { return version },
		snapshot: func() jni.RbxTextOverlaySnapshot {
			snapshotCalls++
			return jni.RbxTextOverlaySnapshot{
				Version: 7, Active: true, Configured: true, Text: "tipsyok",
				SelectionStartUTF16: 7, SelectionEndUTF16: 7,
				Density: 1, X: 4, Y: 5, Width: 120, Height: 30, FontSize: 16,
				TextColor: 0xff123456, TextInputType: 6, CursorVisible: true,
				IncludeFontPadding: true,
			}
		},
	}
	now := time.Unix(100, 0)
	updated, err := sync.refresh(now)
	if err != nil || !updated || snapshotCalls != 1 || len(sink.frames) != 1 {
		t.Fatalf("first refresh updated=%v calls=%d frames=%d err=%v", updated, snapshotCalls, len(sink.frames), err)
	}
	got := sink.frames[0]
	if got.Version != 7 || got.Text != "tipsyok" || got.CursorUTF16 != 7 || got.Density != 1 ||
		got.X != 4 || got.TextColor != 0xff123456 || got.FontFile != filepath.Join("fonts", "SourceSansPro-Regular.ttf") ||
		got.LetterSpacing != 0 ||
		got.TextInputType != 6 || !got.CursorVisible || !got.IncludeFontPadding ||
		math.Abs(float64(got.FontSize-12.72)) > 0.0001 {
		t.Fatalf("frame mismatch: %+v", got)
	}
	updated, err = sync.refresh(now.Add(100 * time.Millisecond))
	if err != nil || updated || snapshotCalls != 1 {
		t.Fatalf("unchanged refresh copied content: updated=%v calls=%d err=%v", updated, snapshotCalls, err)
	}
	updated, err = sync.refresh(now.Add(focusedTextOverlayRepaint))
	if err != nil || !updated || snapshotCalls != 2 {
		t.Fatalf("active repaint updated=%v calls=%d err=%v", updated, snapshotCalls, err)
	}
}

func TestFocusedTextFontResolverMirrorsAPKMappingAndFallback(t *testing.T) {
	assets := t.TempDir()
	mappingDir := filepath.Join(assets, "android", "fonts")
	if err := os.MkdirAll(mappingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const mapping = `[
		{"enum":16,"font":"SourceSansPro-Semibold.ttf","fromRbxFontRatio":0.7955449483},
		{"enum":99,"font":"../outside.ttf","fromRbxFontRatio":1},
		{"enum":100,"font":"invalid.ttf","fromRbxFontRatio":0}
	]`
	if err := os.WriteFile(filepath.Join(mappingDir, "font-mappings.json"), []byte(mapping), 0o600); err != nil {
		t.Fatal(err)
	}
	mappedFontDir := filepath.Join(assets, "content", "fonts")
	if err := os.MkdirAll(mappedFontDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mappedFontDir, "SourceSansPro-Semibold.ttf"), []byte("font fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolver := newFocusedTextFontResolver(assets)
	mapped := resolver.resolve(16)
	if mapped.file != filepath.Join(assets, "content", "fonts", "SourceSansPro-Semibold.ttf") ||
		math.Abs(float64(mapped.ratio-0.7955449483)) > 0.000001 || mapped.letterSpacingEm != 0 {
		t.Fatalf("mapped font contract = %+v", mapped)
	}
	sink := &recordingFocusedTextOverlay{}
	sync := &focusedTextOverlaySync{
		sink: sink, version: func() uint64 { return 1 }, fonts: resolver,
		snapshot: func() jni.RbxTextOverlaySnapshot {
			return jni.RbxTextOverlaySnapshot{
				Version: 1, Active: true, Configured: true,
				Density: 2, Width: 100, Height: 30, FontSize: 20, Font: 16,
			}
		},
	}
	if updated, err := sync.refresh(time.Unix(250, 0)); err != nil || !updated {
		t.Fatalf("mapped font refresh updated=%v err=%v", updated, err)
	}
	frame := sink.frames[0]
	if frame.FontFile != mapped.file || math.Abs(float64(frame.FontSize-31.821798)) > 0.0001 {
		t.Fatalf("mapped density/font size = file=%q size=%v", frame.FontFile, frame.FontSize)
	}
	for _, tc := range []struct {
		font    int32
		file    string
		spacing float32
	}{
		{font: 4, file: "SourceSansPro-Bold.ttf", spacing: 0.04},
		{font: 5, file: "SourceSansPro-Light.ttf"},
		{font: 99, file: "SourceSansPro-Regular.ttf"},
		{font: 100, file: "SourceSansPro-Regular.ttf"},
	} {
		got := resolver.resolve(tc.font)
		if got.file != filepath.Join(assets, "fonts", tc.file) || got.ratio != 0.795 || got.letterSpacingEm != tc.spacing {
			t.Fatalf("fallback font %d contract = %+v", tc.font, got)
		}
	}
}

func TestFocusedTextOverlayKeepsAPKPixelsAcrossWindowModes(t *testing.T) {
	for i, viewport := range []struct {
		name          string
		width, height int32
	}{
		{name: "1280x720", width: 1280, height: 720},
		{name: "resized", width: 1600, height: 900},
		{name: "fullscreen", width: 2560, height: 1440},
	} {
		t.Run(viewport.name, func(t *testing.T) {
			sink := &recordingFocusedTextOverlay{}
			version := uint64(i + 1)
			sync := &focusedTextOverlaySync{
				sink: sink, version: func() uint64 { return version },
				fonts: focusedTextFontResolver{assetsDir: "/apk-assets"},
				snapshot: func() jni.RbxTextOverlaySnapshot {
					return jni.RbxTextOverlaySnapshot{
						Version: version, Active: true, Configured: true,
						Density: 1, ViewportWidthPx: viewport.width, ViewportHeightPx: viewport.height,
						X: 10, Y: 20, Width: 300, Height: 31, FontSize: 17.5,
						Font: 4, XAlignment: 1, YAlignment: 2,
						PaddingLeftPx: 0, PaddingTopPx: 0, PaddingRightPx: 0, PaddingBottomPx: 0,
						CursorVisible: true, IncludeFontPadding: true,
					}
				},
			}
			if updated, err := sync.refresh(time.Unix(300, 0)); err != nil || !updated {
				t.Fatalf("refresh updated=%v err=%v", updated, err)
			}
			got := sink.frames[0]
			if got.X != 10 || got.Y != 20 || got.Width != 300 || got.Height != 31 ||
				got.XAlignment != 1 || got.YAlignment != 2 || got.Density != 1 ||
				math.Abs(float64(got.FontSize-13.9125)) > 0.0001 {
				t.Fatalf("pixel/font contract at %dx%d = %+v", viewport.width, viewport.height, got)
			}
		})
	}
}

func TestFocusedTextOverlaySyncHidesOnGenuineInactiveVersion(t *testing.T) {
	sink := &recordingFocusedTextOverlay{}
	version := uint64(1)
	active := true
	sync := &focusedTextOverlaySync{
		sink:    sink,
		version: func() uint64 { return version },
		snapshot: func() jni.RbxTextOverlaySnapshot {
			return jni.RbxTextOverlaySnapshot{Version: version, Active: active, Configured: active, Text: "tipsyok"}
		},
	}
	now := time.Unix(200, 0)
	if updated, err := sync.refresh(now); err != nil || !updated {
		t.Fatalf("show refresh updated=%v err=%v", updated, err)
	}
	active = false
	version++
	if updated, err := sync.refresh(now.Add(time.Millisecond)); err != nil || !updated {
		t.Fatalf("hide refresh updated=%v err=%v", updated, err)
	}
	if got := sink.frames[len(sink.frames)-1]; got.Active || got.Text != "" {
		t.Fatalf("inactive frame retained content: %+v", got)
	}
	calls := len(sink.frames)
	if updated, err := sync.refresh(now.Add(time.Second)); err != nil || updated || len(sink.frames) != calls {
		t.Fatalf("inactive steady state repainted: updated=%v frames=%d err=%v", updated, len(sink.frames), err)
	}
}

func TestFocusedTextOverlayWakeIsNilWhenInactive(t *testing.T) {
	var wake focusedTextOverlayWake
	defer wake.stop()
	if wake.C() != nil {
		t.Fatal("inactive overlay wake channel must be nil")
	}
	wake.sync(true)
	if wake.C() == nil {
		t.Fatal("focused overlay must tick")
	}
	if wake.ticker == nil {
		t.Fatal("focused overlay ticker must exist")
	}
	wake.sync(true)
	if wake.C() == nil {
		t.Fatal("still-focused overlay must keep ticking")
	}
	wake.sync(false)
	if wake.C() != nil || wake.ticker != nil {
		t.Fatal("inactive overlay wake must become nil")
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
	if !errors.Is(err, ErrAuthorizedGenerationRequired) {
		t.Fatalf("Launch error = %v, want authenticated-generation gate", err)
	}
	if calls != 0 {
		t.Fatalf("Started callback calls = %d, want 0 before client-loop readiness", calls)
	}
}

type fakeAuthorizedGeneration struct {
	set   *integrity.NativeDescriptorSet
	files AuthorizedRuntimeFiles
	err   error
}

func (g fakeAuthorizedGeneration) NativeDescriptorSet(context.Context) (*integrity.NativeDescriptorSet, error) {
	return g.set, g.err
}

func (g fakeAuthorizedGeneration) AuthorizedRuntimeFiles(context.Context) (AuthorizedRuntimeFiles, error) {
	return g.files, g.err
}

func runtimeTestNativeDescriptorSet(t *testing.T) *integrity.NativeDescriptorSet {
	t.Helper()
	id := strings.Repeat("c", 64)
	path := filepath.Join(t.TempDir(), "libroblox.so")
	if err := os.WriteFile(path, []byte("not mapped by this handoff test"), 0o400); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	record := integrity.FileRecord{
		Path: "lib/x86_64/libroblox.so", Size: 31, SHA256: strings.Repeat("d", 64),
		APKEntry: "lib/x86_64/libroblox.so", APKDigest: strings.Repeat("e", 64), Executable: true,
	}
	generation := &integrity.Generation{
		ID: id, InventorySHA256: id,
		Inventory: integrity.Inventory{Files: []integrity.FileRecord{record}},
		Files:     map[string]*integrity.PinnedFile{record.Path: {File: file, Record: record}},
	}
	set, err := generation.NativeDescriptorSet()
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestAuthorizedNativeDescriptorSetHandoffFailsClosed(t *testing.T) {
	if _, err := authorizedNativeDescriptorSet(context.Background(), nil); !errors.Is(err, ErrAuthorizedGenerationRequired) {
		t.Fatalf("nil authority error = %v", err)
	}
	sentinel := errors.New("active.json is unavailable")
	if _, err := authorizedNativeDescriptorSet(context.Background(), fakeAuthorizedGeneration{err: sentinel}); !errors.Is(err, sentinel) {
		t.Fatalf("generation error = %v", err)
	}
	if _, err := authorizedNativeDescriptorSet(context.Background(), fakeAuthorizedGeneration{}); err == nil || !strings.Contains(err.Error(), "no native descriptor set") {
		t.Fatalf("empty generation error = %v", err)
	}
	want := runtimeTestNativeDescriptorSet(t)
	got, err := authorizedNativeDescriptorSet(context.Background(), fakeAuthorizedGeneration{set: want})
	if err != nil || got != want || got.GenerationID() != want.GenerationID() {
		t.Fatalf("authorized descriptor-set handoff = %#v, %v", got, err)
	}
}

func TestAuthorizedRuntimeFilesHandoffFailsClosedAndBindsGeneration(t *testing.T) {
	id := strings.Repeat("c", 64)
	root := filepath.Join(t.TempDir(), "generations", id)
	want := AuthorizedRuntimeFiles{
		GenerationID: id,
		RootDir:      root,
		AssetsDir:    filepath.Join(root, "assets"),
		BaseAPKPath:  filepath.Join(root, "apk", "base.apk"),
		VersionName:  "2.734.917",
	}
	if _, err := authorizedRuntimeFiles(context.Background(), nil, id); !errors.Is(err, ErrAuthorizedGenerationRequired) {
		t.Fatalf("nil authority error = %v", err)
	}
	if got, err := authorizedRuntimeFiles(context.Background(), fakeAuthorizedGeneration{files: want}, id); err != nil || got != want {
		t.Fatalf("authorized runtime files = %+v, %v", got, err)
	}
	foreign := want
	foreign.GenerationID = strings.Repeat("d", 64)
	if _, err := authorizedRuntimeFiles(context.Background(), fakeAuthorizedGeneration{files: foreign}, id); err == nil {
		t.Fatal("foreign-generation runtime files were accepted")
	}
	escaped := want
	escaped.AssetsDir = filepath.Join(t.TempDir(), "assets")
	if _, err := authorizedRuntimeFiles(context.Background(), fakeAuthorizedGeneration{files: escaped}, id); err == nil {
		t.Fatal("noncanonical asset path was accepted")
	}
}

func TestAssetContentDir(t *testing.T) {
	assets := filepath.Join(t.TempDir(), "generation", "assets")
	if got, want := assetContentDir(assets), filepath.Join(assets, "content"); got != want {
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

func TestPostExitForegroundExportAndExactEvent(t *testing.T) {
	const wantSym = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2SendAppEventOnGameLoaded"
	if postExitForegroundSym != wantSym {
		t.Fatalf("postExitForegroundSym=%q want %q", postExitForegroundSym, wantSym)
	}
	if got, want := postExitForegroundEvent, (appForegroundEvent{
		Protocol: "AppInput",
		Payload:  "",
		Topic:    "Focused",
	}); got != want {
		t.Fatalf("post-exit foreground event = %+v, want %+v", got, want)
	}
	const wantLeaveSym = "Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2LeaveGame"
	if postExitLeaveGameSym != wantLeaveSym {
		t.Fatalf("postExitLeaveGameSym=%q want %q", postExitLeaveGameSym, wantLeaveSym)
	}
}

func TestResolvePostExitForegroundFailsClosed(t *testing.T) {
	lookupErr := errors.New("not exported by fixture")
	for _, tc := range []struct {
		name   string
		lookup func(string) (uintptr, error)
	}{
		{name: "nil lookup"},
		{name: "lookup error", lookup: func(string) (uintptr, error) { return 0, lookupErr }},
		{name: "zero address", lookup: func(string) (uintptr, error) { return 0, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolvePostExitForeground(tc.lookup)
			var unavailable *postExitForegroundUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("error = %v, want typed unavailable diagnostic", err)
			}
			if unavailable.Symbol != postExitForegroundSym {
				t.Fatalf("missing symbol = %q, want %q", unavailable.Symbol, postExitForegroundSym)
			}
		})
	}
	fn, err := resolvePostExitForeground(func(sym string) (uintptr, error) {
		if sym != postExitForegroundSym {
			t.Fatalf("lookup symbol = %q, want %q", sym, postExitForegroundSym)
		}
		return 0x1234, nil
	})
	if err != nil || fn != 0x1234 {
		t.Fatalf("resolved foreground foreground = (%#x, %v), want (0x1234, nil)", fn, err)
	}
}

func TestResolvePostExitLeaveGameFailsClosed(t *testing.T) {
	lookupErr := errors.New("not exported by fixture")
	for _, tc := range []struct {
		name   string
		lookup func(string) (uintptr, error)
	}{
		{name: "nil lookup"},
		{name: "lookup error", lookup: func(string) (uintptr, error) { return 0, lookupErr }},
		{name: "zero address", lookup: func(string) (uintptr, error) { return 0, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolvePostExitLeaveGame(tc.lookup)
			var unavailable *postExitGameRestorationUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("error = %v, want typed unavailable diagnostic", err)
			}
			if unavailable.Symbol != postExitLeaveGameSym {
				t.Fatalf("missing symbol = %q, want %q", unavailable.Symbol, postExitLeaveGameSym)
			}
		})
	}
	fn, err := resolvePostExitLeaveGame(func(sym string) (uintptr, error) {
		if sym != postExitLeaveGameSym {
			t.Fatalf("lookup symbol = %q, want %q", sym, postExitLeaveGameSym)
		}
		return 0x5678, nil
	})
	if err != nil || fn != 0x5678 {
		t.Fatalf("resolved leave-game restoration = (%#x, %v), want (0x5678, nil)", fn, err)
	}
}

func TestPostExitAppResumeRequiresOrderedSequenceAndIsOneShot(t *testing.T) {
	var got []appForegroundEvent
	route := newPostExitAppResume(func(event appForegroundEvent) {
		got = append(got, event)
	}, nil)
	emit := func(kind jni.NativeHelperLifecycleKind, placeID int64) {
		route.observe(jni.NativeHelperLifecycleEvent{Kind: kind, PlaceID: placeID})
	}

	// Startup Home, a stop without a matching start, and a Home load before
	// stop must never manufacture a route.
	emit(jni.NativeHelperGameLoadedEvent, 0)
	emit(jni.NativeHelperExperienceStopped, 0)
	emit(jni.NativeHelperExperienceStarted, 0)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	if len(got) != 0 {
		t.Fatalf("unordered lifecycle requested foreground %#v", got)
	}

	// The current live APK path emits no start/stop/Lua callbacks. Its exact
	// engine sequence is a non-zero experience DataModel followed by Home
	// zero; that sequence must request foreground once.
	emit(jni.NativeHelperGameLoadedEvent, 123)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	if gotWant := []appForegroundEvent{postExitForegroundEvent}; !reflect.DeepEqual(got, gotWant) {
		t.Fatalf("live nonzero-to-zero foreground events = %#v, want %#v", got, gotWant)
	}
	// The pair is strict: a duplicate stop disarms rather than treating a
	// later Home load as the original experience's return.
	emit(jni.NativeHelperExperienceStarted, 0)
	emit(jni.NativeHelperExperienceStopped, 0)
	emit(jni.NativeHelperExperienceStopped, 0)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	if len(got) != 1 {
		t.Fatalf("duplicate stop left a post-exit route armed %#v", got)
	}

	// Lua return is observable but is not an ordering prerequisite. The
	// verified start -> stop -> Home(0) transition produces exactly one
	// foreground event, and duplicate Home callbacks cannot repeat it.
	emit(jni.NativeHelperExperienceStarted, 0)
	emit(jni.NativeHelperLuaAppDidReturn, 0)
	emit(jni.NativeHelperExperienceStopped, 0)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	if gotWant := []appForegroundEvent{postExitForegroundEvent, postExitForegroundEvent}; !reflect.DeepEqual(got, gotWant) {
		t.Fatalf("first post-exit foreground events = %#v, want %#v", got, gotWant)
	}

	// A subsequent real non-zero load is independently armed and fires once.
	emit(jni.NativeHelperGameLoadedEvent, 456)
	emit(jni.NativeHelperGameLoadedEvent, 0)
	if gotWant := []appForegroundEvent{postExitForegroundEvent, postExitForegroundEvent, postExitForegroundEvent}; !reflect.DeepEqual(got, gotWant) {
		t.Fatalf("rearmed post-exit foreground events = %#v, want %#v", got, gotWant)
	}
}

func TestPostExitAppResumeMixedLifecycleUsesLoadedExperience(t *testing.T) {
	var got []appForegroundEvent
	route := newPostExitAppResume(func(event appForegroundEvent) {
		got = append(got, event)
	}, nil)
	for _, event := range []jni.NativeHelperLifecycleEvent{
		{Kind: jni.NativeHelperExperienceStarted},
		{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 99},
		{Kind: jni.NativeHelperExperienceStopped},
		{Kind: jni.NativeHelperLuaAppDidReturn},
		{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0},
	} {
		route.observe(event)
	}
	if gotWant := []appForegroundEvent{postExitForegroundEvent}; !reflect.DeepEqual(got, gotWant) {
		t.Fatalf("mixed lifecycle foreground events = %#v, want %#v", got, gotWant)
	}
}

func TestPostExitAppResumePreservesDestinationAndDoesNotInventLeave(t *testing.T) {
	var got []appForegroundEvent
	leaves := 0
	resume := newPostExitAppResume(func(event appForegroundEvent) {
		got = append(got, event)
	}, func() { leaves++ })
	resume.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 10})
	resume.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
	resume.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
	// Current APK xk/a -> xk/c -> fi/e.z passes protocol, payload, topic.
	// A foreground event has no Navigations/Destination or place payload.
	want := []appForegroundEvent{{Protocol: "AppInput", Payload: "", Topic: "Focused"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("post-exit events = %+v, want only %+v", got, want)
	}
	if leaves != 0 {
		t.Fatalf("invoked LeaveGame %d times without gameDidLeave callback", leaves)
	}
}

func TestPostExitAppLifecycleTraceClassifiesIDsAndGate(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	route := newPostExitAppResume(func(appForegroundEvent) {}, nil)
	route.observe(jni.NativeHelperLifecycleEvent{
		Kind:    jni.NativeHelperGameLoadedEvent,
		PlaceID: 8675309,
	})
	route.observe(jni.NativeHelperLifecycleEvent{
		Kind:    jni.NativeHelperGameLoadedEvent,
		PlaceID: 0,
	})
	logText := out.String()
	for _, want := range []string{
		"event=game_loaded id_class=nonzero gate=armed action=none",
		"event=game_loaded id_class=zero gate=consumed action=resume_app",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("lifecycle trace missing %q: %s", want, logText)
		}
	}
	if strings.Contains(logText, "8675309") {
		t.Fatalf("lifecycle trace exposed a place identifier: %s", logText)
	}
}

func TestPostExitAppResumeCloseSuppressesLateCallbacks(t *testing.T) {
	var calls atomic.Int64
	route := newPostExitAppResume(func(appForegroundEvent) { calls.Add(1) }, func() { calls.Add(1) })
	route.close()
	for _, event := range []jni.NativeHelperLifecycleEvent{
		{Kind: jni.NativeHelperExperienceStarted},
		{Kind: jni.NativeHelperExperienceStopped},
		{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0},
	} {
		route.observe(event)
	}
	route.gameDidLeave()
	if calls.Load() != 0 {
		t.Fatalf("closed route invoked %d times", calls.Load())
	}
}

func TestPostExitAppResumeCloseWaitsForInFlightForeground(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	route := newPostExitAppResume(func(appForegroundEvent) {
		close(entered)
		<-release
	}, nil)
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperExperienceStarted})
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperExperienceStopped})
	go func() {
		route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
		close(returned)
	}()
	<-entered
	closed := make(chan struct{})
	go func() {
		route.close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("route close returned while a foreground call was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("foreground callback did not return")
	}
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("route close did not wait for then release in-flight foreground")
	}
}

func TestPostExitGameDidLeaveConsumesSynchronouslyFlushesAfterRouteAndIsNonRecursive(t *testing.T) {
	var events []string
	var route *postExitAppResume
	route = newPostExitAppResume(func(appForegroundEvent) {
		events = append(events, "route-enter")
		route.gameDidLeave()
		events = append(events, "route-return")
	}, func() {
		events = append(events, "leave-game")
		// The native export may re-enter the exact Java callback. Consumption
		// before invocation must make that recursion a no-op.
		route.gameDidLeave()
	})
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 10})
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
	route.gameDidLeave()
	if want := []string{"route-enter", "route-return", "leave-game"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("deferred restoration order = %v, want %v", events, want)
	}

	// A new real experience rearms both the route and its leave cleanup.
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 20})
	route.gameDidLeave()
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
	if want := []string{"route-enter", "route-return", "leave-game", "route-enter", "route-return", "leave-game"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("rearmed restoration order = %v, want %v", events, want)
	}
}

func TestPostExitGameDidLeaveCloseWaitsForInFlightRestoration(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	var route *postExitAppResume
	route = newPostExitAppResume(func(appForegroundEvent) {
		route.gameDidLeave()
	}, func() {
		close(entered)
		<-release
	})
	route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 1})
	go func() {
		route.observe(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
		close(returned)
	}()
	<-entered
	closed := make(chan struct{})
	go func() {
		route.close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("route close returned while leave-game restoration was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("leave-game restoration did not return")
	}
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("route close did not wait for in-flight leave-game restoration")
	}
}

func TestBindPostExitAppObserversRegistersAndRemovesBothSources(t *testing.T) {
	var events []string
	var lifecycle jni.NativeHelperLifecycleListener
	var leave jni.NativeGLGameDidLeaveListener
	var calls atomic.Int64
	route := newPostExitAppResume(func(appForegroundEvent) { calls.Add(1) }, func() { calls.Add(1) })
	closeObservers := bindPostExitAppObservers(route,
		func(fn jni.NativeHelperLifecycleListener) func() {
			events = append(events, "subscribe-lifecycle")
			lifecycle = fn
			return func() { events = append(events, "unsubscribe-lifecycle") }
		},
		func(fn jni.NativeGLGameDidLeaveListener) func() {
			events = append(events, "subscribe-leave")
			leave = fn
			return func() { events = append(events, "unsubscribe-leave") }
		})
	if lifecycle == nil || leave == nil {
		t.Fatal("both session observers must be registered")
	}
	lifecycle(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 1})
	leave()
	if calls.Load() != 0 {
		t.Fatalf("leave callback invoked %d operations before the foreground returned", calls.Load())
	}
	lifecycle(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
	if calls.Load() != 2 {
		t.Fatalf("active observers invoked %d operations, want route then leave-game", calls.Load())
	}
	closeObservers()
	closeObservers()
	if want := []string{"subscribe-lifecycle", "subscribe-leave", "unsubscribe-leave", "unsubscribe-lifecycle"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("observer lifetime order = %v, want %v", events, want)
	}
	// Dispatcher snapshots copied before cancellation remain harmless because
	// the session owner closes the shared controller before module teardown.
	lifecycle(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 2})
	leave()
	if calls.Load() != 2 {
		t.Fatalf("stale observer snapshots invoked %d operations after close", calls.Load())
	}
}

func TestBindPostExitAppObserversUnsubscribesBeforeInflightWait(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	unsubscribedLeave := make(chan struct{})
	unsubscribedLifecycle := make(chan struct{})
	var lifecycle jni.NativeHelperLifecycleListener
	var leave jni.NativeGLGameDidLeaveListener
	route := newPostExitAppResume(func(appForegroundEvent) {}, func() {
		close(entered)
		<-release
	})
	closeObservers := bindPostExitAppObservers(route,
		func(fn jni.NativeHelperLifecycleListener) func() {
			lifecycle = fn
			return func() { close(unsubscribedLifecycle) }
		},
		func(fn jni.NativeGLGameDidLeaveListener) func() {
			leave = fn
			return func() { close(unsubscribedLeave) }
		})
	lifecycle(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 1})
	leave()
	go lifecycle(jni.NativeHelperLifecycleEvent{Kind: jni.NativeHelperGameLoadedEvent, PlaceID: 0})
	<-entered
	closed := make(chan struct{})
	go func() {
		closeObservers()
		close(closed)
	}()
	for name, ch := range map[string]<-chan struct{}{
		"leave":     unsubscribedLeave,
		"lifecycle": unsubscribedLifecycle,
	} {
		select {
		case <-ch:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s observer was not removed before the in-flight wait", name)
		}
	}
	select {
	case <-closed:
		t.Fatal("observer teardown returned while leave-game restoration was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("observer teardown did not finish after in-flight restoration returned")
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
	var lifecycleCloses atomic.Int64
	s := &gameActivitySession{call: func(name, sig string, extra ...uintptr) {
		calls = append(calls, name+sig)
		if name == "onWindowFocusChangedNative" {
			if len(extra) != 1 || extra[0] != 0 {
				t.Fatalf("focus-loss args = %v, want [0]", extra)
			}
		}
	}, closeLifecycle: func() {
		lifecycleCloses.Add(1)
		calls = append(calls, "closeLifecycle")
	}}
	s.shutdown("test-wm-close")
	s.shutdown("test-second-close")
	if lifecycleCloses.Load() != 1 {
		t.Fatalf("lifecycle close calls = %d, want 1", lifecycleCloses.Load())
	}
	want := []string{
		"closeLifecycle",
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

// TestSurfaceResizeDebouncerDeliversOnlyTheSettledDragSize models a title-bar
// resize drag. The X11 window may receive every intermediate ConfigureNotify,
// but the Android/GameActivity lifecycle must receive only the final stable
// geometry: repeating the full V2 update for every 10ms drag rectangle
// crashes the current official client. This generic unit deliberately has no
// Roblox floor; the production floor is covered separately below.
func TestSurfaceResizeDebouncerDeliversOnlyTheSettledDragSize(t *testing.T) {
	s := newSeededResize(1280, 720)
	var d surfaceResizeDebouncer
	for _, size := range [][2]int{
		{1200, 675}, {1120, 630}, {1040, 585}, {960, 540},
		{880, 495}, {800, 450}, {720, 405}, {640, 360},
	} {
		if !d.queue(size[0], size[1]) {
			t.Fatalf("queue(%dx%d) rejected a valid drag size", size[0], size[1])
		}
	}
	if w, h, ok := d.take(); !ok || w != 640 || h != 360 {
		t.Fatalf("settled drag geometry = %dx%d ok=%t, want 640x360 true", w, h, ok)
	} else {
		s.observe(w, h)
	}
	if _, _, ok := d.take(); ok {
		t.Fatal("empty debouncer produced a second surface update")
	}
	want := []string{
		"buffers 640x360",
		"display 640x360",
		"cmd3",
		"cmd4",
		"v2surface 640x360",
		"cmd5",
		"onContentRectChangedNative 0,0,640,360",
		"onWindowInsetsChangedNative",
	}
	if got := recordingSink(s).events; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("settled drag events = %v, want %v", got, want)
	}
}

func TestClampRobloxSurfaceSize(t *testing.T) {
	tests := []struct {
		inW, inH, wantW, wantH int
	}{
		{640, 360, x11.RobloxMinimumWidth, x11.RobloxMinimumHeight},
		{1280, 720, 1280, 720},
		{1600, 900, 1600, 900},
	}
	for _, tt := range tests {
		gotW, gotH := clampRobloxSurfaceSize(tt.inW, tt.inH)
		if gotW != tt.wantW || gotH != tt.wantH {
			t.Fatalf("clampRobloxSurfaceSize(%d,%d) = %dx%d, want %dx%d",
				tt.inW, tt.inH, gotW, gotH, tt.wantW, tt.wantH)
		}
	}
}

// TestRobloxSurfaceResizeMinimumNeverReachesNativeCallbacks is the final
// defense if a window manager or extension configures the X11 client directly.
// A sub-minimum geometry must not touch ANativeWindow, DisplayMetrics, or the
// official V2/content-rect callbacks. A valid size still propagates normally.
func TestRobloxSurfaceResizeMinimumNeverReachesNativeCallbacks(t *testing.T) {
	s := &surfaceResize{
		sink:     &recordingResizeSink{},
		seeded:   true,
		width:    x11.RobloxMinimumWidth,
		height:   x11.RobloxMinimumHeight,
		minWidth: x11.RobloxMinimumWidth, minHeight: x11.RobloxMinimumHeight,
	}
	s.observe(640, 360)
	if got := recordingSink(s).events; len(got) != 0 {
		t.Fatalf("sub-minimum resize reached native callbacks: %v", got)
	}
	if s.width != x11.RobloxMinimumWidth || s.height != x11.RobloxMinimumHeight {
		t.Fatalf("sub-minimum resize replaced accepted geometry with %dx%d", s.width, s.height)
	}

	s.observe(1600, 900)
	got := recordingSink(s).events
	if len(got) == 0 || got[0] != "buffers 1600x900" {
		t.Fatalf("valid resize did not reach native callbacks: %v", got)
	}
}

// TestRobloxSurfaceResizeStormSettlesAtAValidGeometry proves a normal
// interactive drag that remains at or above the 1280x720 minimum still
// delivers its final size through the Android/V2 path.
func TestRobloxSurfaceResizeStormSettlesAtAValidGeometry(t *testing.T) {
	s := &surfaceResize{
		sink:     &recordingResizeSink{},
		seeded:   true,
		width:    x11.RobloxMinimumWidth,
		height:   x11.RobloxMinimumHeight,
		minWidth: x11.RobloxMinimumWidth, minHeight: x11.RobloxMinimumHeight,
	}
	d := surfaceResizeDebouncer{minWidth: x11.RobloxMinimumWidth, minHeight: x11.RobloxMinimumHeight}
	for _, size := range [][2]int{{1280, 720}, {1366, 768}, {1440, 810}, {1600, 900}} {
		if !d.queue(size[0], size[1]) {
			t.Fatalf("queue(%dx%d) rejected a valid Roblox drag size", size[0], size[1])
		}
	}
	w, h, ok := d.take()
	if !ok || w != 1600 || h != 900 {
		t.Fatalf("settled valid drag geometry = %dx%d ok=%t, want 1600x900 true", w, h, ok)
	}
	s.observe(w, h)
	if got := recordingSink(s).events; len(got) == 0 || got[0] != "buffers 1600x900" {
		t.Fatalf("valid storm did not reach native callbacks: %v", got)
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

type pipeCommandWriter struct{ fd int }

func (w pipeCommandWriter) WriteCommand(cmd byte) error {
	_, err := syscall.Write(w.fd, []byte{cmd})
	return err
}

// TestSurfaceResizePipeDeliversCommandBytes runs the production sink through
// its owned command-writer interface: one delta writes exactly command bytes
// 3,4,5 once, an unchanged size writes nothing, and a second delta writes them
// again. internal/android separately pins descriptor validation.
func TestSurfaceResizePipeDeliversCommandBytes(t *testing.T) {
	var pipeFDs [2]int
	if err := syscall.Pipe2(pipeFDs[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	rfd, wfd := os.NewFile(uintptr(pipeFDs[0]), "cmd-r"), os.NewFile(uintptr(pipeFDs[1]), "cmd-w")
	t.Cleanup(func() { rfd.Close(); wfd.Close() })

	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	aw := android.NewWindow(1280, 720, nil)
	s := &surfaceResize{
		sink:   &engineResizeSink{commands: pipeCommandWriter{fd: pipeFDs[1]}, vm: vm, aw: aw},
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

// TestSurfaceResizePublishesPointerClampViewport runs the production sink and
// pins that a surface delta republishes the captured-logical clamp alongside
// DisplayMetrics, so a long-held grab parks at the live edge after resizes.
func TestSurfaceResizePublishesPointerClampViewport(t *testing.T) {
	var pipeFDs [2]int
	if err := syscall.Pipe2(pipeFDs[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(pipeFDs[0]); _ = syscall.Close(pipeFDs[1]) })
	t.Cleanup(func() { jni.SetPointerClampViewport(1280, 720) })

	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	aw := android.NewWindow(1280, 720, nil)
	s := &surfaceResize{
		sink:   &engineResizeSink{commands: pipeCommandWriter{fd: pipeFDs[1]}, vm: vm, aw: aw},
		seeded: true, width: 1280, height: 720,
	}
	s.observe(800, 600)
	if w, h := jni.PointerClampViewport(); w != 800 || h != 600 {
		t.Fatalf("clamp viewport = %dx%d, want 800x600", w, h)
	}
	s.observe(2560, 1440)
	if w, h := jni.PointerClampViewport(); w != 2560 || h != 1440 {
		t.Fatalf("clamp viewport = %dx%d, want 2560x1440", w, h)
	}
}

func TestPostAndroidAppCmdUsesOwnedWriter(t *testing.T) {
	var pipeFDs [2]int
	if err := syscall.Pipe2(pipeFDs[:], syscall.O_CLOEXEC|syscall.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(pipeFDs[0]); _ = syscall.Close(pipeFDs[1]) })
	postAndroidAppCmd(pipeCommandWriter{fd: pipeFDs[1]}, appCmdInitWindow)
	b := make([]byte, 1)
	n, err := syscall.Read(pipeFDs[0], b)
	if err != nil || n != 1 || b[0] != appCmdInitWindow {
		t.Fatalf("pipe read n=%d err=%v b=%v", n, err, b[:n])
	}
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

type launchLoopCounters struct {
	publishes   atomic.Int32
	diagnostics atomic.Int32
	pumps       atomic.Int32
	refreshes   atomic.Int32
}

// newTestLaunchLoopSources builds a loop with no event sources and no-ops for
// every callback. Tests override only the fields their wake policy needs.
func newTestLaunchLoopSources(ctx context.Context) launchLoopSources {
	return launchLoopSources{
		ctx:                ctx,
		textOverlayWake:    func() <-chan time.Time { return nil },
		resizeSettle:       func() <-chan time.Time { return nil },
		pump:               func() error { return nil },
		refreshFocusedText: func(time.Time) {},
		flushResize:        func() {},
		publishRefresh:     func() {},
	}
}

func waitForLoopCounter(t *testing.T, counter *atomic.Int32, want int32, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if counter.Load() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s = %d, want at least %d", what, counter.Load(), want)
}

func TestLaunchLoopCadenceConstants(t *testing.T) {
	if launchDiagnosticPeriod != 2*time.Second {
		t.Fatalf("diagnostic period = %v, want the historical 2s cadence", launchDiagnosticPeriod)
	}
	if refreshFallbackPeriod < 30*time.Second || refreshFallbackPeriod > time.Minute {
		t.Fatalf("refresh fallback = %v, want within [30s, 60s]", refreshFallbackPeriod)
	}
}

// TestLaunchLoopRefreshWakeIsEventDriven pins H2: with diagnostics off there is
// no standing 2s republish wake. One refresh generation change wakes exactly
// one republish, an unchanged generation does not re-query, and a second
// change republishes again.
func TestLaunchLoopRefreshWakeIsEventDriven(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var counters launchLoopCounters
	refreshReady := make(chan struct{}, 1)
	publication := displayRefreshPublication{version: 1, current: 60, supported: []float32{60}}
	var mu sync.Mutex
	version := uint64(1)
	currentRate, supportedRates := float32(165), []float32{60, 165}
	queries, published, lastVersion := 0, 0, uint64(0)
	src := newTestLaunchLoopSources(ctx)
	src.refreshReady = refreshReady
	src.publishRefresh = func() {
		mu.Lock()
		currentVersion := version
		mu.Unlock()
		if err := publication.update(currentVersion, func() (float32, []float32) {
			mu.Lock()
			defer mu.Unlock()
			queries++
			lastVersion = currentVersion
			return currentRate, supportedRates
		}, func(float32, []float32) error {
			mu.Lock()
			published++
			mu.Unlock()
			counters.publishes.Add(1)
			return nil
		}); err != nil {
			t.Errorf("publication update: %v", err)
		}
	}
	shutdowns := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- runLaunchLoop(src, func(reason string) { shutdowns <- reason }, time.Hour, launchDiagnosticPeriod)
	}()

	// A nil diagnostics callback disables the 2s diagnostic ticker even when
	// the production period is passed, so an idle loop must stay asleep.
	time.Sleep(2200 * time.Millisecond)
	mu.Lock()
	gotQueries, gotPublished := queries, published
	mu.Unlock()
	if got := counters.publishes.Load(); got != 0 || gotQueries != 0 || gotPublished != 0 {
		t.Fatalf("idle loop woke without a refresh change: republishes=%d queries=%d published=%d", got, gotQueries, gotPublished)
	}

	// One generation change wakes exactly one republish.
	mu.Lock()
	version = 2
	mu.Unlock()
	refreshReady <- struct{}{}
	waitForLoopCounter(t, &counters.publishes, 1, "republishes after a refresh wake")
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	gotQueries, gotPublished, gotVersion := queries, published, lastVersion
	mu.Unlock()
	if got := counters.publishes.Load(); got != 1 || gotQueries != 1 || gotPublished != 1 || gotVersion != 2 {
		t.Fatalf("refresh change: republishes=%d queries=%d published=%d version=%d, want 1/1/1/2",
			got, gotQueries, gotPublished, gotVersion)
	}

	// An unchanged generation must not re-query the X server or republish.
	refreshReady <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	gotQueries, gotPublished = queries, published
	mu.Unlock()
	if got := counters.publishes.Load(); got != 1 || gotQueries != 1 || gotPublished != 1 {
		t.Fatalf("unchanged generation: republishes=%d queries=%d published=%d, want 1/1/1", got, gotQueries, gotPublished)
	}

	// A second change republishes again.
	mu.Lock()
	version = 3
	currentRate, supportedRates = 144, []float32{60, 144}
	mu.Unlock()
	refreshReady <- struct{}{}
	waitForLoopCounter(t, &counters.publishes, 2, "republishes after a second refresh wake")
	mu.Lock()
	gotQueries, gotPublished = queries, published
	mu.Unlock()
	if gotQueries != 2 || gotPublished != 2 {
		t.Fatalf("second change queries=%d published=%d, want 2/2", gotQueries, gotPublished)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop exit = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("launch loop did not stop on context cancellation")
	}
	if reason := <-shutdowns; reason != "context-cancelled" {
		t.Fatalf("shutdown reason = %q, want context-cancelled", reason)
	}
}

// TestLaunchLoopDiagnosticsKeepOptInCadence pins that an enabled diagnostic
// callback still runs on its own ticker (the production period is pinned at 2s
// above) while refresh republication stays event-driven.
func TestLaunchLoopDiagnosticsKeepOptInCadence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var counters launchLoopCounters
	src := newTestLaunchLoopSources(ctx)
	src.publishRefresh = func() { counters.publishes.Add(1) }
	src.diagnostics = func() { counters.diagnostics.Add(1) }
	done := make(chan error, 1)
	go func() {
		done <- runLaunchLoop(src, func(string) {}, time.Hour, 20*time.Millisecond)
	}()
	waitForLoopCounter(t, &counters.diagnostics, 3, "diagnostic ticks")
	if got := counters.publishes.Load(); got != 0 {
		t.Fatalf("diagnostic ticks republished %d times; refresh must stay event-driven", got)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop exit = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("launch loop did not stop on context cancellation")
	}
}

// TestLaunchLoopDiagnosticsUseInjectedClock ties the aggregate logger to the
// existing diagnostics turn without sleeping for a wall-clock ticker. The
// production source leaves diagnosticsTick nil, so this is a test-only clock
// seam rather than a new launch-loop wake source.
func TestLaunchLoopDiagnosticsUseInjectedClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 2)
	logged := make(chan struct{}, 3)
	diagnostics := stutterInputDrainDiagnostics{
		snapshot: func(reset bool) x11.InputDrainStats {
			if !reset {
				t.Fatal("diagnostic interval did not reset aggregate counters")
			}
			return x11.InputDrainStats{DrainCalls: 1, Events: 1}
		},
		log: func(string, ...any) { logged <- struct{}{} },
	}
	src := newTestLaunchLoopSources(ctx)
	src.diagnostics = func() { diagnostics.logInterval(true) }
	src.diagnosticsTick = ticks
	done := make(chan error, 1)
	go func() {
		done <- runLaunchLoop(src, func(string) {}, time.Hour, time.Hour)
	}()
	for i := 0; i < 2; i++ {
		ticks <- time.Unix(int64(100+i), 0)
		select {
		case <-logged:
		case <-time.After(time.Second):
			t.Fatalf("diagnostic log %d did not follow injected tick", i+1)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop exit = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("launch loop did not stop on context cancellation")
	}
}

func TestLaunchLoopJNIStringDiagnosticsUseInjectedClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 2)
	logged := make(chan struct{}, 2)
	var snapshots atomic.Int32
	var nonReset atomic.Int32
	diagnostics := stutterJNIStringDiagnostics{
		snapshot: func(reset bool) jni.JNIStringDiagnostics {
			if !reset {
				nonReset.Add(1)
			}
			snapshots.Add(1)
			return stringDiagnosticFixture(3_000)
		},
		log: func(string, ...any) { logged <- struct{}{} },
	}
	diagnostics.begin(true)
	src := newTestLaunchLoopSources(ctx)
	src.diagnostics = func() { diagnostics.logInterval(true) }
	src.diagnosticsTick = ticks
	done := make(chan error, 1)
	go func() {
		done <- runLaunchLoop(src, func(string) {}, time.Hour, time.Hour)
	}()
	for i := 0; i < 2; i++ {
		ticks <- time.Unix(int64(200+i), 0)
		select {
		case <-logged:
		case <-time.After(time.Second):
			t.Fatalf("JNI string diagnostic log %d did not follow injected tick", i+1)
		}
	}
	if got := snapshots.Load(); got != 3 || nonReset.Load() != 0 {
		t.Fatalf("JNI string snapshots/reset violations = %d/%d, want 3/0", got, nonReset.Load())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loop exit = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("launch loop did not stop on context cancellation")
	}
}

// TestLaunchLoopShutdownPaths pins the existing close/error semantics and that
// nil input/refresh channels are selectable (they simply never fire).
func TestLaunchLoopShutdownPaths(t *testing.T) {
	t.Run("context-cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		src := newTestLaunchLoopSources(ctx)
		shutdowns := make(chan string, 1)
		err := runLaunchLoop(src, func(reason string) { shutdowns <- reason }, time.Hour, launchDiagnosticPeriod)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("exit = %v, want context.Canceled", err)
		}
		if reason := <-shutdowns; reason != "context-cancelled" {
			t.Fatalf("shutdown reason = %q, want context-cancelled", reason)
		}
	})
	t.Run("wm-delete-window", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var counters launchLoopCounters
		src := newTestLaunchLoopSources(ctx)
		inputReady := make(chan struct{}, 1)
		src.inputReady = inputReady
		src.pump = func() error { counters.pumps.Add(1); return x11.ErrClosed }
		src.refreshFocusedText = func(time.Time) { counters.refreshes.Add(1) }
		shutdowns := make(chan string, 1)
		inputReady <- struct{}{}
		if err := runLaunchLoop(src, func(reason string) { shutdowns <- reason }, time.Hour, launchDiagnosticPeriod); err != nil {
			t.Fatalf("closed-window exit = %v, want nil", err)
		}
		if reason := <-shutdowns; reason != "wm-delete-window" {
			t.Fatalf("shutdown reason = %q, want wm-delete-window", reason)
		}
		if counters.pumps.Load() != 1 || counters.refreshes.Load() != 0 {
			t.Fatalf("closed pump: pumps=%d refreshes=%d, want 1/0", counters.pumps.Load(), counters.refreshes.Load())
		}
	})
	t.Run("x11-pump-error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var counters launchLoopCounters
		src := newTestLaunchLoopSources(ctx)
		inputReady := make(chan struct{}, 1)
		src.inputReady = inputReady
		sentinel := errors.New("pump failed")
		src.pump = func() error { counters.pumps.Add(1); return sentinel }
		shutdowns := make(chan string, 1)
		inputReady <- struct{}{}
		err := runLaunchLoop(src, func(reason string) { shutdowns <- reason }, time.Hour, launchDiagnosticPeriod)
		if !errors.Is(err, sentinel) {
			t.Fatalf("pump-failure exit = %v, want %v", err, sentinel)
		}
		if reason := <-shutdowns; reason != "x11-pump-error" {
			t.Fatalf("shutdown reason = %q, want x11-pump-error", reason)
		}
		if counters.pumps.Load() != 1 {
			t.Fatalf("pump calls = %d, want 1", counters.pumps.Load())
		}
	})
	t.Run("input-wake", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var counters launchLoopCounters
		src := newTestLaunchLoopSources(ctx)
		inputReady := make(chan struct{}, 1)
		src.inputReady = inputReady
		src.pump = func() error { counters.pumps.Add(1); return nil }
		src.refreshFocusedText = func(time.Time) { counters.refreshes.Add(1) }
		done := make(chan error, 1)
		go func() {
			done <- runLaunchLoop(src, func(string) {}, time.Hour, launchDiagnosticPeriod)
		}()
		inputReady <- struct{}{}
		waitForLoopCounter(t, &counters.pumps, 1, "pump calls after an input wake")
		waitForLoopCounter(t, &counters.refreshes, 1, "focused-text refreshes after an input wake")
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("launch loop did not stop on context cancellation")
		}
	})
}
