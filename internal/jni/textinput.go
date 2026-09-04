// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"

// Typed callers for the APK's RbxKeyboard/EditText JNI bridge. All declared
// arguments use integer registers, but keeping the signature here prevents a
// future call-site from confusing the textbox J handle with the jclass slot.
static void tipsy_rbx_pass_text(void *fn, uintptr_t env, uintptr_t cls,
	long long handle, jobject text, unsigned char submit, int cursor) {
	((void (*)(JNIEnv *, jclass, jlong, jstring, jboolean, jint))fn)(
		(JNIEnv *)env, (jclass)cls, (jlong)handle, (jstring)text,
		(jboolean)submit, (jint)cursor);
}

static void tipsy_rbx_return_pressed(void *fn, uintptr_t env, uintptr_t cls,
	long long handle) {
	((void (*)(JNIEnv *, jclass, jlong))fn)(
		(JNIEnv *)env, (jclass)cls, (jlong)handle);
}

static void tipsy_rbx_sync_selection(void *fn, uintptr_t env, uintptr_t cls,
	jobject text, int cursor) {
	((void (*)(JNIEnv *, jclass, jstring, jint))fn)(
		(JNIEnv *)env, (jclass)cls, (jstring)text, (jint)cursor);
}

// ABI witnesses used only by Go tests. They record ids/counts, never copy or
// inspect the jstring payload.
static unsigned long long tipsy_rbx_rec_handle;
static uintptr_t tipsy_rbx_rec_text;
static int tipsy_rbx_rec_submit;
static int tipsy_rbx_rec_cursor;
static int tipsy_rbx_rec_pass_count;
static int tipsy_rbx_rec_return_count;
static int tipsy_rbx_rec_sync_count;
static int tipsy_rbx_rec_sequence;
static int tipsy_rbx_rec_pass_sequence;
static int tipsy_rbx_rec_sync_sequence;
static void tipsy_rbx_record_pass(JNIEnv *env, jclass cls, jlong handle,
	jstring text, jboolean submit, jint cursor) {
	(void)env; (void)cls;
	tipsy_rbx_rec_handle = (unsigned long long)handle;
	tipsy_rbx_rec_text = (uintptr_t)text;
	tipsy_rbx_rec_submit = submit;
	tipsy_rbx_rec_cursor = cursor;
	tipsy_rbx_rec_pass_count++;
	tipsy_rbx_rec_pass_sequence = ++tipsy_rbx_rec_sequence;
}
static void tipsy_rbx_record_return(JNIEnv *env, jclass cls, jlong handle) {
	(void)env; (void)cls; (void)handle; tipsy_rbx_rec_return_count++;
}
static void tipsy_rbx_record_sync(JNIEnv *env, jclass cls, jstring text, jint cursor) {
	(void)env; (void)cls; (void)text; tipsy_rbx_rec_cursor = cursor;
	tipsy_rbx_rec_sync_count++;
	tipsy_rbx_rec_sync_sequence = ++tipsy_rbx_rec_sequence;
}
static void tipsy_rbx_rec_reset(void) {
	tipsy_rbx_rec_handle = 0; tipsy_rbx_rec_text = 0;
	tipsy_rbx_rec_submit = 0; tipsy_rbx_rec_cursor = 0;
	tipsy_rbx_rec_pass_count = 0; tipsy_rbx_rec_return_count = 0;
	tipsy_rbx_rec_sync_count = 0;
	tipsy_rbx_rec_sequence = 0; tipsy_rbx_rec_pass_sequence = 0;
	tipsy_rbx_rec_sync_sequence = 0;
}
static void *tipsy_rbx_record_pass_fn(void) { return (void *)tipsy_rbx_record_pass; }
static void *tipsy_rbx_record_return_fn(void) { return (void *)tipsy_rbx_record_return; }
static void *tipsy_rbx_record_sync_fn(void) { return (void *)tipsy_rbx_record_sync; }
static unsigned long long tipsy_rbx_rec_handle_get(void) { return tipsy_rbx_rec_handle; }
static uintptr_t tipsy_rbx_rec_text_get(void) { return tipsy_rbx_rec_text; }
static int tipsy_rbx_rec_submit_get(void) { return tipsy_rbx_rec_submit; }
static int tipsy_rbx_rec_cursor_get(void) { return tipsy_rbx_rec_cursor; }
static int tipsy_rbx_rec_pass_count_get(void) { return tipsy_rbx_rec_pass_count; }
static int tipsy_rbx_rec_return_count_get(void) { return tipsy_rbx_rec_return_count; }
static int tipsy_rbx_rec_sync_count_get(void) { return tipsy_rbx_rec_sync_count; }
static int tipsy_rbx_rec_pass_sequence_get(void) { return tipsy_rbx_rec_pass_sequence; }
static int tipsy_rbx_rec_sync_sequence_get(void) { return tipsy_rbx_rec_sync_sequence; }
*/
import "C"

