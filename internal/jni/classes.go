// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

func (vm *VM) seedClasses() {
	object := vm.defineClass("java/lang/Object", nil)
	vm.defineClass("java/lang/String", object)
	vm.defineClass("java/lang/Class", object)
	vm.defineClass("java/lang/Throwable", object)
	vm.defineClass("java/lang/RuntimeException", vm.classes["java/lang/Throwable"])
	vm.defineClass("java/io/File", object)
	vm.defineClass("java/lang/ClassLoader", object)

	ctx := vm.defineClass("android/content/Context", object)
	activity := vm.defineClass("android/app/Activity", ctx)
	vm.defineClass("android/content/res/AssetManager", object)
	vm.defineClass("android/content/res/Configuration", object)
	vm.defineClass("android/content/res/Resources", object)
	vm.defineClass("android/view/Surface", object)
	vm.defineClass("android/view/SurfaceHolder", object)
	vm.defineClass("android/view/Window", object)
	vm.defineClass("android/view/View", object)
	vm.defineClass("android/os/Bundle", object)
	vm.defineClass("android/os/Looper", object)

	ga := vm.defineClass("com/google/androidgamesdk/GameActivity", activity)
	vm.defineClass("com/roblox/client/startup/MainGameActivity", ga)

	for _, name := range []string{
		"java/lang/System",
		"java/lang/Runtime",
		"java/lang/Thread",
		"java/lang/Boolean",
		"java/lang/Integer",
		"java/lang/Long",
		"java/lang/Float",
		"java/lang/Double",
		"java/util/ArrayList",
		"java/util/HashMap",
		"java/util/Locale",
		"android/os/LocaleList",
		"java/io/InputStream",
		"java/nio/ByteBuffer",
		"android/os/Build",
		"android/os/Build$VERSION",
		"android/os/Environment",
		"android/os/Handler",
		"android/os/Message",
		"android/os/Process",
		"android/app/Application",
		"android/app/ActivityManager",
		"android/content/ContextWrapper",
		"android/content/Intent",
		"android/content/SharedPreferences",
		"android/content/pm/ApplicationInfo",
		"android/content/pm/PackageManager",
		"android/content/pm/PackageInfo",
		"android/view/WindowManager",
		"android/view/Display",
		"android/view/MotionEvent",
		"android/view/KeyEvent",
		"android/view/inputmethod/InputMethodManager",
		"android/util/DisplayMetrics",
		"android/util/Log",
		"android/graphics/Rect",
		"android/graphics/Point",
		"android/net/ConnectivityManager",
		"android/net/NetworkInfo",
		"android/net/Network",
		"android/net/NetworkCapabilities",
		"android/net/NetworkRequest",
		"android/net/NetworkInfo$State",
		"android/net/NetworkInfo$DetailedState",
		"android/net/ConnectivityManager$NetworkCallback",
		"android/webkit/WebView",
		"android/hardware/input/InputManager",
		"androidx/core/graphics/Insets",
		"androidx/core/view/WindowInsetsCompat$Type",
		"com/roblox/client/JNIAAssetManagerSetup",
		"com/roblox/client/LocalStorageManager",
		"com/roblox/client/flags/NativeFlagsInitResult",
		"com/roblox/engine/jni/locale/NativeLocaleJavaInterface",
		"com/roblox/engine/jni/NativeGLInterface",
		"com/roblox/engine/jni/NativeGLJavaInterface",
		"com/roblox/engine/jni/NativeInputInterface",
		"com/roblox/engine/jni/autovalue/InitParams",
		"com/roblox/engine/jni/autovalue/StartAppParams",
		"com/roblox/engine/jni/model/PlatformParams",
		"com/roblox/engine/jni/model/DeviceParams",
		"com/roblox/engine/jni/model/DeviceStaticParams",
		"com/roblox/universalapp/logging/LoggingProtocol",
		"java/util/List",
		"java/util/Iterator",
	} {
		if vm.classes[name] == nil {
			vm.defineClass(name, object)
		}
	}

	if c := vm.classes["android/os/Build$VERSION"]; c != nil && c.obj != nil {
		c.obj.fields["SDK_INT"] = int32(26)
		c.obj.fields["SDK"] = "26"
		c.obj.fields["RELEASE"] = "8.0.0"
	}
	if c := vm.classes["android/os/Build"]; c != nil && c.obj != nil {
		c.obj.fields["MODEL"] = "tipsy"
		c.obj.fields["DEVICE"] = "tipsy"
		c.obj.fields["MANUFACTURER"] = "Tipsy"
		c.obj.fields["BRAND"] = "Tipsy"
		c.obj.fields["PRODUCT"] = "tipsy"
		c.obj.fields["HARDWARE"] = "amd64"
		c.obj.fields["FINGERPRINT"] = "tipsy/tipsy/tipsy:8.0.0/OPR1.170623.027/26:user/release-keys"
	}
	// androidx Insets.NONE: the official all-zero Insets singleton, readable
	// as a static field and sharing the class's official int field semantics.
	if c := vm.classes["androidx/core/graphics/Insets"]; c != nil && c.obj != nil {
		none := vm.newObjectLocked(c)
		none.fields["left"] = int32(0)
		none.fields["top"] = int32(0)
		none.fields["right"] = int32(0)
		none.fields["bottom"] = int32(0)
		c.obj.fields["NONE"] = none.id
	}

	vm.seedConnectivity()
	_ = object
}

