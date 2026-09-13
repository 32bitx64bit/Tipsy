// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/x11"
)

func TestWebViewProtocolMethodsMatchJavaRegistration(t *testing.T) {
	want := []string{"isAvailable", "openWindow", "mutateWindow", "closeWindow", "handleWindowClose"}
	if len(webViewProtocolMethods) != len(want) {
		t.Fatalf("methods=%v", webViewProtocolMethods)
	}
	for i := range want {
		if webViewProtocolMethods[i] != want[i] {
			t.Fatalf("methods[%d]=%s want %s", i, webViewProtocolMethods[i], want[i])
		}
	}
	if webViewProtocolName != "WebView" {
		t.Fatalf("protocol=%s", webViewProtocolName)
	}
	if webViewMethodField == permissionsMethodField {
		t.Fatal("WebView method tag must be distinct from PermissionsProtocol")
	}
	if webViewTopicMethod(webViewMethodIsAvailable) || webViewTopicMethod(webViewMethodHandleWindowClose) {
		t.Fatal("isAvailable/handleWindowClose are not Java topic subscriptions")
	}
	if !webViewTopicMethod(webViewMethodOpenWindow) || !webViewTopicMethod(webViewMethodMutateWindow) || !webViewTopicMethod(webViewMethodCloseWindow) {
		t.Fatal("open/mutate/close must be topic subscriptions")
	}
}

func TestWebViewIsAvailableResponse(t *testing.T) {
	if got := webViewIsAvailableResponse(); got != `{"available":true}` {
		t.Fatalf("available=%s", got)
	}
}

func TestWebViewOpenParamsParse(t *testing.T) {
	p, ok := parseWebViewOpenParams(`{"url":"https://www.roblox.com/games/1818/x","windowType":"Default","hideHeader":true}`)
	if !ok || p.URL == "" || p.WindowType != "Default" || p.HideHeader == nil || !*p.HideHeader {
		t.Fatalf("params=%+v ok=%v", p, ok)
	}
	class := x11.ClassifyWebViewURL(p.URL)
	if class.PathClass != "/games" || class.Host != "www.roblox.com" {
		t.Fatalf("class=%+v", class)
	}
}

func newWebViewProtocolObject(t *testing.T, vm *VM, class, method string) *Object {
	t.Helper()
	o := newPermissionReceiver(t, vm, class)
	vm.mu.Lock()
	if method != "" {
		o.fields[webViewMethodField] = method
	}
	vm.mu.Unlock()
	return o
}

func TestWebViewProtocolHandlerIsAvailable(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	handler := newWebViewProtocolObject(t, vm, requestHandlerRawClass, webViewMethodIsAvailable)
	logs := captureLogs(t)
	vm.mu.Lock()
	req := vm.newStringLocked(`{}`)
	vm.mu.Unlock()
	v, handled := vm.dispatch(idToJobject(handler.id), requestHandlerRawClass,
		"run", requestHandlerRawRunSig, testPermissionNameArgs(req.id))
	if !handled {
		t.Fatal("RequestHandlerRaw.run not handled")
	}
	out := vm.get(jobjectToID(uintptr(v)))
	if out == nil || out.str != `{"available":true}` {
		t.Fatalf("response=%+v", out)
	}
	if !strings.Contains(logs.String(), "WebViewProtocol") {
		t.Fatalf("missing protocol log: %s", logs.String())
	}
	if strings.Contains(logs.String(), "http") && strings.Contains(logs.String(), "gameInstanceId") {
		t.Fatal("log leaked a url")
	}
}

func TestWebViewProtocolDoesNotSwallowPermissionsRun(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	ResetPermissionsProtocolForTest()
	perm := newPermissionsProtocolObject(t, vm, requestHandlerRawClass, permissionsMethodHas)
	vm.mu.Lock()
	req := vm.newStringLocked(`{"permissions":["LOCAL_NETWORK"]}`)
	vm.mu.Unlock()
	if _, ok := vm.dispatchWebViewProtocol(perm, requestHandlerRawClass, "run", requestHandlerRawRunSig, testPermissionNameArgs(req.id)); ok {
		t.Fatal("permissions-tagged handler must not be answered by WebViewProtocol")
	}
	web := newWebViewProtocolObject(t, vm, requestHandlerRawClass, webViewMethodIsAvailable)
	if _, ok := vm.dispatchPermissionsProtocol(web, requestHandlerRawClass, "run", requestHandlerRawRunSig, testPermissionNameArgs(req.id)); ok {
		t.Fatal("webview-tagged handler must not be answered by PermissionsProtocol")
	}
}

