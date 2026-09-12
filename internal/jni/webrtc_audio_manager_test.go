// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/android"
)

func newWebRtcAudioManager(t *testing.T, vm *VM) *Object {
	t.Helper()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	cls := vm.classes[webRtcAudioManagerClass]
	if cls == nil {
		t.Fatal("WebRtcAudioManager not seeded")
	}
	return vm.newObjectLocked(cls)
}

func installWebRtcRecorder(t *testing.T, vm *VM) {
	t.Helper()
	testWebRtcRecReset()
	t.Cleanup(testWebRtcRecReset)
	vm.testInstallNative(webRtcAudioManagerClass, webRtcCacheAudioParametersName, webRtcCacheAudioParametersSig, testWebRtcRecorderFn())
	if vm.NativeMethod(webRtcAudioManagerClass, webRtcCacheAudioParametersName, webRtcCacheAudioParametersSig) == 0 {
		t.Fatal("test native not installed")
	}
}

// The constructor must call the registered native synchronously, with the
// exact upstream argument order (IIIZZZZZZZIIJ) and the honest host values.
// The recorder has the real JNI prototype, so stack placement of arguments
// seven onward is proven, not assumed.
func TestWebRtcAudioManagerConstructorCachesParameters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetWebRtcAudioManagerLogsForTest()
	installWebRtcRecorder(t, vm)
	logs := captureLogs(t)

	o := newWebRtcAudioManager(t, vm)
	const handle = int64(0x7f00_5150_1234)
	v, handled := vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(handle))
	if !handled {
		t.Fatal("WebRtcAudioManager.<init>(J)V not handled")
	}
	if jobjectToID(uintptr(v)) != o.id {
		t.Fatalf("init return = %d, want receiver %d", jobjectToID(uintptr(v)), o.id)
	}
	rec := testWebRtcRecorded()
	if rec.calls != 1 {
		t.Fatalf("nativeCacheAudioParameters calls = %d, want 1", rec.calls)
	}
	if rec.native != handle {
		t.Fatalf("native handle = %#x, want %#x", rec.native, handle)
	}
	if rec.thiz != uintptr(o.id) {
		t.Fatalf("thiz = %#x, want object %d", rec.thiz, o.id)
	}
	if rec.env == 0 || rec.env != testWebRtcEnvPtr(vm) {
		t.Fatalf("env = %#x, want current JNIEnv %#x", rec.env, testWebRtcEnvPtr(vm))
	}
	want := hostWebRtcAudioParameters()
	if rec.params != want {
		t.Fatalf("parameters = %+v, want %+v", rec.params, want)
	}
	// Pin the honest values themselves: 48 kHz mono, no HW effects, OpenSL
	// both ways, no AAudio, one 10 ms block per buffer.
	if want.SampleRate != 48000 || want.OutputChannels != 1 || want.InputChannels != 1 {
		t.Fatalf("format changed: %+v", want)
	}
	if want.HardwareAEC || want.HardwareAGC || want.HardwareNS {
		t.Fatalf("hardware effects must be false: %+v", want)
	}
	if !want.LowLatencyOutput || !want.LowLatencyInput {
		t.Fatalf("low-latency flags select the OpenSL path and must be true: %+v", want)
	}
	if want.ProAudio || want.AAudio {
		t.Fatalf("proAudio/aAudio must be false: %+v", want)
	}
	if want.OutputBufferSize != 480 || want.InputBufferSize != 480 {
		t.Fatalf("frames per buffer = %d/%d, want 480", want.OutputBufferSize, want.InputBufferSize)
	}
	vm.mu.RLock()
	stored, _ := o.fields[webRtcNativeHandleField].(int64)
	vm.mu.RUnlock()
	if stored != handle {
		t.Fatalf("stored handle = %#x, want %#x", stored, handle)
	}

	// A second instance calls the native again but logs once.
	o2 := newWebRtcAudioManager(t, vm)
	vm.dispatch(idToJobject(o2.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(handle+1))
	if rec = testWebRtcRecorded(); rec.calls != 2 || rec.native != handle+1 {
		t.Fatalf("second construction: calls=%d native=%#x", rec.calls, rec.native)
	}
	out := logs.String()
	if got := strings.Count(out, "WebRtcAudioManager parameters cached"); got != 1 {
		t.Fatalf("parameters-cached logs = %d, want 1: %s", got, out)
	}
	if !strings.Contains(out, "sampleRate=48000") || !strings.Contains(out, "hardwareAEC=false") || !strings.Contains(out, "lowLatencyInput=true") {
		t.Fatalf("parameter log missing values: %s", out)
	}
	if strings.Contains(out, "stub-dispatch") || strings.Contains(out, "missing method") {
		t.Fatalf("constructor fell through to a stub: %s", out)
	}
}

