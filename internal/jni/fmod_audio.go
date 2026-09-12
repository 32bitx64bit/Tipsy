// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo LDFLAGS: -lpulse-simple -lpulse
#include "jni_bridge.h"
#include <pulse/simple.h>
#include <pulse/error.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const fmodAudioClass = "org/fmod/AudioDevice"

// Host output figures answered to org/fmod/FMOD.getOutputSampleRate()I and
// getOutputBlockSize()I (Android: AudioManager PROPERTY_OUTPUT_SAMPLE_RATE /
// PROPERTY_OUTPUT_FRAMES_PER_BUFFER). PipeWire's shipped defaults
// (default.clock.rate / default.clock.quantum); the OpenSL bridge opens the
// Pulse endpoint at whatever rate the client then asks for.
const (
	fmodHostOutputSampleRate  = 48000
	fmodHostOutputBlockFrames = 1024
)

// TIPSY_FMOD_OUTPUT=audiotrack keeps FMOD on its Java AudioTrack output (the
// path verified since §104) by answering "no low-latency output" to both
// FMOD and PackageManager. Diagnostics only: AudioTrack has no recording, so
// voice input is off in that mode. Anything else (default) describes the
// host as it is.
const fmodOutputEnv = "TIPSY_FMOD_OUTPUT"

// hostAudioLowLatency reports whether Tipsy describes the host output as
// low-latency (Android PackageManager FEATURE_AUDIO_LOW_LATENCY,
// "android.hardware.audio.low_latency"). The host bridge is PipeWire with a
// 1024-frame quantum; OpenSL players open Pulse with a 50 ms target; and the
// WebRTC ADM parameters (§275) already assert low-latency output and input.
// PackageManager and org/fmod/FMOD.supportsLowLatency must not disagree.
func hostAudioLowLatency() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(fmodOutputEnv)), "audiotrack")
}

// fmodSupportsLowLatency mirrors org.fmod.FMOD.supportsLowLatency(): the
// device declares FEATURE_AUDIO_LOW_LATENCY, the output block size is known
// and at most 1024 frames, and no Bluetooth output (Tipsy tracks none).
// FMOD's Android autodetect picks its OpenSL ES output when this is true and
// AAudio is unavailable; otherwise AudioTrack, which has no recording.
func fmodSupportsLowLatency() bool {
	return hostAudioLowLatency() &&
		fmodHostOutputBlockFrames > 0 && fmodHostOutputBlockFrames <= 1024
}

// FmodInit plays the Java side of org.fmod.FMOD.init(Context): it stores the
// application Context in the static gContext that checkInit()Z reads. The
// official APK does this itself, natively unobservable, in
// com.roblox.client.startup.NativeHelper (classes2.dex: NativeHelper.P
// invoke-static org/fmod/FMOD.init(Context)) at startup; Tipsy runs no DEX,
// so the call has to be made here at the same phase. FMOD's Android output
// selection (run 282, 2026-09-12) is checkInit → supportsAAudio → …: with
// checkInit false it never evaluates supportsLowLatency() (whose Java body
// needs gContext) and settles on AudioTrack, which has no recording.
// Returns false, with nothing stored, when the context is not a live VM
// object — no fake initialized state.
func (vm *VM) FmodInit(contextObj uintptr) bool {
	vm.mu.Lock()
	cls := vm.classes["org/fmod/FMOD"]
	ok := contextObj != 0 && vm.objects[int64(contextObj)] != nil && cls != nil && cls.obj != nil
	if ok {
		cls.obj.fields["gContext"] = int64(contextObj)
	}
	vm.mu.Unlock()
	if ok {
		logging.Logger(logging.CatAudio).Info("[audio] FMOD Java init", "method", "org.fmod.FMOD.init(Context)", "context", contextObj)
	} else {
		logging.Logger(logging.CatAudio).Info("[audio] FMOD Java init skipped: no live Context object", "context", contextObj)
	}
	return ok
}

var fmodHelperLogOnce sync.Map

// fmodHelperBool answers a boolean org/fmod/FMOD helper and logs it once.
func fmodHelperBool(name string, v bool) C.jobject {
	if v {
		logFmodHelper(name, 1)
		return idToJobject(1)
	}
	logFmodHelper(name, 0)
	return jnull()
}

// logFmodHelper records each org/fmod/FMOD helper answer once so the
// output-selection inputs FMOD saw are in Tipsy's log next to the resulting
// AudioDevice.init or OpenSL engine line.
func logFmodHelper(name string, value int64) {
	if _, seen := fmodHelperLogOnce.LoadOrStore(name, true); seen {
		return
	}
	logging.Logger(logging.CatAudio).Info("[audio] FMOD helper answered", "method", name, "value", value)
}

type fmodPlayback interface {
	write([]byte) error
	close()
}

