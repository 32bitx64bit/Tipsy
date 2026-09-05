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
	"net"
	"strings"
	"unsafe"
)

// Android ConnectivityManager.TYPE_* / NetworkCapabilities constants
// (AOSP android.net; SDK 26). Roblox DEX 2.734.917 GetMethodIDs these.
const (
	typeMobile   int32 = 0
	typeWifi     int32 = 1
	typeEthernet int32 = 9

	netCapNotMetered    int32 = 11
	netCapInternet      int32 = 12
	netCapNotRestricted int32 = 13
	netCapTrusted       int32 = 14
	netCapNotVPN        int32 = 15
	netCapValidated     int32 = 16

	transportCellular int32 = 0
	transportWifi     int32 = 1
	transportEthernet int32 = 3
)

// hostNetworkUp reports whether the Linux host has a non-loopback interface
// that is up with a unicast address. This is host reachability, not Roblox
// auth and not a security bypass.
var hostNetworkUp = detectHostNetwork

func detectHostNetwork() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifs {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() {
				continue
			}
			if ipnet.IP.IsUnspecified() || ipnet.IP.IsLinkLocalMulticast() {
				continue
			}
			if ipnet.IP.To4() != nil || ipnet.IP.To16() != nil {
				return true
			}
		}
	}
	return false
}

func jniBool(v bool) C.jobject {
	if v {
		return C.jobject(unsafe.Pointer(uintptr(1)))
	}
	return jnull()
}

func jniInt(n int32) C.jobject {
	return C.jobject(unsafe.Pointer(uintptr(uint32(n))))
}

func packJint(v int32) *C.jvalue {
	sl := make([]C.jvalue, 1)
	C.tipsy_jvalue_set_i(&sl[0], C.jint(v))
	return &sl[0]
}

func packJobject(obj C.jobject) *C.jvalue {
	sl := make([]C.jvalue, 1)
	C.tipsy_jvalue_set_l(&sl[0], obj)
	return &sl[0]
}

func classIs(o *Object, name string) bool {
	if o == nil {
		return false
	}
	for c := o.class; c != nil; c = c.super {
		if c.name == name {
			return true
		}
	}
	return false
}

func (vm *VM) seedConnectivity() {
	object := vm.classes["java/lang/Object"]
	for _, name := range []string{
		"android/net/Network",
		"android/net/NetworkCapabilities",
		"android/net/NetworkRequest",
		"android/net/NetworkInfo$State",
		"android/net/NetworkInfo$DetailedState",
		"android/net/ConnectivityManager$NetworkCallback",
	} {
		if vm.classes[name] == nil {
			vm.defineClass(name, object)
		}
	}

	if ctx := vm.classes["android/content/Context"]; ctx != nil && ctx.obj != nil {
		ctx.obj.fields["CONNECTIVITY_SERVICE"] = "connectivity"
		ctx.obj.fields["WIFI_SERVICE"] = "wifi"
		ctx.obj.fields["CONNECTIVITY_ACTION"] = "android.net.conn.CONNECTIVITY_CHANGE"
	}

	if cm := vm.classes["android/net/ConnectivityManager"]; cm != nil && cm.obj != nil {
		cm.obj.fields["TYPE_MOBILE"] = typeMobile
		cm.obj.fields["TYPE_WIFI"] = typeWifi
		cm.obj.fields["TYPE_ETHERNET"] = typeEthernet
		cm.obj.fields["TYPE_VPN"] = int32(17)
		cm.obj.fields["DEFAULT_NETWORK_PREFERENCE"] = typeWifi
	}

	if nc := vm.classes["android/net/NetworkCapabilities"]; nc != nil && nc.obj != nil {
		nc.obj.fields["NET_CAPABILITY_NOT_METERED"] = netCapNotMetered
		nc.obj.fields["NET_CAPABILITY_INTERNET"] = netCapInternet
		nc.obj.fields["NET_CAPABILITY_NOT_RESTRICTED"] = netCapNotRestricted
		nc.obj.fields["NET_CAPABILITY_TRUSTED"] = netCapTrusted
		nc.obj.fields["NET_CAPABILITY_NOT_VPN"] = netCapNotVPN
		nc.obj.fields["NET_CAPABILITY_VALIDATED"] = netCapValidated
		nc.obj.fields["TRANSPORT_CELLULAR"] = transportCellular
		nc.obj.fields["TRANSPORT_WIFI"] = transportWifi
		nc.obj.fields["TRANSPORT_ETHERNET"] = transportEthernet
	}

	seedEnum := func(class, name string) {
		cls := vm.classes[class]
		if cls == nil {
			return
		}
		o := vm.newObjectLocked(cls)
		o.markImmortal()
		o.str = name
		o.fields["name"] = name
		if cls.obj != nil {
			vm.storeFieldObjLocked(cls.obj, name, o.id)
		}
	}
	for _, st := range []string{"CONNECTING", "CONNECTED", "SUSPENDED", "DISCONNECTING", "DISCONNECTED", "UNKNOWN"} {
		seedEnum("android/net/NetworkInfo$State", st)
	}
	for _, st := range []string{"IDLE", "SCANNING", "CONNECTING", "AUTHENTICATING", "OBTAINING_IPADDR", "CONNECTED", "SUSPENDED", "DISCONNECTING", "DISCONNECTED", "FAILED", "BLOCKED", "VERIFYING_POOR_LINK", "CAPTIVE_PORTAL_CHECK"} {
		seedEnum("android/net/NetworkInfo$DetailedState", st)
	}
}