import (
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// nativeGLClass is the engine's Java callback surface
// (com/roblox/engine/jni/NativeGLJavaInterface) for the keyboard half of
// the text-input contract. The engine GetMethodIDs showKeyboard/hideKeyboard
// here at startup and calls them when a Lua text box gains focus; when the
// NativeGL call does not settle the request it falls back to NativeHelper's
// gameActivity_showKeyboard/gameActivity_hideKeyboard pair (both directions
// observed live in the same focus storm).
const nativeGLClass = "com/roblox/engine/jni/NativeGLJavaInterface"

// showKeyboardSig/hideKeyboardSig are the exact descriptors the engine
// requests via GetMethodID (live launch logs, 2.734.917): the J slot carries
// the engine's native handle, Z is the engine's boolean flag, [B is the
// initial-bytes payload, and L…NativeTextBoxInfo is the text-box object.
// DEX ground truth (classes2.dex, read-only method-table audit, no
// disassembly): NativeGLJavaInterface.showKeyboard(J,Z,[B,LNativeTextBoxInfo;)V
// and the NativeHelper gameActivity_showKeyboard twin carry the identical
// parameter list; hideKeyboard is ()V on both classes.
const (
	showKeyboardSig = "(JZ[BLcom/roblox/engine/jni/model/NativeTextBoxInfo;)V"
	hideKeyboardSig = "()V"
)

// GameTextInput connection identities (DEX ground truth, classes2.dex
// method-table audit, 2.734.917 — descriptors only, no code copied):
//
//	com/google/androidgamesdk/gametextinput/InputConnection.setState(Lcom/google/androidgamesdk/gametextinput/State;)V
//	com/google/androidgamesdk/gametextinput/InputConnection.setSoftKeyboardActive(ZI)V
//	com/google/androidgamesdk/gametextinput/InputConnection.restartInput()V
//
// Direction is engine→Java: the engine GetMethodIDs these at startup and
// Calls them on the InputConnection object. The Java→native commit route
// the engine registers for a real connection to call into is:
//
//	com/google/androidgamesdk/GameActivity.setInputConnectionNative(JLcom/google/androidgamesdk/gametextinput/InputConnection;)V
//	com/google/androidgamesdk/GameActivity.onTextInputEventNative(JLcom/google/androidgamesdk/gametextinput/State;)V
//	com/google/androidgamesdk/GameActivity.onEditorActionNative(JI)V
//	com/google/androidgamesdk/GameActivity.onSoftwareKeyboardVisibilityChangedNative(JZ)V
//	com/roblox/engine/jni/NativeGLInterface.nativePassText(JLjava/lang/String;ZI)V
//	  (named dynsym Java_com_roblox_engine_jni_NativeGLInterface_nativePassText)
//
// Tipsy implements the engine→Java GameTextInput half below and hands its
// InputConnection object to the engine. That State-based route still has no
// commit source because State content is intentionally opaque. Separately,
// the APK-proven RbxKeyboard editor below receives genuine X11 XIM commits
// and calls the named syncTextboxTextAndCursorPosition2/nativePassText route.
const (
	gameTextInputConnectionClass = "com/google/androidgamesdk/gametextinput/InputConnection"
	gameTextInputStateClass      = "com/google/androidgamesdk/gametextinput/State"
	setStateSig                  = "(Lcom/google/androidgamesdk/gametextinput/State;)V"
	setSoftKeyboardActiveSig     = "(ZI)V"
	restartInputSig              = "()V"
)

// keyboardState records the engine's keyboard announcements verbatim: how
// many show/hide requests arrived and the safe aggregates of the most recent
// show (handle, flag, and the two payload LENGTHS). The [B bytes and the
// NativeTextBoxInfo fields can carry user text, so they are never read,
// stored, or logged — only their lengths cross into Tipsy. The Z flag's
// exact meaning (secure entry vs multiline vs other) is not established
// from any DEX Java body available to Tipsy, so it is recorded as an opaque
// boolean, never named. Nothing here fabricates visibility, focuses a
// field. The same genuine show announcement starts the private RbxKeyboard
// editor session below; it is the only gate through which X11 committed text
// can reach the named engine callbacks.
var keyboardState struct {
	mu         sync.Mutex
	showCount  uint64
	hideCount  uint64
	lastHandle int64
	lastFlag   bool
	lastInit   int
	lastBoxes  int
	lastClass  string
}

// rbxTextBoxConfig is the non-content portion of NativeTextBoxInfo that
// affects desktop editing behavior. The APK's RbxKeyboard reads these fields
// when showKeyboard's boolean is true. They never contain user text.
type rbxTextBoxConfig struct {
	manualFocusRelease bool
	multiline          bool
}

// RobloxTextNativeCaller runs one named JNI export with eight integer/pointer
// ABI slots. Runtime supplies loader.CallP8 so Roblox code always executes on
// the repository's dedicated native Main pthread. Tests may leave it nil and
// use the typed C ABI witnesses below.
type RobloxTextNativeCaller func(fn, a0, a1, a2, a3, a4, a5, a6, a7 uintptr) int64

// rbxTextEditor is Tipsy's platform-side equivalent of the APK's hidden
// RbxKeyboard EditText. The engine owns focus and supplies the textbox handle
// plus UTF-8 initial text through showKeyboard. X11 supplies genuine committed
// UTF-8. The adapter retains content only for the lifetime of that focused
// textbox and never exposes or logs it.
var rbxTextEditor struct {
	mu                 sync.Mutex
	active             bool
	handle             int64
	text               []rune
	cursor             int // rune index; JNI calls convert to Java UTF-16 units
	manualFocusRelease bool
	multiline          bool
	editCount          uint64
	returnCount        uint64
}

// rbxTextTarget is the exact named JNI surface called by the APK's
// RbxKeyboard. Runtime wires it after JNI_OnLoad; until then editor input is
// rejected honestly. No engine pointer or callback identity is invented.
var rbxTextTarget struct {
	mu       sync.RWMutex
	env      *Env
	class    uintptr
	passFn   uintptr
	returnFn uintptr
	syncFn   uintptr
	call     RobloxTextNativeCaller
}

var rbxTextDelivered struct {
	pass    uint64
	returns uint64
	sync    uint64
	dropped uint64
}

// SetRobloxTextInputTarget wires the three APK-proven RbxKeyboard exports.
// nativePassText is required for a ready target; the editor-action/selection/
// focus helpers are optional and fail honestly if an APK omits them.
func SetRobloxTextInputTarget(env *Env, class, passFn, returnFn, syncFn uintptr, call RobloxTextNativeCaller) bool {
	rbxTextTarget.mu.Lock()
	rbxTextTarget.env = env
	rbxTextTarget.class = class
	rbxTextTarget.passFn = passFn
	rbxTextTarget.returnFn = returnFn
	rbxTextTarget.syncFn = syncFn
	rbxTextTarget.call = call
	ready := env != nil && env.Raw() != 0 && class != 0 && passFn != 0
	rbxTextTarget.mu.Unlock()
	logging.Logger(logging.CatJNI).Info("[jni] text delivery path", "ready", ready)
	return ready
}

// ClearRobloxTextInputTarget parks calls before libroblox is unmapped and
// wipes any focused text buffer so credentials do not outlive the session.
func ClearRobloxTextInputTarget() {
	rbxTextTarget.mu.Lock()
	rbxTextTarget.env = nil
	rbxTextTarget.class = 0
	rbxTextTarget.passFn = 0
	rbxTextTarget.returnFn = 0
	rbxTextTarget.syncFn = 0
	rbxTextTarget.call = nil
	rbxTextTarget.mu.Unlock()
	wipeRbxTextEditor()
}

// dispatchTextInput serves the engine→Java keyboard contract on both
// classes that actually call it. Only the four live-observed identities are
// handled; everything else falls through to the honest stub path.
// showKeyboard activates the Tipsy-owned InputConnection and starts the
// private RbxKeyboard editor from the engine-provided initial value;
// hideKeyboard deactivates and wipes it. No text is fabricated or logged.
func (vm *VM) dispatchTextInput(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	isShow := sig == showKeyboardSig &&
		((class == nativeHelperClass && name == "gameActivity_showKeyboard") ||
			(class == nativeGLClass && name == "showKeyboard"))
	isHide := sig == hideKeyboardSig &&
		((class == nativeHelperClass && name == "gameActivity_hideKeyboard") ||
			(class == nativeGLClass && name == "hideKeyboard"))
	if !isShow && !isHide {
		return jnull(), false
	}
	if isShow {
		var handle int64
		if args != nil {
			handle = int64(C.tipsy_jvalue_j(args))
		}
		configure := jvalueIAt(args, 1) != 0
		initial, infoPresent, config := keyboardPayload(vm, args)
		initLen := len([]byte(initial))
		keyboardState.mu.Lock()
		keyboardState.showCount++
		keyboardState.lastHandle = handle
		keyboardState.lastFlag = configure
		keyboardState.lastInit = initLen
		keyboardState.lastBoxes = infoPresent
		keyboardState.lastClass = class
		keyboardState.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] showKeyboard",
			"class", class,
			"handle", handle,
			"flag", configure,
			"initialLen", initLen,
			"info", infoPresent)
		beginRbxTextEditor(handle, initial, configure, config)
		vm.noteTextFocus(true)
	} else {
		keyboardState.mu.Lock()
		keyboardState.hideCount++
		keyboardState.lastClass = class
		keyboardState.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] hideKeyboard",
			"class", class)
		wipeRbxTextEditor()
		vm.noteTextFocus(false)
	}
	if o != nil {
		return idToJobject(o.id), true
	}
	return jnull(), true
}

