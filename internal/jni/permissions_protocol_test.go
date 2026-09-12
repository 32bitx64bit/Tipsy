// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"
)

func TestPermissionsProtocolResponseShapes(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		request  string
		mic      bool
		want     string
		wantCode int32
	}{
		{
			name: "has-mic-authorized", method: permissionsMethodHas,
			request: `{"permissions":["MICROPHONE_ACCESS"]}`, mic: true,
			want: `{"status":"AUTHORIZED","missingPermissions":[]}`, wantCode: 0,
		},
		{
			name: "has-mic-denied", method: permissionsMethodHas,
			request: `{"permissions":["MICROPHONE_ACCESS"]}`, mic: false,
			want: `{"status":"DENIED","missingPermissions":["MICROPHONE_ACCESS"]}`, wantCode: 0,
		},
		{
			name: "request-mic-authorized", method: permissionsMethodRequest,
			request: `{"permissions":["MICROPHONE_ACCESS"]}`, mic: true,
			want: `{"status":"AUTHORIZED","missingPermissions":[]}`, wantCode: 0,
		},
		{
			name: "request-camera-honest-missing", method: permissionsMethodRequest,
			request: `{"permissions":["CAMERA_ACCESS","MICROPHONE_ACCESS"]}`, mic: true,
			want: `{"status":"DENIED","missingPermissions":["CAMERA_ACCESS"]}`, wantCode: 0,
		},
		{
			name: "has-local-network", method: permissionsMethodHas,
			request: `{"permissions":["LOCAL_NETWORK"]}`, mic: false,
			want: `{"status":"AUTHORIZED","missingPermissions":[]}`, wantCode: 0,
		},
		{
			name: "has-unknown-token-missing", method: permissionsMethodHas,
			request: `{"permissions":["CONTACTS_ACCESS"],"extra":1}`, mic: true,
			want: `{"status":"DENIED","missingPermissions":["CONTACTS_ACCESS"]}`, wantCode: 0,
		},
		{
			name: "has-empty-list-authorized", method: permissionsMethodHas,
			request: `{"permissions":[]}`, mic: false,
			want: `{"status":"AUTHORIZED","missingPermissions":[]}`, wantCode: 0,
		},
		{
			name: "has-malformed-json", method: permissionsMethodHas,
			request: `{"permissions":`, mic: true,
			want: `{"status":"DENIED","missingPermissions":[]}`, wantCode: 13,
		},
		{
			name: "has-missing-key", method: permissionsMethodHas,
			request: `{}`, mic: true,
			want: `{"status":"DENIED","missingPermissions":[]}`, wantCode: 13,
		},
		{
			name: "supports", method: permissionsMethodSupports,
			request: `{}`, mic: false,
			want: `{"permissions":["MICROPHONE_ACCESS","LOCAL_NETWORK"]}`, wantCode: 0,
		},
		{
			name: "upsell-denied-shows", method: permissionsMethodUpsell,
			request: `{"permissions":["MICROPHONE_ACCESS"]}`, mic: false,
			want: `{"upsellStatus":"SHOW","hiddenUpsellPermissions":[]}`, wantCode: 0,
		},
		{
			name: "upsell-granted-hides", method: permissionsMethodUpsell,
			request: `{"permissions":["MICROPHONE_ACCESS"]}`, mic: true,
			want: `{"upsellStatus":"HIDE","hiddenUpsellPermissions":["MICROPHONE_ACCESS"]}`, wantCode: 0,
		},
		{
			name: "rationale-malformed", method: permissionsMethodRationale,
			request: `nope`, mic: true,
			want: `{"upsellStatus":"HIDE","hiddenUpsellPermissions":[]}`, wantCode: 13,
		},
		{
			name: "unknown-method", method: "Bogus",
			request: `{}`, mic: true,
			want: `{}`, wantCode: 13,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, code := permissionsProtocolResponse(tt.method, tt.request, tt.mic)
			if got != tt.want {
				t.Fatalf("response = %s, want %s", got, tt.want)
			}
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func TestPermissionsProtocolMethodsMatchJavaRegistration(t *testing.T) {
	want := []string{"PermissionsRequest", "HasPermissions", "SupportsPermissions",
		"ShouldShowPermissionUpsell", "ShouldShowRequestPermissionRationale"}
	if len(permissionsProtocolMethods) != len(want) {
		t.Fatalf("methods = %v, want %v", permissionsProtocolMethods, want)
	}
	for i := range want {
		if permissionsProtocolMethods[i] != want[i] {
			t.Fatalf("methods[%d] = %s, want %s", i, permissionsProtocolMethods[i], want[i])
		}
	}
	if permissionsProtocolName != "PermissionsProtocol" {
		t.Fatalf("protocol = %s", permissionsProtocolName)
	}
}

func TestPermissionsProtocolImplemented(t *testing.T) {
	for _, sig := range []string{requestHandlerRawRunSig, rawCallbackRunSig} {
		if !isImplementedMethod("run", sig) {
			t.Fatalf("run%s not implemented", sig)
		}
	}
}

func newPermissionsProtocolObject(t *testing.T, vm *VM, class, method string) *Object {
	t.Helper()
	o := newPermissionReceiver(t, vm, class)
	vm.mu.Lock()
	if method != "" {
		o.fields[permissionsMethodField] = method
	}
	vm.mu.Unlock()
	return o
}

func TestPermissionsProtocolHandlerDispatch(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetPermissionsProtocolForTest()
	resetAudioPermissionLogsForTest()
	handler := newPermissionsProtocolObject(t, vm, requestHandlerRawClass, permissionsMethodHas)

	for _, tt := range []struct {
		name string
		mic  string
		want string
		log  string
	}{
		{name: "open", mic: "", want: `{"status":"AUTHORIZED","missingPermissions":[]}`, log: "RECORD_AUDIO granted"},
		{name: "closed", mic: "0", want: `{"status":"DENIED","missingPermissions":["MICROPHONE_ACCESS"]}`, log: "RECORD_AUDIO denied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TIPSY_MICROPHONE", tt.mic)
			t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
			resetAudioPermissionLogsForTest()
			logs := captureLogs(t)
			vm.mu.Lock()
			req := vm.newStringLocked(`{"permissions":["MICROPHONE_ACCESS"]}`)
			vm.mu.Unlock()
			v, handled := vm.dispatch(idToJobject(handler.id), requestHandlerRawClass,
				"run", requestHandlerRawRunSig, testPermissionNameArgs(req.id))
			if !handled {
				t.Fatal("RequestHandlerRaw.run not handled")
			}
			out := vm.get(jobjectToID(uintptr(v)))
			if out == nil || out.str != tt.want {
				t.Fatalf("response object = %+v, want %s", out, tt.want)
			}
			if !strings.Contains(logs.String(), tt.log) {
				t.Fatalf("missing %q in logs: %s", tt.log, logs.String())
			}
			if !strings.Contains(logs.String(), "PermissionsProtocol") {
				t.Fatalf("missing protocol log: %s", logs.String())
			}
		})
	}
}