func (vm *VM) enumField(class, name string) C.jobject {
	cls := vm.classes[class]
	if cls == nil || cls.obj == nil {
		return jnull()
	}
	id, ok := cls.obj.fields[name].(int64)
	if !ok || id == 0 {
		return jnull()
	}
	return idToJobject(id)
}

func (vm *VM) newNetworkInfoLocked(up bool) *Object {
	cls := vm.ensureClassLocked("android/net/NetworkInfo")
	o := vm.newObjectLocked(cls)
	o.fields["connected"] = up
	o.fields["available"] = up
	o.fields["roaming"] = false
	o.fields["type"] = typeWifi
	o.fields["typeName"] = "WIFI"
	o.fields["subtype"] = int32(0)
	o.fields["subtypeName"] = ""
	if up {
		vm.storeFieldObjLocked(o, "state", jobjectToID(uintptr(vm.enumField("android/net/NetworkInfo$State", "CONNECTED"))))
		vm.storeFieldObjLocked(o, "detailedState", jobjectToID(uintptr(vm.enumField("android/net/NetworkInfo$DetailedState", "CONNECTED"))))
	} else {
		vm.storeFieldObjLocked(o, "state", jobjectToID(uintptr(vm.enumField("android/net/NetworkInfo$State", "DISCONNECTED"))))
		vm.storeFieldObjLocked(o, "detailedState", jobjectToID(uintptr(vm.enumField("android/net/NetworkInfo$DetailedState", "DISCONNECTED"))))
	}
	return o
}

func (vm *VM) newNetworkLocked() *Object {
	cls := vm.ensureClassLocked("android/net/Network")
	o := vm.newObjectLocked(cls)
	o.fields["netId"] = int32(100)
	o.fields["networkHandle"] = int64(100)
	return o
}

func (vm *VM) newNetworkCapabilitiesLocked(up bool) *Object {
	cls := vm.ensureClassLocked("android/net/NetworkCapabilities")
	o := vm.newObjectLocked(cls)
	var capMask int64
	var transportMask int64
	if up {
		for _, b := range []int32{
			netCapNotMetered, netCapInternet, netCapNotRestricted,
			netCapTrusted, netCapNotVPN, netCapValidated,
		} {
			capMask |= 1 << uint32(b)
		}
		transportMask = 1 << uint32(transportWifi)
	}
	o.fields["capMask"] = capMask
	o.fields["transportMask"] = transportMask
	return o
}

func (vm *VM) lookupSystemService(svc string) C.jobject {
	clsName := "java/lang/Object"
	switch svc {
	case "window":
		clsName = "android/view/WindowManager"
	case "connectivity":
		clsName = "android/net/ConnectivityManager"
	case "activity":
		clsName = "android/app/ActivityManager"
	case "input_method":
		clsName = "android/view/inputmethod/InputMethodManager"
	case "input":
		clsName = "android/hardware/input/InputManager"
	}
	vm.mu.Lock()
	cls := vm.ensureClassLocked(clsName)
	s := vm.newObjectLocked(cls)
	vm.mu.Unlock()
	return idToJobject(s.id)
}