// keyboardPayload decodes exactly what the APK's RbxKeyboard does: slot 2 is
// a UTF-8 byte[] used as the EditText's initial value and slot 3 is one
// NativeTextBoxInfo object (not an array). Content is copied into the private
// focused editor and never logged or exposed. Invalid UTF-8 follows Java's
// String(byte[], UTF_8) behavior by replacing malformed input.
func keyboardPayload(vm *VM, args *C.jvalue) (initial string, infoPresent int, config rbxTextBoxConfig) {
	if vm == nil || args == nil {
		return "", 0, config
	}
	initID := jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 2)))
	infoID := jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 3)))
	vm.mu.Lock()
	if o := vm.objects[initID]; o != nil {
		initial = strings.ToValidUTF8(string(o.bytes), "\uFFFD")
	}
	if o := vm.objects[infoID]; o != nil {
		infoPresent = 1
		config.manualFocusRelease, _ = o.fields["manualFocusRelease"].(bool)
		config.multiline, _ = o.fields["multiline"].(bool)
	}
	vm.mu.Unlock()
	return initial, infoPresent, config
}

// TextInputKeyboardState reports the keyboard announcements received from
// the engine: show/hide counts plus the safe aggregates of the most recent
// show (handle, opaque flag, initial-bytes length, text-box count, calling
// class). Zero counts mean the engine has not requested the keyboard —
// never a fabricated value. No text content is exposed here by construction.
func TextInputKeyboardState() (show, hide uint64, handle int64, flag bool, initLen, boxes int, class string) {
	keyboardState.mu.Lock()
	defer keyboardState.mu.Unlock()
	return keyboardState.showCount, keyboardState.hideCount,
		keyboardState.lastHandle, keyboardState.lastFlag,
		keyboardState.lastInit, keyboardState.lastBoxes,
		keyboardState.lastClass
}

