// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"
)

func TestFindClassMissingLogs(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()

	tests := []struct {
		name      string
		class     string
		wantFound bool
		auto      bool
	}{
		{name: "string", class: "java/lang/String", wantFound: true},
		{name: "object", class: "java/lang/Object", wantFound: true},
		{name: "activity", class: "android/app/Activity", wantFound: true},
		{name: "gameactivity", class: "com/google/androidgamesdk/GameActivity", wantFound: true},
		{name: "maingameactivity", class: "com/roblox/client/startup/MainGameActivity", wantFound: true},
		{name: "missing-foo", class: "java/no/SuchClass", wantFound: true, auto: true},
		{name: "missing-bar", class: "com/example/NotRegistered", wantFound: true, auto: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			got := env.FindClass(tt.class)
			if tt.wantFound {
				if got == 0 {
					t.Fatalf("FindClass(%q) = NULL, want class", tt.class)
				}
				out := buf.String()
				if tt.auto {
					if !strings.Contains(out, "[jni] auto-class: "+tt.class) {
						t.Fatalf("missing auto-class log: %s", out)
					}
					return
				}
				if strings.Contains(out, "[jni] auto-class: "+tt.class) {
					t.Fatalf("unexpected auto-class log: %s", out)
				}
				return
			}
			if got != 0 {
				t.Fatalf("FindClass(%q) = %v, want NULL", tt.class, got)
			}
			out := buf.String()
			if !strings.Contains(out, "[jni] FindClass: "+tt.class) {
				t.Fatalf("missing FindClass log, got: %s", out)
			}
		})
	}
}

func TestNewStringUTFRoundtrip(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	const want = "hello Tipsy"
	js := env.NewStringUTF(want)
	if js == 0 {
		t.Fatal("NewStringUTF returned NULL")
	}
	got, err := env.GetStringUTFChars(js)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("GetStringUTFChars = %q, want %q", got, want)
	}
}

func TestJavaVMHandle(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if vm.JavaVM() == 0 {
		t.Fatal("JavaVM* is nil")
	}
	if vm.Env() == nil || vm.Env().Raw() == 0 {
		t.Fatal("JNIEnv* is nil")
	}
	if vm.NativeInterface() == 0 {
		t.Fatal("JNINativeInterface* is nil")
	}
	if vm.NativeInterface() == vm.Env().Raw() {
		t.Fatal("NativeInterface must be the function table, not JNIEnv*")
	}
}

func TestFindClassSeeded(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	for _, n := range []string{
		"java/lang/Object",
		"java/lang/String",
		"java/lang/Class",
		"android/app/Activity",
		"android/content/Context",
		"android/content/res/AssetManager",
		"android/content/res/Configuration",
		"android/view/Surface",
		"android/view/SurfaceHolder",
		"android/os/Bundle",
		"com/google/androidgamesdk/GameActivity",
		"com/roblox/client/startup/MainGameActivity",
		"com/roblox/client/LocalStorageManager",
		"com/roblox/engine/jni/locale/NativeLocaleJavaInterface",
		"com/roblox/engine/jni/autovalue/InitParams",
		"com/roblox/engine/jni/model/PlatformParams",
		"java/util/ArrayList",
		"android/net/ConnectivityManager",
		"android/net/NetworkInfo",
		"android/net/Network",
		"android/net/NetworkCapabilities",
	} {
		if env.FindClass(n) == 0 {
			t.Fatalf("seeded class missing: %s", n)
		}
	}
}

func TestAllocObject(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	cls := env.FindClass("com/roblox/client/startup/MainGameActivity")
	if cls == 0 {
		t.Fatal("MainGameActivity missing")
	}
	obj := env.AllocObject(cls)
	if obj == 0 {
		t.Fatal("AllocObject returned NULL")
	}
}

func TestGetAllocatableBytes(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	obj := env.AllocObject(env.FindClass("com/roblox/client/LocalStorageManager"))
	if obj == 0 {
		t.Fatal("LocalStorageManager AllocObject")
	}
	v, ok := vm.dispatch(idToJobject(jobjectToID(obj)), "com/roblox/client/LocalStorageManager", "getAllocatableBytes", "()J", nil)
	if !ok {
		t.Fatal("getAllocatableBytes()J not handled")
	}
	if uintptr(v) != 8<<30 {
		t.Fatalf("getAllocatableBytes = %d", uintptr(v))
	}
}

