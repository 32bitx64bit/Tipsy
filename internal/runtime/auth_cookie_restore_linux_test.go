// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/jni"
)

func TestRobloxCookieRestoreConfiguresOriginBeforeNativeSetter(t *testing.T) {
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	configured, restored := false, false
	err = restoreRobloxCookieHeader(env, "fixture=synthetic", func(symbol string, settings, first, second uintptr) error {
		if settings != env.FindClass("com/roblox/engine/jni/NativeSettingsInterface") {
			t.Fatal("cookie startup must use the official settings class receiver")
		}
		origin, _ := env.GetStringUTFChars(first)
		value, _ := env.GetStringUTFChars(second)
		if origin != "https://www.roblox.com/" {
			t.Fatal("cookie startup origin does not match the APK")
		}
		switch {
		case strings.HasSuffix(symbol, "_nativeSetBaseUrl"):
			if configured || restored || value != "https://api.roblox.com/" {
				t.Fatal("native origin configuration must occur first with the APK API origin")
			}
			configured = true
		case strings.HasSuffix(symbol, "_nativeSetMultipleCookies"):
			if !configured || restored || value != "fixture=synthetic" {
				t.Fatal("native restore must receive the original header after origin configuration")
			}
			restored = true
		default:
			t.Fatal("unexpected cookie startup native API")
		}
		return nil
	})
	if err != nil || !configured || !restored {
		t.Fatal("cookie startup did not complete both native boundaries")
	}
}

func TestRobloxCookieRestoreStopsOnOriginFailure(t *testing.T) {
	vm, err := jni.NewVM()
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("origin API unavailable")
	calls := 0
	err = restoreRobloxCookieHeader(vm.Env(), "fixture=synthetic", func(symbol string, _, _, _ uintptr) error {
		calls++
		return want
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatal("origin failure must prevent a falsely successful restore")
	}
}
