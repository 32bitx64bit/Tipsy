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

	"github.com/tipsy-linux/tipsy/internal/loader"
	"github.com/tipsy-linux/tipsy/internal/logging"
	"github.com/tipsy-linux/tipsy/internal/x11"
)

// Official universalapp WebView protocol, answered over the MessageBus.
//
// Lua listing clicks (Servers, similar web dialogs) go through
// BrowserService.OpenBrowserWindow / WebViewProtocolCore::OpenWindow, which
// is a MessageBus request the APK's Java WebViewProtocol answers. Tipsy
// plays that Java role using only the exported MessageBus natives (no
// hooks) and shows the page in an X11 child WebKit overlay.
//
// Ground truth (classes2.dex WebViewProtocol.<init> + JNI getters in
// libroblox.so): protocol name getProtocolName → "WebView"; methods
// isAvailable (MessageBus.p → setRequestHandlerRaw), openWindow /
// mutateWindow / closeWindow (MessageBus.t → doSubscribeRaw). JSON keys
// from getUrlKey / getWindowTypeKey / getHideHeaderKey / getAvailableKey.
const (
	webViewProtocolClass = "com/roblox/protocols/webview/WebViewProtocol"

	webViewProtocolName = "WebView"

	webViewMethodIsAvailable       = "isAvailable"
	webViewMethodOpenWindow        = "openWindow"
	webViewMethodMutateWindow      = "mutateWindow"
	webViewMethodCloseWindow       = "closeWindow"
	webViewMethodHandleWindowClose = "handleWindowClose"

	webViewKeyAvailable  = "available"
	webViewKeyURL        = "url"
	webViewKeyTitle      = "title"
	webViewKeyWindowType = "windowType"
	webViewKeyVisible    = "isVisible"
	webViewKeyHideHeader = "hideHeader"

	webViewMethodField = "tipsy.webViewProtocolMethod"

	webViewResponseOK      int32 = 0
	webViewResponseBadJSON int32 = 13
)

// webViewProtocolMethods is Java WebViewProtocol.<init> registration order
// plus handleWindowClose (getter exists; Java does not subscribe it there).
var webViewProtocolMethods = []string{
	webViewMethodIsAvailable,
	webViewMethodOpenWindow,
	webViewMethodMutateWindow,
	webViewMethodCloseWindow,
	webViewMethodHandleWindowClose,
}

// WebViewProtocolExports are official Java→native entry points resolved by
// name. Zero means the export is absent; each path degrades independently.
type WebViewProtocolExports struct {
	SetRequestHandlerRaw             uintptr
	PublishProtocolMethodResponseRaw uintptr
	DoSubscribeRaw                   uintptr
	GetMessageId                     uintptr
	PublishRaw                       uintptr
	InitializeAndroidWebViewProtocol uintptr
	SignalJavascriptCallback         uintptr
}

type webViewProtocolState struct {
	mu      sync.Mutex
	exports WebViewProtocolExports
	busID   int64
	vm      *VM
}

var (
	webViewProtocol       webViewProtocolState
	webViewProtocolLogged sync.Map
)

