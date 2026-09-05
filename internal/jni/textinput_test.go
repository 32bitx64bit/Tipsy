// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

// TestStringGetBytesUTF8ForRobloxTextSync pins the standard-Java conversion
// used internally by Roblox's syncTextboxTextAndCursorPosition2 wrapper. The
// returned object is a genuine byte[] carrying standard UTF-8, including a
// supplementary rune; neither the source nor result is logged.
func TestStringGetBytesUTF8ForRobloxTextSync(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	const secret = "text-sync-A😀é"
	vm.mu.Lock()
	source := vm.newStringLocked(secret)
	charset := vm.newStringLocked("UTF-8")
	vm.mu.Unlock()
	got, handled := vm.dispatch(idToJobject(source.id), "java/lang/String",
		"getBytes", "(Ljava/lang/String;)[B", testPackObjectArg(charset.id))
	if !handled || uintptr(got) == 0 {
		t.Fatalf("String.getBytes UTF-8 = handled=%v result=%#x, want byte[]", handled, uintptr(got))
	}
	array := vm.get(jobjectToID(uintptr(got)))
	if array == nil || array.class == nil || array.class.name != "[B" || array.arrKind != int('B') {
		t.Fatalf("result = %#v, want genuine [B", array)
	}
	if want := []byte(secret); !bytes.Equal(array.bytes, want) {
		t.Fatalf("UTF-8 bytes differ: gotLen=%d wantLen=%d", len(array.bytes), len(want))
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("String.getBytes leaked text content: %s", buf.String())
	}
	if !isImplementedMethod("getBytes", "(Ljava/lang/String;)[B") {
		t.Fatal("String.getBytes UTF-8 missing from implementedMethods")
	}
}

func TestStringGetBytesUTF8EmptyAndUnsupported(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	empty := vm.newStringLocked("")
	utf8Name := vm.newStringLocked("utf8")
	unsupported := vm.newStringLocked("UTF-16")
	vm.mu.Unlock()

	got, handled := vm.dispatch(idToJobject(empty.id), "java/lang/String",
		"getBytes", "(Ljava/lang/String;)[B", testPackObjectArg(utf8Name.id))
	if !handled || uintptr(got) == 0 {
		t.Fatal("empty UTF-8 String.getBytes did not return byte[]")
	}
	if array := vm.get(jobjectToID(uintptr(got))); array == nil || len(array.bytes) != 0 {
		t.Fatalf("empty UTF-8 result = %#v, want zero-length byte[]", array)
	}
	got, handled = vm.dispatch(idToJobject(empty.id), "java/lang/String",
		"getBytes", "(Ljava/lang/String;)[B", testPackObjectArg(unsupported.id))
	if !handled || uintptr(got) != 0 {
		t.Fatalf("unsupported charset = handled=%v result=%#x, want handled null", handled, uintptr(got))
	}
	if got, handled = vm.dispatch(jnull(), "java/lang/String",
		"getBytes", "(Ljava/lang/String;)[B", testPackObjectArg(utf8Name.id)); !handled || uintptr(got) != 0 {
		t.Fatalf("nil receiver = handled=%v result=%#x, want handled null", handled, uintptr(got))
	}
}

// keyboardTestObjects builds the two payload objects for a showKeyboard
// call: a [B initial-bytes object and one NativeTextBoxInfo object.
// Contents are fixed non-secret markers; the privacy test below uses its
// own secret payload.
func keyboardTestObjects(t *testing.T, vm *VM, initBytes []byte, boxes int) (initID, boxesID int64) {
	t.Helper()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	initCls := vm.ensureClassLocked("[B")
	initObj := vm.newObjectLocked(initCls)
	initObj.bytes = append([]byte(nil), initBytes...)
	if boxes <= 0 {
		return initObj.id, 0
	}
	boxCls := vm.ensureClassLocked("com/roblox/engine/jni/model/NativeTextBoxInfo")
	box := vm.newObjectLocked(boxCls)
	return initObj.id, box.id
}

func wireRecordingRbxTextTarget(t *testing.T, vm *VM) {
	t.Helper()
	resetTextInputConnectionForTest()
	env := vm.Env()
	if !SetRobloxTextInputTarget(env,
		env.FindClass("com/roblox/engine/jni/NativeGLInterface"),
		testRbxRecordPassFn(), testRbxRecordReturnFn(), testRbxRecordSyncFn(), 0, nil) {
		t.Fatal("recording RbxKeyboard text target not ready")
	}
	t.Cleanup(resetTextInputConnectionForTest)
	t.Cleanup(ClearRobloxTextInputTarget)
}

