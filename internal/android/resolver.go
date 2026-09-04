// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

/*
#cgo LDFLAGS: -ldl -lpulse-simple -lpulse
#cgo CFLAGS: -D_GNU_SOURCE
#include "android_bridge.h"
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Resolver is loader.Resolver (Lookup(lib, sym string) (uintptr, error)).
type Resolver = loader.Resolver

// LookupFunc resolves a single symbol inside a registered module.
type LookupFunc func(sym string) (uintptr, error)

// Config controls asset and default window sizing for the resolver.
type Config struct {
	AssetsDir string
	APKPath   string // base.apk zip for assets/ entries
	Width     int32
	Height    int32
}

// provider is the process-wide Android symbol resolver.
type provider struct {
	mu        sync.Mutex
	assetsDir string
	apkPath   string
	width     int32
	height    int32
}

var defaultProvider = &provider{width: 1920, height: 1080}

// Provider returns the process-wide in-process Android resolver.
func Provider() loader.Resolver {
	return defaultProvider
}

// NewResolver returns a configurable resolver (also registered as the default
// for AAssetManager / ANativeWindow helpers).
func NewResolver(cfg Config) loader.Resolver {
	p := &provider{
		assetsDir: cfg.AssetsDir,
		apkPath:   cfg.APKPath,
		width:     cfg.Width,
		height:    cfg.Height,
	}
	if p.width <= 0 {
		p.width = 1920
	}
	if p.height <= 0 {
		p.height = 1080
	}
	defaultProvider = p
	setAssetsLocked(p.assetsDir, p.apkPath)
	return p
}

// SetAssetsDir sets the directory (extracted APK assets/) used by AAssetManager.
func SetAssetsDir(dir string) {
	defaultProvider.mu.Lock()
	defaultProvider.assetsDir = dir
	defaultProvider.mu.Unlock()
	setAssetsLocked(dir, defaultProvider.apkPath)
}

// SetAPKPath sets a base.apk zip path for assets/ lookup when the file is not
// already extracted under AssetsDir.
func SetAPKPath(apk string) {
	defaultProvider.mu.Lock()
	defaultProvider.apkPath = apk
	defaultProvider.mu.Unlock()
	setAssetsLocked(defaultProvider.assetsDir, apk)
}

func init() {
	C.tipsy_bionic_compat_init()
}

// Sysconf is bionic sysconf: `name` is an AOSP `_SC_*` number, not glibc's.
func Sysconf(name int32) int64 {
	return int64(C.tipsy_sysconf(C.int(name)))
}

func fflushBionicIndex(idx int) int {
	return int(C.tipsy_fflush_bionic_index(C.int(idx)))
}

func getaddrinfoNumericLoopback() int {
	return int(C.tipsy_test_getaddrinfo_numeric_loopback())
}

func getaddrinfoBionicAddrconfig() int {
	return int(C.tipsy_test_getaddrinfo_bionic_addrconfig())
}

func glibcUntranslatedAddrconfig() int {
	return int(C.tipsy_test_glibc_untranslated_addrconfig())
}

func getaddrinfoEAINoname() int {
	return int(C.tipsy_test_getaddrinfo_eai_noname())
}

func getnameinfoNumeric() int {
	return int(C.tipsy_test_getnameinfo_numeric())
}

func glibcBionicAiAddrNull() int {
	return int(C.tipsy_test_glibc_bionic_ai_addr_null())
}

func glibcSigactionSmashesBionicAct() int {
	return int(C.tipsy_test_glibc_sigaction_smashes_bionic_act())
}

func bionicSigactionQueryCanary() int {
	return int(C.tipsy_test_bionic_sigaction_query_canary())
}

func bionicSigactionSetQuery() int {
	return int(C.tipsy_test_bionic_sigaction_set_query())
}

func rtSigactionQueryCanary() int {
	return int(C.tipsy_test_rt_sigaction_query_canary())
}

func bionicSigactionSize() int {
	return int(C.tipsy_bionic_sigaction_size())
}

func glibcSigactionSize() int {
	return int(C.tipsy_glibc_sigaction_size())
}

func looperForThread() uintptr {
	return uintptr(unsafe.Pointer(C.tipsy_ALooper_forThread()))
}

func pollOnce(timeoutMs int) int {
	return int(C.tipsy_ALooper_pollOnce(C.int(timeoutMs), nil, nil, nil))
}

func condWaitPollsLooper() int {
	return int(C.tipsy_test_cond_wait_polls_looper())
}

func audioTestPlayback(rate, channels, bytes uint32) (uint64, uint32, int) {
	var written C.uint64_t
	var callbacks C.uint32_t
	rc := C.tipsy_audio_test_playback(C.uint32_t(rate), C.uint32_t(channels), C.uint32_t(bytes), &written, &callbacks)
	return uint64(written), uint32(callbacks), int(rc)
}

func audioTestCapture(rate, channels, bytes uint32) (uint64, uint32, int) {
	var read C.uint64_t
	var callbacks C.uint32_t
	rc := C.tipsy_audio_test_capture(C.uint32_t(rate), C.uint32_t(channels), C.uint32_t(bytes), &read, &callbacks)
	return uint64(read), uint32(callbacks), int(rc)
}

func audioTestInvalidFormat() int {
	return int(C.tipsy_audio_test_invalid_format())
}

func audioTestRetry() (uint32, uint32, uint32, int) {
	var opens C.uint32_t
	var writes C.uint32_t
	var callbacks C.uint32_t
	rc := C.tipsy_audio_test_retry(&opens, &writes, &callbacks)
	return uint32(opens), uint32(writes), uint32(callbacks), int(rc)
}

func audioTestHostPlayback(rate, channels, bytes uint32) (uint64, uint32, int) {
	var written C.uint64_t
	var callbacks C.uint32_t
	rc := C.tipsy_audio_test_host_playback(C.uint32_t(rate), C.uint32_t(channels), C.uint32_t(bytes), &written, &callbacks)
	return uint64(written), uint32(callbacks), int(rc)
}

func soname(lib string) string {
	lib = strings.TrimSpace(lib)
	if lib == "" {
		return ""
	}
	return path.Base(lib)
}

func missingSymbol(name string) error {
	logging.Logger(logging.CatAndroid).Error("[android] missing native symbol: " + name)
	return fmt.Errorf("missing native symbol: %s", name)
}

// Lookup implements Resolver. lib is a DT_NEEDED soname or "".
func (p *provider) Lookup(lib, sym string) (uintptr, error) {
	if p == nil {
		p = defaultProvider
	}
	if sym == "" {
		return 0, missingSymbol(sym)
	}
	lib = soname(lib)

	switch lib {
	case "libc.so", "libm.so", "libz.so":
		return lookupLibc(lib, sym)
	case "libdl.so":
		if addr := androidLookup(lib, sym); addr != 0 {
			return addr, nil
		}
		return 0, missingSymbol(sym)
	case "liblog.so", "libandroid.so", "libOpenSLES.so", "libOpenMAXAL.so", "libjnigraphics.so", "libmediandk.so":
		if addr := androidLookup(lib, sym); addr != 0 {
			return addr, nil
		}
		return 0, missingSymbol(sym)
	case "libEGL.so":
		if addr := androidLookup(lib, sym); addr != 0 {
			return addr, nil
		}
		return 0, missingSymbol(sym)
	case "libGLESv2.so":
		if addr := androidLookup(lib, sym); addr != 0 {
			return addr, nil
		}
		return 0, missingSymbol(sym)
	case "":
		if addr := androidLookup("", sym); addr != 0 {
			return addr, nil
		}
		if addr := androidLookup("libEGL.so", sym); addr != 0 {
			return addr, nil
		}
		if addr := androidLookup("libGLESv2.so", sym); addr != 0 {
			return addr, nil
		}
		if addr, err := lookupLibc("libc.so", sym); err == nil && addr != 0 {
			return addr, nil
		}
		if addr := dlRegistryLookup(sym); addr != 0 {
			return addr, nil
		}
		return 0, missingSymbol(sym)
	default:
		if addr := dlRegistryLookupLib(lib, sym); addr != 0 {
			return addr, nil
		}
		return 0, missingSymbol(lib + ":" + sym)
	}
}

func androidLookup(lib, sym string) uintptr {
	clib := C.CString(lib)
	csym := C.CString(sym)
	defer C.free(unsafe.Pointer(clib))
	defer C.free(unsafe.Pointer(csym))
	p := C.tipsy_android_lookup(clib, csym)
	return uintptr(p)
}

func lookupLibc(lib, sym string) (uintptr, error) {
	if addr := androidLookup("libc.so", sym); addr != 0 {
		return addr, nil
	}
	hostName := sym
	if alt, ok := bionicAliases[sym]; ok {
		hostName = alt
	}
	if addr := hostDlsym(hostName); addr != 0 {
		return addr, nil
	}
	if addr := hostDlsym(sym); addr != 0 {
		return addr, nil
	}
	_ = lib
	return 0, missingSymbol(sym)
}

func hostDlsym(name string) uintptr {
	if name == "" {
		return 0
	}
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	return uintptr(C.tipsy_host_dlsym(cs))
}

var errMissing = errors.New("missing native symbol")
