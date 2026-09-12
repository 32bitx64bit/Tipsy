// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

/*
#cgo CFLAGS: -I${SRCDIR}/../../native
#include "jni_bridge.h"
*/
import "C"

import (
	"encoding/json"
	"math"
	"sync"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// nativeUserClass is the engine's Java user-snapshot surface.
// Official Android getPlatformName is ""; Computer-only Playable Devices
// need Enum.Platform.Windows (owner-licensed spoof). Other NativeUser
// getters return the DID_LOG_IN snapshot from
// NativeHelper.gameActivity_onDidLogInReceived, or honest zeros before that
// callback supplies a JSON object.
const nativeUserClass = "com/roblox/engine/jni/user/NativeUserJavaInterface"

const nativeUserPlatformName = "Windows"

// nativeUserSnapshot is the process-wide Java user identity the engine
// already logged as DID_LOG_IN. Only official JSON fields are stored.
// Nothing here is logged except the allowed aggregate in applyNativeUserLoginJSON.
type nativeUserSnapshot struct {
	UserID                int64
	HasUserID             bool
	IsUnder13             bool
	Username              string
	DisplayName           string
	AlternateName         string
	MembershipType        int32
	HasRobloxSubscription bool
	Theme                 string
}

var (
	nativeUserLogged sync.Map
	nativeUserMu     sync.Mutex
	nativeUserSnap   nativeUserSnapshot

	immortalNativeUserPlatform      *Object
	immortalNativeUserUsername      *Object
	immortalNativeUserDisplayName   *Object
	immortalNativeUserAlternateName *Object
	immortalNativeUserTheme         *Object
)

func nativeUserSnapshotCopy() nativeUserSnapshot {
	nativeUserMu.Lock()
	snap := nativeUserSnap
	nativeUserMu.Unlock()
	return snap
}

func storeNativeUserSnapshot(snap nativeUserSnapshot) {
	nativeUserMu.Lock()
	nativeUserSnap = snap
	nativeUserMu.Unlock()
}

func jniLong(n int64) C.jobject {
	return C.jobject(uintptr(uint64(n)))
}

func nativeUserEnv(vm *VM) unsafe.Pointer {
	env := currentEnvPtr()
	if env == nil && vm != nil {
		env = vm.envRaw
	}
	return env
}

func jsonNumberInt64(n json.Number) (int64, bool) {
	if i, err := n.Int64(); err == nil {
		return i, true
	}
	f, err := n.Float64()
	if err != nil {
		return 0, false
	}
	i := int64(f)
	if float64(i) != f {
		return 0, false
	}
	return i, true
}

func jsonRawInt64(raw map[string]json.RawMessage, key string) (int64, bool) {
	v, ok := raw[key]
	if !ok {
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	return jsonNumberInt64(n)
}

func jsonRawBool(raw map[string]json.RawMessage, key string) (bool, bool) {
	v, ok := raw[key]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false, false
	}
	return b, true
}

func jsonRawString(raw map[string]json.RawMessage, key string) (string, bool) {
	v, ok := raw[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

// parseNativeUserLoginJSON reads one official DID_LOG_IN object. Unknown
// keys are ignored. Empty or malformed input returns ok=false so the
// process-wide snapshot stays unchanged. The payload itself is never logged.
func parseNativeUserLoginJSON(payload string) (nativeUserSnapshot, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil || raw == nil {
		return nativeUserSnapshot{}, false
	}
	var snap nativeUserSnapshot
	touched := false
	if id, ok := jsonRawInt64(raw, "userId"); ok {
		snap.UserID = id
		snap.HasUserID = true
		touched = true
	}
	if v, ok := jsonRawBool(raw, "isUnder13"); ok {
		snap.IsUnder13 = v
		touched = true
	}
	if v, ok := jsonRawString(raw, "username"); ok {
		snap.Username = v
		touched = true
	}
	if v, ok := jsonRawString(raw, "displayName"); ok {
		snap.DisplayName = v
		touched = true
	}
	if v, ok := jsonRawString(raw, "alternateName"); ok {
		snap.AlternateName = v
		touched = true
	}
	if v, ok := jsonRawString(raw, "theme"); ok {
		snap.Theme = v
		touched = true
	}
	if n, ok := jsonRawInt64(raw, "membershipType"); ok && n >= math.MinInt32 && n <= math.MaxInt32 {
		snap.MembershipType = int32(n)
		touched = true
	}
	if v, ok := jsonRawBool(raw, "hasRobloxSubscription"); ok {
		snap.HasRobloxSubscription = v
		touched = true
	}
	if !touched {
		return nativeUserSnapshot{}, false
	}
	return snap, true
}

// applyNativeUserLoginJSON stores a process-wide NativeUser snapshot from
// the official gameActivity_onDidLogInReceived JSON. Malformed or empty
// payloads leave the snapshot unchanged. The raw JSON, names, and ids are
// never logged.
func applyNativeUserLoginJSON(payload string) {
	snap, ok := parseNativeUserLoginJSON(payload)
	if !ok {
		return
	}
	storeNativeUserSnapshot(snap)
	logging.Logger(logging.CatJNI).Info("[jni] native-user snapshot",
		"hasUserId", snap.HasUserID,
		"under13", snap.IsUnder13,
		"membershipType", snap.MembershipType)
}

// ResetNativeUserForTest clears the DID_LOG_IN snapshot and interned
// getter strings to the pre-login state. Test seam: production snapshots
// are engine statements and are never cleared.
func ResetNativeUserForTest() {
	storeNativeUserSnapshot(nativeUserSnapshot{})
	immortalNativeUserPlatform = nil
	immortalNativeUserUsername = nil
	immortalNativeUserDisplayName = nil
	immortalNativeUserAlternateName = nil
	immortalNativeUserTheme = nil
	nativeUserLogged.Range(func(k, _ any) bool {
		nativeUserLogged.Delete(k)
		return true
	})
}

func (vm *VM) dispatchNativeUser(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	_ = o
	_ = args
	if class != nativeUserClass {
		return jnull(), false
	}
	env := nativeUserEnv(vm)
	snap := nativeUserSnapshotCopy()
	switch name + sig {
	case "getPlatformName()Ljava/lang/String;":
		if _, dup := nativeUserLogged.LoadOrStore(name, struct{}{}); !dup {
			logging.Logger(logging.CatJNI).Info("[jni] native-user",
				"method", name, "nonEmpty", true, "spoof", "pc")
		}
		return vm.internString(env, &immortalNativeUserPlatform, nativeUserPlatformName), true
	case "getUserId()J":
		return jniLong(snap.UserID), true
	case "getIsUnder13()Z":
		return jniBool(snap.IsUnder13), true
	case "getUsername()Ljava/lang/String;":
		return vm.internString(env, &immortalNativeUserUsername, snap.Username), true
	case "getDisplayName()Ljava/lang/String;":
		return vm.internString(env, &immortalNativeUserDisplayName, snap.DisplayName), true
	case "getAlternateName()Ljava/lang/String;":
		return vm.internString(env, &immortalNativeUserAlternateName, snap.AlternateName), true
	case "getMembershipType()I":
		return jniInt(snap.MembershipType), true
	case "getHasRobloxSubscription()Z":
		return jniBool(snap.HasRobloxSubscription), true
	case "getTheme()Ljava/lang/String;":
		return vm.internString(env, &immortalNativeUserTheme, snap.Theme), true
	default:
		return jnull(), false
	}
}
