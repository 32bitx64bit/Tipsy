// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"fmt"
	"os"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/jni"
	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// setRobloxPreferencesFile follows NativeHelper.P -> el/y.f: the parameter is
// an Android SharedPreferences NAME, not a filesystem path. "rbx.prefs" is only
// the APK's logging tag. Cookie persistence is the separate CookieProtocol.
func setRobloxPreferencesFile(mod *loader.Module, env *jni.Env) {
	class := env.FindClass("com/roblox/engine/jni/NativeSettingsInterface")
	callRobloxJNI(mod, env.Raw(), class, setPreferencesFileSym, env.NewStringUTF(robloxPreferencesID))
}

// configureRobloxCookieBridge implements the APK's Java-owned lifecycle:
// MainGameActivity.onCreate restores scoped cookies before native creation;
// NativeHelper initializes CookieProtocol's official callback. Cookie content
// never enters diagnostics, flags, account stubs, or engine memory patches.
func configureRobloxCookieBridge(vm *jni.VM, mod *loader.Module, env *jni.Env, path string) error {
	const baseURL = "https://www.roblox.com/"
	const registerSym = "Java_com_roblox_universalapp_cookie_JNICookieProtocol_updateOnSetCookieHandler"
	register, err := mod.Lookup(registerSym)
	if err != nil || register == 0 {
		return fmt.Errorf("official cookie callback unavailable")
	}
	if err = vm.ConfigureAuthCookies(path, baseURL); err != nil {
		return fmt.Errorf("prepare official cookie storage: %w", err)
	}
	protocol := env.AllocObject(env.FindClass("com/roblox/universalapp/cookie/JNICookieProtocol"))
	handler := env.AllocObject(env.FindClass("com/roblox/universalapp/cookie/CookieProtocol$OnSetCookieHandlerImpl"))
	vm.SetAuthCookieRegistration(func() {
		setRobloxPreferencesFile(mod, env)
		loader.CallP8(register, env.Raw(), protocol, handler, 0, 0, 0, 0, 0)
		logging.Logger(logging.CatFilesystem).Info("official cookie callback registered")
	})
	header, err := vm.RestoreAuthCookies()
	if err != nil {
		return err
	}
	if err := restoreRobloxCookieHeader(env, header, func(symbol string, settings, first, second uintptr) error {
		fn, err := mod.Lookup(symbol)
		if err != nil || fn == 0 {
			return fmt.Errorf("official cookie startup API unavailable: %s", symbol)
		}
		loader.CallP8(fn, env.Raw(), settings, first, second, 0, 0, 0, 0)
		return nil
	}); err != nil {
		return err
	}
	logging.Logger(logging.CatFilesystem).Info("official cookie restore delivered", "stored_cookies", header != "")
	logNativeCookieRestoreState(mod, env, "before-native-init")
	return nil
}

// restoreRobloxCookieHeader preserves rh/w0.V0 -> R0's ordered JNI contract.
// The native cookie setter filters against its configured origin; calling
// restore before nativeSetBaseUrl silently discards valid saved cookies.
func restoreRobloxCookieHeader(env *jni.Env, header string, invoke func(symbol string, settings, first, second uintptr) error) error {
	const baseURL = "https://www.roblox.com/"
	settings := env.FindClass("com/roblox/engine/jni/NativeSettingsInterface")
	if err := invoke("Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetBaseUrl", settings, env.NewStringUTF(baseURL), env.NewStringUTF("https://api.roblox.com/")); err != nil {
		return err
	}
	return invoke("Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetMultipleCookies", settings, env.NewStringUTF(baseURL), env.NewStringUTF(header))
}

// logNativeCookieRestoreState is an opt-in, read-only check of the named APK
// cookie getter. It emits only record counts and auth-category presence, never
// cookie names, values, URLs, paths, account data, or raw native strings.
func logNativeCookieRestoreState(mod *loader.Module, env *jni.Env, phase string) {
	if os.Getenv("TIPSY_AUTH_RESTORE_DIAGNOSTICS") != "1" {
		return
	}
	const sym = "Java_com_roblox_engine_jni_NativeSettingsInterface_nativeGetCookiesInNetscapeFormat"
	fn, err := mod.Lookup(sym)
	if err != nil || fn == 0 {
		logging.Logger(logging.CatFilesystem).Info("official cookie readback unavailable")
		return
	}
	result := loader.CallP8(fn, env.Raw(), env.FindClass("com/roblox/engine/jni/NativeSettingsInterface"), env.NewStringUTF("https://www.roblox.com/"), 0, 0, 0, 0, 0)
	if result == 0 {
		logging.Logger(logging.CatFilesystem).Info("official cookie readback unavailable")
		return
	}
	snapshot, err := env.GetStringUTFChars(uintptr(result))
	if err != nil {
		logging.Logger(logging.CatFilesystem).Info("official cookie readback unavailable")
		return
	}
	count, authPresent := 0, false
	for _, record := range strings.Split(snapshot, ";") {
		fields := strings.Split(record, "\t")
		if len(fields) == 6 || len(fields) == 7 {
			count++
			if fields[5] == ".ROBLOSECURITY" && len(fields) == 7 && fields[6] != "" {
				authPresent = true
			}
		}
	}
	logging.Logger(logging.CatFilesystem).Info("official cookie readback", "phase", phase, "records", count, "auth_present", authPresent)
}