// Constructed before RegisterNatives: nothing to call, say so, do not fake.
func TestWebRtcAudioManagerConstructorWithoutNative(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetWebRtcAudioManagerLogsForTest()
	testWebRtcRecReset()
	logs := captureLogs(t)
	o := newWebRtcAudioManager(t, vm)
	_, handled := vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(9))
	if !handled {
		t.Fatal("<init> without native must still be handled")
	}
	if rec := testWebRtcRecorded(); rec.calls != 0 {
		t.Fatalf("native called without registration: %+v", rec)
	}
	if !strings.Contains(logs.String(), "before nativeCacheAudioParameters was registered") {
		t.Fatalf("missing honest not-registered log: %s", logs.String())
	}
}

// Generic constructors keep the no-op handler; only this class reaches the
// native.
func TestWebRtcAudioManagerGenericInitUnchanged(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	installWebRtcRecorder(t, vm)
	f := newPermissionReceiver(t, vm, "java/io/File")
	if _, handled := vm.dispatch(idToJobject(f.id), "java/io/File", "<init>", "(J)V", testRtcInitArgs(1)); !handled {
		t.Fatal("generic <init> must still be handled")
	}
	if rec := testWebRtcRecorded(); rec.calls != 0 {
		t.Fatal("generic <init> reached nativeCacheAudioParameters")
	}
	if _, ok := f.fields[webRtcNativeHandleField]; ok {
		t.Fatal("generic <init> stored a WebRTC handle")
	}
}

func TestWebRtcAudioManagerInitDisposeBlacklist(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetWebRtcAudioManagerLogsForTest()
	resetStubDispatchForTest()
	installWebRtcRecorder(t, vm)
	logs := captureLogs(t)
	o := newWebRtcAudioManager(t, vm)
	vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(3))

	v, handled := vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "init", "()Z", nil)
	if !handled || uintptr(v) != 1 {
		t.Fatalf("init()Z = (%d, %v), want (1, true)", uintptr(v), handled)
	}
	vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "init", "()Z", nil)
	vm.mu.RLock()
	initialized, _ := o.fields[webRtcInitializedField].(bool)
	vm.mu.RUnlock()
	if !initialized {
		t.Fatal("init did not mark the manager initialized")
	}

	v, handled = vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "isDeviceBlacklistedForOpenSLESUsage", "()Z", nil)
	if !handled || uintptr(v) != 0 {
		t.Fatalf("isDeviceBlacklistedForOpenSLESUsage = (%d, %v), want (0, true)", uintptr(v), handled)
	}

	_, handled = vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "dispose", "()V", nil)
	if !handled {
		t.Fatal("dispose()V not handled")
	}
	vm.mu.RLock()
	initialized, _ = o.fields[webRtcInitializedField].(bool)
	vm.mu.RUnlock()
	if initialized {
		t.Fatal("dispose left the manager initialized")
	}

	// The real JNIEnv path: CallBooleanMethod through an interned methodID
	// must return jboolean 1 and bind the family handler, not a stub.
	mid, _ := internMethod(webRtcAudioManagerClass, "init", "()Z", false)
	if out := vm.callA(o.id, mid, 0, 'Z'); out.i != 1 {
		t.Fatalf("CallBooleanMethod(init) = %d, want 1", out.i)
	}
	if out := vm.callA(o.id, mid, 0, 'Z'); out.i != 1 {
		t.Fatalf("bound CallBooleanMethod(init) = %d, want 1", out.i)
	}

	out := logs.String()
	if got := strings.Count(out, "[jni] WebRtcAudioManager init"); got != 1 {
		t.Fatalf("init logs = %d, want 1: %s", got, out)
	}
	if got := strings.Count(out, "[jni] WebRtcAudioManager dispose"); got != 1 {
		t.Fatalf("dispose logs = %d, want 1: %s", got, out)
	}
	if strings.Contains(out, "stub-dispatch") {
		t.Fatalf("a WebRtcAudioManager method fell through to a stub: %s", out)
	}
}

