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
	"strings"
	"sync"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Official universalapp PermissionsProtocol, answered over the MessageBus.
//
// The engine never checks RECORD_AUDIO through Context.checkSelfPermission.
// RBX::Voice::RobloxAudioDevice::CheckMicrophonePermissionAsync (and
// Soundscape::AudioDeviceInput, RealtimeMedia, VideoCaptureClient) go through
// RBX::PermissionsProtocolCore::hasPermissions/requestPermissions, which is a
// MessageBus request the APK's Java PermissionsProtocol answers. Without an
// answer the voice stack never initializes and the in-experience microphone UI
// never appears. Tipsy plays that Java role using only the exported
// Java_com_roblox_universalapp_messagebus_MessageBus_* natives (no hooks).
//
// Ground truth (client 2.738.1397): APK classes2.dex, obfuscated Java class
// sm/k; every token below also appears verbatim in libroblox.so strings
// (PermissionsProtocol, HasPermissions, PermissionsRequest, SupportsPermissions,
// permissions, status, AUTHORIZED, DENIED, UNSUPPORTED, MICROPHONE_ACCESS,
// CAMERA_ACCESS).
const (
	messageBusClass           = "com/roblox/universalapp/messagebus/MessageBus"
	messageBusConnectionClass = "com/roblox/universalapp/messagebus/Connection"
	requestHandlerRawClass    = "com/roblox/universalapp/messagebus/RequestHandlerRaw"
	rawCallbackClass          = "com/roblox/universalapp/messagebus/RawCallback"

	permissionsProtocolName = "PermissionsProtocol"

	permissionsMethodHas       = "HasPermissions"
	permissionsMethodRequest   = "PermissionsRequest"
	permissionsMethodSupports  = "SupportsPermissions"
	permissionsMethodUpsell    = "ShouldShowPermissionUpsell"
	permissionsMethodRationale = "ShouldShowRequestPermissionRationale"

	permissionMicrophoneAccess = "MICROPHONE_ACCESS"
	permissionLocalNetwork     = "LOCAL_NETWORK"

	permissionStatusAuthorized = "AUTHORIZED"
	permissionStatusDenied     = "DENIED"
	permissionUpsellShow       = "SHOW"
	permissionUpsellHide       = "HIDE"

	// RequestHandlerRaw.run(String)String is the synchronous handler contract
	// (Java MessageBus$b); RawCallback.run(String)V is the legacy request-topic
	// subscription contract (Java MessageBus$a). Both receive the request
	// params JSON.
	requestHandlerRawRunSig = "(Ljava/lang/String;)Ljava/lang/String;"
	rawCallbackRunSig       = "(Ljava/lang/String;)V"

	permissionsMethodField = "tipsy.permissionsProtocolMethod"

	// MessageBus.m() response codes used by the Java protocol.
	permissionsResponseOK      int32 = 0
	permissionsResponseBadJSON int32 = 13
)

// permissionsProtocolMethods is every request method the Java protocol
// registers, in its registration order.
var permissionsProtocolMethods = []string{
	permissionsMethodRequest,
	permissionsMethodHas,
	permissionsMethodSupports,
	permissionsMethodUpsell,
	permissionsMethodRationale,
}

// PermissionsProtocolExports are the official Java→native MessageBus entry
// points (libroblox.so dynsym), resolved by name in the launch sequence.
// Zero means the export is absent; each path degrades independently.
type PermissionsProtocolExports struct {
	// (JNIEnv*, jobject bus, jstring protocol, jstring method, jobject RequestHandlerRaw) -> void
	SetRequestHandlerRaw uintptr
	// (JNIEnv*, jobject bus, jstring protocol, jstring method, jobject RawCallback, jboolean sticky) -> jobject Connection
	DoSubscribeProtocolMethodRequestRaw uintptr
	// (JNIEnv*, jobject bus, jstring protocol, jstring method, jstring response, jint code, jstring telemetry) -> void
	PublishProtocolMethodResponseRaw uintptr
}

type permissionsProtocolState struct {
	mu      sync.Mutex
	exports PermissionsProtocolExports
	busID   int64
}