func TestArrayListAndInitParamsGetters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	list := env.NewArrayList()
	if list == 0 {
		t.Fatal("NewArrayList")
	}
	v, ok := vm.dispatch(idToJobject(jobjectToID(list)), "java/util/ArrayList", "size", "()I", nil)
	if !ok {
		t.Fatal("size() not handled")
	}
	if uintptr(v) != 0 {
		t.Fatalf("empty ArrayList size = %d", uintptr(v))
	}

	init := env.AllocObject(env.FindClass("com/roblox/engine/jni/autovalue/InitParams"))
	env.PutField(init, "baseURL", "https://www.roblox.com")
	v, ok = vm.dispatch(idToJobject(jobjectToID(init)), "", "baseURL", "()Ljava/lang/String;", nil)
	if !ok {
		t.Fatal("baseURL() not handled")
	}
	got, err := env.GetStringUTFChars(uintptr(v))
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://www.roblox.com" {
		t.Fatalf("baseURL = %q", got)
	}

	plat := env.AllocObject(env.FindClass("com/roblox/engine/jni/model/PlatformParams"))
	env.PutField(plat, "dpiScale", float32(1))
	env.PutField(plat, "isTouchDevice", false)
	v, ok = vm.dispatch(idToJobject(jobjectToID(plat)), "", "dpiScale", "()F", nil)
	if !ok {
		t.Fatal("dpiScale() not handled")
	}
	if math.Float32frombits(uint32(uintptr(v))) != 1 {
		t.Fatalf("dpiScale bits = %v", math.Float32frombits(uint32(uintptr(v))))
	}
	v, ok = vm.dispatch(idToJobject(jobjectToID(plat)), "", "isTouchDevice", "()Z", nil)
	if !ok || uintptr(v) != 0 {
		t.Fatalf("isTouchDevice = %d ok=%v", uintptr(v), ok)
	}
}

func TestGetDeviceStaticParams(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	if env.FindClass("com/roblox/engine/jni/NativeGLJavaInterface") == 0 {
		t.Fatal("NativeGLJavaInterface")
	}
	if env.FindClass("com/roblox/engine/jni/model/DeviceStaticParams") == 0 {
		t.Fatal("DeviceStaticParams")
	}
	gl := env.AllocObject(env.FindClass("com/roblox/engine/jni/NativeGLJavaInterface"))
	v, ok := vm.dispatch(idToJobject(jobjectToID(gl)), "com/roblox/engine/jni/NativeGLJavaInterface", "getDeviceStaticParams", "()Lcom/roblox/engine/jni/model/DeviceStaticParams;", nil)
	if !ok {
		t.Fatal("getDeviceStaticParams not handled")
	}
	obj := uintptr(v)
	if obj == 0 {
		t.Fatal("getDeviceStaticParams NULL")
	}
	o := vm.get(jobjectToID(obj))
	if o == nil || o.class == nil || o.class.name != "com/roblox/engine/jni/model/DeviceStaticParams" {
		t.Fatalf("class=%v", o)
	}
	if o.fields["osVersion"] != "26" {
		t.Fatalf("osVersion=%v", o.fields["osVersion"])
	}
	if o.fields["cpu64Bit"] != true {
		t.Fatalf("cpu64Bit=%v", o.fields["cpu64Bit"])
	}
	if o.fields["deviceName"] != "tipsy" {
		t.Fatalf("deviceName=%v", o.fields["deviceName"])
	}
	if !isImplementedMethod("getDeviceStaticParams", "()Lcom/roblox/engine/jni/model/DeviceStaticParams;") {
		t.Fatal("getDeviceStaticParams not in implementedMethods")
	}
}

func TestNewStringLarge(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	buf := make([]byte, 200000)
	for i := range buf {
		buf[i] = 'x'
	}
	payload := `{"ClientAppSettings":{"FFlagFoo":"` + string(buf) + `"}}`
	js := env.NewString(payload)
	got, err := env.GetStringUTFChars(js)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(payload) {
		t.Fatalf("len=%d want %d", len(got), len(payload))
	}
}

func TestBytesArrayRoundtrip(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	want := []byte("flag-cache-bytes")
	arr := env.BytesArray(want)
	if arr == 0 {
		t.Fatal("BytesArray returned NULL")
	}
	o := vm.get(jobjectToID(arr))
	if o == nil || !bytes.Equal(o.bytes, want) {
		t.Fatalf("bytes=%q want %q", o.bytes, want)
	}
}

