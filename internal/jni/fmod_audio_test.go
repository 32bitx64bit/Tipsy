// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

type fakeFmodPlayback struct {
	data   []byte
	closed bool
	fail   bool
}

func (p *fakeFmodPlayback) write(data []byte) error {
	if p.fail {
		return errors.New("injected device failure")
	}
	p.data = append(p.data, data...)
	return nil
}
func (p *fakeFmodPlayback) close() { p.closed = true }

func TestFmodAudioDeviceDispatch(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	output := &fakeFmodPlayback{}
	var opened fmodAudioFormat
	d := &fmodAudioDevice{open: func(format fmodAudioFormat) (fmodPlayback, error) {
		opened = format
		return output, nil
	}}
	vm.mu.Lock()
	o := vm.newObjectLocked(vm.classes[fmodAudioClass])
	o.fields["tipsy.fmodPlayback"] = d
	a := vm.newObjectLocked(vm.classes["java/lang/Object"])
	a.arrKind, a.bytes = 'B', []byte("private audio buffer content")
	vm.mu.Unlock()
	value, handled := vm.dispatch(idToJobject(o.id), fmodAudioClass, "init", "(IIII)Z", testFmodInitArgs(2, 48000, 256, 4))
	if !handled || uintptr(value) != 1 {
		t.Fatal("real AudioDevice init dispatch did not return success")
	}
	if opened != (fmodAudioFormat{2, 48000, 4096}) {
		t.Fatalf("wrong APK argument order: %+v", opened)
	}
	_, handled = vm.dispatch(idToJobject(o.id), fmodAudioClass, "write", "([BI)V", testFmodWriteArgs(a.id, 12))
	if !handled || !bytes.Equal(output.data, a.bytes[:12]) {
		t.Fatal("JNI byte array prefix did not reach output")
	}
	for _, count := range []int32{-1, int32(len(a.bytes) + 1), 3} {
		vm.dispatch(idToJobject(o.id), fmodAudioClass, "write", "([BI)V", testFmodWriteArgs(a.id, count))
	}
	if len(output.data) != 12 {
		t.Fatal("invalid buffers reached output")
	}
	vm.dispatch(idToJobject(o.id), fmodAudioClass, "close", "()V", nil)
	vm.dispatch(idToJobject(o.id), fmodAudioClass, "close", "()V", nil)
	if !output.closed {
		t.Fatal("close did not release output")
	}
	vm.dispatch(idToJobject(o.id), fmodAudioClass, "write", "([BI)V", testFmodWriteArgs(a.id, 12))
	if len(output.data) != 12 {
		t.Fatal("write after close reached output")
	}
	if strings.Contains(logs.String(), "private audio") {
		t.Fatal("PCM appeared in diagnostics")
	}
	if _, handled := vm.dispatch(idToJobject(o.id), "org/other/AudioDevice", "init", "(IIII)Z", testFmodInitArgs(2, 48000, 256, 4)); handled {
		t.Fatal("FMOD dispatch accepted another class")
	}
}

func TestFmodAudioInitFailureAndReinitialize(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	var output *fakeFmodPlayback
	fail := true
	d := &fmodAudioDevice{open: func(fmodAudioFormat) (fmodPlayback, error) {
		if fail {
			return nil, errors.New("injected device unavailable")
		}
		output = &fakeFmodPlayback{}
		return output, nil
	}}
	vm.mu.Lock()
	o := vm.newObjectLocked(vm.classes[fmodAudioClass])
	o.fields["tipsy.fmodPlayback"] = d
	vm.mu.Unlock()
	value, handled := vm.dispatch(idToJobject(o.id), fmodAudioClass, "init", "(IIII)Z", testFmodInitArgs(2, 48000, 256, 4))
	if !handled || uintptr(value) != 0 {
		t.Fatal("failed device open was reported as success")
	}
	fail = false
	value, _ = vm.dispatch(idToJobject(o.id), fmodAudioClass, "init", "(IIII)Z", testFmodInitArgs(2, 48000, 256, 4))
	if uintptr(value) != 1 {
		t.Fatal("retry init failed")
	}
	first := output
	value, _ = vm.dispatch(idToJobject(o.id), fmodAudioClass, "init", "(IIII)Z", testFmodInitArgs(1, 44100, 256, 4))
	if uintptr(value) != 1 || !first.closed || output == first {
		t.Fatal("reinitialization did not replace/release stream")
	}
	output.fail = true
	if err := d.write(make([]byte, 8)); err == nil || !output.closed {
		t.Fatal("write failure did not close broken output")
	}
	if d.stream != nil {
		t.Fatal("broken output retained")
	}
}

func TestFmodAudioRejectsInvalidFormat(t *testing.T) {
	for _, input := range [][4]int32{{0, 48000, 256, 4}, {3, 48000, 256, 4}, {2, 0, 256, 4}, {2, 48000, -1, 4}, {2, 48000, 256, 0}, {8, 48000, 1 << 30, 1 << 30}} {
		if _, err := fmodFormat(input[0], input[1], input[2], input[3]); err == nil {
			t.Fatalf("accepted invalid format %v", input)
		}
	}
}

func TestFmodAudioCapabilityPredicates(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkInit", "supportsAAudio"} {
		value, handled := vm.dispatch(jnull(), "org/fmod/FMOD", name, "()Z", nil)
		if !handled || uintptr(value) != 0 {
			t.Fatalf("%s invented initialized context or SDK capability", name)
		}
	}
}