func (vm *VM) classNameFromArg(args *C.jvalue, i int) string {
	if args == nil {
		return ""
	}
	o := vm.get(jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, C.int(i)))))
	if o == nil {
		return ""
	}
	if n, ok := o.fields["name"].(string); ok {
		return n
	}
	return o.str
}

func connectivityIdentity(class, name, sig string) bool {
	switch class {
	case "android/net/ConnectivityManager",
		"android/net/NetworkInfo",
		"android/net/Network",
		"android/net/NetworkCapabilities":
		return true
	}
	// Context.getSystemService(Class) is answered here so the Class overload
	// is not lost when the receiver is an Activity rather than a net type.
	return name == "getSystemService" && sig == "(Ljava/lang/Class;)Ljava/lang/Object;"
}

func (vm *VM) dispatchConnectivity(o *Object, class, name, sig string, args *C.jvalue) (C.jobject, bool) {
	if !connectivityIdentity(class, name, sig) {
		return jnull(), false
	}
	key := name + sig
	switch key {
	case "getActiveNetworkInfo()Landroid/net/NetworkInfo;":
		if !hostNetworkUp() {
			return jnull(), true
		}
		vm.mu.Lock()
		n := vm.newNetworkInfoLocked(true)
		vm.mu.Unlock()
		return idToJobject(n.id), true
	case "getNetworkInfo(I)Landroid/net/NetworkInfo;":
		typ := jvalueIAt(args, 0)
		if !hostNetworkUp() || (typ != typeWifi && typ != typeEthernet) {
			return jnull(), true
		}
		vm.mu.Lock()
		n := vm.newNetworkInfoLocked(true)
		if typ == typeEthernet {
			n.fields["type"] = typeEthernet
			n.fields["typeName"] = "ETHERNET"
		}
		vm.mu.Unlock()
		return idToJobject(n.id), true
	case "getActiveNetwork()Landroid/net/Network;":
		if !hostNetworkUp() {
			return jnull(), true
		}
		vm.mu.Lock()
		n := vm.newNetworkLocked()
		vm.mu.Unlock()
		return idToJobject(n.id), true
	case "getAllNetworks()[Landroid/net/Network;":
		vm.mu.Lock()
		arrCls := vm.ensureClassLocked("java/lang/Object")
		arr := vm.newObjectLocked(arrCls)
		if hostNetworkUp() {
			n := vm.newNetworkLocked()
			arr.elems = []int64{n.id}
		} else {
			arr.elems = nil
		}
		vm.mu.Unlock()
		return idToJobject(arr.id), true
	case "getNetworkCapabilities(Landroid/net/Network;)Landroid/net/NetworkCapabilities;":
		netObj := (*Object)(nil)
		if args != nil {
			netObj = vm.get(jobjectToID(uintptr(C.tipsy_jvalue_l_at(args, 0))))
		}
		if netObj == nil {
			return jnull(), true
		}
		vm.mu.Lock()
		c := vm.newNetworkCapabilitiesLocked(hostNetworkUp())
		vm.mu.Unlock()
		return idToJobject(c.id), true
	case "isActiveNetworkMetered()Z":
		return jniBool(false), true
	case "registerDefaultNetworkCallback(Landroid/net/ConnectivityManager$NetworkCallback;)V",
		"registerDefaultNetworkCallback(Landroid/net/ConnectivityManager$NetworkCallback;Landroid/os/Handler;)V",
		"registerNetworkCallback(Landroid/net/NetworkRequest;Landroid/net/ConnectivityManager$NetworkCallback;)V",
		"registerNetworkCallback(Landroid/net/NetworkRequest;Landroid/net/ConnectivityManager$NetworkCallback;Landroid/os/Handler;)V",
		"unregisterNetworkCallback(Landroid/net/ConnectivityManager$NetworkCallback;)V":
		return jnull(), true
	case "isConnected()Z", "isConnectedOrConnecting()Z":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		if b, ok := o.fields["connected"].(bool); ok {
			return jniBool(b), true
		}
		return jniBool(hostNetworkUp()), true
	case "isAvailable()Z":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		if b, ok := o.fields["available"].(bool); ok {
			return jniBool(b), true
		}
		return jniBool(hostNetworkUp()), true
	case "isRoaming()Z":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		if b, ok := o.fields["roaming"].(bool); ok {
			return jniBool(b), true
		}
		return jniBool(false), true
	case "getType()I":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		if n, ok := o.fields["type"].(int32); ok {
			return jniInt(n), true
		}
		return jniInt(typeWifi), true
	case "getTypeName()Ljava/lang/String;":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		s := "WIFI"
		if n, ok := o.fields["typeName"].(string); ok {
			s = n
		}
		vm.mu.Lock()
		str := vm.newStringLocked(s)
		vm.mu.Unlock()
		return idToJobject(str.id), true
	case "getSubtype()I":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		if n, ok := o.fields["subtype"].(int32); ok {
			return jniInt(n), true
		}
		return jniInt(0), true
	case "getSubtypeName()Ljava/lang/String;":
		if !classIs(o, "android/net/NetworkInfo") {
			return jnull(), false
		}
		s := ""
		if n, ok := o.fields["subtypeName"].(string); ok {
			s = n
		}
		vm.mu.Lock()
		str := vm.newStringLocked(s)
		vm.mu.Unlock()
		return idToJobject(str.id), true
	case "getState()Landroid/net/NetworkInfo$State;":
		if o != nil {
			if id, ok := o.fields["state"].(int64); ok && id != 0 {
				return idToJobject(id), true
			}
		}
		name := "DISCONNECTED"
		if hostNetworkUp() {
			name = "CONNECTED"
		}
		return vm.enumField("android/net/NetworkInfo$State", name), true
	case "getDetailedState()Landroid/net/NetworkInfo$DetailedState;":
		if o != nil {
			if id, ok := o.fields["detailedState"].(int64); ok && id != 0 {
				return idToJobject(id), true
			}
		}
		name := "DISCONNECTED"
		if hostNetworkUp() {
			name = "CONNECTED"
		}
		return vm.enumField("android/net/NetworkInfo$DetailedState", name), true
	case "hasCapability(I)Z":
		if o == nil || !classIs(o, "android/net/NetworkCapabilities") {
			return jnull(), false
		}
		mask, _ := o.fields["capMask"].(int64)
		bit := jvalueIAt(args, 0)
		if bit < 0 || bit > 62 {
			return jniBool(false), true
		}
		return jniBool(mask&(1<<uint32(bit)) != 0), true
	case "hasTransport(I)Z":
		if o == nil || !classIs(o, "android/net/NetworkCapabilities") {
			return jnull(), false
		}
		mask, _ := o.fields["transportMask"].(int64)
		bit := jvalueIAt(args, 0)
		if bit < 0 || bit > 62 {
			return jniBool(false), true
		}
		return jniBool(mask&(1<<uint32(bit)) != 0), true
	case "getNetworkHandle()J":
		if o == nil || !classIs(o, "android/net/Network") {
			return jnull(), false
		}
		n := int64(0)
		if v, ok := o.fields["networkHandle"].(int64); ok {
			n = v
		}
		return C.jobject(unsafe.Pointer(uintptr(n))), true
	case "getSystemService(Ljava/lang/Class;)Ljava/lang/Object;":
		cn := vm.classNameFromArg(args, 0)
		cn = strings.ReplaceAll(cn, ".", "/")
		switch cn {
		case "android/net/ConnectivityManager":
			return vm.lookupSystemService("connectivity"), true
		case "android/view/WindowManager":
			return vm.lookupSystemService("window"), true
		case "android/app/ActivityManager":
			return vm.lookupSystemService("activity"), true
		case "android/view/inputmethod/InputMethodManager":
			return vm.lookupSystemService("input_method"), true
		case "android/hardware/input/InputManager":
			return vm.lookupSystemService("input"), true
		}
		return vm.lookupSystemService(""), true
	default:
		return jnull(), false
	}
}
