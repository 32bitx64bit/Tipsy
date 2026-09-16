// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package android

import "testing"

func TestEGLSetupTraceRequiresExactOptIn(t *testing.T) {
	for _, value := range []string{"", "0", "true", "1 ", " 1", "01", "2"} {
		if testEGLSetupTraceValueEnabled(value) {
			t.Errorf("trace value %q enabled EGL setup diagnostics", value)
		}
	}
	if !testEGLSetupTraceValueEnabled("1") {
		t.Fatal("exact EGL setup trace opt-in did not enable diagnostics")
	}
}

func TestEGLSetupLookupsUseCompatibilityWrappers(t *testing.T) {
	for _, name := range []string{
		"eglGetPlatformDisplay",
		"eglGetDisplay",
		"eglInitialize",
		"eglChooseConfig",
		"eglCreateContext",
		"eglCreateWindowSurface",
		"eglMakeCurrent",
		"eglSwapBuffers",
	} {
		ours, err := Provider().Lookup("libEGL.so", name)
		if err != nil || ours == 0 {
			t.Fatalf("%s: p=%#x err=%v", name, ours, err)
		}
		host := hostDlsym(name)
		if host != 0 && ours == host {
			t.Fatalf("%s Lookup=%#x is host EGL, want setup trace wrapper", name, ours)
		}
		if !testEGLProcIsWrapped(name) {
			t.Fatalf("eglGetProcAddress(%q) did not return setup trace wrapper", name)
		}
	}
	// EXT is optional. When the host advertises it, the guest must still receive
	// the traced wrapper; when absent, the resolver must preserve NULL rather
	// than fabricate extension support.
	if ours, err := Provider().Lookup("libEGL.so", "eglGetPlatformDisplayEXT"); err == nil {
		host := hostDlsym("eglGetPlatformDisplayEXT")
		if ours == 0 || (host != 0 && ours == host) ||
			!testEGLProcIsWrapped("eglGetPlatformDisplayEXT") {
			t.Fatalf("eglGetPlatformDisplayEXT lookup=%#x host=%#x is not traced", ours, host)
		}
	}
}

func TestEGLSetupTraceNamesFirstFakeHostFailure(t *testing.T) {
	if !testEGLSetupTraceFixture() {
		t.Fatal("fake EGL setup trace matrix failed")
	}
}