// isCommunicationModeEnabled stays false: AppRtcDeviceWrapper is never
// invoked on this client, and live voice does not put AudioManager into
// MODE_IN_COMMUNICATION.
func TestWebRtcAudioManagerCommunicationModeOff(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetWebRtcAudioManagerLogsForTest()
	installWebRtcRecorder(t, vm)
	mgr := newWebRtcAudioManager(t, vm)
	vm.dispatch(idToJobject(mgr.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(4))
	v, handled := vm.dispatch(idToJobject(mgr.id), webRtcAudioManagerClass, "isCommunicationModeEnabled", "()Z", nil)
	if !handled || uintptr(v) != 0 {
		t.Fatal("isCommunicationModeEnabled must stay false")
	}
}

func TestWebRtcAudioManagerMicrophoneMute(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetWebRtcAudioManagerLogsForTest()
	installWebRtcRecorder(t, vm)
	android.SetCaptureMuted(false)
	t.Cleanup(func() { android.SetCaptureMuted(false) })
	logs := captureLogs(t)
	o := newWebRtcAudioManager(t, vm)
	vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(5))

	_, handled := vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "setMicrophoneMute", "(Z)V", testRtcMuteArgs(true))
	if !handled || !android.CaptureMuted() {
		t.Fatal("setMicrophoneMute(true) did not set OpenSL capture mute")
	}
	vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "setMicrophoneMute", "(Z)V", testRtcMuteArgs(true))
	if !android.CaptureMuted() {
		t.Fatal("idempotent mute cleared OpenSL capture mute")
	}
	_, handled = vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "setMicrophoneMute", "(Z)V", testRtcMuteArgs(false))
	if !handled || android.CaptureMuted() {
		t.Fatal("setMicrophoneMute(false) did not clear OpenSL capture mute")
	}
	out := logs.String()
	if got := strings.Count(out, "WebRtcAudioManager microphone mute"); got != 2 {
		t.Fatalf("mute logs = %d, want 2 (true, false): %s", got, out)
	}
}

func TestWebRtcAudioManagerImplementedMethods(t *testing.T) {
	for _, pair := range [][2]string{
		{"init", "()Z"},
		{"dispose", "()V"},
		{"isCommunicationModeEnabled", "()Z"},
		{"setMicrophoneMute", "(Z)V"},
		{"isDeviceBlacklistedForOpenSLESUsage", "()Z"},
	} {
		if !isImplementedMethod(pair[0], pair[1]) {
			t.Fatalf("%s%s not in implementedMethods", pair[0], pair[1])
		}
	}
	if isImplementedMethod("<init>", "(J)V") {
		t.Fatal("<init>(J)V must not be a global implementedMethods entry")
	}
	for _, pair := range [][2]string{
		{"wrapStartCommunication", "()V"},
		{"wrapStopCommunication", "()V"},
		{"wrapSetCommunicationMute", "(Z)V"},
		{"getSelectedAudioDeviceAsInt", "()I"},
		{"getSelectedAudioDeviceName", "()Ljava/lang/String;"},
	} {
		if isImplementedMethod(pair[0], pair[1]) {
			t.Fatalf("unused AppRtc method %s%s still in implementedMethods", pair[0], pair[1])
		}
	}
}