// RegisterWebViewProtocol plays the APK's Java WebViewProtocol role.
func (e *Env) RegisterWebViewProtocol(x WebViewProtocolExports) int {
	if e == nil || e.vm == nil {
		return 0
	}
	if x.SetRequestHandlerRaw == 0 && x.DoSubscribeRaw == 0 {
		logging.Logger(logging.CatJNI).Error("[jni] WebViewProtocol unavailable: MessageBus registration exports missing")
		return 0
	}
	e.FindClass(webViewProtocolClass)
	bus := e.AllocObject(e.FindClass(messageBusClass))
	if bus == 0 {
		return 0
	}
	e.vm.pinObject(bus)
	webViewProtocol.mu.Lock()
	webViewProtocol.exports = x
	webViewProtocol.busID = jobjectToID(bus)
	webViewProtocol.vm = e.vm
	webViewProtocol.mu.Unlock()
	x11.SetWebViewJavascriptSignal(signalWebViewJavascript)
	x11.SetWebViewUserClosed(publishWebViewHandleWindowClose)

	handlerCls := e.FindClass(requestHandlerRawClass)
	callbackCls := e.FindClass(rawCallbackClass)
	e.FindClass(messageBusConnectionClass)
	protocol := e.NewStringUTF(webViewProtocolName)
	registered := 0
	for _, method := range webViewProtocolMethods {
		m := e.NewStringUTF(method)
		ok := false
		// Java setRequestHandlerRaw only for isAvailable (MessageBus.p).
		if method == webViewMethodIsAvailable && x.SetRequestHandlerRaw != 0 {
			h := e.AllocObject(handlerCls)
			e.PutField(h, webViewMethodField, method)
			e.vm.pinObject(h)
			loader.CallP8(x.SetRequestHandlerRaw, e.Raw(), bus, protocol, m, h, 0, 0, 0)
			ok = true
		}
		// Java MessageBus.t → doSubscribeRaw(getMessageId(protocol, method), cb, sticky=false).
		// DEX getMessageId(Ljava/lang/String;Ljava/lang/String;)Ljava/lang/String;
		// formats "%s.%s" (WebView.openWindow). Native NewStringUTF must be read
		// through GetStringUTFChars; o.str alone is empty on some jstrings.
		// CallP8 doSubscribeRaw: env, this=bus, messageId, callback, sticky=false.
		if webViewTopicMethod(method) && x.DoSubscribeRaw != 0 {
			cb := e.AllocObject(callbackCls)
			e.PutField(cb, webViewMethodField, method)
			e.vm.pinObject(cb)
			topic := e.webViewSubscribeTopic(x.GetMessageId, bus, protocol, m, method)
			conn := loader.CallP8(x.DoSubscribeRaw, e.Raw(), bus, topic, cb, 0, 0, 0, 0)
			if conn != 0 {
				e.vm.pinObject(uintptr(conn))
			}
			ok = true
		}
		if ok {
			registered++
		}
	}
	if x.InitializeAndroidWebViewProtocol != 0 {
		cls := e.FindClass(webViewProtocolClass)
		loader.CallP8(x.InitializeAndroidWebViewProtocol, e.Raw(), cls, 0, 0, 0, 0, 0, 0)
	}
	logging.Logger(logging.CatJNI).Info("[jni] WebViewProtocol registered",
		"methods", registered,
		"requestHandler", x.SetRequestHandlerRaw != 0,
		"subscribeRaw", x.DoSubscribeRaw != 0,
		"publishRaw", x.PublishRaw != 0,
		"jsCallback", x.SignalJavascriptCallback != 0,
		"nativeInit", x.InitializeAndroidWebViewProtocol != 0)
	return registered
}

func webViewTopicMethod(method string) bool {
	switch method {
	case webViewMethodOpenWindow, webViewMethodMutateWindow, webViewMethodCloseWindow:
		return true
	}
	return false
}

// webViewJavaTopic is the official MessageBus getMessageId format ("%s.%s").
func webViewJavaTopic(method string) string {
	return webViewProtocolName + "." + method
}

func (e *Env) jstringText(str uintptr) string {
	if e == nil || str == 0 {
		return ""
	}
	if s, err := e.GetStringUTFChars(str); err == nil && s != "" {
		return s
	}
	if e.vm == nil {
		return ""
	}
	if o := e.vm.get(jobjectToID(str)); o != nil {
		return o.str
	}
	return ""
}

func (e *Env) webViewSubscribeTopic(getID, bus, protocol, methodStr uintptr, method string) uintptr {
	topic := ""
	if getID != 0 && e != nil {
		mid := loader.CallP8(getID, e.Raw(), bus, protocol, methodStr, 0, 0, 0, 0)
		topic = e.jstringText(uintptr(mid))
	}
	if topic == "" {
		topic = webViewJavaTopic(method)
	}
	logWebViewSubscribeOnce(method, topic)
	return e.NewStringUTF(topic)
}

func logWebViewSubscribeOnce(method, topic string) {
	key := "sub|" + method + "|" + topic
	if _, dup := webViewProtocolLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	logging.Logger(logging.CatJNI).Info("[jni] WebViewProtocol subscribed",
		"method", method, "topic", topic)
}