func TestWebViewProtocolIgnoresUntaggedRun(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	untagged := newWebViewProtocolObject(t, vm, requestHandlerRawClass, "")
	vm.mu.Lock()
	req := vm.newStringLocked(`{"url":"https://www.roblox.com/games/1818"}`)
	vm.mu.Unlock()
	if _, ok := vm.dispatchWebViewProtocol(untagged, requestHandlerRawClass, "run", requestHandlerRawRunSig, testPermissionNameArgs(req.id)); ok {
		t.Fatal("untagged RequestHandlerRaw must not be answered")
	}
}

func TestWebViewProtocolRegisterWithoutExports(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	env := vm.Env()
	logs := captureLogs(t)
	if n := env.RegisterWebViewProtocol(WebViewProtocolExports{}); n != 0 {
		t.Fatalf("registered %d methods without exports", n)
	}
	if !strings.Contains(logs.String(), "MessageBus registration exports missing") {
		t.Fatalf("missing honest unavailable log: %s", logs.String())
	}
}

func TestWebViewProtocolOpenWindowMissingURL(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	handler := newWebViewProtocolObject(t, vm, requestHandlerRawClass, webViewMethodOpenWindow)
	logs := captureLogs(t)
	vm.mu.Lock()
	req := vm.newStringLocked(`{"windowType":"Default"}`)
	vm.mu.Unlock()
	v, handled := vm.dispatch(idToJobject(handler.id), requestHandlerRawClass,
		"run", requestHandlerRawRunSig, testPermissionNameArgs(req.id))
	if !handled {
		t.Fatal("openWindow not handled")
	}
	out := vm.get(jobjectToID(uintptr(v)))
	if out == nil || out.str != "{}" {
		t.Fatalf("response=%+v", out)
	}
	if !strings.Contains(logs.String(), "missing-url") {
		t.Fatalf("missing missing-url log: %s", logs.String())
	}
}

func TestWebViewProtocolCloseHidesOverlay(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	handler := newWebViewProtocolObject(t, vm, requestHandlerRawClass, webViewMethodCloseWindow)
	vm.mu.Lock()
	req := vm.newStringLocked(`{}`)
	vm.mu.Unlock()
	v, handled := vm.dispatch(idToJobject(handler.id), requestHandlerRawClass,
		"run", requestHandlerRawRunSig, testPermissionNameArgs(req.id))
	if !handled {
		t.Fatal("closeWindow not handled")
	}
	out := vm.get(jobjectToID(uintptr(v)))
	if out == nil || out.str != "{}" {
		t.Fatalf("response=%+v", out)
	}
}

func TestWebViewJavaTopicMatchesMessageBusFormat(t *testing.T) {
	if got := webViewJavaTopic(webViewMethodOpenWindow); got != "WebView.openWindow" {
		t.Fatalf("topic=%s", got)
	}
	if webViewJavaTopic(webViewMethodMutateWindow) != "WebView.mutateWindow" {
		t.Fatal("mutate topic")
	}
	if webViewJavaTopic(webViewMethodCloseWindow) != "WebView.closeWindow" {
		t.Fatal("close topic")
	}
}

func TestJstringTextReadsUTFChars(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	s := env.NewStringUTF("WebView.openWindow")
	if got := env.jstringText(s); got != "WebView.openWindow" {
		t.Fatalf("jstringText=%q", got)
	}
}

func dispatchUntaggedRawCallback(t *testing.T, vm *VM, body string) (handled bool, logs string) {
	t.Helper()
	ResetWebViewProtocolForTest()
	cb := newWebViewProtocolObject(t, vm, rawCallbackClass, "")
	buf := captureLogs(t)
	vm.mu.Lock()
	req := vm.newStringLocked(body)
	vm.mu.Unlock()
	_, handled = vm.dispatch(idToJobject(cb.id), rawCallbackClass,
		"run", rawCallbackRunSig, testPermissionNameArgs(req.id))
	return handled, buf.String()
}

func TestWebViewProtocolUntaggedRawCallbackOpen(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	handled, logs := dispatchUntaggedRawCallback(t, vm,
		`{"url":"https://www.roblox.com/games/1818/x","windowType":"Default"}`)
	if !handled {
		t.Fatal("untagged RawCallback with url must open")
	}
	if !strings.Contains(logs, "WebViewProtocol") || !strings.Contains(logs, "hasUrl") {
		t.Fatalf("missing open log: %s", logs)
	}
	if !strings.Contains(logs, "www.roblox.com") || !strings.Contains(logs, "/games") {
		t.Fatalf("missing class: %s", logs)
	}
	if strings.Contains(logs, "1818") || strings.Contains(logs, "://") {
		t.Fatal("log leaked a url")
	}
}