type fmodAudioFormat struct {
	channels, rate, bufferBytes int32
}

// The APK's AudioDevice.init arguments are channels, rate, DSP block frames,
// and block count. AudioTrack is always streaming signed 16-bit PCM here.
func fmodFormat(channels, rate, frames, blocks int32) (fmodAudioFormat, error) {
	// The observed output is stereo. Surround needs an explicit Android to
	// Pulse channel map before it can be advertised as supported.
	if (channels != 1 && channels != 2) ||
		rate < 8000 || rate > 384000 || frames <= 0 || blocks <= 0 {
		return fmodAudioFormat{}, errors.New("unsupported PCM configuration")
	}
	bytes := int64(frames) * int64(blocks)
	if bytes > (16<<20)/int64(channels*2) {
		return fmodAudioFormat{}, errors.New("PCM buffer exceeds limit")
	}
	return fmodAudioFormat{channels, rate, int32(bytes) * channels * 2}, nil
}

type fmodPulsePlayback struct{ stream *C.pa_simple }

func openFmodPlayback(format fmodAudioFormat) (fmodPlayback, error) {
	sample := C.pa_sample_spec{format: C.PA_SAMPLE_S16LE, rate: C.uint32_t(format.rate), channels: C.uint8_t(format.channels)}
	if C.pa_sample_spec_valid(&sample) == 0 {
		return nil, errors.New("invalid PulseAudio sample specification")
	}
	app, name := C.CString("Tipsy"), C.CString("Roblox audio")
	defer C.free(unsafe.Pointer(app))
	defer C.free(unsafe.Pointer(name))
	// Preserve the client buffer request; the server chooses its minimum
	// request quantum. No capture API is reachable from this output bridge.
	attr := C.pa_buffer_attr{maxlength: ^C.uint32_t(0), tlength: C.uint32_t(format.bufferBytes),
		prebuf: ^C.uint32_t(0), minreq: ^C.uint32_t(0), fragsize: ^C.uint32_t(0)}
	var code C.int
	stream := C.pa_simple_new(nil, app, C.PA_STREAM_PLAYBACK, nil, name, &sample, nil, &attr, &code)
	if stream == nil {
		return nil, fmt.Errorf("PulseAudio: %s", C.GoString(C.pa_strerror(code)))
	}
	return &fmodPulsePlayback{stream}, nil
}

func (p *fmodPulsePlayback) write(data []byte) error {
	var code C.int
	if C.pa_simple_write(p.stream, unsafe.Pointer(&data[0]), C.size_t(len(data)), &code) < 0 {
		return fmt.Errorf("PulseAudio: %s", C.GoString(C.pa_strerror(code)))
	}
	return nil
}

func (p *fmodPulsePlayback) close() { C.pa_simple_free(p.stream) }

// The official Java AudioTrack.write blocks on FMOD's audio thread. Match
// that backpressure; do not start an unbounded goroutine/PCM queue. The device
// mutex serializes init/write/close without holding the VM mutex during I/O.
type fmodAudioDevice struct {
	mu          sync.Mutex
	format      fmodAudioFormat
	stream      fmodPlayback
	open        func(fmodAudioFormat) (fmodPlayback, error)
	flowLogged  bool
	errorLogged bool
}

func (d *fmodAudioDevice) initialize(format fmodAudioFormat) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
	open := d.open
	if open == nil {
		open = openFmodPlayback
	}
	stream, err := open(format)
	if err != nil {
		return err
	}
	d.format, d.stream, d.flowLogged, d.errorLogged = format, stream, false, false
	logging.Logger(logging.CatAudio).Info("[audio] FMOD playback stream opened",
		"rate", format.rate, "channels", format.channels, "bufferBytes", format.bufferBytes)
	return nil
}

func (d *fmodAudioDevice) write(data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stream == nil {
		return errors.New("playback device is closed")
	}
	if len(data) == 0 {
		return nil
	}
	if len(data)%int(d.format.channels*2) != 0 {
		return errors.New("unaligned PCM buffer")
	}
	if err := d.stream.write(data); err != nil {
		d.closeLocked()
		return err
	}
	if !d.flowLogged {
		d.flowLogged = true
		logging.Logger(logging.CatAudio).Info("[audio] FMOD playback flowing", "bytes", len(data))
	}
	return nil
}

func (d *fmodAudioDevice) closeLocked() {
	if d.stream != nil {
		d.stream.close()
		d.stream = nil
	}
}

func (d *fmodAudioDevice) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
}

func (d *fmodAudioDevice) logWriteError(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.errorLogged {
		d.errorLogged = true
		logging.Logger(logging.CatAudio).Error("[audio] FMOD playback write failed", "error", err)
	}
}