// ResetWebViewProtocolForTest clears registration state and one-shot logs.
func ResetWebViewProtocolForTest() {
	webViewProtocol.mu.Lock()
	webViewProtocol.exports = WebViewProtocolExports{}
	webViewProtocol.busID = 0
	webViewProtocol.vm = nil
	webViewProtocol.mu.Unlock()
	webViewProtocolLogged.Range(func(k, _ any) bool {
		webViewProtocolLogged.Delete(k)
		return true
	})
	x11.SetWebViewJavascriptSignal(nil)
	x11.SetWebViewUserClosed(nil)
}

type webViewOpenParams struct {
	URL               string          `json:"url"`
	Title             string          `json:"title"`
	WindowType        string          `json:"windowType"`
	IsVisible         *bool           `json:"isVisible"`
	HideHeader        *bool           `json:"hideHeader"`
	ShowDomainAsTitle *bool           `json:"showDomainAsTitle"`
	BackButtonVisible *bool           `json:"backButtonVisible"`
	SearchParams      json.RawMessage `json:"searchParams"`
	SearchType        string          `json:"searchType"`
}

func parseWebViewOpenParams(request string) (webViewOpenParams, bool) {
	var p webViewOpenParams
	dec := json.NewDecoder(strings.NewReader(request))
	if err := dec.Decode(&p); err != nil {
		return webViewOpenParams{}, false
	}
	return p, true
}

func untaggedWebViewMethod(request string) (string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(request), &raw); err != nil || raw == nil {
		return "", false
	}
	for _, steal := range []string{"permissions", "status", "moduleID", "openURL", "intent"} {
		if _, ok := raw[steal]; ok {
			return "", false
		}
	}
	p, ok := parseWebViewOpenParams(request)
	if !ok {
		return "", false
	}
	if p.IsVisible != nil && !*p.IsVisible {
		return webViewMethodMutateWindow, true
	}
	if strings.TrimSpace(p.URL) != "" {
		return webViewMethodOpenWindow, true
	}
	if len(raw) == 0 {
		return webViewMethodCloseWindow, true
	}
	if _, hasURL := raw["url"]; hasURL {
		return webViewMethodCloseWindow, true
	}
	return "", false
}

func webViewIsAvailableResponse() string {
	return mustJSON(map[string]bool{webViewKeyAvailable: true})
}

func webViewProtocolMethodOf(vm *VM, o *Object) (string, bool) {
	if vm == nil || o == nil {
		return "", false
	}
	vm.mu.RLock()
	defer vm.mu.RUnlock()
	m, ok := o.fields[webViewMethodField].(string)
	return m, ok && m != ""
}

func logWebViewProtocolOnce(arm, method, windowType string, class x11.WebViewURLClass, result string, code int32) {
	logWebViewProtocolDetail(arm, method, windowType, class, result, code, -1, "")
}

func logWebViewProtocolDetail(arm, method, windowType string, class x11.WebViewURLClass, result string, code int32, cookies int, theme string) {
	key := arm + "|" + method + "|" + windowType + "|" + class.Scheme + "|" + class.Host + "|" + class.PathClass + "|" + result
	if cookies >= 0 {
		key += "|c"
	}
	if _, dup := webViewProtocolLogged.LoadOrStore(key, struct{}{}); dup {
		return
	}
	fields := []any{
		"arm", arm, "method", method, "hasUrl", class.HasURL,
		"scheme", class.Scheme, "host", class.Host, "path", class.PathClass,
		"windowType", windowType, "open", result, "code", code,
	}
	if cookies >= 0 {
		fields = append(fields, "cookies", cookies, "theme", theme, "layout", "client")
	}
	logging.Logger(logging.CatJNI).Info("[jni] WebViewProtocol", fields...)
}

