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
	acquisitions := []uint32{
		eglSetupPlatformDisplay,
		eglSetupPlatformDisplayEXT,
		eglSetupDisplay,
	}
	commonStages := []uint32{
		eglSetupInitialize,
		eglSetupChooseConfig,
		eglSetupCreateContext,
		eglSetupCreateWindow,
		eglSetupMakeCurrent,
		eglSetupFirstSwap,
	}
	for _, acquisition := range acquisitions {
		stages := append([]uint32{acquisition}, commonStages...)
		got := testEGLSetupTraceFixture(acquisition, 0, eglSetupFailure)
		if !got.passed || got.firstFailure != 0 || len(got.events) != len(stages) {
			t.Fatalf("successful acquisition %d fixture=%+v", acquisition, got)
		}
		for i, event := range got.events {
			if event.stage != stages[i] || event.outcome != eglSetupSuccess {
				t.Fatalf("successful acquisition %d event[%d]=%+v want stage=%d success",
					acquisition, i, event, stages[i])
			}
		}

		for _, stage := range stages {
			for _, outcome := range []uint32{eglSetupFailure, eglSetupAbsence} {
				got := testEGLSetupTraceFixture(acquisition, stage, outcome)
				if !got.passed || got.firstFailure != stage || len(got.events) == 0 {
					t.Fatalf("acquisition=%d stage=%d outcome=%d fixture=%+v",
						acquisition, stage, outcome, got)
				}
				last := got.events[len(got.events)-1]
				if last.stage != stage || last.outcome != outcome {
					t.Fatalf("acquisition=%d stage=%d outcome=%d last=%+v",
						acquisition, stage, outcome, last)
				}
			}
		}
	}
}