func (vm *VM) ensureClassLocked(name string) *Class {
	if name == "" {
		name = "java/lang/Object"
	}
	if c := vm.classes[name]; c != nil {
		return c
	}
	return vm.defineClass(name, vm.classes["java/lang/Object"])
}

func (vm *VM) defineClass(name string, super *Class) *Class {
	cls := &Class{name: name, super: super}
	classClass := vm.classes["java/lang/Class"]
	o := &Object{id: vm.allocID(), class: classClass, fields: map[string]any{"name": name}}
	if classClass == nil && name == "java/lang/Class" {
		o.class = cls
	}
	cls.obj = o
	vm.put(o)
	vm.classes[name] = cls
	if name == "java/lang/Class" {
		for _, c := range vm.classes {
			if c.obj != nil && c.obj.class == nil {
				c.obj.class = cls
			}
		}
	}
	return cls
}

var implementedMethods = map[string]bool{
	"getFilesDir()Ljava/io/File;":                                                                             true,
	"getCacheDir()Ljava/io/File;":                                                                             true,
	"getObbDir()Ljava/io/File;":                                                                               true,
	"getExternalFilesDir(Ljava/lang/String;)Ljava/io/File;":                                                   true,
	"getAssets()Landroid/content/res/AssetManager;":                                                           true,
	"getWindow()Landroid/view/Window;":                                                                        true,
	"getApplicationContext()Landroid/content/Context;":                                                        true,
	"getPackageName()Ljava/lang/String;":                                                                      true,
	"getAbsolutePath()Ljava/lang/String;":                                                                     true,
	"getPath()Ljava/lang/String;":                                                                             true,
	"getSurface()Landroid/view/Surface;":                                                                      true,
	"getDecorView()Landroid/view/View;":                                                                       true,
	"getWindowManager()Landroid/view/WindowManager;":                                                          true,
	"getDefaultDisplay()Landroid/view/Display;":                                                               true,
	"getResources()Landroid/content/res/Resources;":                                                           true,
	"getPackageManager()Landroid/content/pm/PackageManager;":                                                  true,
	"hasSystemFeature(Ljava/lang/String;)Z":                                                                  true,
	"getApplicationInfo()Landroid/content/pm/ApplicationInfo;":                                                true,
	"getSystemService(Ljava/lang/String;)Ljava/lang/Object;":                                                  true,
	"getSystemService(Ljava/lang/Class;)Ljava/lang/Object;":                                                   true,
	"getActiveNetworkInfo()Landroid/net/NetworkInfo;":                                                         true,
	"getNetworkInfo(I)Landroid/net/NetworkInfo;":                                                              true,
	"getActiveNetwork()Landroid/net/Network;":                                                                 true,
	"getAllNetworks()[Landroid/net/Network;":                                                                  true,
	"getNetworkCapabilities(Landroid/net/Network;)Landroid/net/NetworkCapabilities;":                          true,
	"isActiveNetworkMetered()Z":                                                                               true,
	"registerDefaultNetworkCallback(Landroid/net/ConnectivityManager$NetworkCallback;)V":                      true,
	"registerDefaultNetworkCallback(Landroid/net/ConnectivityManager$NetworkCallback;Landroid/os/Handler;)V":  true,
	"registerNetworkCallback(Landroid/net/NetworkRequest;Landroid/net/ConnectivityManager$NetworkCallback;)V": true,
	"registerNetworkCallback(Landroid/net/NetworkRequest;Landroid/net/ConnectivityManager$NetworkCallback;Landroid/os/Handler;)V": true,
	"unregisterNetworkCallback(Landroid/net/ConnectivityManager$NetworkCallback;)V":                                               true,
	"isConnected()Z":                            true,
	"isConnectedOrConnecting()Z":                true,
	"isAvailable()Z":                            true,
	"isRoaming()Z":                              true,
	"getType()I":                                true,
	"getTypeName()Ljava/lang/String;":           true,
	"getSubtype()I":                             true,
	"getSubtypeName()Ljava/lang/String;":        true,
	"getState()Landroid/net/NetworkInfo$State;": true,
	"getDetailedState()Landroid/net/NetworkInfo$DetailedState;": true,
	"hasCapability(I)Z":                                true,
	"hasTransport(I)Z":                                 true,
	"getNetworkHandle()J":                              true,
	"getClassLoader()Ljava/lang/ClassLoader;":          true,
	"findClass(Ljava/lang/String;)Ljava/lang/Class;":   true,
	"getMainLooper()Landroid/os/Looper;":               true,
	"getMetrics(Landroid/util/DisplayMetrics;)V":       true,
	"getDisplayMetrics()Landroid/util/DisplayMetrics;": true,
	"getScreenPhysicalSizeInMillimeters(Landroid/content/Context;)Landroid/graphics/Point;": true,
	"getLocales()Landroid/os/LocaleList;":                                                   true,
	"get(I)Ljava/util/Locale;":                                                              true,
	"getLanguage()Ljava/lang/String;":                                                       true,
	"getScript()Ljava/lang/String;":                                                         true,
	"getCountry()Ljava/lang/String;":                                                        true,
	"getVariant()Ljava/lang/String;":                                                        true,
	"getNativeHelper()Lcom/roblox/client/startup/NativeHelper;":                             true,
	"bootstrapTheApp()V":                                                                    true,
	// MotionEvent/KeyEvent getters the engine's GameActivity glue
	// GetMethodIDs during initializeNativeCode (observed missing-method
	// list, last-honest.log:104-131). Answered by dispatchInput.
	"getDeviceId()I":                    true,
	"getSource()I":                      true,
	"getAction()I":                      true,
	"getEventTime()J":                   true,
	"getDownTime()J":                    true,
	"getFlags()I":                       true,
	"getMetaState()I":                   true,
	"getEdgeFlags()I":                   true,
	"getHistorySize()I":                 true,
	"getHistoricalEventTime(I)J":        true,
	"getPointerCount()I":                true,
	"getPointerId(I)I":                  true,
	"getToolType(I)I":                   true,
	"getXPrecision()F":                  true,
	"getYPrecision()F":                  true,
	"getAxisValue(II)F":                 true,
	"getHistoricalAxisValue(III)F":      true,
	"getRepeatCount()I":                 true,
	"getKeyCode()I":                     true,
	"getScanCode()I":                    true,
	"getUnicodeChar()I":                 true,
	"addBoolean(Ljava/lang/String;ZZ)V": true,
	"gameActivity_onFlagsFailed()V":     true,
	"gameActivity_onFlagsLoaded()V":     true,
	// NativeHelper.gameActivity_onAppReady(String): the engine's Java-side
	// readiness announcement (GetMethodID'd every ~30 s in launch logs;
	// official traces show step-name payloads like "Startup"/"Landing").
	// Received and recorded in nativehelper.go — never fabricated.
	"gameActivity_onAppReady(Ljava/lang/String;)V": true,
	// NativeHelper.gameActivity_onScreenOrientationChanged(IZ)V: the
	// engine's orientation request push (GetMethodID'd and CALLED at
	// startup, launch logs; official classes2.dex forwards to
	// Activity.setRequestedOrientation/requestOrientationAsDefault).
	// X11 has no orientation surface (WM-owned, no rotation API), so the
	// receiver records the announcement; the request is never faked as
	// applied.
	"gameActivity_onScreenOrientationChanged(IZ)V": true,
	// NativeHelper.gameActivity_onGameLoaded(J)V: the engine's game-loaded
	// announcement, CALLED once per launch at startup in the
	// experience-lifecycle batch (launch logs: same second as
	// NativeGLJavaInterface.gameLoadedCallback(J)V with handle=0).
	// Received and recorded in nativehelper.go — never fabricated, never
	// acted on.
	"gameActivity_onGameLoaded(J)V": true,
	// Keyboard text-input contract, engine→Java direction, CALLED when a Lua
	// text box gains focus (live focus storm: NativeGLInterface first, then
	// the NativeHelper fallback pair). Received and recorded in textinput.go
	// (counts + handle/flag/payload lengths only — never text content, never
	// acted on). The [B/TextBoxInfo payloads are length-observed, not read.
	"showKeyboard(JZ[BLcom/roblox/engine/jni/model/NativeTextBoxInfo;)V": true,
	"hideKeyboard()V": true,
	"gameActivity_showKeyboard(JZ[BLcom/roblox/engine/jni/model/NativeTextBoxInfo;)V": true,
	"gameActivity_hideKeyboard()V": true,
	// GameTextInput InputConnection engine→Java contract (DEX-proven
	// classes2.dex descriptors, GetMethodID'd at startup): the engine Calls
	// these on the Tipsy-owned InputConnection object. Received and
	// recorded in textinput.go (counts + opaque aggregates only — State
	// content never read/stored/logged, nothing committed, physical
	// nativePassKeyEvent untouched).
	"setState(Lcom/google/androidgamesdk/gametextinput/State;)V": true,
	"setSoftKeyboardActive(ZI)V":                                 true,
	"restartInput()V":                                            true,
	// Lua-textbox contract, engine→Java direction, CALLED during real
	// Lua-textbox activity (live log: NativeGL pair GetMethodID'd at
	// startup, NativeHelper pair lazily, all four stub-dispatched at
	// 20:38:26 in tipsy-direct-retest.log). Received and recorded in
	// luatextbox.go (counts + opaque handle/lengths/flags — the String
	// twin's payload bytes are never read/stored/logged, nothing
	// committed, physical nativePassKeyEvent untouched).
	"gameActivity_onLuaTextBoxChanged(Ljava/lang/String;)V":                  true,
	"gameActivity_onLuaTextBoxPropertyChanged()V":                            true,
	"onLuaTextBoxChangedCallback(Ljava/lang/String;)V":                       true,
	"onLuaTextBoxPropertyChangedCallback()V":                                 true,
	"getIdentifier(Ljava/lang/String;Ljava/lang/String;Ljava/lang/String;)I": true,
	"loadLibrary(Ljava/lang/String;)V":                                       true,
	"getProperty(Ljava/lang/String;)Ljava/lang/String;":                      true,
	// java.lang.String.getBytes(String) is used inside Roblox's named
	// syncTextboxTextAndCursorPosition2 JNI wrapper to turn the platform
	// editor snapshot into UTF-8. The exact, privacy-safe implementation
	// lives in export.go and supports the UTF-8 charset aliases only.
	"getBytes(Ljava/lang/String;)[B":                           true,
	"currentTimeMillis()J":                                     true,
	"nanoTime()J":                                              true,
	"gc()V":                                                    true,
	"availableProcessors()I":                                   true,
	"getRuntime()Ljava/lang/Runtime;":                          true,
	"toString()Ljava/lang/String;":                             true,
	"getName()Ljava/lang/String;":                              true,
	"getClass()Ljava/lang/Class;":                              true,
	"getLocale()Ljava/lang/String;":                            true,
	"getRobloxLocale()Ljava/lang/String;":                      true,
	"getGameLocale()Ljava/lang/String;":                        true,
	"getProcessTimestamp()J":                                   true,
	"getAllocatableBytes()J":                                   true,
	"getFilesDir()Ljava/lang/String;":                          true,
	"getAppVersion()Ljava/lang/String;":                        true,
	"getUserId()J":                                             true,
	"getIsUnder13()Z":                                          true,
	"getUsername()Ljava/lang/String;":                          true,
	"getDisplayName()Ljava/lang/String;":                       true,
	"getAlternateName()Ljava/lang/String;":                     true,
	"getPlatformName()Ljava/lang/String;":                      true,
	"getMembershipType()I":                                     true,
	"getHasRobloxSubscription()Z":                              true,
	"getTheme()Ljava/lang/String;":                             true,
	"size()I":                                                  true,
	"isEmpty()Z":                                               true,
	"get(I)Ljava/lang/Object;":                                 true,
	"add(Ljava/lang/Object;)Z":                                 true,
	"iterator()Ljava/util/Iterator;":                           true,
	"hasNext()Z":                                               true,
	"next()Ljava/lang/Object;":                                 true,
	"baseURL()Ljava/lang/String;":                              true,
	"buildVariant()Ljava/lang/String;":                         true,
	"userAgent()Ljava/lang/String;":                            true,
	"deviceParams()Lcom/roblox/engine/jni/model/DeviceParams;": true,
	"getDeviceStaticParams()Lcom/roblox/engine/jni/model/DeviceStaticParams;":  true,
	"setDeviceStaticParams(Lcom/roblox/engine/jni/model/DeviceStaticParams;)V": true,
	"platformParams()Lcom/roblox/engine/jni/model/PlatformParams;":             true,
	"vrContext()Landroid/app/Activity;":                                        true,
	"isPotato()Z":                                                              true,
	"isTablet()Z":                                                              true,
	"isVrDevice()Z":                                                            true,
	"appStarterPlace()Ljava/lang/String;":                                      true,
	"appStarterScript()Ljava/lang/String;":                                     true,
	"appUserId()J":                                                             true,
	"isUnder13()Z":                                                             true,
	"membershipType()I":                                                        true,
	"selectedTheme()Ljava/lang/String;":                                        true,
	"surface()Landroid/view/Surface;":                                          true,
	"username()Ljava/lang/String;":                                             true,
	"assetFolderPath()Ljava/lang/String;":                                      true,
	"dpiScale()F":                                                              true,
	"isKeyboardDevice()Z":                                                      true,
	"isMouseDevice()Z":                                                         true,
	"isTouchDevice()Z":                                                         true,
	"viewportWidthMm()I":                                                       true,
	"viewportHeightMm()I":                                                      true,
	"getWindowInsets(I)Landroidx/core/graphics/Insets;":                        true,
	"getWaterfallInsets()Landroidx/core/graphics/Insets;":                      true,
	"statusBars()I":                                                            true,
	"navigationBars()I":                                                        true,
	"captionBar()I":                                                            true,
	"ime()I":                                                                   true,
	"systemGestures()I":                                                        true,
	"mandatorySystemGestures()I":                                               true,
	"tappableElement()I":                                                       true,
	"displayCutout()I":                                                         true,
	"systemOverlays()I":                                                        true,
	"systemBars()I":                                                            true,
	"appBuildVariant()Ljava/lang/String;":                                      true,
	"appVersion()Ljava/lang/String;":                                           true,
	"country()Ljava/lang/String;":                                              true,
	"cpu64Bit()Z":                                                              true,
	"deviceName()Ljava/lang/String;":                                           true,
	"deviceSku()Ljava/lang/String;":                                            true,
	"deviceTotalMemoryMB()I":                                                   true,
	"displayPhysicalHeightPixels()I":                                           true,
	"displayPhysicalWidthPixels()I":                                            true,
	"displayResolution()Ljava/lang/String;":                                    true,
	"isChrome()Z":                                                              true,
	"isLowRamDevice()Z":                                                        true,
	"largeMemoryClass()I":                                                      true,
	"lowMemoryKillerBackgroundAppThreshold()J":                                 true,
	"lowMemoryKillerForegroundAppThreshold()J":                                 true,
	"manufacturer()Ljava/lang/String;":                                         true,
	"memoryClass()I":                                                           true,
	"networkType()Ljava/lang/String;":                                          true,
	"osVersion()Ljava/lang/String;":                                            true,
	"socModel()Ljava/lang/String;":                                             true,
	"testDeviceName()Ljava/lang/String;":                                       true,
}

func isImplementedMethod(name, sig string) bool {
	return implementedMethods[name+sig]
}