func beginRbxTextEditor(handle int64, initial string, configure bool, config rbxTextBoxConfig) {
	runes := []rune(initial)
	rbxTextEditor.mu.Lock()
	for i := range rbxTextEditor.text {
		rbxTextEditor.text[i] = 0
	}
	rbxTextEditor.active = handle != 0
	rbxTextEditor.handle = handle
	rbxTextEditor.text = runes
	rbxTextEditor.cursor = len(runes)
	if configure {
		rbxTextEditor.manualFocusRelease = config.manualFocusRelease
		rbxTextEditor.multiline = config.multiline
	}
	rbxTextEditor.mu.Unlock()
}

func wipeRbxTextEditor() {
	rbxTextEditor.mu.Lock()
	for i := range rbxTextEditor.text {
		rbxTextEditor.text[i] = 0
	}
	rbxTextEditor.active = false
	rbxTextEditor.handle = 0
	rbxTextEditor.text = nil
	rbxTextEditor.cursor = 0
	rbxTextEditor.manualFocusRelease = false
	rbxTextEditor.multiline = false
	rbxTextEditor.mu.Unlock()
}

func utf16Cursor(text []rune, cursor int) int {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(text) {
		cursor = len(text)
	}
	return len(utf16.Encode(text[:cursor]))
}

// DispatchRobloxTextCommit inserts genuine UTF-8 committed by X11 into the
// focused RbxKeyboard editor and calls the APK's exact TextWatcher route:
// nativePassText(textboxHandle, completeText, false, UTF16Cursor). It never
// derives characters from keycodes and never waits for GameTextInput State or
// keyboard-visibility callbacks. Empty/control-only/invalid commits do not
// produce a call.
func DispatchRobloxTextCommit(committed string) bool {
	if committed == "" || !utf8.ValidString(committed) {
		return false
	}
	in := []rune(committed)
	clean := in[:0]
	for _, r := range in {
		if r >= 0x20 && r != 0x7f {
			clean = append(clean, r)
		}
	}
	if len(clean) == 0 {
		return false
	}

	rbxTextEditor.mu.Lock()
	if !rbxTextEditor.active || rbxTextEditor.handle == 0 {
		rbxTextEditor.mu.Unlock()
		return false
	}
	cursor := rbxTextEditor.cursor
	text := make([]rune, 0, len(rbxTextEditor.text)+len(clean))
	text = append(text, rbxTextEditor.text[:cursor]...)
	text = append(text, clean...)
	text = append(text, rbxTextEditor.text[cursor:]...)
	rbxTextEditor.text = text
	rbxTextEditor.cursor = cursor + len(clean)
	handle := rbxTextEditor.handle
	full := string(text)
	pos := utf16Cursor(text, rbxTextEditor.cursor)
	rbxTextEditor.mu.Unlock()

	if !sendRbxText(handle, full, false, pos) {
		atomic.AddUint64(&rbxTextDelivered.dropped, 1)
		return false
	}
	rbxTextEditor.mu.Lock()
	rbxTextEditor.editCount++
	rbxTextEditor.mu.Unlock()
	textConnection.mu.Lock()
	textConnection.committedCount++
	textConnection.mu.Unlock()
	atomic.AddUint64(&rbxTextDelivered.pass, 1)
	logging.Logger(logging.CatJNI).Info("[jni] text committed",
		"handle", handle, "textLen", len([]rune(full)), "cursor", pos)
	return true
}