// TestRbxTextOverlaySnapshotCapturesAPKConfig drives the exact
// NativeTextBoxInfo constructor plus a genuine showKeyboard session. The
// render-facing snapshot is transient, complete, uses UTF-16 selection, and
// is wiped on hide.
func TestRbxTextOverlaySnapshotCapturesAPKConfig(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)
	resetTextInputConnectionForTest()
	t.Cleanup(resetTextInputConnectionForTest)
	vm.SetDisplaySize(1280, 720)

	const sentinel = "safe😀"
	vm.mu.Lock()
	info := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
	vm.mu.Unlock()
	infoID := info.id
	seedNativeTextBoxInfoConstructor(vm, idToJobject(infoID), nativeTextBoxInfoSig,
		testPackNativeTextBoxInfoArgs(
			12.5, 24.5, 320, 44, 18, false,
			2, 1, 0xffaabbcc, 4, 5, 2, true, false, true))
	initID, _ := keyboardTestObjects(t, vm, []byte(sentinel), 0)
	if _, handled := vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(77, 1, initID, infoID)); !handled {
		t.Fatal("showKeyboard not handled")
	}
	s := CurrentRbxTextOverlay()
	if !s.Active || !s.Configured || s.Text != sentinel || s.Version == 0 {
		t.Fatalf("overlay state = active=%v configured=%v len=%d version=%d",
			s.Active, s.Configured, len([]rune(s.Text)), s.Version)
	}
	if s.SelectionStartUTF16 != 6 || s.SelectionEndUTF16 != 6 {
		t.Fatalf("UTF-16 selection = %d/%d, want 6/6",
			s.SelectionStartUTF16, s.SelectionEndUTF16)
	}
	if s.Density != 1 || s.ViewportWidthPx != 1280 || s.ViewportHeightPx != 720 {
		t.Fatalf("display contract = density=%v viewport=%dx%d", s.Density, s.ViewportWidthPx, s.ViewportHeightPx)
	}
	if s.X != 12 || s.Y != 24 || s.Width != 320 || s.Height != 44 || s.FontSize != 18 {
		t.Fatalf("geometry = %v,%v %vx%v fontSize=%v", s.X, s.Y, s.Width, s.Height, s.FontSize)
	}
	if s.TextColor != 0xffaabbcc || s.Font != 4 || s.TextInputType != 5 ||
		s.XAlignment != 2 || s.YAlignment != 1 || s.ReturnKeyType != 2 {
		t.Fatalf("style = color=%#x font=%d input=%d align=%d/%d return=%d",
			s.TextColor, s.Font, s.TextInputType, s.XAlignment, s.YAlignment, s.ReturnKeyType)
	}
	if !s.Editable || s.Multiline || s.TextWrapped || !s.ManualFocusRelease {
		t.Fatalf("flags = editable=%v multiline=%v wrapped=%v manual=%v",
			s.Editable, s.Multiline, s.TextWrapped, s.ManualFocusRelease)
	}
	if !s.CursorVisible || !s.IncludeFontPadding || s.PaddingLeftPx != 0 || s.PaddingTopPx != 0 ||
		s.PaddingRightPx != 0 || s.PaddingBottomPx != 0 {
		t.Fatalf("view defaults = cursor=%v includeFontPadding=%v padding=%d/%d/%d/%d",
			s.CursorVisible, s.IncludeFontPadding,
			s.PaddingLeftPx, s.PaddingTopPx, s.PaddingRightPx, s.PaddingBottomPx)
	}
	out := buf.String()
	if !strings.Contains(out, "textColorARGB=0xffaabbcc") || !strings.Contains(out, "textAlpha=255") {
		t.Fatalf("show diagnostic lost raw Android color: %s", out)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("show diagnostic leaked sentinel content: %s", out)
	}

	beforeHide := RbxTextOverlayVersion()
	vm.dispatch(jnull(), nativeGLClass, "hideKeyboard", hideKeyboardSig, nil)
	hidden := CurrentRbxTextOverlay()
	if hidden.Active || hidden.Text != "" || hidden.Version <= beforeHide {
		t.Fatalf("hidden overlay = active=%v textLen=%d version=%d before=%d",
			hidden.Active, len(hidden.Text), hidden.Version, beforeHide)
	}
}

func TestRbxTextOverlayGeometryMatchesAndroidAcrossViewportChanges(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetTextInputConnectionForTest()
	t.Cleanup(resetTextInputConnectionForTest)
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{name: "initial-1280x720", width: 1280, height: 720},
		{name: "resized-1600x900", width: 1600, height: 900},
		{name: "fullscreen-2560x1440", width: 2560, height: 1440},
	} {
		t.Run(size.name, func(t *testing.T) {
			vm.SetDisplaySize(size.width, size.height)
			vm.mu.Lock()
			info := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
			vm.mu.Unlock()
			seedNativeTextBoxInfoConstructor(vm, idToJobject(info.id), nativeTextBoxInfoSig,
				testPackNativeTextBoxInfoArgs(
					10.875, 20.625, 300.99, 31.99, 17.5, false,
					0, 0, 0xffffffff, 0, 0, 0, false, false, true))
			initID, _ := keyboardTestObjects(t, vm, nil, 0)
			vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
				testPackKeyboardArgs(91, 1, initID, info.id))
			s := CurrentRbxTextOverlay()
			if s.ViewportWidthPx != int32(size.width) || s.ViewportHeightPx != int32(size.height) ||
				s.Density != 1 || s.X != 10 || s.Y != 20 || s.Width != 300 || s.Height != 31 {
				t.Fatalf("snapshot viewport=%dx%d density=%v geometry=%v,%v %vx%v",
					s.ViewportWidthPx, s.ViewportHeightPx, s.Density, s.X, s.Y, s.Width, s.Height)
			}
			vm.dispatch(jnull(), nativeGLClass, "hideKeyboard", hideKeyboardSig, nil)
		})
	}

	for _, tc := range []struct {
		value, density, want float32
	}{
		{value: 12.9, density: 1, want: 12},
		{value: 12.9, density: 2, want: 25},
		{value: -1.9, density: 1, want: -1},
	} {
		if got := androidViewPixel(tc.value, tc.density); got != tc.want {
			t.Fatalf("androidViewPixel(%v,%v)=%v want %v", tc.value, tc.density, got, tc.want)
		}
	}
}