func (vm *VM) applyWebViewOpen(arm, request string) (string, int32) {
	p, ok := parseWebViewOpenParams(request)
	class := x11.ClassifyWebViewURL(p.URL)
	if !ok {
		logWebViewProtocolOnce(arm, webViewMethodOpenWindow, "", class, "bad-json", webViewResponseBadJSON)
		return "{}", webViewResponseBadJSON
	}
	if strings.TrimSpace(p.URL) == "" {
		logWebViewProtocolOnce(arm, webViewMethodOpenWindow, p.WindowType, class, "missing-url", webViewResponseOK)
		return "{}", webViewResponseOK
	}
	result := "ok"
	cookies, theme, err := openWebViewOverlay(p)
	if err != nil {
		result = "overlay-unavailable"
	}
	logWebViewProtocolDetail(arm, webViewMethodOpenWindow, p.WindowType, class, result, webViewResponseOK, cookies, theme)
	return "{}", webViewResponseOK
}

func openWebViewOverlay(p webViewOpenParams) (int, string, error) {
	theme := NativeUserTheme()
	cookies := CopyAuthCookiesForWebView()
	n := len(cookies)
	xc := make([]x11.WebViewCookie, len(cookies))
	for i, c := range cookies {
		xc[i] = x11.WebViewCookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			HostOnly: c.HostOnly, Secure: c.Secure, HTTPOnly: c.HTTPOnly, Expires: c.Expires,
		}
	}
	xc = x11.EnsureWebViewThemeCookie(xc, theme)
	hide := true
	if p.HideHeader != nil {
		hide = *p.HideHeader
	}
	err := x11.ShowWebViewOverlay(x11.WebViewOpen{
		URL:        p.URL,
		WindowType: p.WindowType,
		Theme:      theme,
		Title:      p.Title,
		HideHeader: hide,
		Cookies:    xc,
	})
	return n, x11.WebViewWebsiteTheme(theme), err
}

func (vm *VM) applyWebViewMutate(arm, request string) (string, int32) {
	p, ok := parseWebViewOpenParams(request)
	if !ok {
		return "{}", webViewResponseBadJSON
	}
	if p.IsVisible != nil && !*p.IsVisible {
		x11.HideWebViewOverlay()
		logWebViewProtocolOnce(arm, webViewMethodMutateWindow, p.WindowType, x11.WebViewURLClass{}, "hide", webViewResponseOK)
		return "{}", webViewResponseOK
	}
	if strings.TrimSpace(p.URL) != "" {
		_, _, _ = openWebViewOverlay(p)
	}
	logWebViewProtocolOnce(arm, webViewMethodMutateWindow, p.WindowType, x11.ClassifyWebViewURL(p.URL), "ok", webViewResponseOK)
	return "{}", webViewResponseOK
}

func (vm *VM) applyWebViewClose(arm string) (string, int32) {
	x11.HideWebViewOverlay()
	logWebViewProtocolOnce(arm, webViewMethodCloseWindow, "", x11.WebViewURLClass{}, "ok", webViewResponseOK)
	return "{}", webViewResponseOK
}

func signalWebViewJavascript(cmd string) {
	webViewProtocol.mu.Lock()
	fn := webViewProtocol.exports.SignalJavascriptCallback
	vm := webViewProtocol.vm
	webViewProtocol.mu.Unlock()
	if fn == 0 || vm == nil || strings.TrimSpace(cmd) == "" {
		return
	}
	env := vm.Env()
	if env == nil {
		return
	}
	cls := env.FindClass(webViewProtocolClass)
	s := env.NewStringUTF(cmd)
	loader.CallP8(fn, env.Raw(), cls, s, 0, 0, 0, 0, 0)
}