func (vm *VM) fmodDevice(o *Object) *fmodAudioDevice {
	if o == nil {
		return nil
	}
	vm.mu.RLock()
	if d, ok := o.fields["tipsy.fmodPlayback"].(*fmodAudioDevice); ok {
		vm.mu.RUnlock()
		return d
	}
	vm.mu.RUnlock()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if d, ok := o.fields["tipsy.fmodPlayback"].(*fmodAudioDevice); ok {
		return d
	}
	d := &fmodAudioDevice{}
	o.fields["tipsy.fmodPlayback"] = d
	return d
}

func (vm *VM) dispatchFmodAudio(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	// These two predicates were called before the observed AudioDevice
	// fallback. Keep the APK semantics: no invented initialized context or
	// Android low-latency feature. The seeded SDK is 26, below AAudio's 27.
	if class == "org/fmod/FMOD" {
		switch name + sig {
		case "checkInit()Z":
			vm.mu.Lock()
			ctx, _ := vm.classes[class].obj.fields["gContext"].(int64)
			ready := ctx != 0 && vm.objects[ctx] != nil
			vm.mu.Unlock()
			return fmodHelperBool(name, ready), true
		case "supportsAAudio()Z":
			vm.mu.Lock()
			sdk, _ := vm.classes["android/os/Build$VERSION"].obj.fields["SDK_INT"].(int32)
			vm.mu.Unlock()
			return fmodHelperBool(name, sdk >= 27), true
		case "supportsLowLatency()Z":
			// Live GetMethodID at experience start (2026-09-12 voice session).
			// Run 281 (2026-09-12): with this false FMOD never dlopen'd
			// libOpenSLES.so and opened its AudioTrack output, which has
			// no recording — hence the empty record-driver list behind
			// `RobloxAudioDevice::InitRecording No input audio format!`.
			return fmodHelperBool(name, fmodSupportsLowLatency()), true
		case "getOutputSampleRate()I":
			// Java: AudioManager.getProperty(PROPERTY_OUTPUT_SAMPLE_RATE).
			// FMOD's OpenSL/AAudio outputs size their device stream from
			// these two helpers (Java returns 0 when unknown, which makes
			// FMOD guess). The host figures are PipeWire's defaults
			// (default.clock.rate 48000, default.clock.quantum 1024) and
			// match the 48 kHz the AudioTrack path already observed.
			logFmodHelper(name, fmodHostOutputSampleRate)
			return idToJobject(fmodHostOutputSampleRate), true
		case "getOutputBlockSize()I":
			// Java: AudioManager.getProperty(PROPERTY_OUTPUT_FRAMES_PER_BUFFER).
			logFmodHelper(name, fmodHostOutputBlockFrames)
			return idToJobject(fmodHostOutputBlockFrames), true
		}
	}
	if class != fmodAudioClass {
		return jnull(), false
	}
	switch name + sig {
	case "init(IIII)Z":
		d := vm.fmodDevice(o)
		if d == nil || args == nil {
			return jnull(), true
		}
		format, err := fmodFormat(jvalueIAt(args, 0), jvalueIAt(args, 1), jvalueIAt(args, 2), jvalueIAt(args, 3))
		if err == nil {
			err = d.initialize(format)
		}
		if err != nil {
			logging.Logger(logging.CatAudio).Error("[audio] FMOD playback init failed", "error", err)
			return jnull(), true
		}
		return idToJobject(1), true
	case "write([BI)V":
		d := vm.fmodDevice(o)
		if d == nil || args == nil {
			return jnull(), true
		}
		array := vm.get(jobjectToID(uintptr(jvalueLAt(args, 0))))
		count := jvalueIAt(args, 1)
		if array == nil || array.arrKind != 'B' || count < 0 || int(count) > len(array.bytes) {
			logging.Logger(logging.CatAudio).Error("[audio] FMOD playback rejected invalid byte array")
			return jnull(), true
		}
		if err := d.write(array.bytes[:int(count)]); err != nil {
			d.logWriteError(err)
		}
		return jnull(), true
	case "close()V":
		if d := vm.fmodDevice(o); d != nil {
			d.close()
		}
		return jnull(), true
	}
	return jnull(), false
}

// Test helpers construct actual JNI slots; Go test files cannot import C.
func testFmodInitArgs(channels, rate, frames, blocks int32) *C.jvalue {
	a := new([4]C.jvalue)
	for i, value := range []int32{channels, rate, frames, blocks} {
		jvalueSetI(&a[i], C.jint(value))
	}
	return &a[0]
}

func testFmodWriteArgs(arrayID int64, count int32) *C.jvalue {
	a := new([2]C.jvalue)
	jvalueSetL(&a[0], idToJobject(arrayID))
	jvalueSetI(&a[1], C.jint(count))
	return &a[0]
}