// DispatchRobloxTextKey gives the focused editor first refusal on physical
// keys, matching Android focus ownership: when RbxKeyboard is active its
// EditText, not the SurfaceView's nativePassKeyEvent listener, receives key
// events. It applies editing/navigation on presses and consumes matching
// releases. Printable text arrives independently through InputText.
func DispatchRobloxTextKey(keyCode int32, pressed bool) bool {
	rbxTextEditor.mu.Lock()
	if !rbxTextEditor.active || rbxTextEditor.handle == 0 {
		rbxTextEditor.mu.Unlock()
		return false
	}
	if !pressed {
		rbxTextEditor.mu.Unlock()
		return true
	}

	changed, selectionChanged := false, false
	submit := false
	switch keyCode {
	case 67: // AKEYCODE_DEL / Backspace
		if rbxTextEditor.cursor > 0 {
			i := rbxTextEditor.cursor
			rbxTextEditor.text = append(rbxTextEditor.text[:i-1], rbxTextEditor.text[i:]...)
			rbxTextEditor.cursor--
			changed = true
		}
	case 112: // AKEYCODE_FORWARD_DEL
		if rbxTextEditor.cursor < len(rbxTextEditor.text) {
			i := rbxTextEditor.cursor
			rbxTextEditor.text = append(rbxTextEditor.text[:i], rbxTextEditor.text[i+1:]...)
			changed = true
		}
	case 21: // AKEYCODE_DPAD_LEFT
		if rbxTextEditor.cursor > 0 {
			rbxTextEditor.cursor--
			selectionChanged = true
		}
	case 22: // AKEYCODE_DPAD_RIGHT
		if rbxTextEditor.cursor < len(rbxTextEditor.text) {
			rbxTextEditor.cursor++
			selectionChanged = true
		}
	case 122: // AKEYCODE_MOVE_HOME
		if rbxTextEditor.cursor != 0 {
			rbxTextEditor.cursor = 0
			selectionChanged = true
		}
	case 123: // AKEYCODE_MOVE_END
		if rbxTextEditor.cursor != len(rbxTextEditor.text) {
			rbxTextEditor.cursor = len(rbxTextEditor.text)
			selectionChanged = true
		}
	case 66, 160: // Enter / numpad Enter
		if rbxTextEditor.multiline {
			i := rbxTextEditor.cursor
			rbxTextEditor.text = append(rbxTextEditor.text, 0)
			copy(rbxTextEditor.text[i+1:], rbxTextEditor.text[i:])
			rbxTextEditor.text[i] = '\n'
			rbxTextEditor.cursor++
			changed = true
		} else {
			submit = true
		}
	}
	handle := rbxTextEditor.handle
	manual := rbxTextEditor.manualFocusRelease
	full := string(rbxTextEditor.text)
	pos := utf16Cursor(rbxTextEditor.text, rbxTextEditor.cursor)
	if submit && !manual {
		for i := range rbxTextEditor.text {
			rbxTextEditor.text[i] = 0
		}
		rbxTextEditor.active = false
		rbxTextEditor.handle = 0
		rbxTextEditor.text = nil
		rbxTextEditor.cursor = 0
	}
	rbxTextEditor.mu.Unlock()

	if submit {
		if sendRbxReturnPressed(handle) {
			atomic.AddUint64(&rbxTextDelivered.returns, 1)
		}
		if !manual {
			if sendRbxText(handle, full, true, pos) {
				atomic.AddUint64(&rbxTextDelivered.pass, 1)
			}
			textConnection.mu.Lock()
			textConnection.active = false
			textConnection.mu.Unlock()
		}
		rbxTextEditor.mu.Lock()
		rbxTextEditor.returnCount++
		rbxTextEditor.mu.Unlock()
		return true
	}
	if changed {
		if sendRbxText(handle, full, false, pos) {
			rbxTextEditor.mu.Lock()
			rbxTextEditor.editCount++
			rbxTextEditor.mu.Unlock()
			textConnection.mu.Lock()
			textConnection.committedCount++
			textConnection.mu.Unlock()
			atomic.AddUint64(&rbxTextDelivered.pass, 1)
		}
	} else if selectionChanged && sendRbxSelection(full, pos) {
		atomic.AddUint64(&rbxTextDelivered.sync, 1)
	}
	return true
}