var (
	permissionsProtocol       permissionsProtocolState
	permissionsProtocolLogged sync.Map
)

// RegisterPermissionsProtocol plays the APK's Java PermissionsProtocol role:
// Tipsy becomes the request handler and the legacy request-topic subscriber
// for every official method, through the exported MessageBus natives only.
// Java registers both arms as well (the handler arm behind a flag), so the
// engine is answered whichever request path its build uses. Returns the number
// of methods registered on at least one arm.
func (e *Env) RegisterPermissionsProtocol(x PermissionsProtocolExports) int {
	if e == nil || e.vm == nil {
		return 0
	}
	if x.SetRequestHandlerRaw == 0 && x.DoSubscribeProtocolMethodRequestRaw == 0 {
		logging.Logger(logging.CatJNI).Error("[jni] PermissionsProtocol unavailable: MessageBus registration exports missing")
		return 0
	}
	bus := e.AllocObject(e.FindClass(messageBusClass))
	if bus == 0 {
		return 0
	}
	e.vm.pinObject(bus)
	permissionsProtocol.mu.Lock()
	permissionsProtocol.exports = x
	permissionsProtocol.busID = jobjectToID(bus)
	permissionsProtocol.mu.Unlock()

	handlerCls := e.FindClass(requestHandlerRawClass)
	callbackCls := e.FindClass(rawCallbackClass)
	e.FindClass(messageBusConnectionClass)
	protocol := e.NewStringUTF(permissionsProtocolName)
	registered := 0
	for _, method := range permissionsProtocolMethods {
		m := e.NewStringUTF(method)
		ok := false
		if x.SetRequestHandlerRaw != 0 {
			h := e.AllocObject(handlerCls)
			e.PutField(h, permissionsMethodField, method)
			e.vm.pinObject(h)
			loader.CallP8(x.SetRequestHandlerRaw, e.Raw(), bus, protocol, m, h, 0, 0, 0)
			ok = true
		}
		if x.DoSubscribeProtocolMethodRequestRaw != 0 {
			cb := e.AllocObject(callbackCls)
			e.PutField(cb, permissionsMethodField, method)
			e.vm.pinObject(cb)
			// sticky=false, as Java MessageBus.w(). The returned Connection is
			// owned by the native shared_ptr; Java only finalizes it on GC,
			// which Tipsy never does, so the subscription lives for the process.
			conn := loader.CallP8(x.DoSubscribeProtocolMethodRequestRaw, e.Raw(), bus, protocol, m, cb, 0, 0, 0)
			if conn != 0 {
				e.vm.pinObject(uintptr(conn))
			}
			ok = true
		}
		if ok {
			registered++
		}
	}
	logging.Logger(logging.CatJNI).Info("[jni] PermissionsProtocol registered",
		"methods", registered,
		"requestHandler", x.SetRequestHandlerRaw != 0,
		"requestTopic", x.DoSubscribeProtocolMethodRequestRaw != 0,
		"responsePublish", x.PublishProtocolMethodResponseRaw != 0)
	return registered
}

// pinObject keeps a Tipsy jobject alive for the process regardless of local
// frame lifetime. Handlers and the bus object are referenced from native
// global refs; pinning removes any dependence on how long the launch thread's
// local frame survives.
func (vm *VM) pinObject(obj uintptr) {
	if vm == nil || obj == 0 {
		return
	}
	o := vm.get(jobjectToID(obj))
	if o == nil {
		return
	}
	vm.mu.Lock()
	o.markImmortal()
	vm.mu.Unlock()
}

// ResetPermissionsProtocolForTest clears registration state and one-shot logs.
func ResetPermissionsProtocolForTest() {
	permissionsProtocol.mu.Lock()
	permissionsProtocol.exports = PermissionsProtocolExports{}
	permissionsProtocol.busID = 0
	permissionsProtocol.mu.Unlock()
	permissionsProtocolLogged.Range(func(k, _ any) bool {
		permissionsProtocolLogged.Delete(k)
		return true
	})
}