func TestRbxTextOverlayViewportUpdateInvalidatesWithoutScaling(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetTextInputConnectionForTest()
	t.Cleanup(resetTextInputConnectionForTest)
	vm.SetDisplaySize(1280, 720)
	vm.mu.Lock()
	info := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
	vm.mu.Unlock()
	seedNativeTextBoxInfoConstructor(vm, idToJobject(info.id), nativeTextBoxInfoSig,
		testPackNativeTextBoxInfoArgs(
			10.875, 20.625, 300.99, 31.99, 17.5, false,
			0, 0, 0xffffffff, 0, 0, 0, false, false, true))
	initID, _ := keyboardTestObjects(t, vm, nil, 0)
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(92, 1, initID, info.id))
	before := CurrentRbxTextOverlay()
	SetRbxTextOverlayViewport(2560, 1440, 1)
	after := CurrentRbxTextOverlay()
	if after.Version <= before.Version || after.ViewportWidthPx != 2560 || after.ViewportHeightPx != 1440 {
		t.Fatalf("updated viewport=%dx%d version=%d before=%d",
			after.ViewportWidthPx, after.ViewportHeightPx, after.Version, before.Version)
	}
	if after.X != before.X || after.Y != before.Y || after.Width != before.Width || after.Height != before.Height {
		t.Fatalf("resize rescaled View pixels: before=%v,%v %vx%v after=%v,%v %vx%v",
			before.X, before.Y, before.Width, before.Height,
			after.X, after.Y, after.Width, after.Height)
	}
}

func TestRbxTextBoxInfoRefreshRunsAfterCallbackAndReleasesLocalRef(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)
	resetTextInputConnectionForTest()
	t.Cleanup(resetTextInputConnectionForTest)
	t.Cleanup(ClearRobloxTextInputTarget)
	vm.SetDisplaySize(1280, 720)
	vm.mu.Lock()
	info := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
	vm.mu.Unlock()
	seedNativeTextBoxInfoConstructor(vm, idToJobject(info.id), nativeTextBoxInfoSig,
		testPackNativeTextBoxInfoArgs(
			40.9, 50.9, 420.9, 38.9, 20, false,
			1, 1, 0xffddeeff, 16, 0, 0, false, false, true))
	const sentinel = "safe-secret"
	initID, _ := keyboardTestObjects(t, vm, []byte(sentinel), 0)
	// Exact DEX behavior: Z=false still focuses/shows the EditText but skips
	// applying the show payload's NativeTextBoxInfo.
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(201, 0, initID, info.id))
	if before := CurrentRbxTextOverlay(); !before.Active || before.Configured {
		t.Fatalf("pre-property state = active=%v configured=%v", before.Active, before.Configured)
	}

	getterCalls := 0
	const getterFn = uintptr(0x1234)
	env := vm.Env()
	if !SetRobloxTextInputTarget(env, env.FindClass("com/roblox/engine/jni/NativeGLInterface"),
		testRbxRecordPassFn(), 0, 0, getterFn,
		func(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) int64 {
			getterCalls++
			if fn != getterFn || a0 != env.Raw() || a1 == 0 {
				t.Fatalf("getter ABI = fn=%#x env=%#x class=%#x", fn, a0, a1)
			}
			return info.id
		}) {
		t.Fatal("text target not ready")
	}
	vm.dispatch(jnull(), nativeGLClass, luaTextBoxPropertyCallback,
		luaTextBoxPropertyChangedSig, nil)
	if getterCalls != 0 {
		t.Fatal("property callback re-entered native getter")
	}
	if stats := RbxTextInfoRefreshStats(); stats.Requested != 1 || stats.Attempted != 0 {
		t.Fatalf("post-callback refresh stats = %+v", stats)
	}

	if !RefreshRbxTextOverlayInfo() || getterCalls != 1 {
		t.Fatalf("delayed refresh = applied=%v calls=%d", CurrentRbxTextOverlay().Configured, getterCalls)
	}
	got := CurrentRbxTextOverlay()
	if !got.Configured || got.X != 40 || got.Y != 50 || got.Width != 420 || got.Height != 38 ||
		got.FontSize != 20 || got.TextColor != 0xffddeeff || got.Font != 16 ||
		got.XAlignment != 1 || got.YAlignment != 1 {
		t.Fatalf("refreshed non-content contract = %+v", got)
	}
	if vm.get(info.id) != nil {
		t.Fatal("nativeGetTextBoxInfo local reference survived field copy")
	}
	if RefreshRbxTextOverlayInfo() || getterCalls != 1 {
		t.Fatalf("consumed request retried: calls=%d", getterCalls)
	}
	stats := RbxTextInfoRefreshStats()
	if stats.Requested != 1 || stats.Attempted != 1 || stats.Applied != 1 ||
		stats.MissingTarget != 0 || stats.NullResult != 0 || stats.StaleSession != 0 {
		t.Fatalf("refresh stats = %+v", stats)
	}
	if out := buf.String(); !strings.Contains(out, "text box info refreshed") ||
		!strings.Contains(out, "textColorARGB=0xffddeeff") || strings.Contains(out, sentinel) {
		t.Fatalf("refresh diagnostic = %s", out)
	}
}

func TestRbxTextBoxInfoRefreshRejectsStaleFocusSession(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetTextInputConnectionForTest()
	t.Cleanup(resetTextInputConnectionForTest)
	t.Cleanup(ClearRobloxTextInputTarget)
	vm.mu.Lock()
	info := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
	vm.mu.Unlock()
	seedNativeTextBoxInfoConstructor(vm, idToJobject(info.id), nativeTextBoxInfoSig,
		testPackNativeTextBoxInfoArgs(
			10, 20, 300, 40, 18, false,
			0, 0, 0xffffffff, 0, 0, 0, false, false, true))
	beginRbxTextEditor(301, "", false, rbxTextBoxConfig{})
	markLuaTextBoxPropertyChanged()

	env := vm.Env()
	const getterFn = uintptr(0x5678)
	SetRobloxTextInputTarget(env, env.FindClass("com/roblox/engine/jni/NativeGLInterface"),
		testRbxRecordPassFn(), 0, 0, getterFn,
		func(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) int64 {
			// Simulate the engine changing focus while its getter is in flight.
			// Even a recycled native handle cannot defeat the session generation.
			beginRbxTextEditor(301, "", false, rbxTextBoxConfig{})
			return info.id
		})
	if RefreshRbxTextOverlayInfo() {
		t.Fatal("stale property result applied to a replacement focus session")
	}
	if got := CurrentRbxTextOverlay(); !got.Active || got.Configured {
		t.Fatalf("replacement focus state = active=%v configured=%v", got.Active, got.Configured)
	}
	if vm.get(info.id) != nil {
		t.Fatal("stale getter local reference was not released")
	}
	if stats := RbxTextInfoRefreshStats(); stats.Attempted != 1 || stats.Applied != 0 || stats.StaleSession != 1 {
		t.Fatalf("stale refresh stats = %+v", stats)
	}
}