func sendRbxText(handle int64, text string, submit bool, cursor int) bool {
	// RbxKeyboard's TextWatcher and editor-action listener both call k()
	// immediately before nativePassText. k() invokes this exact selection
	// sync with the complete EditText snapshot; preserve that APK order.
	if sendRbxSelection(text, cursor) {
		atomic.AddUint64(&rbxTextDelivered.sync, 1)
	}
	rbxTextTarget.mu.RLock()
	defer rbxTextTarget.mu.RUnlock()
	if rbxTextTarget.env == nil || rbxTextTarget.class == 0 || rbxTextTarget.passFn == 0 || handle == 0 {
		return false
	}
	str := rbxTextTarget.env.NewString(text)
	if str == 0 {
		return false
	}
	var z C.uchar
	var zArg uintptr
	if submit {
		z = 1
		zArg = 1
	}
	if rbxTextTarget.call != nil {
		rbxTextTarget.call(rbxTextTarget.passFn,
			rbxTextTarget.env.Raw(), rbxTextTarget.class, uintptr(handle), str,
			zArg, uintptr(cursor), 0, 0)
		return true
	}
	C.tipsy_rbx_pass_text(unsafe.Pointer(rbxTextTarget.passFn),
		C.uintptr_t(rbxTextTarget.env.Raw()), C.uintptr_t(rbxTextTarget.class),
		C.longlong(handle), C.jobject(unsafe.Pointer(str)), z, C.int(cursor))
	return true
}

func sendRbxReturnPressed(handle int64) bool {
	rbxTextTarget.mu.RLock()
	defer rbxTextTarget.mu.RUnlock()
	if rbxTextTarget.env == nil || rbxTextTarget.class == 0 || rbxTextTarget.returnFn == 0 || handle == 0 {
		atomic.AddUint64(&rbxTextDelivered.dropped, 1)
		return false
	}
	if rbxTextTarget.call != nil {
		rbxTextTarget.call(rbxTextTarget.returnFn,
			rbxTextTarget.env.Raw(), rbxTextTarget.class, uintptr(handle), 0, 0, 0, 0, 0)
		return true
	}
	C.tipsy_rbx_return_pressed(unsafe.Pointer(rbxTextTarget.returnFn),
		C.uintptr_t(rbxTextTarget.env.Raw()), C.uintptr_t(rbxTextTarget.class), C.longlong(handle))
	return true
}

func sendRbxSelection(text string, cursor int) bool {
	rbxTextTarget.mu.RLock()
	defer rbxTextTarget.mu.RUnlock()
	if rbxTextTarget.env == nil || rbxTextTarget.class == 0 || rbxTextTarget.syncFn == 0 {
		return false
	}
	str := rbxTextTarget.env.NewString(text)
	if str == 0 {
		return false
	}
	if rbxTextTarget.call != nil {
		rbxTextTarget.call(rbxTextTarget.syncFn,
			rbxTextTarget.env.Raw(), rbxTextTarget.class, str, uintptr(cursor), 0, 0, 0, 0)
		return true
	}
	C.tipsy_rbx_sync_selection(unsafe.Pointer(rbxTextTarget.syncFn),
		C.uintptr_t(rbxTextTarget.env.Raw()), C.uintptr_t(rbxTextTarget.class),
		C.jobject(unsafe.Pointer(str)), C.int(cursor))
	return true
}

// RbxTextInputState exposes privacy-safe diagnostics only. Neither current nor
// historical text content leaves the adapter.
func RbxTextInputState() (active bool, handle int64, textLen, cursorUTF16 int, edits, returns uint64) {
	rbxTextEditor.mu.Lock()
	defer rbxTextEditor.mu.Unlock()
	return rbxTextEditor.active, rbxTextEditor.handle, len(rbxTextEditor.text),
		utf16Cursor(rbxTextEditor.text, rbxTextEditor.cursor),
		rbxTextEditor.editCount, rbxTextEditor.returnCount
}

// RbxTextDeliveryStats reports content-free delivery counters.
func RbxTextDeliveryStats() (pass, returns, sync, dropped uint64) {
	return atomic.LoadUint64(&rbxTextDelivered.pass),
		atomic.LoadUint64(&rbxTextDelivered.returns),
		atomic.LoadUint64(&rbxTextDelivered.sync),
		atomic.LoadUint64(&rbxTextDelivered.dropped)
}

// textConnection is the Tipsy-owned GameActivity InputConnection contract:
// one real InputConnection Java object (VM object id, opaque 64-bit, never
// an engine pointer) whose active flag follows the engine's own
// showKeyboard/hideKeyboard focus announcements, plus receive-and-record
// counts for the engine→Java State contract (setState,
// setSoftKeyboardActive, restartInput). The State argument object is
// recorded as an opaque reference id only: its fields/strings/bytes can
// carry user text and are never read, stored, or logged. committedCount is
// incremented only by edits from the separate, showKeyboard-owned
// RbxKeyboard editor. Physical nativePassKeyEvent delivery is untouched
// when no text editor owns focus.
var textConnection struct {
	mu                sync.Mutex
	connID            int64
	tipsyHandle       uint64
	nextHandle        uint64
	active            bool
	setStateCount     uint64
	lastStateRef      int64
	softKeyboardCount uint64
	lastSoftActive    bool
	lastSoftCounter   int32
	restartCount      uint64
	committedCount    uint64
}