// org.fmod.FMOD.init(Context) is the APK's own startup step (NativeHelper);
// Tipsy plays it with the real activity object, and only with a live one.
func TestFmodInitBacksCheckInit(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if vm.FmodInit(0) {
		t.Fatal("FmodInit(0) claimed an initialized context")
	}
	if vm.FmodInit(1 << 40) {
		t.Fatal("FmodInit of a non-object claimed an initialized context")
	}
	if value, _ := vm.dispatch(jnull(), "org/fmod/FMOD", "checkInit", "()Z", nil); uintptr(value) != 0 {
		t.Fatal("checkInit()Z true before FMOD.init")
	}
	vm.mu.Lock()
	ctx := vm.newObjectLocked(vm.classes["com/roblox/client/startup/MainGameActivity"])
	vm.mu.Unlock()
	if !vm.FmodInit(uintptr(ctx.id)) {
		t.Fatal("FmodInit with the activity object failed")
	}
	if value, handled := vm.dispatch(jnull(), "org/fmod/FMOD", "checkInit", "()Z", nil); !handled || uintptr(value) != 1 {
		t.Fatalf("checkInit()Z = %d after FMOD.init(activity)", uintptr(value))
	}
}

// FMOD's Android autodetect: AAudio when the SDK allows (not here, SDK 26),
// else its OpenSL ES output when the Java supportsLowLatency() helper is
// true, else AudioTrack, which FMOD documents as having no recording. Tipsy
// answers the helper the way FMOD's Java does — FEATURE_AUDIO_LOW_LATENCY
// from PackageManager plus a block size in (0, 1024] — so the two surfaces
// agree, and TIPSY_FMOD_OUTPUT=audiotrack turns both off together.
func TestFmodAudioLowLatencyFollowsHostAnswer(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fmodOutputEnv, "")
	value, handled := vm.dispatch(jnull(), "org/fmod/FMOD", "supportsLowLatency", "()Z", nil)
	if !handled || uintptr(value) != 1 {
		t.Fatalf("supportsLowLatency()Z = %d handled=%v, want true on the host bridge", uintptr(value), handled)
	}
	if !platformSystemFeature(androidHardwareAudioLowLatency) {
		t.Fatal("PackageManager denies android.hardware.audio.low_latency while FMOD is told low latency")
	}
	if platformSystemFeature("android.hardware.audio.pro") {
		t.Fatal("android.hardware.audio.pro advertised without a host guarantee")
	}
	if fmodHostOutputBlockFrames <= 0 || fmodHostOutputBlockFrames > 1024 {
		t.Fatalf("block size %d contradicts the low-latency answer", fmodHostOutputBlockFrames)
	}

	t.Setenv(fmodOutputEnv, "AudioTrack")
	value, handled = vm.dispatch(jnull(), "org/fmod/FMOD", "supportsLowLatency", "()Z", nil)
	if !handled || uintptr(value) != 0 {
		t.Fatalf("TIPSY_FMOD_OUTPUT=audiotrack still answered supportsLowLatency()Z = %d", uintptr(value))
	}
	if platformSystemFeature(androidHardwareAudioLowLatency) {
		t.Fatal("TIPSY_FMOD_OUTPUT=audiotrack left android.hardware.audio.low_latency advertised")
	}
}

// FMOD's OpenSL/AAudio outputs size their device stream from the two
// AudioManager property helpers; Java answers 0 when the property is
// unknown, which makes FMOD guess. Tipsy answers PipeWire's defaults and the
// methods are on the implemented list so GetMethodID does not flag them.
func TestFmodAudioOutputProperties(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]uintptr{
		"getOutputSampleRate": fmodHostOutputSampleRate,
		"getOutputBlockSize":  fmodHostOutputBlockFrames,
	} {
		value, handled := vm.dispatch(jnull(), "org/fmod/FMOD", name, "()I", nil)
		if !handled || uintptr(value) != want {
			t.Fatalf("%s()I = %d handled=%v, want %d", name, uintptr(value), handled, want)
		}
		if !isImplementedMethod(name, "()I") {
			t.Fatalf("%s()I missing from implementedMethods", name)
		}
	}
	if _, handled := vm.dispatch(jnull(), "org/fmod/AudioDevice", "getOutputBlockSize", "()I", nil); handled {
		t.Fatal("output property answered on the wrong class")
	}
}

// This explicitly selected integration test exercises the actual JNI
// AudioDevice dispatch against a real output. It never opens capture.
func TestFmodAudioHostPlayback(t *testing.T) {
	if os.Getenv("TIPSY_AUDIO_HOST_TEST") != "1" {
		t.Skip("set TIPSY_AUDIO_HOST_TEST=1 for host playback")
	}
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	vm.mu.Lock()
	o := vm.newObjectLocked(vm.classes[fmodAudioClass])
	a := vm.newObjectLocked(vm.classes["java/lang/Object"])
	a.arrKind, a.bytes = 'B', make([]byte, 19200)
	vm.mu.Unlock()
	value, handled := vm.dispatch(idToJobject(o.id), fmodAudioClass, "init", "(IIII)Z", testFmodInitArgs(2, 48000, 512, 4))
	if !handled || uintptr(value) != 1 {
		t.Fatal("host output could not initialize")
	}
	defer vm.dispatch(idToJobject(o.id), fmodAudioClass, "close", "()V", nil)
	vm.dispatch(idToJobject(o.id), fmodAudioClass, "write", "([BI)V", testFmodWriteArgs(a.id, int32(len(a.bytes))))
	d := vm.fmodDevice(o)
	if !d.flowLogged || d.stream == nil {
		t.Fatal("host buffer did not flow")
	}
}