func TestRbxTextBoxInfoRefreshMissingAndNullFailOnce(t *testing.T) {
	for _, tc := range []struct {
		name          string
		getInfoFn     uintptr
		withCaller    bool
		wantAttempted uint64
		wantMissing   uint64
		wantNull      uint64
	}{
		{name: "missing-target", wantMissing: 1},
		{name: "null-result", getInfoFn: 0x9abc, withCaller: true, wantAttempted: 1, wantNull: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, err := NewVM()
			if err != nil {
				t.Fatal(err)
			}
			resetTextInputConnectionForTest()
			t.Cleanup(resetTextInputConnectionForTest)
			t.Cleanup(ClearRobloxTextInputTarget)
			beginRbxTextEditor(401, "", false, rbxTextBoxConfig{})
			markLuaTextBoxPropertyChanged()
			calls := 0
			var caller RobloxTextNativeCaller
			if tc.withCaller {
				caller = func(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) int64 {
					calls++
					return 0
				}
			}
			env := vm.Env()
			SetRobloxTextInputTarget(env, env.FindClass("com/roblox/engine/jni/NativeGLInterface"),
				testRbxRecordPassFn(), 0, 0, tc.getInfoFn, caller)
			if RefreshRbxTextOverlayInfo() || RefreshRbxTextOverlayInfo() {
				t.Fatal("missing/null getter fabricated a text-box configuration")
			}
			if calls > 1 {
				t.Fatalf("failed refresh retried %d times", calls)
			}
			stats := RbxTextInfoRefreshStats()
			if stats.Requested != 1 || stats.Attempted != tc.wantAttempted ||
				stats.MissingTarget != tc.wantMissing || stats.NullResult != tc.wantNull || stats.Applied != 0 {
				t.Fatalf("failed refresh stats = %+v", stats)
			}
		})
	}
}

// TestNativeTextBoxInfoTextColorIsVerbatimARGB pins the current APK contract:
// constructor integer slot 8 is Android packed ARGB and neither the full nor
// copy constructor invents an alpha/default when the raw value is zero.
func TestNativeTextBoxInfoTextColorIsVerbatimARGB(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []uint32{0x00000000, 0x01020304, 0xffabcdef} {
		vm.mu.Lock()
		src := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
		dst := vm.newObjectLocked(vm.ensureClassLocked(nativeTextBoxInfoClass))
		vm.mu.Unlock()
		seedNativeTextBoxInfoConstructor(vm, idToJobject(src.id), nativeTextBoxInfoSig,
			testPackNativeTextBoxInfoArgs(
				0, 0, 1, 1, 1, false,
				0, 0, want, 0, 0, 0, false, false, true))
		seedNativeTextBoxInfoConstructor(vm, idToJobject(dst.id), nativeTextBoxInfoCopySig,
			testPackObjectArg(src.id))
		vm.mu.RLock()
		gotSource := uint32(src.fields["textColor"].(int32))
		gotCopy := uint32(dst.fields["textColor"].(int32))
		vm.mu.RUnlock()
		if gotSource != want || gotCopy != want {
			t.Fatalf("raw ARGB = source=%#08x copy=%#08x, want %#08x", gotSource, gotCopy, want)
		}
	}
}

// TestRbxKeyboardEditorCommitUTF16 drives the complete desktop adapter:
// an engine-owned showKeyboard session seeds the hidden editor, genuine
// committed UTF-8 updates the exact nativePassText ABI, and Java cursor
// offsets count UTF-16 code units rather than UTF-8 bytes or Go runes.
func TestRbxKeyboardEditorCommitUTF16(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)
	wireRecordingRbxTextTarget(t, vm)

	const secret = "A😀"
	initID, infoID := keyboardTestObjects(t, vm, []byte(secret), 1)
	if _, handled := vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(99, 1, initID, infoID)); !handled {
		t.Fatal("showKeyboard not handled")
	}
	active, handle, textLen, cursor, _, _ := RbxTextInputState()
	if !active || handle != 99 || textLen != 2 || cursor != 3 {
		t.Fatalf("seeded editor = active=%v handle=%d len=%d cursor=%d, want true/99/2/3", active, handle, textLen, cursor)
	}

	versionBefore := RbxTextOverlayVersion()
	if !DispatchRobloxTextCommit("é") {
		t.Fatal("committed UTF-8 was not delivered")
	}
	overlay := CurrentRbxTextOverlay()
	if overlay.Version <= versionBefore || overlay.Text != secret+"é" || overlay.SelectionEndUTF16 != 4 {
		t.Fatalf("immediate overlay = version=%d before=%d len=%d cursor=%d",
			overlay.Version, versionBefore, len([]rune(overlay.Text)), overlay.SelectionEndUTF16)
	}
	if testRbxRecPassCount() != 1 || testRbxRecHandle() != 99 || testRbxRecSubmit() || testRbxRecCursor() != 4 {
		t.Fatalf("pass ABI = count=%d handle=%d submit=%v cursor=%d, want 1/99/false/4",
			testRbxRecPassCount(), testRbxRecHandle(), testRbxRecSubmit(), testRbxRecCursor())
	}
	if testRbxRecSyncCount() != 1 || testRbxRecSyncSequence()+1 != testRbxRecPassSequence() {
		t.Fatalf("sync/pass order = syncCount=%d syncSeq=%d passSeq=%d, want one sync immediately before pass",
			testRbxRecSyncCount(), testRbxRecSyncSequence(), testRbxRecPassSequence())
	}
	if got, err := vm.Env().GetStringUTFChars(testRbxRecText()); err != nil || got != secret+"é" {
		t.Fatalf("pass text = %q, %v; want complete editor snapshot", got, err)
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), "é") {
		t.Fatalf("text content leaked to logs: %s", buf.String())
	}
}