// protocolPermissionGranted is Tipsy's honest grant table. MICROPHONE_ACCESS follows
// the microphone door (Settings toggle / TIPSY_MICROPHONE). LOCAL_NETWORK
// needs no runtime permission on Android and none on the host. Everything
// else (camera, contacts, notifications, media picker, media storage) has no
// host bridge and is reported missing rather than faked.
func protocolPermissionGranted(name string, micAllowed bool) bool {
	switch name {
	case permissionMicrophoneAccess:
		return micAllowed
	case permissionLocalNetwork:
		return true
	}
	return false
}

// supportedPermissions is the honest SupportsPermissions answer: the
// permissions Tipsy can actually evaluate.
var supportedPermissions = []string{permissionMicrophoneAccess, permissionLocalNetwork}

type permissionsRequestParams struct {
	Permissions []string `json:"permissions"`
}

type permissionsStatusResponse struct {
	Status             string   `json:"status"`
	MissingPermissions []string `json:"missingPermissions"`
}

type permissionsUpsellResponse struct {
	UpsellStatus            string   `json:"upsellStatus"`
	HiddenUpsellPermissions []string `json:"hiddenUpsellPermissions"`
}

type permissionsSupportsResponse struct {
	Permissions []string `json:"permissions"`
}

// parsePermissionsRequest reads {"permissions":[...]} exactly as Java
// sm/k.p() does (getJSONArray("permissions")). Unknown keys are ignored.
func parsePermissionsRequest(request string) ([]string, bool) {
	var p permissionsRequestParams
	dec := json.NewDecoder(strings.NewReader(request))
	if err := dec.Decode(&p); err != nil || p.Permissions == nil {
		return nil, false
	}
	return p.Permissions, true
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// permissionsProtocolResponse builds the response JSON for one official
// method, mirroring the Java response shapes:
//
//	HasPermissions / PermissionsRequest -> {"status":"AUTHORIZED"|"DENIED","missingPermissions":[...]}
//	SupportsPermissions                 -> {"permissions":[...]}
//	ShouldShowPermissionUpsell /
//	ShouldShowRequestPermissionRationale -> {"upsellStatus":"SHOW"|"HIDE","hiddenUpsellPermissions":[...granted...]}
//
// PermissionsRequest has no OS prompt to show: the Tipsy Settings toggle is
// the consent surface, so it answers like HasPermissions. Malformed requests
// answer the denied shape with the Java JSON-error code 13.
func permissionsProtocolResponse(method, request string, micAllowed bool) (string, int32) {
	perms, ok := parsePermissionsRequest(request)
	switch method {
	case permissionsMethodHas, permissionsMethodRequest:
		if !ok {
			return mustJSON(permissionsStatusResponse{Status: permissionStatusDenied, MissingPermissions: []string{}}), permissionsResponseBadJSON
		}
		missing := []string{}
		for _, p := range perms {
			if !protocolPermissionGranted(p, micAllowed) {
				missing = append(missing, p)
			}
		}
		status := permissionStatusAuthorized
		if len(missing) > 0 {
			status = permissionStatusDenied
		}
		return mustJSON(permissionsStatusResponse{Status: status, MissingPermissions: missing}), permissionsResponseOK
	case permissionsMethodSupports:
		return mustJSON(permissionsSupportsResponse{Permissions: append([]string(nil), supportedPermissions...)}), permissionsResponseOK
	case permissionsMethodUpsell, permissionsMethodRationale:
		if !ok {
			return mustJSON(permissionsUpsellResponse{UpsellStatus: permissionUpsellHide, HiddenUpsellPermissions: []string{}}), permissionsResponseBadJSON
		}
		hidden := []string{}
		for _, p := range perms {
			if protocolPermissionGranted(p, micAllowed) {
				hidden = append(hidden, p)
			}
		}
		status := permissionUpsellHide
		if len(hidden) == 0 {
			status = permissionUpsellShow
		}
		return mustJSON(permissionsUpsellResponse{UpsellStatus: status, HiddenUpsellPermissions: hidden}), permissionsResponseOK
	}
	return "{}", permissionsResponseBadJSON
}

func permissionsProtocolMethodOf(vm *VM, o *Object) (string, bool) {
	if vm == nil || o == nil {
		return "", false
	}
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	m, ok := o.fields[permissionsMethodField].(string)
	return m, ok && m != ""
}

func permissionsProtocolReceiver(o *Object, class, want string) bool {
	return class == want || classIs(o, want)
}

// logPermissionsProtocolOnce records one line per distinct (method, request,
// status) triple. Permission names are protocol tokens, never secrets.
func logPermissionsProtocolOnce(arm, method, request, response string, code int32) {
	key := arm + "|" + method + "|" + request + "|" + response
	if _, dup := permissionsProtocolLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] PermissionsProtocol",
		"arm", arm, "method", method, "request", request, "response", response, "code", code)
}