func TestWebViewProtocolUntaggedRawCallbackHide(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	handled, logs := dispatchUntaggedRawCallback(t, vm, `{"isVisible":false}`)
	if !handled {
		t.Fatal("untagged isVisible:false must hide")
	}
	if !strings.Contains(logs, "hide") {
		t.Fatalf("missing hide log: %s", logs)
	}
}

func TestWebViewProtocolUntaggedRawCallbackClose(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	handled, logs := dispatchUntaggedRawCallback(t, vm, `{}`)
	if !handled {
		t.Fatal("untagged empty JSON must close")
	}
	if !strings.Contains(logs, "WebViewProtocol") {
		t.Fatalf("missing close log: %s", logs)
	}
}

func TestWebViewProtocolUntaggedTaggedStillWorks(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	cb := newWebViewProtocolObject(t, vm, rawCallbackClass, webViewMethodOpenWindow)
	logs := captureLogs(t)
	vm.mu.Lock()
	req := vm.newStringLocked(`{"url":"https://www.roblox.com/games/1818"}`)
	vm.mu.Unlock()
	_, handled := vm.dispatch(idToJobject(cb.id), rawCallbackClass,
		"run", rawCallbackRunSig, testPermissionNameArgs(req.id))
	if !handled {
		t.Fatal("tagged openWindow must still run")
	}
	if !strings.Contains(logs.String(), "WebViewProtocol") {
		t.Fatalf("missing tagged log: %s", logs.String())
	}
}

func TestWebViewProtocolUntaggedDoesNotStealPermissionsOrLinking(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	ResetPermissionsProtocolForTest()
	cb := newWebViewProtocolObject(t, vm, rawCallbackClass, "")
	vm.mu.Lock()
	perm := vm.newStringLocked(`{"permissions":["MICROPHONE_ACCESS"]}`)
	link := vm.newStringLocked(`{"openURL":"https://www.roblox.com/games/1818"}`)
	vm.mu.Unlock()
	if _, ok := vm.dispatchWebViewProtocol(cb, rawCallbackClass, "run", rawCallbackRunSig, testPermissionNameArgs(perm.id)); ok {
		t.Fatal("permissions JSON must not be claimed as WebView")
	}
	if _, ok := vm.dispatchWebViewProtocol(cb, rawCallbackClass, "run", rawCallbackRunSig, testPermissionNameArgs(link.id)); ok {
		t.Fatal("Linking openURL JSON must not be claimed as WebView")
	}
	taggedPerm := newPermissionsProtocolObject(t, vm, rawCallbackClass, permissionsMethodHas)
	if _, ok := vm.dispatchWebViewProtocol(taggedPerm, rawCallbackClass, "run", rawCallbackRunSig, testPermissionNameArgs(perm.id)); ok {
		t.Fatal("permissions-tagged RawCallback must stay exclusive")
	}
}

func TestWebViewProtocolSharedRunInternDoesNotStubLaterFamily(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	ResetWebViewProtocolForTest()
	ResetPermissionsProtocolForTest()
	resetStubDispatchForTest()
	mid, _ := internMethod(rawCallbackClass, "run", rawCallbackRunSig, false)
	if info, ok := lookupMethod(mid); ok && info != nil {
		info.handler.Store(wrapDispatchFamilies(rawCallbackClass, "run", rawCallbackRunSig))
	}
	perm := newPermissionsProtocolObject(t, vm, rawCallbackClass, permissionsMethodSupports)
	vm.mu.Lock()
	empty := vm.newStringLocked(`{}`)
	open := vm.newStringLocked(`{"url":"https://www.roblox.com/games/1818"}`)
	vm.mu.Unlock()
	logs := captureLogs(t)
	vm.callAArgs(perm.id, mid, 0, 'V', testPermissionNameArgs(empty.id))
	untagged := newWebViewProtocolObject(t, vm, rawCallbackClass, "")
	vm.callAArgs(untagged.id, mid, 0, 'V', testPermissionNameArgs(open.id))
	out := logs.String()
	if !strings.Contains(out, "WebViewProtocol") {
		t.Fatalf("interned permissions run must still dispatch WebView: %s", out)
	}
	if strings.Contains(out, "stub-dispatch") && strings.Contains(out, "RawCallback") {
		t.Fatalf("shared RawCallback.run stubbed a later family: %s", out)
	}
}