// TestRbxKeyboardEditorNavigationAndSubmit pins the APK's companion paths:
// selection uses syncTextboxTextAndCursorPosition2, Backspace publishes a
// new full snapshot without splitting a supplementary rune, and Enter uses
// nativeReturnPressed plus nativePassText(submit=true) before releasing a
// non-manual textbox session.
func TestRbxKeyboardEditorNavigationAndSubmit(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	wireRecordingRbxTextTarget(t, vm)

	initID, infoID := keyboardTestObjects(t, vm, []byte("A😀é"), 1)
	vm.mu.Lock()
	vm.objects[infoID].fields["manualFocusRelease"] = false
	vm.objects[infoID].fields["multiline"] = false
	vm.mu.Unlock()
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(101, 1, initID, infoID))

	consumed := DispatchRobloxTextKey(21, true)
	if !consumed || testRbxRecSyncCount() != 1 || testRbxRecCursor() != 3 {
		t.Fatalf("left selection = consumed=%v sync=%d cursor=%d, want true/1/3",
			consumed, testRbxRecSyncCount(), testRbxRecCursor())
	}
	if !DispatchRobloxTextKey(67, true) || testRbxRecPassCount() != 1 || testRbxRecCursor() != 1 {
		t.Fatalf("backspace = pass=%d cursor=%d, want 1/1", testRbxRecPassCount(), testRbxRecCursor())
	}
	if got, _ := vm.Env().GetStringUTFChars(testRbxRecText()); got != "Aé" {
		t.Fatalf("backspace snapshot = %q, want %q", got, "Aé")
	}
	if !DispatchRobloxTextKey(66, true) {
		t.Fatal("Enter was not consumed by focused editor")
	}
	if testRbxRecReturnCount() != 1 || testRbxRecPassCount() != 2 || !testRbxRecSubmit() {
		t.Fatalf("submit ABI = returns=%d pass=%d submit=%v, want 1/2/true",
			testRbxRecReturnCount(), testRbxRecPassCount(), testRbxRecSubmit())
	}
	if active, _, textLen, _, _, returns := RbxTextInputState(); active || textLen != 0 || returns != 1 {
		t.Fatalf("post-submit editor = active=%v len=%d returns=%d, want false/0/1", active, textLen, returns)
	}
}

func TestRbxKeyboardTextWrappedUsesAndroidMultilineGate(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	wireRecordingRbxTextTarget(t, vm)

	const sentinel = "safe"
	initID, infoID := keyboardTestObjects(t, vm, []byte(sentinel), 1)
	vm.mu.Lock()
	vm.objects[infoID].fields["multiline"] = false
	vm.objects[infoID].fields["textWrapped"] = true
	vm.mu.Unlock()
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(102, 1, initID, infoID))

	if !DispatchRobloxTextKey(66, true) {
		t.Fatal("Enter was not consumed by wrapped editor")
	}
	if testRbxRecReturnCount() != 0 || testRbxRecPassCount() != 1 || testRbxRecSubmit() {
		t.Fatalf("wrapped Enter ABI = returns=%d pass=%d submit=%v, want 0/1/false",
			testRbxRecReturnCount(), testRbxRecPassCount(), testRbxRecSubmit())
	}
	if active, _, textLen, _, _, _ := RbxTextInputState(); !active || textLen != len([]rune(sentinel))+1 {
		t.Fatalf("wrapped editor = active=%v len=%d", active, textLen)
	}
}

// TestX11TextNeedsEngineFocus proves the routing boundary. An unfocused text
// commit is ignored and the ordinary physical key path remains live; once
// showKeyboard supplies a real textbox handle, the physical edge belongs to
// the editor and its separate InputText produces one text update.
func TestX11TextNeedsEngineFocus(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	resetTextInputConnectionForTest()
	selectKeyboardPath(t, "direct")
	wireRecordingDirectKeyTarget(t, vm.Env().Raw(), 88)
	defer ClearRobloxDirectKeyTarget()

	directBefore := RobloxDirectInputStats().KeyDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 29, ScanCode: 38})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputText, Text: "a"})
	if got := RobloxDirectInputStats().KeyDelivered - directBefore; got != 1 {
		t.Fatalf("unfocused direct physical delivery = %d, want 1", got)
	}
	if testRbxRecPassCount() != 0 {
		t.Fatal("unfocused text produced nativePassText")
	}

	wireRecordingRbxTextTarget(t, vm)
	initID, infoID := keyboardTestObjects(t, vm, nil, 1)
	vm.dispatch(jnull(), nativeGLClass, "showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(123, 1, initID, infoID))
	directBefore = RobloxDirectInputStats().KeyDelivered
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputKey, KeyPressed: true, KeyCode: 29, ScanCode: 38})
	handleX11InputEvent(x11.InputEvent{Kind: x11.InputText, Text: "a"})
	if got := RobloxDirectInputStats().KeyDelivered - directBefore; got != 0 {
		t.Fatalf("focused physical edge leaked to SurfaceView path: %d", got)
	}
	if testRbxRecPassCount() != 1 || testRbxRecHandle() != 123 {
		t.Fatalf("focused text delivery = count=%d handle=%d, want 1/123", testRbxRecPassCount(), testRbxRecHandle())
	}
}

