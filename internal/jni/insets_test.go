// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

func TestInsetsClassesSeeded(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	buf := captureLogs(t)
	env := vm.Env()
	for _, n := range []string{
		"androidx/core/graphics/Insets",
		"androidx/core/view/WindowInsetsCompat$Type",
	} {
		if env.FindClass(n) == 0 {
			t.Fatalf("seeded class missing: %s", n)
		}
		if strings.Contains(buf.String(), "[jni] auto-class: "+n) {
			t.Fatalf("seeded class logged as auto-class: %s", n)
		}
	}
}

func TestWindowInsetsTypeStatics(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	for name, want := range map[string]int32{
		"statusBars":              1,
		"navigationBars":          2,
		"captionBar":              4,
		"ime":                     8,
		"systemGestures":          16,
		"mandatorySystemGestures": 32,
		"tappableElement":         64,
		"displayCutout":           128,
		"systemOverlays":          512,
		"systemBars":              519,
	} {
		v, handled := callDispatchOrStub(vm, jnull(),
			"androidx/core/view/WindowInsetsCompat$Type", name, "()I", nil, 'I')
		if !handled {
			t.Fatalf("%s()I not handled", name)
		}
		if int32(uintptr(v)) != want {
			t.Fatalf("%s() = %d, want %d", name, int32(uintptr(v)), want)
		}
		if !isImplementedMethod(name, "()I") {
			t.Fatalf("%s()I not in implementedMethods", name)
		}
	}
	out := buf.String()
	if strings.Contains(out, "stub-dispatch") || strings.Contains(out, "missing method") {
		t.Fatalf("diagnostic fired on implemented Type statics: %s", out)
	}
}

func TestGameActivityInsetsReturnsNullOnX11(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetStubDispatchForTest()
	buf := captureLogs(t)

	// Official GameActivity.getWindowInsets: with real X11 geometry the
	// computed insets for every Type mask are Insets.NONE (no bars, cutout,
	// IME, or gesture areas), and NONE maps to null.
	v, handled := callDispatchOrStub(vm, jnull(), "com/google/androidgamesdk/GameActivity",
		"getWindowInsets", "(I)Landroidx/core/graphics/Insets;", packJint(519), 'L')
	if !handled {
		t.Fatal("getWindowInsets not handled")
	}
	if uintptr(v) != 0 {
		t.Fatalf("getWindowInsets(systemBars) = %d, want null (Insets.NONE)", uintptr(v))
	}
	if !isImplementedMethod("getWindowInsets", "(I)Landroidx/core/graphics/Insets;") {
		t.Fatal("getWindowInsets not in implementedMethods")
	}

	// Official getWaterfallInsets: null without a display cutout; X11 has none.
	v, handled = callDispatchOrStub(vm, jnull(), "com/google/androidgamesdk/GameActivity",
		"getWaterfallInsets", "()Landroidx/core/graphics/Insets;", nil, 'L')
	if !handled {
		t.Fatal("getWaterfallInsets not handled")
	}
	if uintptr(v) != 0 {
		t.Fatalf("getWaterfallInsets = %d, want null (no cutout)", uintptr(v))
	}
	if !isImplementedMethod("getWaterfallInsets", "()Landroidx/core/graphics/Insets;") {
		t.Fatal("getWaterfallInsets not in implementedMethods")
	}

	out := buf.String()
	if strings.Contains(out, "stub-dispatch") || strings.Contains(out, "missing method") {
		t.Fatalf("diagnostic fired on implemented insets getters: %s", out)
	}
}

func TestInsetsNoneAndValueFields(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	cls := vm.classes["androidx/core/graphics/Insets"]
	if cls == nil || cls.obj == nil {
		t.Fatal("Insets class not seeded")
	}
	noneID, ok := cls.obj.fields["NONE"].(int64)
	if !ok || noneID == 0 {
		t.Fatal("Insets.NONE static not seeded")
	}
	none := vm.get(noneID)
	if none == nil || none.class != cls {
		t.Fatalf("Insets.NONE object wrong: %v", none)
	}
	for _, f := range []string{"left", "top", "right", "bottom"} {
		if v, _ := none.fields[f].(int32); v != 0 {
			t.Fatalf("Insets.NONE field %s = %d, want 0", f, v)
		}
	}

	// Value semantics: non-zero Insets objects carry the official public
	// int fields (left/top/right/bottom) readable via the field path.
	o := vm.get(jobjectToID(uintptr(vm.newInsetsObject(3, 5, 7, 9))))
	if o == nil {
		t.Fatal("newInsetsObject NULL")
	}
	want := map[string]int32{"left": 3, "top": 5, "right": 7, "bottom": 9}
	for f, w := range want {
		if v, _ := o.fields[f].(int32); v != w {
			t.Fatalf("Insets field %s = %d, want %d", f, v, w)
		}
	}
}
