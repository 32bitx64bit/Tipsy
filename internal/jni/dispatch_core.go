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
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

type coreFn func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool)

var coreHandlers = map[string]callHandler{}

func registerCore(key string, fn coreFn) {
	coreHandlers[key] = func(vm *VM, env unsafe.Pointer, obj C.jobject, args *C.jvalue, retKind rune) (C.jobject, bool) {
		_ = retKind
		o := vm.get(jobjectToID(uintptr(obj)))
		return fn(vm, env, o, obj, args)
	}
}

func lookupCoreHandler(name, sig string) callHandler {
	return coreHandlers[name+sig]
}

func init() {
	registerCore("getFilesDir()Ljava/io/File;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internFilesDirFile(env), true
	})
	registerCore("getFilesDir()Ljava/lang/String;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internFilesDirString(env), true
	})
	registerCore("getAppVersion()Ljava/lang/String;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		vm.mu.RLock()
		version := vm.appVersion
		vm.mu.RUnlock()
		return vm.internString(env, &vm.immortalAppVersion, version), true
	})
	registerCore("getCacheDir()Ljava/io/File;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internCacheDirFile(env), true
	})
	registerCore("getObbDir()Ljava/io/File;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internObbDirFile(env), true
	})
	registerCore("getExternalFilesDir(Ljava/lang/String;)Ljava/io/File;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internFilesDirFile(env), true
	})
	registerCore("getAssets()Landroid/content/res/AssetManager;", internClass("android/content/res/AssetManager"))
	registerCore("getWindow()Landroid/view/Window;", internClass("android/view/Window"))
	registerCore("getApplicationContext()Landroid/content/Context;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if o != nil {
			return idToJobject(o.id), true
		}
		return obj, true
	})
	registerCore("getPackageName()Ljava/lang/String;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internString(env, &vm.immortalPackageName, "com.roblox.client"), true
	})
	pathString := func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		p := ""
		if o != nil {
			p = o.str
			vm.mu.RLock()
			id, ok := o.fields["tipsy.pathString"].(int64)
			vm.mu.RUnlock()
			if ok && id != 0 {
				if vm.get(id) != nil {
					vm.addLocal(env, id)
					return idToJobject(id), true
				}
				vm.mu.Lock()
				delete(o.fields, "tipsy.pathString")
				vm.mu.Unlock()
			}
		}
		vm.mu.Lock()
		s := vm.newStringOn(env, p)
		if o != nil {
			vm.storeFieldObjLocked(o, "tipsy.pathString", s.id)
		}
		vm.mu.Unlock()
		return idToJobject(s.id), true
	}
	registerCore("getAbsolutePath()Ljava/lang/String;", pathString)
	registerCore("getPath()Ljava/lang/String;", pathString)
	registerCore("getSurface()Landroid/view/Surface;", allocNamed("android/view/Surface"))
	registerCore("getDecorView()Landroid/view/View;", allocNamed("android/view/View"))
	registerCore("getWindowManager()Landroid/view/WindowManager;", internClass("android/view/WindowManager"))
	registerCore("getDefaultDisplay()Landroid/view/Display;", internClass("android/view/Display"))
	registerCore("getResources()Landroid/content/res/Resources;", internClass("android/content/res/Resources"))
	registerCore("getPackageManager()Landroid/content/pm/PackageManager;", internClass("android/content/pm/PackageManager"))
	registerCore("hasSystemFeature(Ljava/lang/String;)Z", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if oClassName(o) == "android/content/pm/PackageManager" && platformSystemFeature(vm.stringFromArg(args, 0)) {
			return idToJobject(1), true
		}
		return jnull(), true
	})
	registerCore("getApplicationInfo()Landroid/content/pm/ApplicationInfo;", internClass("android/content/pm/ApplicationInfo"))
	locale := func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internString(env, &vm.immortalLocale, "en_US"), true
	}
	registerCore("getLocale()Ljava/lang/String;", locale)
	registerCore("getRobloxLocale()Ljava/lang/String;", locale)
	registerCore("getGameLocale()Ljava/lang/String;", locale)
	registerCore("getSystemService(Ljava/lang/String;)Ljava/lang/Object;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.lookupSystemService(vm.stringFromArg(args, 0)), true
	})
	registerCore("getClassLoader()Ljava/lang/ClassLoader;", internClass("java/lang/ClassLoader"))
	registerCore("findClass(Ljava/lang/String;)Ljava/lang/Class;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		fcName := vm.stringFromArg(args, 0)
		logFindClassName(fcName)
		return vm.findClassByName(env, fcName), true
	})
	registerCore("getMainLooper()Landroid/os/Looper;", internClass("android/os/Looper"))
	registerCore("getRuntime()Ljava/lang/Runtime;", internClass("java/lang/Runtime"))
	registerCore("getMetrics(Landroid/util/DisplayMetrics;)V", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if args != nil {
			m := vm.get(jobjectToID(uintptr(jvalueLAt(args, 0))))
			vm.mu.Lock()
			vm.fillDisplayMetricsLocked(m)
			vm.mu.Unlock()
		}
		return jnull(), true
	})
	registerCore("getDisplayMetrics()Landroid/util/DisplayMetrics;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		vm.mu.Lock()
		cls := vm.ensureClassLocked("android/util/DisplayMetrics")
		m := vm.newObjectOn(env, cls)
		vm.fillDisplayMetricsLocked(m)
		vm.mu.Unlock()
		return idToJobject(m.id), true
	})
	registerCore("getScreenPhysicalSizeInMillimeters(Landroid/content/Context;)Landroid/graphics/Point;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		w, h := vm.screenPhysicalSizeMM()
		if w <= 0 || h <= 0 {
			logging.Logger(logging.CatJNI).Error("[jni] getScreenPhysicalSizeInMillimeters: no display geometry (x=0 or y=0)")
		}
		vm.mu.Lock()
		cls := vm.ensureClassLocked("android/graphics/Point")
		p := vm.newObjectOn(env, cls)
		if p.fields == nil {
			p.fields = make(map[string]any)
		}
		p.fields["x"] = w
		p.fields["y"] = h
		vm.mu.Unlock()
		logging.Logger(logging.CatJNI).Info("[jni] getScreenPhysicalSizeInMillimeters", "x", w, "y", h)
		return idToJobject(p.id), true
	})
	registerCore("getLocales()Landroid/os/LocaleList;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		vm.mu.Lock()
		loc := vm.newObjectOn(env, vm.ensureClassLocked("java/util/Locale"))
		loc.fields["language"] = "en"
		loc.fields["script"] = ""
		loc.fields["country"] = "US"
		loc.fields["variant"] = ""
		list := vm.newObjectOn(env, vm.ensureClassLocked("android/os/LocaleList"))
		list.elems = []int64{loc.id}
		vm.mu.Unlock()
		return idToJobject(list.id), true
	})
	localeField := func(field string) coreFn {
		return func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
			value := ""
			if o != nil {
				vm.mu.RLock()
				value, _ = o.fields[field].(string)
				vm.mu.RUnlock()
			}
			vm.mu.Lock()
			s := vm.newStringOn(env, value)
			vm.mu.Unlock()
			return idToJobject(s.id), true
		}
	}
	registerCore("getLanguage()Ljava/lang/String;", localeField("language"))
	registerCore("getScript()Ljava/lang/String;", localeField("script"))
	registerCore("getCountry()Ljava/lang/String;", localeField("country"))
	registerCore("getVariant()Ljava/lang/String;", localeField("variant"))
	registerCore("getNativeHelper()Lcom/roblox/client/startup/NativeHelper;", allocNamed("com/roblox/client/startup/NativeHelper"))
	registerCore("getDeviceStaticParams()Lcom/roblox/engine/jni/model/DeviceStaticParams;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.newDeviceStaticParams(), true
	})
	registerCore("setDeviceStaticParams(Lcom/roblox/engine/jni/model/DeviceStaticParams;)V", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return obj, true
	})
	registerCore("bootstrapTheApp()V", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if onBootstrap != nil {
			key := uintptr(env)
			if key == 0 {
				key = vm.currentEnvKey()
			}
			onBootstrap(key, uintptr(obj))
		}
		return obj, true
	})
	noopObj := func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return obj, true
	}
	registerCore("addBoolean(Ljava/lang/String;ZZ)V", noopObj)
	registerCore("addInt(Ljava/lang/String;II)V", noopObj)
	registerCore("addString(Ljava/lang/String;Ljava/lang/String;Z)V", noopObj)
	registerCore("gameActivity_onFlagsFailed()V", noopObj)
	registerCore("gameActivity_onFlagsLoaded()V", noopObj)
	registerCore("loadLibrary(Ljava/lang/String;)V", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return jnull(), true
	})
	registerCore("gc()V", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return jnull(), true
	})
	registerCore("getProperty(Ljava/lang/String;)Ljava/lang/String;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		vm.mu.Lock()
		s := vm.newStringOn(env, "")
		vm.mu.Unlock()
		return idToJobject(s.id), true
	})
	registerCore("getBytes(Ljava/lang/String;)[B", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if oClassName(o) != "java/lang/String" || o == nil {
			return jnull(), true
		}
		charset := vm.stringFromArg(args, 0)
		if !strings.EqualFold(charset, "UTF-8") &&
			!strings.EqualFold(charset, "UTF8") &&
			!strings.EqualFold(charset, "unicode-1-1-utf-8") {
			return jnull(), true
		}
		vm.mu.Lock()
		arrayClass := vm.ensureClassLocked("[B")
		bytes := vm.newObjectOn(env, arrayClass)
		bytes.arrKind = int('B')
		bytes.bytes = append([]byte(nil), []byte(o.str)...)
		vm.mu.Unlock()
		return idToJobject(bytes.id), true
	})
	registerCore("currentTimeMillis()J", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		ms := time.Now().UnixMilli()
		return C.jobject(unsafe.Pointer(uintptr(ms))), true
	})
	registerCore("getProcessTimestamp()J", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		ms := time.Now().UnixMilli()
		return C.jobject(unsafe.Pointer(uintptr(ms))), true
	})
	registerCore("nanoTime()J", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		ns := time.Now().UnixNano()
		return C.jobject(unsafe.Pointer(uintptr(ns))), true
	})
	registerCore("getAllocatableBytes()J", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		const allocatable = 8 << 30
		return C.jobject(unsafe.Pointer(uintptr(allocatable))), true
	})
	registerCore("availableProcessors()I", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return C.jobject(unsafe.Pointer(uintptr(runtime.NumCPU()))), true
	})
	registerCore("getIdentifier(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;)I", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return jnull(), true
	})
	registerCore("toString()Ljava/lang/String;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		str := ""
		if o != nil {
			str = o.str
			if str == "" && o.class != nil {
				str = o.class.name
			}
		}
		vm.mu.Lock()
		s := vm.newStringOn(env, str)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	})
	registerCore("getName()Ljava/lang/String;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		n := oClassName(o)
		if o != nil {
			vm.mu.RLock()
			name, ok := o.fields["name"].(string)
			vm.mu.RUnlock()
			if ok {
				n = name
			}
		}
		vm.mu.Lock()
		s := vm.newStringOn(env, n)
		vm.mu.Unlock()
		return idToJobject(s.id), true
	})
	registerCore("getClass()Ljava/lang/Class;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if o != nil && o.class != nil && o.class.obj != nil {
			return idToJobject(o.class.obj.id), true
		}
		return jnull(), true
	})
	registerCore("size()I", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		n := 0
		if o != nil {
			vm.mu.RLock()
			n = len(o.elems)
			vm.mu.RUnlock()
		}
		return C.jobject(unsafe.Pointer(uintptr(n))), true
	})
	registerCore("isEmpty()Z", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		empty := 1
		if o != nil {
			vm.mu.RLock()
			n := len(o.elems)
			vm.mu.RUnlock()
			if n > 0 {
				empty = 0
			}
		}
		return C.jobject(unsafe.Pointer(uintptr(empty))), true
	})
	getIndex := func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if o == nil || args == nil {
			return jnull(), true
		}
		idx := int(jvalueIAt(args, 0))
		vm.mu.RLock()
		if idx < 0 || idx >= len(o.elems) {
			vm.mu.RUnlock()
			return jnull(), true
		}
		id := o.elems[idx]
		vm.mu.RUnlock()
		if vm.objectsAlive(id) {
			vm.addLocal(env, id)
		}
		return idToJobject(id), true
	}
	registerCore("get(I)Ljava/lang/Object;", getIndex)
	registerCore("get(I)Ljava/util/Locale;", getIndex)
	registerCore("add(Ljava/lang/Object;)Z", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if o != nil && args != nil {
			id := jobjectToID(uintptr(jvalueLAt(args, 0)))
			vm.mu.Lock()
			vm.replaceHeapEdgeLocked(o, 0, id)
			o.elems = append(o.elems, id)
			vm.mu.Unlock()
		}
		return C.jobject(unsafe.Pointer(uintptr(1))), true
	})
	registerCore("iterator()Ljava/util/Iterator;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		vm.mu.Lock()
		cls := vm.ensureClassLocked("java/util/Iterator")
		it := vm.newObjectOn(env, cls)
		if o != nil {
			vm.storeFieldObjLocked(it, "list", o.id)
		}
		it.fields["index"] = int32(0)
		vm.mu.Unlock()
		return idToJobject(it.id), true
	})
	registerCore("hasNext()Z", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if o == nil {
			return jnull(), true
		}
		vm.mu.RLock()
		listID, _ := o.fields["list"].(int64)
		idx, _ := o.fields["index"].(int32)
		list := vm.objects[listID]
		n := 0
		if list != nil {
			n = len(list.elems)
		}
		vm.mu.RUnlock()
		if int(idx) < n {
			return C.jobject(unsafe.Pointer(uintptr(1))), true
		}
		return jnull(), true
	})
	registerCore("next()Ljava/lang/Object;", func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		if o == nil {
			return jnull(), true
		}
		vm.mu.Lock()
		listID, _ := o.fields["list"].(int64)
		idx, _ := o.fields["index"].(int32)
		list := vm.objects[listID]
		if list == nil || int(idx) < 0 || int(idx) >= len(list.elems) {
			vm.mu.Unlock()
			return jnull(), true
		}
		elem := list.elems[idx]
		o.fields["index"] = idx + 1
		if vm.objects[elem] != nil {
			vm.addLocalOnLocked(env, elem)
		}
		vm.mu.Unlock()
		return idToJobject(elem), true
	})
}

func internClass(name string) coreFn {
	return func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		return vm.internClassObject(env, name), true
	}
}

func allocNamed(name string) coreFn {
	return func(vm *VM, env unsafe.Pointer, o *Object, obj C.jobject, args *C.jvalue) (C.jobject, bool) {
		vm.mu.Lock()
		cls := vm.classes[name]
		if cls == nil {
			cls = vm.ensureClassLocked(name)
		}
		v := vm.newObjectOn(env, cls)
		vm.mu.Unlock()
		return idToJobject(v.id), true
	}
}

func (vm *VM) objectsAlive(id int64) bool {
	if id == 0 {
		return false
	}
	return vm.get(id) != nil
}
