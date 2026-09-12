// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
#include "webrtc_audio_manager.h"
*/
import "C"

import (
	"sync/atomic"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Java role of WebRTC's legacy Android AudioDeviceModule
// (org.webrtc.voiceengine.WebRtcAudioManager, upstream
// modules/audio_device/android/java/.../WebRtcAudioManager.java and the
// C++ owner modules/audio_device/android/audio_manager.cc).
//
// The native AudioManager constructor RegisterNatives
// nativeCacheAudioParameters(IIIZZZZZZZIIJ)V on this class and then
// NewObject("<init>", "(J)V", this). On Android the Java constructor reads the
// device's audio properties and immediately calls that native back; the C++
// side stores the values as playout/record AudioParameters and layer flags,
// and AudioDeviceModuleImpl::CreatePlatformSpecificObjects picks OpenSL ES for
// both directions when lowLatencyOutput && lowLatencyInput (and the device is
// not blacklisted for OpenSL ES). Later adm->Init() needs Java init()Z == true.
//
// Tipsy answers with honest host values: 48 kHz mono both ways, no hardware
// AEC/AGC/NS (WebRTC's software APM does the work; Android also hardcodes
// hardwareAGC=false), low-latency in+out so the engine reaches Tipsy's OpenSL
// bridge (internal/android/opensles.c), no pro-audio, no AAudio (SDK 26 has
// none and Tipsy does not bump it), 480 frames per buffer (one WebRTC 10 ms
// block at 48 kHz). Roblox's fork adds setMicrophoneMute(Z)V; live capture
// is FMOD's OpenSL recorder (§276), which start/stops recording itself, but
// the method is GetMethodID'd on this live class so it stays a real mute
// gate into the same OpenSL recorder.
const webRtcAudioManagerClass = "org/webrtc/voiceengine/WebRtcAudioManager"

const (
	webRtcCacheAudioParametersName = "nativeCacheAudioParameters"
	webRtcCacheAudioParametersSig  = "(IIIZZZZZZZIIJ)V"

	webRtcNativeHandleField = "tipsy.webrtcAudioManager"
	webRtcInitializedField  = "tipsy.webrtcAudioManagerInitialized"
)

// Host audio parameters cached into the native AudioManager. Never HW
// effects: Tipsy has no hardware AEC/NS, and claiming one would make WebRTC
// skip its own echo canceller.
const (
	webRtcSampleRateHz    int32 = 48000
	webRtcOutputChannels  int32 = 1
	webRtcInputChannels   int32 = 1
	webRtcFramesPerBuffer int32 = 480 // 10 ms at 48 kHz: one WebRTC block per OpenSL buffer
)

// webRtcAudioParameters is the argument list of nativeCacheAudioParameters in
// declaration order (minus the trailing native handle).
type webRtcAudioParameters struct {
	SampleRate       int32
	OutputChannels   int32
	InputChannels    int32
	HardwareAEC      bool
	HardwareAGC      bool
	HardwareNS       bool
	LowLatencyOutput bool
	LowLatencyInput  bool
	ProAudio         bool
	AAudio           bool
	OutputBufferSize int32
	InputBufferSize  int32
}

func hostWebRtcAudioParameters() webRtcAudioParameters {
	return webRtcAudioParameters{
		SampleRate:       webRtcSampleRateHz,
		OutputChannels:   webRtcOutputChannels,
		InputChannels:    webRtcInputChannels,
		HardwareAEC:      false,
		HardwareAGC:      false,
		HardwareNS:       false,
		LowLatencyOutput: true,
		LowLatencyInput:  true,
		ProAudio:         false,
		AAudio:           false,
		OutputBufferSize: webRtcFramesPerBuffer,
		InputBufferSize:  webRtcFramesPerBuffer,
	}
}

var (
	webRtcParametersLogged atomic.Bool
	webRtcInitLogged       atomic.Bool
	webRtcDisposeLogged    atomic.Bool
	webRtcNoNativeLogged   atomic.Bool
)

func isWebRtcAudioManager(o *Object, class string) bool {
	return class == webRtcAudioManagerClass || classIs(o, webRtcAudioManagerClass)
}

func (vm *VM) dispatchWebRtcAudioManager(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if !isWebRtcAudioManager(o, class) {
		return jnull(), false
	}
	switch name + sig {
	case "<init>(J)V":
		return vm.constructWebRtcAudioManager(o, args), true
	case "init()Z":
		if o != nil {
			vm.mu.Lock()
			o.fields[webRtcInitializedField] = true
			vm.mu.Unlock()
		}
		if webRtcInitLogged.CompareAndSwap(false, true) {
			logging.Logger(logging.CatJNI).Info("[jni] WebRtcAudioManager init")
		}
		return jniBool(true), true
	case "dispose()V":
		if o != nil {
			vm.mu.Lock()
			o.fields[webRtcInitializedField] = false
			vm.mu.Unlock()
		}
		if webRtcDisposeLogged.CompareAndSwap(false, true) {
			logging.Logger(logging.CatJNI).Info("[jni] WebRtcAudioManager dispose")
		}
		return jnull(), true
	case "isCommunicationModeEnabled()Z":
		// Android: audioManager.getMode() == MODE_IN_COMMUNICATION.
		// AppRtcDeviceWrapper.wrapStartCommunication, which would flip that
		// mode, is never called on this client (live voice is FMOD OpenSL).
		// Upstream only warns when this is false.
		return jniBool(false), true
	case "isDeviceBlacklistedForOpenSLESUsage()Z":
		// WebRtcAudioUtils.BLACKLISTED_OPEN_SL_ES_MODELS is a phone model
		// list; Tipsy's OpenSL bridge is the intended path.
		return jniBool(false), true
	case "setMicrophoneMute(Z)V":
		// Process-wide OpenSL capture mute; logged only when the gate flips.
		muted := jvalueIAt(args, 0) != 0
		prev := android.CaptureMuted()
		android.SetCaptureMuted(muted)
		if prev != muted {
			logging.Logger(logging.CatJNI).Info("[jni] WebRtcAudioManager microphone mute", "muted", muted)
		}
		return jnull(), true
	}
	return jnull(), false
}

// constructWebRtcAudioManager is WebRtcAudioManager(long nativeAudioManager):
// remember the native owner, then synchronously call the registered
// nativeCacheAudioParameters exactly as the Java constructor does. Without a
// registered native there is nothing honest to call; the C++ side will then
// RTC_CHECK its invalid parameters, which is the truthful failure.
func (vm *VM) constructWebRtcAudioManager(o *Object, args *C.jvalue) C.jobject {
	var handle int64
	if args != nil {
		handle = int64(jvalueJ(args))
	}
	thiz := jnull()
	if o != nil {
		vm.mu.Lock()
		o.fields[webRtcNativeHandleField] = handle
		vm.mu.Unlock()
		thiz = idToJobject(o.id)
	}
	fn := vm.NativeMethod(webRtcAudioManagerClass, webRtcCacheAudioParametersName, webRtcCacheAudioParametersSig)
	if fn == 0 {
		if webRtcNoNativeLogged.CompareAndSwap(false, true) {
			logging.Logger(logging.CatJNI).Error("[jni] WebRtcAudioManager constructed before nativeCacheAudioParameters was registered; parameters not cached")
		}
		return thiz
	}
	env := currentEnvPtr()
	if env == nil {
		env = vm.envRaw
	}
	p := hostWebRtcAudioParameters()
	C.tipsy_webrtc_cache_audio_parameters(C.uintptr_t(fn), C.uintptr_t(uintptr(env)), C.uintptr_t(uintptr(thiz)),
		C.int32_t(p.SampleRate), C.int32_t(p.OutputChannels), C.int32_t(p.InputChannels),
		cBool(p.HardwareAEC), cBool(p.HardwareAGC), cBool(p.HardwareNS),
		cBool(p.LowLatencyOutput), cBool(p.LowLatencyInput),
		cBool(p.ProAudio), cBool(p.AAudio),
		C.int32_t(p.OutputBufferSize), C.int32_t(p.InputBufferSize),
		C.int64_t(handle))
	if webRtcParametersLogged.CompareAndSwap(false, true) {
		logging.Logger(logging.CatJNI).Info("[jni] WebRtcAudioManager parameters cached",
			"sampleRate", p.SampleRate,
			"outputChannels", p.OutputChannels,
			"inputChannels", p.InputChannels,
			"hardwareAEC", p.HardwareAEC,
			"hardwareAGC", p.HardwareAGC,
			"hardwareNS", p.HardwareNS,
			"lowLatencyOutput", p.LowLatencyOutput,
			"lowLatencyInput", p.LowLatencyInput,
			"proAudio", p.ProAudio,
			"aAudio", p.AAudio,
			"outputBufferSize", p.OutputBufferSize,
			"inputBufferSize", p.InputBufferSize,
			"hasNativeManager", handle != 0)
	}
	return thiz
}

func cBool(v bool) C.uint8_t {
	if v {
		return 1
	}
	return 0
}

func resetWebRtcAudioManagerLogsForTest() {
	webRtcParametersLogged.Store(false)
	webRtcInitLogged.Store(false)
	webRtcDisposeLogged.Store(false)
	webRtcNoNativeLogged.Store(false)
}

// Test witnesses (Go test files cannot import "C").

type webRtcRecordedCall struct {
	calls  int
	env    uintptr
	thiz   uintptr
	params webRtcAudioParameters
	native int64
}

func testWebRtcRecorderFn() uintptr { return uintptr(C.tipsy_webrtc_record_cache_fn()) }
func testWebRtcRecReset()           { C.tipsy_webrtc_rec_reset() }

func testWebRtcRecorded() webRtcRecordedCall {
	b := func(i int) bool { return C.tipsy_webrtc_rec_bool(C.int(i)) != 0 }
	return webRtcRecordedCall{
		calls: int(C.tipsy_webrtc_rec_count()),
		env:   uintptr(C.tipsy_webrtc_rec_env()),
		thiz:  uintptr(C.tipsy_webrtc_rec_thiz()),
		params: webRtcAudioParameters{
			SampleRate:       int32(C.tipsy_webrtc_rec_int(0)),
			OutputChannels:   int32(C.tipsy_webrtc_rec_int(1)),
			InputChannels:    int32(C.tipsy_webrtc_rec_int(2)),
			HardwareAEC:      b(0),
			HardwareAGC:      b(1),
			HardwareNS:       b(2),
			LowLatencyOutput: b(3),
			LowLatencyInput:  b(4),
			ProAudio:         b(5),
			AAudio:           b(6),
			OutputBufferSize: int32(C.tipsy_webrtc_rec_int(3)),
			InputBufferSize:  int32(C.tipsy_webrtc_rec_int(4)),
		},
		native: int64(C.tipsy_webrtc_rec_native()),
	}
}

// testInstallNative plants a RegisterNatives entry the way GoJNI_RegisterNatives
// does, without a C JNINativeMethod table.
func (vm *VM) testInstallNative(class, name, sig string, fn uintptr) {
	vm.mu.Lock()
	vm.natives[methodLogName(class, name, sig)] = fn
	vm.mu.Unlock()
}

func testWebRtcEnvPtr(vm *VM) uintptr {
	env := currentEnvPtr()
	if env == nil {
		env = vm.envRaw
	}
	return uintptr(env)
}

func testRtcInitArgs(handle int64) *C.jvalue {
	return packJlong(handle)
}

func testRtcMuteArgs(muted bool) *C.jvalue {
	a := new([1]C.jvalue)
	v := int32(0)
	if muted {
		v = 1
	}
	jvalueSetI(&a[0], C.jint(v))
	return &a[0]
}
