package compat

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/tipsy-linux/tipsy/internal/apk"
	"github.com/tipsy-linux/tipsy/internal/elfinspect"
)

func TestClassifyGraphics(t *testing.T) {
	t.Parallel()
	elves := []elfinspect.Report{{
		DTNeeded: []string{"libEGL.so", "libGLESv2.so", "libvulkan.so"},
		Imports: []elfinspect.Symbol{
			{Name: "eglGetDisplay"},
			{Name: "glClear"},
			{Name: "vkCreateInstance"},
		},
	}}
	got := ClassifyGraphics(elves)
	for _, want := range []string{"egl", "gles", "vulkan", "libEGL", "libGLESv2", "libvulkan"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}

func TestClassifyAudio(t *testing.T) {
	t.Parallel()
	elves := []elfinspect.Report{{
		DTNeeded: []string{"libOpenSLES.so", "libaaudio.so"},
		Imports: []elfinspect.Symbol{
			{Name: "SLCreateEngine"},
			{Name: "AAudioStream_requestStart"},
		},
	}}
	got := ClassifyAudio(elves)
	for _, want := range []string{"OpenSLES", "aaudio", "libOpenSLES", "libaaudio"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}

func TestClassifyGameActivity(t *testing.T) {
	t.Parallel()
	apkRep := &apk.Report{Packages: []apk.Package{{
		LauncherActivity: "com.google.androidgamesdk.GameActivity",
		GameActivities:   []string{"androidx.games.GameActivity"},
	}}}
	elves := []elfinspect.Report{{
		DTNeeded:       []string{"libgame.so"},
		JNIEntryPoints: []string{"Java_androidx_games_GameActivity_initializeNativeCode"},
		Imports:        []elfinspect.Symbol{{Name: "ANativeActivity_onCreate"}},
	}}
	got := ClassifyGameActivity(apkRep, elves)
	for _, want := range []string{"GameActivity", "NativeActivity", "libgame.so", "androidx.games"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}

func TestClassifyJNI(t *testing.T) {
	t.Parallel()
	elves := []elfinspect.Report{{
		DTNeeded:       []string{"libjnigraphics.so"},
		JNIEntryPoints: []string{"JNI_OnLoad", "Java_com_example_Foo_bar"},
		Imports:        []elfinspect.Symbol{{Name: "JNI_GetCreatedJavaVMs"}},
	}}
	got := ClassifyJNI(elves)
	for _, want := range []string{"JNI_OnLoad", "Java_exports", "libjnigraphics", "JNI_imports"} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}

func TestClassifyAndroidImports(t *testing.T) {
	t.Parallel()
	elves := []elfinspect.Report{{
		Imports: []elfinspect.Symbol{
			{Name: "ALooper_pollOnce"},
			{Name: "ANativeWindow_fromSurface"},
			{Name: "AAssetManager_open"},
			{Name: "AAudioStream_close"},
			{Name: "SLCreateEngine"},
			{Name: "eglGetDisplay"},
			{Name: "glClear"},
			{Name: "android_log_write"},
			{Name: "__libc_init"},
			{Name: "JNI_OnLoad"},
			{Name: "printf"},
			{Name: "malloc"},
			{Name: "abort"},
			{Name: "glob"},
			{Name: "atoi"},
		},
	}}
	got := ClassifyAndroidImports(elves)
	for _, want := range []string{
		"ALooper_pollOnce",
		"ANativeWindow_fromSurface",
		"AAssetManager_open",
		"AAudioStream_close",
		"SLCreateEngine",
		"eglGetDisplay",
		"glClear",
		"android_log_write",
		"__libc_init",
		"JNI_OnLoad",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
	for _, no := range []string{"printf", "malloc", "abort", "glob", "atoi"} {
		if slices.Contains(got, no) {
			t.Fatalf("false positive %q in %v", no, got)
		}
	}
	if !slices.IsSorted(got) {
		t.Fatalf("not sorted: %v", got)
	}
}

func TestAssembleAndFormat(t *testing.T) {
	t.Parallel()
	elves := []elfinspect.Report{{
		Name:     "lib/x86_64/libdemo.so",
		Class:    "ELF64",
		Machine:  "EM_X86_64",
		Type:     "ET_DYN",
		DTNeeded: []string{"libc.so", "libEGL.so", "libandroid.so"},
		Imports: []elfinspect.Symbol{
			{Name: "ALooper_pollOnce", Library: "libandroid.so"},
			{Name: "eglGetDisplay"},
		},
		Exports:        []elfinspect.Symbol{{Name: "JNI_OnLoad"}},
		JNIEntryPoints: []string{"JNI_OnLoad"},
		AndroidNotes:   []string{"needed libc.so (bionic)"},
	}}
	rep := Assemble(nil, elves, []string{"arm64-v8a", "armeabi-v7a", "arm64-v8a"})
	if !slices.Equal(rep.OtherABIs, []string{"arm64-v8a", "armeabi-v7a"}) {
		t.Fatalf("OtherABIs=%v", rep.OtherABIs)
	}
	if !slices.Contains(rep.GraphicsHints, "egl") {
		t.Fatalf("graphics=%v", rep.GraphicsHints)
	}
	if !slices.Contains(rep.JNIHints, "JNI_OnLoad") {
		t.Fatalf("jni=%v", rep.JNIHints)
	}
	if !slices.Contains(rep.ImportedAndroidSymbols, "ALooper_pollOnce") {
		t.Fatalf("android imports=%v", rep.ImportedAndroidSymbols)
	}
	text := FormatText(rep)
	if !strings.Contains(text, "arm64-v8a") || !strings.Contains(text, "JNI_OnLoad") {
		t.Fatalf("FormatText:\n%s", text)
	}
	js, err := FormatJSON(rep)
	if err != nil {
		t.Fatal(err)
	}
	var round Report
	if err := json.Unmarshal(js, &round); err != nil {
		t.Fatal(err)
	}
	if len(round.Elves) != 1 || round.Elves[0].Name != elves[0].Name {
		t.Fatalf("json elves: %+v", round.Elves)
	}
}

func TestLooksLikeAndroidBionic(t *testing.T) {
	t.Parallel()
	yes := []string{"ALooper_pollOnce", "AAsset_read", "android_set_abort_message", "__libc_init", "JNI_CreateJavaVM", "eglSwapBuffers", "glDrawArrays"}
	no := []string{"", "printf", "abort", "glob", "atoi", "accept"}
	for _, s := range yes {
		if !LooksLikeAndroidBionic(s) {
			t.Fatalf("want true for %q", s)
		}
	}
	for _, s := range no {
		if LooksLikeAndroidBionic(s) {
			t.Fatalf("want false for %q", s)
		}
	}
}

func TestFormatNil(t *testing.T) {
	t.Parallel()
	if FormatText(nil) != "<nil>\n" {
		t.Fatal(FormatText(nil))
	}
	if _, err := FormatJSON(nil); err == nil {
		t.Fatal("expected error")
	}
}