// TestShowKeyboardNativeHelper drives the exact production dispatch path
// for NativeHelper.gameActivity_showKeyboard(JZ[BL...NativeTextBoxInfo;)V
// (live-observed in the login focus storm): handled, counted, aggregates
// recorded, log line emitted, nothing fabricated or acted on.
func TestShowKeyboardNativeHelper(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	cls := vm.ensureClassLocked(nativeHelperClass)
	h := vm.newObjectLocked(cls)
	vm.mu.Unlock()
	initID, boxesID := keyboardTestObjects(t, vm, []byte{1, 2, 3}, 2)

	showBefore, hideBefore, _, _, _, _, _ := TextInputKeyboardState()
	v, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass,
		"gameActivity_showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(42, 1, initID, boxesID))
	if !handled {
		t.Fatal("gameActivity_showKeyboard not handled by dispatchTextInput")
	}
	if uintptr(v) != uintptr(idToJobject(h.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	show, hide, handle, flag, initLen, boxes, class := TextInputKeyboardState()
	if show != showBefore+1 {
		t.Fatalf("show count = %d, want %d", show, showBefore+1)
	}
	if hide != hideBefore {
		t.Fatalf("hide count = %d, want unchanged %d", hide, hideBefore)
	}
	if handle != 42 || !flag || initLen != 3 || boxes != 1 {
		t.Fatalf("record = (%d, %v, %d, %d), want (42, true, 3, 1)", handle, flag, initLen, boxes)
	}
	if class != nativeHelperClass {
		t.Fatalf("class = %q, want NativeHelper", class)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] showKeyboard") || !strings.Contains(out, "handle=42") {
		t.Fatalf("log missing showKeyboard record: %s", out)
	}

	if !isImplementedMethod("gameActivity_showKeyboard", showKeyboardSig) {
		t.Fatal("gameActivity_showKeyboard missing from implementedMethods")
	}
	if !isImplementedMethod("gameActivity_hideKeyboard", hideKeyboardSig) {
		t.Fatal("gameActivity_hideKeyboard missing from implementedMethods")
	}

	// Wrong class with the same name/sig is not the contract.
	if _, handled := vm.dispatch(idToJobject(h.id), "java/io/File",
		"gameActivity_showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(1, 0, initID, boxesID)); handled {
		t.Fatal("non-text class handled by text-input dispatch")
	}
	// Wrong sig on the right class stays unhandled.
	if _, handled := vm.dispatch(idToJobject(h.id), nativeHelperClass,
		"gameActivity_showKeyboard", "(JZ)V",
		testPackKeyboardArgs(1, 0, initID, boxesID)); handled {
		t.Fatal("wrong-sig showKeyboard handled by text-input dispatch")
	}
}

// TestShowHideKeyboardNativeGL drives the NativeGLJavaInterface half of the
// contract (showKeyboard/hideKeyboard, called first in the live storm):
// same receive-and-record semantics, shared counters.
func TestShowHideKeyboardNativeGL(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	initID, boxesID := keyboardTestObjects(t, vm, []byte{9}, 1)
	showBefore, hideBefore, _, _, _, _, _ := TextInputKeyboardState()

	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		"showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(0, 0, initID, boxesID)); !handled {
		t.Fatal("NativeGL showKeyboard not handled")
	}
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		"hideKeyboard", hideKeyboardSig, nil); !handled {
		t.Fatal("NativeGL hideKeyboard not handled")
	}
	if _, handled := vm.dispatch(jnull(), nativeHelperClass,
		"gameActivity_hideKeyboard", hideKeyboardSig, nil); !handled {
		t.Fatal("NativeHelper hideKeyboard not handled")
	}

	show, hide, handle, flag, initLen, boxes, _ := TextInputKeyboardState()
	if show != showBefore+1 || hide != hideBefore+2 {
		t.Fatalf("counts = (%d, %d), want (%d, %d)", show, hide, showBefore+1, hideBefore+2)
	}
	if handle != 0 || flag || initLen != 1 || boxes != 1 {
		t.Fatalf("record = (%d, %v, %d, %d), want (0, false, 1, 1)", handle, flag, initLen, boxes)
	}
	out := buf.String()
	if !strings.Contains(out, "[jni] showKeyboard") || !strings.Contains(out, "[jni] hideKeyboard") {
		t.Fatalf("log missing keyboard records: %s", out)
	}
	if !isImplementedMethod("showKeyboard", showKeyboardSig) {
		t.Fatal("showKeyboard missing from implementedMethods")
	}
	if !isImplementedMethod("hideKeyboard", hideKeyboardSig) {
		t.Fatal("hideKeyboard missing from implementedMethods")
	}
}

// TestShowKeyboardNilArgs proves an absent argument slot degrades to zeros
// (handle 0, lengths 0) without a panic — the engine's J slot reads 0 on a
// nil slot, never a fabricated value.
func TestShowKeyboardNilArgs(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	showBefore, _, _, _, _, _, _ := TextInputKeyboardState()
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		"showKeyboard", showKeyboardSig, nil); !handled {
		t.Fatal("nil-arg showKeyboard not handled")
	}
	show, _, handle, _, initLen, boxes, _ := TextInputKeyboardState()
	if show != showBefore+1 || handle != 0 || initLen != 0 || boxes != 0 {
		t.Fatalf("nil-arg record = show+1=%v handle=%d init=%d boxes=%d", show == showBefore+1, handle, initLen, boxes)
	}
}

