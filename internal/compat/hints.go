package compat

import (
	"path"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/elfinspect"
)

// ClassifyGraphics returns unique tags such as egl, gles, vulkan, libEGL, libGLESv2, libvulkan.
func ClassifyGraphics(elves []elfinspect.Report) []string {
	tags := map[string]struct{}{}
	for _, e := range elves {
		for _, n := range e.DTNeeded {
			switch strings.ToLower(neededBase(n)) {
			case "libegl.so":
				tags["egl"] = struct{}{}
				tags["libEGL"] = struct{}{}
			case "libglesv1_cm.so":
				tags["gles"] = struct{}{}
				tags["libGLESv1"] = struct{}{}
			case "libglesv2.so":
				tags["gles"] = struct{}{}
				tags["libGLESv2"] = struct{}{}
			case "libglesv3.so":
				tags["gles"] = struct{}{}
				tags["libGLESv3"] = struct{}{}
			case "libvulkan.so":
				tags["vulkan"] = struct{}{}
				tags["libvulkan"] = struct{}{}
			}
		}
		for _, s := range append(append([]elfinspect.Symbol{}, e.Imports...), e.Exports...) {
			name := s.Name
			switch {
			case strings.HasPrefix(name, "egl") || strings.HasPrefix(name, "EGL"):
				tags["egl"] = struct{}{}
			case isGLESSymbol(name):
				tags["gles"] = struct{}{}
			case strings.HasPrefix(name, "vk") || strings.Contains(strings.ToLower(name), "vulkan"):
				tags["vulkan"] = struct{}{}
			}
		}
	}
	return sortedKeys(tags)
}

// ClassifyAudio returns unique tags such as OpenSLES, aaudio, libOpenSLES, libaaudio.
func ClassifyAudio(elves []elfinspect.Report) []string {
	tags := map[string]struct{}{}
	for _, e := range elves {
		for _, n := range e.DTNeeded {
			switch strings.ToLower(neededBase(n)) {
			case "libopensles.so":
				tags["OpenSLES"] = struct{}{}
				tags["libOpenSLES"] = struct{}{}
			case "libaaudio.so":
				tags["aaudio"] = struct{}{}
				tags["libaaudio"] = struct{}{}
			}
		}
		for _, s := range append(append([]elfinspect.Symbol{}, e.Imports...), e.Exports...) {
			name := s.Name
			switch {
			case strings.HasPrefix(name, "AAudio") || strings.Contains(name, "aaudio"):
				tags["aaudio"] = struct{}{}
			case strings.HasPrefix(name, "SL") || strings.Contains(name, "OpenSLES"):
				tags["OpenSLES"] = struct{}{}
			}
		}
	}
	return sortedKeys(tags)
}

// ClassifyGameActivity looks for GameActivity / NativeActivity / libgame.so / androidx.games.
func ClassifyGameActivity(apkRep *apk.Report, elves []elfinspect.Report) []string {
	tags := map[string]struct{}{}
	addHay := func(s string) {
		if s == "" {
			return
		}
		low := strings.ToLower(s)
		if strings.Contains(low, "gameactivity") {
			tags["GameActivity"] = struct{}{}
		}
		if strings.Contains(low, "nativeactivity") {
			tags["NativeActivity"] = struct{}{}
		}
		if strings.Contains(low, "androidx.games") || strings.Contains(low, "androidx/games") || strings.Contains(low, "androidx_games") {
			tags["androidx.games"] = struct{}{}
		}
		if strings.Contains(low, "libgame.so") || neededBase(low) == "libgame.so" {
			tags["libgame.so"] = struct{}{}
		}
	}
	if apkRep != nil {
		for _, p := range apkRep.Packages {
			addHay(p.LauncherActivity)
			for _, a := range p.GameActivities {
				addHay(a)
			}
		}
	}
	for _, e := range elves {
		for _, n := range e.DTNeeded {
			addHay(n)
			if strings.EqualFold(neededBase(n), "libgame.so") {
				tags["libgame.so"] = struct{}{}
			}
		}
		for _, n := range e.JNIEntryPoints {
			addHay(n)
		}
		for _, s := range e.Imports {
			addHay(s.Name)
		}
		for _, s := range e.Exports {
			addHay(s.Name)
		}
	}
	return sortedKeys(tags)
}

// ClassifyJNI returns JNI_OnLoad / Java_* / libjnigraphics-style tags.
func ClassifyJNI(elves []elfinspect.Report) []string {
	tags := map[string]struct{}{}
	for _, e := range elves {
		for _, n := range e.JNIEntryPoints {
			switch {
			case n == "JNI_OnLoad":
				tags["JNI_OnLoad"] = struct{}{}
			case n == "JNI_OnUnload":
				tags["JNI_OnUnload"] = struct{}{}
			case strings.HasPrefix(n, "Java_"):
				tags["Java_exports"] = struct{}{}
			}
		}
		for _, n := range e.DTNeeded {
			if strings.EqualFold(neededBase(n), "libjnigraphics.so") {
				tags["libjnigraphics"] = struct{}{}
			}
		}
		for _, s := range e.Imports {
			if strings.HasPrefix(s.Name, "JNI_") {
				tags["JNI_imports"] = struct{}{}
			}
		}
	}
	return sortedKeys(tags)
}

// ClassifyAndroidImports returns unique sorted undefined symbols that look like Android/bionic APIs.
func ClassifyAndroidImports(elves []elfinspect.Report) []string {
	set := map[string]struct{}{}
	for _, e := range elves {
		for _, s := range e.Imports {
			if LooksLikeAndroidBionic(s.Name) {
				set[s.Name] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}

// LooksLikeAndroidBionic reports whether an undefined symbol name looks like NDK/bionic/JNI/EGL/GLES/OpenSLES.
func LooksLikeAndroidBionic(name string) bool {
	if name == "" {
		return false
	}
	prefixes := []string{
		"android_",
		"__android_",
		"__libc_",
		"JNI_",
		"egl",
		"EGL",
		"ALooper",
		"ANativeWindow",
		"ANativeActivity",
		"AAsset",
		"AAudio",
		"AConfiguration",
		"AChoreographer",
		"AInput",
		"AKeyEvent",
		"AMotionEvent",
		"AHardwareBuffer",
		"ASensor",
		"ASharedMemory",
		"AStorageManager",
		"ASurface",
		"ATrace_",
		"SL",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	// NDK A* APIs are CamelCase after A (ALooper, not abort).
	if len(name) >= 2 && name[0] == 'A' && name[1] >= 'A' && name[1] <= 'Z' {
		return true
	}
	if isGLESSymbol(name) {
		return true
	}
	return false
}

func isGLESSymbol(name string) bool {
	if strings.HasPrefix(name, "gl") {
		if strings.HasPrefix(name, "glob") || strings.HasPrefix(name, "glib") || strings.HasPrefix(name, "glow") {
			return false
		}
		return true
	}
	return strings.HasPrefix(name, "GL") && !strings.HasPrefix(name, "GLIB")
}

func neededBase(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	return path.Base(s)
}