// EnsureTextInputConnection returns the Tipsy-owned InputConnection object
// id, creating the real object on first use. The id is a Tipsy VM object
// handle (opaque 64-bit), never an engine pointer and never fabricated
// engine memory. A nil VM degrades to 0, never a fabricated value. The
// GameActivity/Runtime owner passes this id (with the engine's own J
// handle) to the registered setInputConnectionNative(J,InputConnection)V
// once that wiring lands; this function itself calls nothing engine-side.
func (vm *VM) EnsureTextInputConnection() int64 {
	if vm == nil {
		return 0
	}
	textConnection.mu.Lock()
	existing := textConnection.connID
	textConnection.mu.Unlock()
	if existing != 0 {
		return existing
	}
	vm.mu.Lock()
	connCls := vm.ensureClassLocked(gameTextInputConnectionClass)
	vm.ensureClassLocked(gameTextInputStateClass)
	o := vm.newObjectLocked(connCls)
	id := o.id
	vm.mu.Unlock()
	textConnection.mu.Lock()
	if textConnection.connID == 0 {
		textConnection.nextHandle++
		textConnection.connID = id
		textConnection.tipsyHandle = textConnection.nextHandle
		handle := textConnection.tipsyHandle
		textConnection.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] textConnection",
			"conn", id,
			"tipsyHandle", handle)
		return id
	}
	id = textConnection.connID
	textConnection.mu.Unlock()
	return id
}

// noteTextFocus drives connection activation from the engine's own
// showKeyboard/hideKeyboard focus signals and nothing else: show ensures
// the real object exists and marks it active; hide marks it inactive and
// keeps the stable object. No text action is taken. Safe on a nil VM
// (records the flag; object creation degrades to a no-op).
func (vm *VM) noteTextFocus(active bool) {
	if active && vm != nil {
		vm.EnsureTextInputConnection()
	}
	textConnection.mu.Lock()
	textConnection.active = active
	textConnection.mu.Unlock()
}

// dispatchTextConnection serves the engine→Java GameTextInput contract on
// the InputConnection class: setState(State)V, setSoftKeyboardActive(ZI)V,
// restartInput()V. Each call is received and recorded (counts plus opaque
// aggregates only) with a trigger-gated log line; State content is never
// read, stored, or logged. Everything else falls through to the honest
// stub path. Void semantics mirror dispatchTextInput/dispatchNativeHelper.
func (vm *VM) dispatchTextConnection(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if class != gameTextInputConnectionClass {
		return jnull(), false
	}
	switch {
	case name == "setState" && sig == setStateSig:
		// The single State slot is recorded as an opaque reference id.
		// The referenced object's fields/strings/bytes can carry user
		// text: they are never dereferenced here.
		var ref int64
		if args != nil {
			ref = jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 0)))
		}
		textConnection.mu.Lock()
		textConnection.setStateCount++
		textConnection.lastStateRef = ref
		n := textConnection.setStateCount
		textConnection.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] setState",
			"count", n,
			"stateRef", ref)
	case name == "setSoftKeyboardActive" && sig == setSoftKeyboardActiveSig:
		active := jvalueIAt(args, 0) != 0
		counter := jvalueIAt(args, 1)
		textConnection.mu.Lock()
		textConnection.softKeyboardCount++
		textConnection.lastSoftActive = active
		textConnection.lastSoftCounter = counter
		n := textConnection.softKeyboardCount
		textConnection.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] setSoftKeyboardActive",
			"count", n,
			"active", active,
			"counter", counter)
	case name == "restartInput" && sig == restartInputSig:
		textConnection.mu.Lock()
		textConnection.restartCount++
		n := textConnection.restartCount
		textConnection.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] restartInput",
			"count", n)
	default:
		return jnull(), false
	}
	if o != nil {
		return idToJobject(o.id), true
	}
	return jnull(), true
}

// TextInputConnectionState reports the Tipsy-owned connection: the real
// InputConnection object id (0 = not yet created — no showKeyboard seen),
// the opaque Tipsy handle cookie, and whether the engine's focus signals
// currently hold it active. Zero values mean no connection yet, never a
// fabricated handle.
func TextInputConnectionState() (connID int64, tipsyHandle uint64, active bool) {
	textConnection.mu.Lock()
	defer textConnection.mu.Unlock()
	return textConnection.connID, textConnection.tipsyHandle, textConnection.active
}