func TestPermissionsProtocolTopicDispatchWithoutPublishExport(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetPermissionsProtocolForTest()
	cb := newPermissionsProtocolObject(t, vm, rawCallbackClass, permissionsMethodSupports)
	logs := captureLogs(t)
	vm.mu.Lock()
	req := vm.newStringLocked(`{}`)
	vm.mu.Unlock()
	_, handled := vm.dispatch(idToJobject(cb.id), rawCallbackClass, "run", rawCallbackRunSig, testPermissionNameArgs(req.id))
	if !handled {
		t.Fatal("RawCallback.run not handled")
	}
	if !strings.Contains(logs.String(), "publishProtocolMethodResponseRaw export unavailable") {
		t.Fatalf("missing honest dropped-response log: %s", logs.String())
	}
}

func TestPermissionsProtocolIgnoresForeignRun(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetPermissionsProtocolForTest()
	// Same class, but not one of Tipsy's registered handlers: no method tag.
	untagged := newPermissionsProtocolObject(t, vm, requestHandlerRawClass, "")
	vm.mu.Lock()
	req := vm.newStringLocked(`{"permissions":["MICROPHONE_ACCESS"]}`)
	vm.mu.Unlock()
	if _, ok := vm.dispatchPermissionsProtocol(untagged, requestHandlerRawClass, "run", requestHandlerRawRunSig, testPermissionNameArgs(req.id)); ok {
		t.Fatal("untagged RequestHandlerRaw must not be answered")
	}
	// Unrelated class with a run(String)V method must fall through.
	other := newPermissionReceiver(t, vm, "java/lang/Object")
	if _, ok := vm.dispatchPermissionsProtocol(other, "java/lang/Object", "run", rawCallbackRunSig, testPermissionNameArgs(req.id)); ok {
		t.Fatal("foreign run(String)V must not be answered")
	}
}

func TestPermissionsProtocolRegisterWithoutExports(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetPermissionsProtocolForTest()
	env := vm.Env()
	logs := captureLogs(t)
	if n := env.RegisterPermissionsProtocol(PermissionsProtocolExports{}); n != 0 {
		t.Fatalf("registered %d methods without exports", n)
	}
	if !strings.Contains(logs.String(), "MessageBus registration exports missing") {
		t.Fatalf("missing honest unavailable log: %s", logs.String())
	}
}