func permissionsProtocolEnv(vm *VM) unsafe.Pointer {
	env := currentEnvPtr()
	if env == nil && vm != nil {
		env = vm.envRaw
	}
	return env
}

func (vm *VM) answerPermissionsProtocol(arm, method, request string) (string, int32) {
	micAllowed := microphoneDoorOpen()
	response, code := permissionsProtocolResponse(method, request, micAllowed)
	logPermissionsProtocolOnce(arm, method, request, response, code)
	if strings.Contains(request, permissionMicrophoneAccess) &&
		(method == permissionsMethodHas || method == permissionsMethodRequest) {
		logRecordAudioOnce(micAllowed)
	}
	return response, code
}

// publishPermissionsResponse answers a legacy request-topic delivery the way
// Java MessageBus.m() does: publishProtocolMethodResponseRaw(protocol,
// method, responseJSON, code, telemetryJSON). Called re-entrantly from inside
// the RawCallback upcall, exactly as the Java callback does.
func (vm *VM) publishPermissionsResponse(method, response string, code int32) {
	permissionsProtocol.mu.Lock()
	fn := permissionsProtocol.exports.PublishProtocolMethodResponseRaw
	busID := permissionsProtocol.busID
	permissionsProtocol.mu.Unlock()
	if fn == 0 || busID == 0 {
		if _, dup := permissionsProtocolLogged.LoadOrStore("publish-missing", struct{}{}); !dup {
			logging.Logger(logging.CatJNI).Error("[jni] PermissionsProtocol response dropped: publishProtocolMethodResponseRaw export unavailable")
		}
		return
	}
	env := permissionsProtocolEnv(vm)
	vm.mu.Lock()
	protocol := vm.newStringOn(env, permissionsProtocolName)
	m := vm.newStringOn(env, method)
	body := vm.newStringOn(env, response)
	telemetry := vm.newStringOn(env, "{}")
	vm.mu.Unlock()
	loader.CallP8(fn, uintptr(env), uintptr(idToJobject(busID)),
		uintptr(idToJobject(protocol.id)), uintptr(idToJobject(m.id)), uintptr(idToJobject(body.id)),
		uintptr(uint32(code)), uintptr(idToJobject(telemetry.id)), 0)
}

func (vm *VM) dispatchPermissionsProtocol(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if name != "run" {
		return jnull(), false
	}
	switch sig {
	case requestHandlerRawRunSig:
		if !permissionsProtocolReceiver(o, class, requestHandlerRawClass) {
			return jnull(), false
		}
		method, ok := permissionsProtocolMethodOf(vm, o)
		if !ok {
			return jnull(), false
		}
		response, _ := vm.answerPermissionsProtocol("handler", method, vm.stringFromArg(args, 0))
		env := permissionsProtocolEnv(vm)
		vm.mu.Lock()
		s := vm.newStringOn(env, response)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case rawCallbackRunSig:
		if !permissionsProtocolReceiver(o, class, rawCallbackClass) {
			return jnull(), false
		}
		method, ok := permissionsProtocolMethodOf(vm, o)
		if !ok {
			return jnull(), false
		}
		response, code := vm.answerPermissionsProtocol("topic", method, vm.stringFromArg(args, 0))
		vm.publishPermissionsResponse(method, response, code)
		return jnull(), true
	}
	return jnull(), false
}