func TestConnectivityManagerIsConnected(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	for _, n := range []string{
		"android/net/ConnectivityManager",
		"android/net/NetworkInfo",
		"android/net/Network",
		"android/net/NetworkCapabilities",
		"android/net/NetworkInfo$State",
		"android/net/ConnectivityManager$NetworkCallback",
	} {
		if env.FindClass(n) == 0 {
			t.Fatalf("seeded class missing: %s", n)
		}
	}
	for _, pair := range [][2]string{
		{"getActiveNetworkInfo", "()Landroid/net/NetworkInfo;"},
		{"isConnected", "()Z"},
		{"isConnectedOrConnecting", "()Z"},
		{"isAvailable", "()Z"},
		{"getType", "()I"},
		{"getTypeName", "()Ljava/lang/String;"},
		{"getActiveNetwork", "()Landroid/net/Network;"},
		{"hasCapability", "(I)Z"},
		{"hasTransport", "(I)Z"},
		{"isActiveNetworkMetered", "()Z"},
	} {
		if !isImplementedMethod(pair[0], pair[1]) {
			t.Fatalf("not in implementedMethods: %s%s", pair[0], pair[1])
		}
	}

	cm := env.AllocObject(env.FindClass("android/net/ConnectivityManager"))
	if cm == 0 {
		t.Fatal("ConnectivityManager AllocObject")
	}

	t.Run("up", func(t *testing.T) {
		prev := hostNetworkUp
		hostNetworkUp = func() bool { return true }
		defer func() { hostNetworkUp = prev }()

		info, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getActiveNetworkInfo", "()Landroid/net/NetworkInfo;", nil)
		if !ok {
			t.Fatal("getActiveNetworkInfo not handled")
		}
		if uintptr(info) == 0 {
			t.Fatal("getActiveNetworkInfo NULL on a connected host")
		}
		o := vm.get(jobjectToID(uintptr(info)))
		if o == nil || o.class == nil || o.class.name != "android/net/NetworkInfo" {
			t.Fatalf("class=%v", o)
		}
		v, ok := vm.dispatch(info, "android/net/NetworkInfo", "isConnected", "()Z", nil)
		if !ok || uintptr(v) == 0 {
			t.Fatalf("isConnected = %d ok=%v, want true", uintptr(v), ok)
		}
		v, ok = vm.dispatch(info, "android/net/NetworkInfo", "isConnectedOrConnecting", "()Z", nil)
		if !ok || uintptr(v) == 0 {
			t.Fatalf("isConnectedOrConnecting = %d ok=%v", uintptr(v), ok)
		}
		v, ok = vm.dispatch(info, "android/net/NetworkInfo", "isAvailable", "()Z", nil)
		if !ok || uintptr(v) == 0 {
			t.Fatalf("isAvailable = %d ok=%v", uintptr(v), ok)
		}
		v, ok = vm.dispatch(info, "android/net/NetworkInfo", "isRoaming", "()Z", nil)
		if !ok || uintptr(v) != 0 {
			t.Fatalf("isRoaming = %d ok=%v, want false", uintptr(v), ok)
		}
		v, ok = vm.dispatch(info, "android/net/NetworkInfo", "getType", "()I", nil)
		if !ok || int32(uintptr(v)) != typeWifi {
			t.Fatalf("getType = %d ok=%v, want TYPE_WIFI=%d", uintptr(v), ok, typeWifi)
		}
		v, ok = vm.dispatch(info, "android/net/NetworkInfo", "getTypeName", "()Ljava/lang/String;", nil)
		if !ok {
			t.Fatal("getTypeName not handled")
		}
		name, err := env.GetStringUTFChars(uintptr(v))
		if err != nil {
			t.Fatal(err)
		}
		if name != "WIFI" {
			t.Fatalf("getTypeName = %q", name)
		}
		state, ok := vm.dispatch(info, "android/net/NetworkInfo", "getState", "()Landroid/net/NetworkInfo$State;", nil)
		if !ok || uintptr(state) == 0 {
			t.Fatal("getState")
		}
		st := vm.get(jobjectToID(uintptr(state)))
		if st == nil || st.str != "CONNECTED" {
			t.Fatalf("getState = %v", st)
		}

		netw, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getActiveNetwork", "()Landroid/net/Network;", nil)
		if !ok || uintptr(netw) == 0 {
			t.Fatal("getActiveNetwork")
		}
		caps, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getNetworkCapabilities", "(Landroid/net/Network;)Landroid/net/NetworkCapabilities;", packJobject(netw))
		if !ok || uintptr(caps) == 0 {
			t.Fatal("getNetworkCapabilities")
		}
		v, ok = vm.dispatch(caps, "android/net/NetworkCapabilities", "hasCapability", "(I)Z", packJint(netCapInternet))
		if !ok || uintptr(v) == 0 {
			t.Fatalf("hasCapability(INTERNET) = %d ok=%v", uintptr(v), ok)
		}
		v, ok = vm.dispatch(caps, "android/net/NetworkCapabilities", "hasCapability", "(I)Z", packJint(netCapValidated))
		if !ok || uintptr(v) == 0 {
			t.Fatalf("hasCapability(VALIDATED) = %d ok=%v", uintptr(v), ok)
		}
		v, ok = vm.dispatch(caps, "android/net/NetworkCapabilities", "hasTransport", "(I)Z", packJint(transportWifi))
		if !ok || uintptr(v) == 0 {
			t.Fatalf("hasTransport(WIFI) = %d ok=%v", uintptr(v), ok)
		}
		v, ok = vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "isActiveNetworkMetered", "()Z", nil)
		if !ok || uintptr(v) != 0 {
			t.Fatalf("isActiveNetworkMetered = %d ok=%v", uintptr(v), ok)
		}

		wifi, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getNetworkInfo", "(I)Landroid/net/NetworkInfo;", packJint(typeWifi))
		if !ok || uintptr(wifi) == 0 {
			t.Fatal("getNetworkInfo(TYPE_WIFI)")
		}
		mobile, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getNetworkInfo", "(I)Landroid/net/NetworkInfo;", packJint(typeMobile))
		if !ok || uintptr(mobile) != 0 {
			t.Fatalf("getNetworkInfo(TYPE_MOBILE) = %d, want NULL", uintptr(mobile))
		}
	})

	t.Run("down", func(t *testing.T) {
		prev := hostNetworkUp
		hostNetworkUp = func() bool { return false }
		defer func() { hostNetworkUp = prev }()

		info, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getActiveNetworkInfo", "()Landroid/net/NetworkInfo;", nil)
		if !ok {
			t.Fatal("getActiveNetworkInfo not handled")
		}
		if uintptr(info) != 0 {
			t.Fatal("getActiveNetworkInfo must be NULL when the host has no network")
		}
		netw, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getActiveNetwork", "()Landroid/net/Network;", nil)
		if !ok || uintptr(netw) != 0 {
			t.Fatalf("getActiveNetwork = %d ok=%v, want NULL", uintptr(netw), ok)
		}
	})

	t.Run("host", func(t *testing.T) {
		if !detectHostNetwork() {
			t.Skip("no non-loopback interface up on this host")
		}
		info, ok := vm.dispatch(idToJobject(jobjectToID(cm)), "android/net/ConnectivityManager", "getActiveNetworkInfo", "()Landroid/net/NetworkInfo;", nil)
		if !ok || uintptr(info) == 0 {
			t.Fatal("host has a network; getActiveNetworkInfo must not be NULL")
		}
		v, ok := vm.dispatch(info, "android/net/NetworkInfo", "isConnected", "()Z", nil)
		if !ok || uintptr(v) == 0 {
			t.Fatal("host has a network; isConnected must be true")
		}
	})
}