func publishWebViewHandleWindowClose() {
	webViewProtocol.mu.Lock()
	publish := webViewProtocol.exports.PublishRaw
	getID := webViewProtocol.exports.GetMessageId
	busID := webViewProtocol.busID
	vm := webViewProtocol.vm
	webViewProtocol.mu.Unlock()
	if publish == 0 || busID == 0 || vm == nil {
		return
	}
	env := vm.Env()
	if env == nil {
		return
	}
	protocol := env.NewStringUTF(webViewProtocolName)
	method := env.NewStringUTF(webViewMethodHandleWindowClose)
	topic := method
	if getID != 0 {
		mid := loader.CallP8(getID, env.Raw(), uintptr(idToJobject(busID)), protocol, method, 0, 0, 0, 0)
		if s := env.jstringText(uintptr(mid)); s != "" {
			topic = env.NewStringUTF(s)
		} else {
			topic = env.NewStringUTF(webViewJavaTopic(webViewMethodHandleWindowClose))
		}
	} else {
		topic = env.NewStringUTF(webViewJavaTopic(webViewMethodHandleWindowClose))
	}
	body := env.NewStringUTF("{}")
	loader.CallP8(publish, env.Raw(), uintptr(idToJobject(busID)), topic, body, 0, 0, 0, 0)
}

func (vm *VM) answerWebViewProtocol(arm, method, request string) (string, int32) {
	switch method {
	case webViewMethodIsAvailable:
		logWebViewProtocolOnce(arm, method, "", x11.WebViewURLClass{}, "true", webViewResponseOK)
		return webViewIsAvailableResponse(), webViewResponseOK
	case webViewMethodOpenWindow:
		return vm.applyWebViewOpen(arm, request)
	case webViewMethodMutateWindow:
		return vm.applyWebViewMutate(arm, request)
	case webViewMethodCloseWindow, webViewMethodHandleWindowClose:
		return vm.applyWebViewClose(arm)
	}
	return "{}", webViewResponseBadJSON
}

func (vm *VM) publishWebViewResponse(method, response string, code int32) {
	webViewProtocol.mu.Lock()
	fn := webViewProtocol.exports.PublishProtocolMethodResponseRaw
	busID := webViewProtocol.busID
	webViewProtocol.mu.Unlock()
	if fn == 0 || busID == 0 {
		if _, dup := webViewProtocolLogged.LoadOrStore("publish-missing", struct{}{}); !dup {
			logging.Logger(logging.CatJNI).Error("[jni] WebViewProtocol response dropped: publishProtocolMethodResponseRaw export unavailable")
		}
		return
	}
	env := permissionsProtocolEnv(vm)
	vm.mu.Lock()
	protocol := vm.newStringOn(env, webViewProtocolName)
	m := vm.newStringOn(env, method)
	body := vm.newStringOn(env, response)
	telemetry := vm.newStringOn(env, "{}")
	vm.mu.Unlock()
	loader.CallP8(fn, uintptr(env), uintptr(idToJobject(busID)),
		uintptr(idToJobject(protocol.id)), uintptr(idToJobject(m.id)), uintptr(idToJobject(body.id)),
		uintptr(uint32(code)), uintptr(idToJobject(telemetry.id)), 0)
}

func (vm *VM) dispatchWebViewProtocol(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if name != "run" {
		return jnull(), false
	}
	switch sig {
	case requestHandlerRawRunSig:
		if !permissionsProtocolReceiver(o, class, requestHandlerRawClass) {
			return jnull(), false
		}
		if _, perm := permissionsProtocolMethodOf(vm, o); perm {
			return jnull(), false
		}
		method, ok := webViewProtocolMethodOf(vm, o)
		if !ok {
			return jnull(), false
		}
		response, _ := vm.answerWebViewProtocol("handler", method, vm.stringFromArg(args, 0))
		env := permissionsProtocolEnv(vm)
		vm.mu.Lock()
		s := vm.newStringOn(env, response)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	case rawCallbackRunSig:
		if !permissionsProtocolReceiver(o, class, rawCallbackClass) {
			return jnull(), false
		}
		if _, perm := permissionsProtocolMethodOf(vm, o); perm {
			return jnull(), false
		}
		method, ok := webViewProtocolMethodOf(vm, o)
		arm := "topic"
		request := vm.stringFromArg(args, 0)
		if !ok {
			method, ok = untaggedWebViewMethod(request)
			arm = "untagged"
		}
		if !ok {
			return jnull(), false
		}
		response, code := vm.answerWebViewProtocol(arm, method, request)
		if method == webViewMethodIsAvailable {
			vm.publishWebViewResponse(method, response, code)
		}
		return jnull(), true
	}
	return jnull(), false
}
