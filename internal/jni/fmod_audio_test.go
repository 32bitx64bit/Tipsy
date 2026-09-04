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