func TestGetSystemServiceConnectivity(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	env := vm.Env()
	act := env.AllocObject(env.FindClass("com/roblox/client/startup/MainGameActivity"))
	svc := vm.lookupSystemService("connectivity")
	if uintptr(svc) == 0 {
		t.Fatal("lookupSystemService connectivity")
	}
	o := vm.get(jobjectToID(uintptr(svc)))
	if o == nil || o.class == nil || o.class.name != "android/net/ConnectivityManager" {
		t.Fatalf("class=%v", o)
	}
	_ = act
}

func TestGetScreenPhysicalSizeInMillimeters(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	// Wired physical size — what launch.go sets from the X server's
	// DisplayWidthMM/DisplayHeightMM on the already-open Display*.
	vm.SetDisplayPhysicalSizeMM(527, 296)
	v, ok := vm.dispatch(jnull(), "java/lang/Class", "getScreenPhysicalSizeInMillimeters", "(Landroid/content/Context;)Landroid/graphics/Point;", nil)
	if !ok {
		t.Fatal("getScreenPhysicalSizeInMillimeters not handled")
	}
	obj := uintptr(v)
	if obj == 0 {
		t.Fatal("NULL Point")
	}
	o := vm.get(jobjectToID(obj))
	if o == nil || o.class == nil || o.class.name != "android/graphics/Point" {
		t.Fatalf("class=%v", o)
	}
	if o.fields["x"] != int32(527) || o.fields["y"] != int32(296) {
		t.Fatalf("x=%v y=%v want 527,296", o.fields["x"], o.fields["y"])
	}
	if !isImplementedMethod("getScreenPhysicalSizeInMillimeters", "(Landroid/content/Context;)Landroid/graphics/Point;") {
		t.Fatal("getScreenPhysicalSizeInMillimeters not in implementedMethods")
	}
}

func TestGetScreenPhysicalSizeFallback(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	// No X11 physical size wired: the standard 96-DPI derivation from
	// the pixel size (px * 25.4 / 96, rounded): 1280x720 -> 339x191.
	vm.SetDisplaySize(1280, 720)
	w, h := vm.screenPhysicalSizeMM()
	if w != 339 || h != 191 {
		t.Fatalf("fallback mm = %d,%d want 339,191", w, h)
	}
	// Once wired, the X-server-reported size wins over the fallback.
	vm.SetDisplayPhysicalSizeMM(500, 280)
	w, h = vm.screenPhysicalSizeMM()
	if w != 500 || h != 280 {
		t.Fatalf("wired mm = %d,%d want 500,280", w, h)
	}
}
