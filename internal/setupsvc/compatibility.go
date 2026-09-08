// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/tipsy-linux/tipsy/internal/elfinspect"
)

// requiredClientCapabilities are the named dynamic entry contracts the
// current loader has no alternate route around. Capability names, rather than
// Roblox versions, build ids, addresses, sizes, or hashes, define support.
// GameActivity on*Native methods are deliberately absent because JNI_OnLoad
// supplies those through RegisterNatives rather than exported Java_ symbols.
var requiredClientCapabilities = []struct {
	name    string
	exports []string
}{
	{name: "loader bootstrap", exports: []string{
		"JNI_OnLoad",
		"Java_com_google_androidgamesdk_GameActivity_initializeNativeCode",
	}},
	{name: "official session persistence", exports: []string{
		"Java_com_roblox_universalapp_cookie_JNICookieProtocol_updateOnSetCookieHandler",
		"Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetBaseUrl",
		"Java_com_roblox_engine_jni_NativeSettingsInterface_nativeSetMultipleCookies",
	}},
	{name: "client settings", exports: []string{
		"Java_com_roblox_engine_jni_NativeGLInterface_nativeInitClientSettings",
		"Java_com_roblox_engine_jni_NativeGLInterface_nativePostClientSettingsLoadedInitialization3",
	}},
	{name: "Home application startup", exports: []string{
		"Java_com_roblox_client_startup_MainGameActivity_nativeAppBridgeSetInitParams",
		"Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2InitWithParams",
		"Java_com_roblox_engine_jni_NativeGLInterface_nativeAppBridgeV2StartAppWithParams",
		"Java_com_roblox_engine_jni_NativeAppBridgeInterface_nativeAppBridgeAppStart__Ljava_lang_String_2Ljava_lang_String_2ZLjava_lang_String_2Ljava_lang_String_2Ljava_lang_String_2",
	}},
}

// ValidateExtractedClientCompatibility performs a bounded static preflight on
// the root client DSO. Passing means only that Tipsy's loader can recognize the
// immutable architecture/type/JNI entry contract; it is not proof that Roblox
// will render, connect, accept input, play audio, or join an experience.
func ValidateExtractedClientCompatibility(ctx context.Context, runtimeDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return setupError(ErrCanceled, "check client compatibility", "operation canceled", err)
	}
	root := filepath.Join(runtimeDir, "lib", "x86_64", "libroblox.so")
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return setupError(ErrCompatibility, "check client compatibility", "the verified x86-64 root client library is unavailable", err)
	}
	file, err := os.Open(root)
	if err != nil {
		return setupError(ErrCompatibility, "check client compatibility", "the verified x86-64 root client library is unavailable", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != info.Size() || !os.SameFile(info, opened) {
		return setupError(ErrCompatibility, "check client compatibility", "the verified x86-64 root client library changed before inspection", err)
	}
	return validateOpenedClientCompatibility(ctx, file, opened.Size())
}

// validateOpenedClientCompatibility inspects the already authenticated file
// description. In particular, installed-state validation must never reopen a
// pathname and accidentally inspect bytes other than the pinned generation
// descriptor that earned the readiness verdict.
func validateOpenedClientCompatibility(ctx context.Context, file *os.File, size int64) error {
	if file == nil || size <= 0 || uint64(size) > uint64(^uint(0)>>1) {
		return setupError(ErrCompatibility, "check client compatibility", "the verified root client library has an invalid size", nil)
	}
	raw := make([]byte, int(size))
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return setupError(ErrCanceled, "check client compatibility", "operation canceled", err)
		}
		end := offset + 1<<20
		if end > size {
			end = size
		}
		n, err := file.ReadAt(raw[int(offset):int(end)], offset)
		offset += int64(n)
		if err != nil && !(err == io.EOF && offset == size) {
			return setupError(ErrCompatibility, "check client compatibility", "the verified root client library could not be read", err)
		}
		if n == 0 && offset < size {
			return setupError(ErrCompatibility, "check client compatibility", "the verified root client library ended before its authenticated size", io.ErrUnexpectedEOF)
		}
	}
	report, err := elfinspect.AnalyzeBytes("authenticated libroblox.so", raw)
	if err != nil {
		return setupError(ErrCompatibility, "check client compatibility", "the verified root client library is not a readable ELF object", err)
	}
	if err := validateClientELFReport(report); err != nil {
		return setupError(ErrCompatibility, "check client compatibility", err.Error(), nil)
	}
	if err := ctx.Err(); err != nil {
		return setupError(ErrCanceled, "check client compatibility", "operation canceled", err)
	}
	return nil
}

func validateClientELFReport(report *elfinspect.Report) error {
	if report == nil || report.Class != "ELF64" || report.Machine != "EM_X86_64" || report.Type != "ET_DYN" {
		return fmt.Errorf("the root client library must be an ELF64 x86-64 shared object")
	}
	for _, capability := range requiredClientCapabilities {
		for _, required := range capability.exports {
			if !slices.Contains(report.JNIEntryPoints, required) {
				return fmt.Errorf("the root client library is missing the %s capability", capability.name)
			}
		}
	}
	return nil
}