// TextInputStateTransitions reports the engine→Java State contract counts:
// setState, setSoftKeyboardActive, and restartInput calls received, plus
// genuine committed edits delivered by the separate RbxKeyboard editor.
// Zero counts mean the corresponding transition has not occurred — never a
// fabricated value.
func TextInputStateTransitions() (setState, softKeyboard, restart, committed uint64) {
	textConnection.mu.Lock()
	defer textConnection.mu.Unlock()
	return textConnection.setStateCount, textConnection.softKeyboardCount,
		textConnection.restartCount, textConnection.committedCount
}

// TextInputLastStateRef reports the opaque reference id of the most recent
// setState State argument (0 = none yet). The referenced object's content
// is never exposed here by construction.
func TextInputLastStateRef() int64 {
	textConnection.mu.Lock()
	defer textConnection.mu.Unlock()
	return textConnection.lastStateRef
}

// TextInputLastSoftKeyboard reports the most recent setSoftKeyboardActive
// aggregates: the opaque active flag and its integer counter. Zeros mean
// no announcement yet.
func TextInputLastSoftKeyboard() (active bool, counter int32) {
	textConnection.mu.Lock()
	defer textConnection.mu.Unlock()
	return textConnection.lastSoftActive, textConnection.lastSoftCounter
}

// TextInputCommitPending reports whether the opaque GameTextInput State path
// has a delta ready. It remains false because State content is never read.
// RbxKeyboard edits use DispatchRobloxTextCommit directly and do not queue
// through this legacy gate.
func TextInputCommitPending() bool {
	return false
}

// resetTextInputConnectionForTest clears the Tipsy-owned connection state
// (object handle, activation, and transition counts). Test seam only;
// production never resets within a process. Keyboard announcements are
// untouched.
func resetTextInputConnectionForTest() {
	textConnection.mu.Lock()
	textConnection.connID = 0
	textConnection.tipsyHandle = 0
	textConnection.nextHandle = 0
	textConnection.active = false
	textConnection.setStateCount = 0
	textConnection.lastStateRef = 0
	textConnection.softKeyboardCount = 0
	textConnection.lastSoftActive = false
	textConnection.lastSoftCounter = 0
	textConnection.restartCount = 0
	textConnection.committedCount = 0
	textConnection.mu.Unlock()
	wipeRbxTextEditor()
	rbxTextEditor.mu.Lock()
	rbxTextEditor.editCount = 0
	rbxTextEditor.returnCount = 0
	rbxTextEditor.mu.Unlock()
	atomic.StoreUint64(&rbxTextDelivered.pass, 0)
	atomic.StoreUint64(&rbxTextDelivered.returns, 0)
	atomic.StoreUint64(&rbxTextDelivered.sync, 0)
	atomic.StoreUint64(&rbxTextDelivered.dropped, 0)
	C.tipsy_rbx_rec_reset()
}

// testPackKeyboardArgs packs the four showKeyboard argument slots for tests
// (test files cannot import "C" in this package): J handle, Z flag as an
// int slot, [B object id, and one NativeTextBoxInfo object id.
func testPackKeyboardArgs(handle int64, flag int32, initID, boxesID int64) *C.jvalue {
	sl := make([]C.jvalue, 4)
	C.tipsy_jvalue_set_j(&sl[0], C.jlong(handle))
	C.tipsy_jvalue_set_i(&sl[1], C.jint(flag))
	C.tipsy_jvalue_set_l(&sl[2], idToJobject(initID))
	C.tipsy_jvalue_set_l(&sl[3], idToJobject(boxesID))
	return &sl[0]
}

// Test-only ABI witness accessors. _test.go files cannot import C directly.
func testRbxRecordPassFn() uintptr   { return uintptr(C.tipsy_rbx_record_pass_fn()) }
func testRbxRecordReturnFn() uintptr { return uintptr(C.tipsy_rbx_record_return_fn()) }
func testRbxRecordSyncFn() uintptr   { return uintptr(C.tipsy_rbx_record_sync_fn()) }
func testRbxRecHandle() uint64       { return uint64(C.tipsy_rbx_rec_handle_get()) }
func testRbxRecText() uintptr        { return uintptr(C.tipsy_rbx_rec_text_get()) }
func testRbxRecSubmit() bool         { return C.tipsy_rbx_rec_submit_get() != 0 }
func testRbxRecCursor() int          { return int(C.tipsy_rbx_rec_cursor_get()) }
func testRbxRecPassCount() int       { return int(C.tipsy_rbx_rec_pass_count_get()) }
func testRbxRecReturnCount() int     { return int(C.tipsy_rbx_rec_return_count_get()) }
func testRbxRecSyncCount() int       { return int(C.tipsy_rbx_rec_sync_count_get()) }
func testRbxRecPassSequence() int    { return int(C.tipsy_rbx_rec_pass_sequence_get()) }
func testRbxRecSyncSequence() int    { return int(C.tipsy_rbx_rec_sync_sequence_get()) }