// Other WebRTC Java classes stay loud-missing: no AudioRecord/AudioTrack
// role is invented here.
func TestWebRtcAudioManagerWrongClassUnhandled(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	file := newPermissionReceiver(t, vm, "java/io/File")
	if _, handled := vm.dispatch(idToJobject(file.id), "java/io/File", "init", "()Z", nil); handled {
		t.Fatal("init()Z handled on File")
	}
	rtc := newPermissionReceiver(t, vm, "com/roblox/audio/AppRtcDeviceWrapper")
	if _, handled := vm.dispatch(idToJobject(rtc.id), "com/roblox/audio/AppRtcDeviceWrapper", "setMicrophoneMute", "(Z)V", testRtcMuteArgs(true)); handled {
		t.Fatal("setMicrophoneMute handled on AppRtcDeviceWrapper")
	}
	track := newPermissionReceiver(t, vm, "org/webrtc/voiceengine/WebRtcAudioTrack")
	if _, handled := vm.dispatch(idToJobject(track.id), "org/webrtc/voiceengine/WebRtcAudioTrack", "initPlayout", "(IID)I", nil); handled {
		t.Fatal("WebRtcAudioTrack.initPlayout must stay loud-missing")
	}
}

func TestWebRtcAudioManagerNoSecretsInLogs(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetWebRtcAudioManagerLogsForTest()
	installWebRtcRecorder(t, vm)
	logs := captureLogs(t)
	o := newWebRtcAudioManager(t, vm)
	vm.mu.Lock()
	o.fields["tipsy.planted"] = "PCM-FAKE-4242 private audio buffer content"
	vm.mu.Unlock()
	vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "<init>", "(J)V", testRtcInitArgs(0x4242))
	vm.dispatch(idToJobject(o.id), webRtcAudioManagerClass, "init", "()Z", nil)
	out := logs.String()
	for _, banned := range []string{"PCM-FAKE-4242", "private audio", "0x4242", "16962"} {
		if strings.Contains(out, banned) {
			t.Fatalf("banned %q in logs: %s", banned, out)
		}
	}
}

func TestVoiceEngineFindClassSeeds(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)
	env := vm.Env()
	for _, n := range []string{
		"com/roblox/audio/AppRtcDeviceWrapper",
		"com/roblox/audio/WebRtcLoader",
		webRtcAudioManagerClass,
		"org/webrtc/voiceengine/WebRtcAudioRecord",
		"org/webrtc/voiceengine/WebRtcAudioTrack",
		"org/webrtc/voiceengine/WebRtcAudioUtils",
	} {
		if env.FindClass(n) == 0 {
			t.Fatalf("seeded class missing: %s", n)
		}
		if strings.Contains(buf.String(), "[jni] auto-class: "+n) {
			t.Fatalf("seeded class logged as auto-class: %s", n)
		}
	}
}

func TestWebRtcAudioRecordStaysLoudMissing(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	logs := captureLogs(t)
	rec := newPermissionReceiver(t, vm, "org/webrtc/voiceengine/WebRtcAudioRecord")
	if _, handled := vm.dispatch(idToJobject(rec.id), "org/webrtc/voiceengine/WebRtcAudioRecord", "startRecording", "()Z", nil); handled {
		t.Fatal("WebRtcAudioRecord.startRecording must stay loud-missing")
	}
	_, handled := callDispatchOrStub(vm, idToJobject(rec.id), "org/webrtc/voiceengine/WebRtcAudioRecord", "startRecording", "()Z", nil, 'Z')
	if handled {
		t.Fatal("WebRtcAudioRecord.startRecording reported handled")
	}
	if !strings.Contains(logs.String(), "stub-dispatch") {
		t.Fatalf("unknown WebRTC method was silent: %s", logs.String())
	}
}
