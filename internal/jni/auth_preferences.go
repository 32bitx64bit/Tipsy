// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#include "jni_bridge.h"
*/
import "C"

import (
	"errors"
	"sync"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

const cookieProtocolClass = "com/roblox/universalapp/cookie/CookieProtocol"
const cookieHandlerClass = cookieProtocolClass + "$OnSetCookieHandlerImpl"

// Cookie persistence implements the Java side of the APK's CookieProtocol.
// The payload is handled only by this transport and its private serializer;
// diagnostic messages contain fixed identities and success/failure only.
var authCookies struct {
	sync.Mutex
	vm         *VM
	store      *authCookieStore
	register   func()
	registered bool
}

// ConfigureAuthCookies opens the private cookie store before native startup.
// It never invents an authenticated value. The registered official callback is
// the only writer; RestoreAuthCookies returns only unexpired scoped cookies.
func (vm *VM) ConfigureAuthCookies(path, baseURL string) error {
	store, err := openAuthCookieStore(path, baseURL)
	if err != nil {
		return err
	}
	authCookies.Lock()
	authCookies.vm, authCookies.store = vm, store
	authCookies.register = nil
	authCookies.registered = false
	authCookies.Unlock()
	return nil
}

func (vm *VM) RestoreAuthCookies() (string, error) {
	authCookies.Lock()
	defer authCookies.Unlock()
	if authCookies.vm != vm || authCookies.store == nil {
		return "", errors.New("cookie storage unavailable")
	}
	return authCookies.store.header(), nil
}

// SetAuthCookieRegistration prepares the Java-owned CookieProtocol constructor.
// Runtime completes it after the named native client-settings initialization;
// Tipsy emulates that Java phase directly instead of running the async loader.
func (vm *VM) SetAuthCookieRegistration(fn func()) {
	authCookies.Lock()
	defer authCookies.Unlock()
	if authCookies.vm == vm {
		authCookies.register = fn
	}
}

// CompleteAuthCookieInitialization runs once on Runtime's startup thread after
// real client-settings initialization, never while a native worker waits for
// the Main thread from inside a Java callback.
func (e *Env) CompleteAuthCookieInitialization() {
	authCookies.Lock()
	ready := e != nil && authCookies.vm == e.vm && !authCookies.registered && authCookies.register != nil
	fn := authCookies.register
	if ready {
		authCookies.registered = true
	}
	authCookies.Unlock()
	if ready {
		fn()
	}
}

// StoreAuthCookies receives official Set-Cookie strings. It deliberately
// returns fixed errors; neither native values nor filesystem names are logged.
func (vm *VM) StoreAuthCookies(url string, cookies []string) error {
	authCookies.Lock()
	defer authCookies.Unlock()
	if authCookies.vm != vm || authCookies.store == nil {
		return errors.New("cookie storage unavailable")
	}
	if err := authCookies.store.set(url, cookies); err != nil {
		return errors.New("cookie storage write failed")
	}
	logging.Logger(logging.CatFilesystem).Info("official cookie update persisted")
	return nil
}

func (vm *VM) authCookieFailure() {
	logging.Logger(logging.CatFilesystem).Error("official cookie persistence failed")
	vm.mu.Lock()
	defer vm.mu.Unlock()
	o := vm.newObjectLocked(vm.ensureClassLocked("java/lang/IllegalStateException"))
	o.str = "cookie persistence unavailable"
	vm.pending = o.id
}

func (vm *VM) dispatchAuthCookies(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	switch {
	case class == cookieHandlerClass && name == "onSetCookie" && sig == "([Ljava/lang/String;Ljava/lang/String;)V":
		var cookies []string
		var arr *Object
		if args != nil {
			arr = vm.get(jobjectToID(uintptr(C.tipsy_jvalue_l(args))))
		}
		if arr != nil {
			vm.mu.Lock()
			for _, id := range arr.elems {
				if s := vm.objects[id]; s != nil {
					cookies = append(cookies, s.str)
				}
			}
			vm.mu.Unlock()
		}
		if err := vm.StoreAuthCookies(vm.stringFromArg(args, 1), cookies); err != nil {
			vm.authCookieFailure()
		}
	case class == cookieProtocolClass && name == "setCookie" && sig == "(Ljava/lang/String;Ljava/lang/String;)V":
		if err := vm.StoreAuthCookies(vm.stringFromArg(args, 0), []string{vm.stringFromArg(args, 1)}); err != nil {
			vm.authCookieFailure()
		}

	default:
		return jnull(), false
	}
	return jnull(), true
}

// JNI test harness uses the exact two-object callback ABI without native
// client values; Go test files cannot import C.
func testAuthCookieArgs(first, second int64) *C.jvalue {
	args := make([]C.jvalue, 2)
	C.tipsy_jvalue_set_l(&args[0], idToJobject(first))
	C.tipsy_jvalue_set_l(&args[1], idToJobject(second))
	return &args[0]
}