// TestShowKeyboardNeverLogsTextContent is the privacy boundary: payload
// bytes that stand in for user-typed text appear in the log only as a
// length, never as content.
func TestShowKeyboardNeverLogsTextContent(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	const secret = "s3cr3t-typed-text-marker"
	initID, boxesID := keyboardTestObjects(t, vm, []byte(secret), 0)
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		"showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(7, 0, initID, boxesID)); !handled {
		t.Fatal("showKeyboard not handled")
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log leaked payload bytes: %s", out)
	}
	if !strings.Contains(out, "initialLen=") {
		t.Fatalf("log missing length-only aggregate: %s", out)
	}
	_, _, _, _, initLen, _, _ := TextInputKeyboardState()
	if initLen != len(secret) {
		t.Fatalf("initialLen = %d, want %d", initLen, len(secret))
	}
}

// TestTextInputConnectionLifecycle proves the Tipsy-owned InputConnection
// contract: no object before the engine's own focus signals, one real
// InputConnection object from the first showKeyboard, a stable identity
// across further shows (both classes), and hideKeyboard deactivating
// without destroying the handle. The handle is a Tipsy VM object id
// (opaque 64-bit), never an engine pointer and never fabricated.
func TestTextInputConnectionLifecycle(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)
	resetTextInputConnectionForTest()

	if conn, _, active := TextInputConnectionState(); conn != 0 || active {
		t.Fatalf("initial connection = (%d, active=%v), want (0, false)", conn, active)
	}

	initID, boxesID := keyboardTestObjects(t, vm, []byte{1}, 0)
	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		"showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(42, 1, initID, boxesID)); !handled {
		t.Fatal("showKeyboard not handled")
	}
	conn, handle, active := TextInputConnectionState()
	if conn == 0 || handle == 0 || !active {
		t.Fatalf("post-show connection = (%d, handle=%d, active=%v), want non-zero active", conn, handle, active)
	}
	if o := vm.get(conn); o == nil || o.class == nil || o.class.name != gameTextInputConnectionClass {
		t.Fatalf("connection object = %+v, want InputConnection instance", o)
	}
	if got := vm.EnsureTextInputConnection(); got != conn {
		t.Fatalf("Ensure = %d, want stable %d", got, conn)
	}

	// Second show on the fallback class keeps the stable object.
	if _, handled := vm.dispatch(jnull(), nativeHelperClass,
		"gameActivity_showKeyboard", showKeyboardSig,
		testPackKeyboardArgs(43, 0, initID, boxesID)); !handled {
		t.Fatal("fallback showKeyboard not handled")
	}
	if conn2, _, active2 := TextInputConnectionState(); conn2 != conn || !active2 {
		t.Fatalf("post-second-show = (%d, active=%v), want (%d, true)", conn2, active2, conn)
	}

	if _, handled := vm.dispatch(jnull(), nativeGLClass,
		"hideKeyboard", hideKeyboardSig, nil); !handled {
		t.Fatal("hideKeyboard not handled")
	}
	if conn3, _, active3 := TextInputConnectionState(); conn3 != conn || active3 {
		t.Fatalf("post-hide = (%d, active=%v), want (%d, false)", conn3, active3, conn)
	}
}

// TestTextInputSetStateTransitions drives the exact production dispatch
// path for the engine→Java GameTextInput contract
// (InputConnection.setState(State)V, setSoftKeyboardActive(ZI)V,
// restartInput()V — DEX-proven classes2.dex descriptors): each call is
// handled, counted, and recorded as opaque aggregates only, with a
// trigger-gated log line and void receiver-echo semantics.
func TestTextInputSetStateTransitions(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	vm.mu.Lock()
	connCls := vm.ensureClassLocked(gameTextInputConnectionClass)
	conn := vm.newObjectLocked(connCls)
	stateCls := vm.ensureClassLocked(gameTextInputStateClass)
	state := vm.newObjectLocked(stateCls)
	vm.mu.Unlock()

	setBefore, softBefore, restartBefore, _ := TextInputStateTransitions()

	v, handled := vm.dispatch(idToJobject(conn.id), gameTextInputConnectionClass,
		"setState", setStateSig, testPackObjectArg(state.id))
	if !handled {
		t.Fatal("setState not handled by dispatchTextConnection")
	}
	if uintptr(v) != uintptr(idToJobject(conn.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	v, handled = vm.dispatch(idToJobject(conn.id), gameTextInputConnectionClass,
		"setSoftKeyboardActive", setSoftKeyboardActiveSig, testPackTwoInts(1, 7))
	if !handled {
		t.Fatal("setSoftKeyboardActive not handled")
	}
	if uintptr(v) != uintptr(idToJobject(conn.id)) {
		t.Fatalf("void dispatch return = %#x, want the receiver", uintptr(v))
	}
	if _, handled := vm.dispatch(idToJobject(conn.id), gameTextInputConnectionClass,
		"restartInput", restartInputSig, nil); !handled {
		t.Fatal("restartInput not handled")
	}

	setAfter, softAfter, restartAfter, _ := TextInputStateTransitions()
	if setAfter != setBefore+1 || softAfter != softBefore+1 || restartAfter != restartBefore+1 {
		t.Fatalf("transitions = (%d, %d, %d), want each +1", setAfter-setBefore, softAfter-softBefore, restartAfter-restartBefore)
	}
	if got := TextInputLastStateRef(); got != state.id {
		t.Fatalf("last State ref = %d, want opaque %d", got, state.id)
	}
	if active, counter := TextInputLastSoftKeyboard(); !active || counter != 7 {
		t.Fatalf("last soft-keyboard = (%v, %d), want (true, 7)", active, counter)
	}
	out := buf.String()
	for _, want := range []string{"[jni] setState", "[jni] setSoftKeyboardActive", "[jni] restartInput"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q: %s", want, out)
		}
	}

	for _, tc := range []struct{ name, sig string }{
		{"setState", setStateSig},
		{"setSoftKeyboardActive", setSoftKeyboardActiveSig},
		{"restartInput", restartInputSig},
	} {
		if !isImplementedMethod(tc.name, tc.sig) {
			t.Fatalf("%s%s missing from implementedMethods", tc.name, tc.sig)
		}
	}

	// Wrong class with the same name/sig is not the contract.
	if _, handled := vm.dispatch(idToJobject(conn.id), "java/io/File",
		"setState", setStateSig, testPackObjectArg(state.id)); handled {
		t.Fatal("non-InputConnection class handled by text-connection dispatch")
	}
	// Wrong sig on the right class stays unhandled.
	if _, handled := vm.dispatch(idToJobject(conn.id), gameTextInputConnectionClass,
		"setState", "(Lcom/google/androidgamesdk/gametextinput/State;I)V",
		testPackObjectArg(state.id)); handled {
		t.Fatal("wrong-sig setState handled by text-connection dispatch")
	}
	// Nil-arg setState degrades to a zero ref without a panic.
	if _, handled := vm.dispatch(jnull(), gameTextInputConnectionClass,
		"setState", setStateSig, nil); !handled {
		t.Fatal("nil-arg setState not handled")
	}
}

// TestTextInputNeverLogsStateContent is the privacy boundary for the State
// contract: a State object carrying a secret marker in every carrier Tipsy
// could reach (string, fields, bytes) appears in the log only as an opaque
// reference id, never as content, and no accessor exposes content.
func TestTextInputNeverLogsStateContent(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)

	const secret = "s3cr3t-text-state-marker"
	vm.mu.Lock()
	stateCls := vm.ensureClassLocked(gameTextInputStateClass)
	state := vm.newObjectLocked(stateCls)
	state.str = secret
	state.fields["text"] = secret
	state.bytes = []byte(secret)
	vm.mu.Unlock()

	if _, handled := vm.dispatch(jnull(), gameTextInputConnectionClass,
		"setState", setStateSig, testPackObjectArg(state.id)); !handled {
		t.Fatal("setState not handled")
	}
	out := buf.String()
	if strings.Contains(out, secret) {
		t.Fatalf("log leaked State content: %s", out)
	}
	if !strings.Contains(out, "[jni] setState") {
		t.Fatalf("log missing trigger-gated setState line: %s", out)
	}
	if got := TextInputLastStateRef(); got != state.id {
		t.Fatalf("last State ref = %d, want opaque %d", got, state.id)
	}
}

