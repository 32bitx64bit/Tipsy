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
	"sync"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const fmodAudioClass = "org/fmod/AudioDevice"

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
			if ready {
				return idToJobject(1), true
			}
			return jnull(), true
		case "supportsAAudio()Z":
			vm.mu.Lock()
			sdk, _ := vm.classes["android/os/Build$VERSION"].obj.fields["SDK_INT"].(int32)
			vm.mu.Unlock()
			if sdk >= 27 {
				return idToJobject(1), true
			}
			return jnull(), true
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
		array := vm.get(jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 0))))
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
		C.tipsy_jvalue_set_i(&a[i], C.jint(value))
	}
	return &a[0]
}

func testFmodWriteArgs(arrayID int64, count int32) *C.jvalue {
	a := new([2]C.jvalue)
	C.tipsy_jvalue_set_l(&a[0], idToJobject(arrayID))
	C.tipsy_jvalue_set_i(&a[1], C.jint(count))
	return &a[0]
}