// TestTextInputCommitStaysHonestWhenSilent pins the no-synthesis rule: even
// with recorded State transitions, there is no commit source — committed
// text stays zero, no commit is pending, and none of the Java→native
// commit identities has a Tipsy-side caller wired (no RegisterNatives
// entries without the engine). The official nativePassText route is never
// fed from keystrokes or thin air.
func TestTextInputCommitStaysHonestWhenSilent(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	captureLogs(t)

	if TextInputCommitPending() {
		t.Fatal("commit pending without a text buffer: synthesis would be fabrication")
	}
	if _, _, _, committed := TextInputStateTransitions(); committed != 0 {
		t.Fatalf("committed = %d, want 0 without deltas", committed)
	}
	for _, tc := range []struct{ name, sig string }{
		{"setInputConnectionNative", "(JLcom/google/androidgamesdk/gametextinput/InputConnection;)V"},
		{"onTextInputEventNative", "(JLcom/google/androidgamesdk/gametextinput/State;)V"},
		{"onEditorActionNative", "(JI)V"},
		{"onSoftwareKeyboardVisibilityChangedNative", "(JZ)V"},
	} {
		if got := vm.NativeMethod(gameActivityClass, tc.name, tc.sig); got != 0 {
			t.Fatalf("%s%s wired without the engine: %#x", tc.name, tc.sig, got)
		}
	}
}

// TestTextInputPhysicalKeyPathUntouched proves the commit-source work leaves
// the official physical-key route intact: a real Q press/release edge still
// delivers the exact nativePassKeyEvent(ZIIZ)V ABI while State counts and
// the commit gate do not move.
func TestTextInputPhysicalKeyPathUntouched(t *testing.T) {
	const env, class = uintptr(0x1234), uintptr(0x9abc)
	wireRecordingDirectKeyTarget(t, env, class)

	setBefore, softBefore, restartBefore, _ := TextInputStateTransitions()
	keyBefore := RobloxDirectInputStats().KeyDelivered

	// X11 <AD01>=24 maps to evdev KEY_Q=16; AKEYCODE_Q is 45.
	if !DispatchRobloxDirectKey(24, 45, true) {
		t.Fatal("direct Q DOWN was not delivered")
	}
	if !DispatchRobloxDirectKey(24, 45, false) {
		t.Fatal("direct Q UP was not delivered")
	}
	if got := testDirectRecKeyInt(1); got != 16 {
		t.Fatalf("Q scan code = %d, want evdev KEY_Q=16", got)
	}
	if got := testDirectRecKeyInt(2); got != 45 {
		t.Fatalf("Q Android keycode = %d, want AKEYCODE_Q=45", got)
	}
	if got := RobloxDirectInputStats().KeyDelivered - keyBefore; got != 2 {
		t.Fatalf("direct key delivery delta = %d, want 2", got)
	}
	setAfter, softAfter, restartAfter, committed := TextInputStateTransitions()
	if setAfter != setBefore || softAfter != softBefore || restartAfter != restartBefore {
		t.Fatal("physical key edges moved State transition counts")
	}
	if committed != 0 || TextInputCommitPending() {
		t.Fatal("physical key edges fabricated committed text")
	}
}
