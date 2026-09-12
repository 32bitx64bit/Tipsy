// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateMicrophoneConfigHome points config.Paths() at an empty XDG tree
// so tests never read the developer's real config.json. Missing file is
// defaults (enabled).
func isolateMicrophoneConfigHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// writeMicrophoneConfigFile plants one settings file with a microphone
// section under an isolated XDG_CONFIG_HOME. Production resolves the same path
// via internal/config; tests must never touch the real home.
func writeMicrophoneConfigFile(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tipsy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))
}

func permissionDeniedJ() uintptr { return uintptr(uint32(0xFFFFFFFF)) }

func newPermissionReceiver(t *testing.T, vm *VM, class string) *Object {
	t.Helper()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	cls := vm.classes[class]
	if cls == nil {
		t.Fatalf("missing class %s", class)
	}
	return vm.newObjectLocked(cls)
}

func TestAudioPermissionRecordAudioGrantDeny(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	act := newPermissionReceiver(t, vm, "com/roblox/client/startup/MainGameActivity")

	tests := []struct {
		name    string
		disable string
		mic     string
		want    uintptr
		grant   bool
	}{
		{name: "default-open", disable: "", mic: "", want: 0, grant: true},
		{name: "disable-1", disable: "1", mic: "", want: permissionDeniedJ(), grant: false},
		{name: "disable-true", disable: "true", mic: "", want: permissionDeniedJ(), grant: false},
		{name: "disable-yes", disable: "yes", mic: "", want: permissionDeniedJ(), grant: false},
		{name: "mic-0", disable: "", mic: "0", want: permissionDeniedJ(), grant: false},
		{name: "mic-off", disable: "", mic: "off", want: permissionDeniedJ(), grant: false},
		{name: "mic-false", disable: "", mic: "false", want: permissionDeniedJ(), grant: false},
		{name: "mic-no", disable: "", mic: "no", want: permissionDeniedJ(), grant: false},
		{name: "mic-on-stays-open", disable: "", mic: "1", want: 0, grant: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TIPSY_DISABLE_MICROPHONE", tt.disable)
			t.Setenv("TIPSY_MICROPHONE", tt.mic)
			resetAudioPermissionLogsForTest()
			logs := captureLogs(t)
			vm.mu.Lock()
			name := vm.newStringLocked(recordAudioPermissionName)
			vm.mu.Unlock()
			v, handled := vm.dispatch(idToJobject(act.id), "android/content/Context",
				"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id))
			if !handled {
				t.Fatal("checkSelfPermission not handled")
			}
			if uintptr(v) != tt.want {
				t.Fatalf("checkSelfPermission = %#x, want %#x", uintptr(v), tt.want)
			}
			out := logs.String()
			if tt.grant {
				if !strings.Contains(out, "RECORD_AUDIO granted") {
					t.Fatalf("missing grant log: %s", out)
				}
			} else {
				if !strings.Contains(out, "RECORD_AUDIO denied") {
					t.Fatalf("missing deny log: %s", out)
				}
				if !strings.Contains(out, "Settings microphone toggle") {
					t.Fatalf("denial log missing consent surface: %s", out)
				}
				if !strings.Contains(out, "TIPSY_DISABLE_MICROPHONE") {
					t.Fatalf("denial log missing kill-switch: %s", out)
				}
			}
			if strings.Contains(out, "private audio") {
				t.Fatal("PCM appeared in permission logs")
			}
		})
	}
}

func TestAudioPermissionUnknownDenied(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	logs := captureLogs(t)
	act := newPermissionReceiver(t, vm, "android/app/Activity")
	vm.mu.Lock()
	name := vm.newStringLocked("android.permission.MODIFY_AUDIO_SETTINGS")
	vm.mu.Unlock()
	v, handled := vm.dispatch(idToJobject(act.id), "android/app/Activity",
		"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id))
	if !handled {
		t.Fatal("checkSelfPermission not handled")
	}
	if uintptr(v) != permissionDeniedJ() {
		t.Fatalf("unknown permission granted: %#x", uintptr(v))
	}
	out := logs.String()
	if !strings.Contains(out, "[jni] missing permission: android.permission.MODIFY_AUDIO_SETTINGS") {
		t.Fatalf("missing unknown-permission log: %s", out)
	}
	if strings.Contains(out, "RECORD_AUDIO granted") {
		t.Fatal("unknown permission logged as RECORD_AUDIO grant")
	}
	// Second call is denied again but the missing-permission line is once.
	vm.dispatch(idToJobject(act.id), "android/app/Activity",
		"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id))
	if got := strings.Count(logs.String(), "missing permission: android.permission.MODIFY_AUDIO_SETTINGS"); got != 1 {
		t.Fatalf("missing-permission logs = %d, want 1", got)
	}
}

func TestAudioPermissionCheckPermissionPackageManager(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	pm := newPermissionReceiver(t, vm, "android/content/pm/PackageManager")
	vm.mu.Lock()
	perm := vm.newStringLocked(recordAudioPermissionName)
	pkg := vm.newStringLocked("com.roblox.client")
	vm.mu.Unlock()
	v, handled := vm.dispatch(idToJobject(pm.id), "android/content/pm/PackageManager",
		"checkPermission", checkPermissionSig, testCheckPermissionArgs(perm.id, pkg.id))
	if !handled {
		t.Fatal("PackageManager.checkPermission not handled")
	}
	if uintptr(v) != 0 {
		t.Fatalf("RECORD_AUDIO checkPermission = %#x, want granted", uintptr(v))
	}
	t.Setenv("TIPSY_MICROPHONE", "no")
	v, _ = vm.dispatch(idToJobject(pm.id), "android/content/pm/PackageManager",
		"checkPermission", checkPermissionSig, testCheckPermissionArgs(perm.id, pkg.id))
	if uintptr(v) != permissionDeniedJ() {
		t.Fatalf("kill-switch checkPermission = %#x, want denied", uintptr(v))
	}
}

func TestAudioPermissionReceiverClasses(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	vm.mu.Lock()
	name := vm.newStringLocked(recordAudioPermissionName)
	vm.mu.Unlock()
	for _, class := range []string{
		"android/content/Context",
		"android/app/Activity",
		"com/google/androidgamesdk/GameActivity",
		"com/roblox/client/startup/MainGameActivity",
	} {
		o := newPermissionReceiver(t, vm, class)
		v, handled := vm.dispatch(idToJobject(o.id), class,
			"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id))
		if !handled {
			t.Fatalf("%s.checkSelfPermission not handled", class)
		}
		if uintptr(v) != 0 {
			t.Fatalf("%s.checkSelfPermission = %#x, want granted", class, uintptr(v))
		}
	}
	file := newPermissionReceiver(t, vm, "java/io/File")
	if _, handled := vm.dispatch(idToJobject(file.id), "java/io/File",
		"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id)); handled {
		t.Fatal("File.checkSelfPermission must stay unhandled")
	}
}

func TestAudioPermissionRequestPermissions(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	logs := captureLogs(t)
	act := newPermissionReceiver(t, vm, "com/roblox/client/startup/MainGameActivity")
	vm.mu.Lock()
	rec := vm.newStringLocked(recordAudioPermissionName)
	mod := vm.newStringLocked("android.permission.MODIFY_AUDIO_SETTINGS")
	arr := vm.newObjectLocked(vm.classes["java/lang/Object"])
	arr.elems = []int64{rec.id, mod.id}
	vm.mu.Unlock()

	_, handled := vm.dispatch(idToJobject(act.id), "com/roblox/client/startup/MainGameActivity",
		"requestPermissions", requestPermissionsSig, testRequestPermissionsArgs(arr.id, 17))
	if !handled {
		t.Fatal("requestPermissions not handled")
	}
	vm.mu.RLock()
	code, _ := act.fields["tipsy.permRequestCode"].(int32)
	names, _ := act.fields["tipsy.permNames"].([]string)
	grants, _ := act.fields["tipsy.permGrantResults"].([]int32)
	vm.mu.RUnlock()
	if code != 17 {
		t.Fatalf("requestCode = %d, want 17", code)
	}
	if len(names) != 2 || names[0] != recordAudioPermissionName || names[1] != "android.permission.MODIFY_AUDIO_SETTINGS" {
		t.Fatalf("names = %#v", names)
	}
	if len(grants) != 2 || grants[0] != permissionGranted || grants[1] != permissionDenied {
		t.Fatalf("grantResults = %#v", grants)
	}
	out := logs.String()
	if !strings.Contains(out, "RECORD_AUDIO granted") {
		t.Fatalf("missing grant telemetry: %s", out)
	}
	if !strings.Contains(out, "[jni] missing permission: android.permission.MODIFY_AUDIO_SETTINGS") {
		t.Fatalf("MODIFY_AUDIO_SETTINGS was silently granted: %s", out)
	}
}

func TestAudioPermissionOnRequestPermissionsResultDispatch(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	act := newPermissionReceiver(t, vm, "android/app/Activity")
	vm.mu.Lock()
	rec := vm.newStringLocked(recordAudioPermissionName)
	arr := vm.newObjectLocked(vm.classes["java/lang/Object"])
	arr.elems = []int64{rec.id}
	results := vm.newIntArrayLocked([]int32{permissionGranted})
	vm.mu.Unlock()
	_, handled := vm.dispatch(idToJobject(act.id), "android/app/Activity",
		"onRequestPermissionsResult", onRequestPermissionsResultSig,
		testOnRequestPermissionsResultArgs(42, arr.id, results.id))
	if !handled {
		t.Fatal("onRequestPermissionsResult not handled via dispatch")
	}
	vm.mu.RLock()
	code, _ := act.fields["tipsy.permRequestCode"].(int32)
	grants, _ := act.fields["tipsy.permGrantResults"].([]int32)
	vm.mu.RUnlock()
	if code != 42 || len(grants) != 1 || grants[0] != permissionGranted {
		t.Fatalf("callback record code=%d grants=%v", code, grants)
	}
}

func TestAudioPermissionImplemented(t *testing.T) {
	for _, pair := range [][2]string{
		{"checkSelfPermission", checkSelfPermissionSig},
		{"checkPermission", checkPermissionSig},
		{"requestPermissions", requestPermissionsSig},
		{"onRequestPermissionsResult", onRequestPermissionsResultSig},
	} {
		if !isImplementedMethod(pair[0], pair[1]) {
			t.Fatalf("%s%s not in implementedMethods", pair[0], pair[1])
		}
	}
}

func TestAudioPermissionNoPCM(t *testing.T) {
	isolateMicrophoneConfigHome(t)
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	t.Setenv("TIPSY_MICROPHONE", "")
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	resetAudioPermissionLogsForTest()
	logs := captureLogs(t)
	act := newPermissionReceiver(t, vm, "android/app/Activity")
	vm.mu.Lock()
	act.fields["tipsy.planted"] = "private audio buffer content"
	name := vm.newStringLocked(recordAudioPermissionName)
	vm.mu.Unlock()
	vm.dispatch(idToJobject(act.id), "android/app/Activity",
		"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id))
	if strings.Contains(logs.String(), "private audio") {
		t.Fatal("planted PCM appeared in logs")
	}
}

func TestAudioPermissionMicAllowedFileEnv(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	act := newPermissionReceiver(t, vm, "android/app/Activity")
	vm.mu.Lock()
	name := vm.newStringLocked(recordAudioPermissionName)
	vm.mu.Unlock()

	tests := []struct {
		name     string
		file     string
		disable  string
		mic      string
		wantOpen bool
	}{
		{name: "file-off-empty-env", file: `{"microphone":{"enabled":false}}`, wantOpen: false},
		{name: "file-on-mic-0", file: `{"microphone":{"enabled":true}}`, mic: "0", wantOpen: false},
		{name: "missing-file-mic-0", mic: "0", wantOpen: false},
		{name: "file-off-mic-1-env-wins", file: `{"microphone":{"enabled":false}}`, mic: "1", wantOpen: true},
		{name: "missing-file-empty-env", wantOpen: true},
		{name: "file-on-empty-env", file: `{"microphone":{"enabled":true}}`, wantOpen: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.file != "" {
				writeMicrophoneConfigFile(t, tt.file)
			} else {
				isolateMicrophoneConfigHome(t)
			}
			t.Setenv("TIPSY_DISABLE_MICROPHONE", tt.disable)
			t.Setenv("TIPSY_MICROPHONE", tt.mic)
			if got := microphoneDoorOpen(); got != tt.wantOpen {
				t.Fatalf("microphoneDoorOpen() = %v, want %v", got, tt.wantOpen)
			}
			if got := platformSystemFeature(androidHardwareMicrophone); got != tt.wantOpen {
				t.Fatalf("hasSystemFeature(microphone) = %v, want %v", got, tt.wantOpen)
			}
			resetAudioPermissionLogsForTest()
			logs := captureLogs(t)
			v, handled := vm.dispatch(idToJobject(act.id), "android/app/Activity",
				"checkSelfPermission", checkSelfPermissionSig, testPermissionNameArgs(name.id))
			if !handled {
				t.Fatal("checkSelfPermission not handled")
			}
			want := uintptr(0)
			if !tt.wantOpen {
				want = permissionDeniedJ()
			}
			if uintptr(v) != want {
				t.Fatalf("checkSelfPermission = %#x, want %#x", uintptr(v), want)
			}
			out := logs.String()
			if tt.wantOpen {
				if !strings.Contains(out, "RECORD_AUDIO granted") {
					t.Fatalf("missing grant log: %s", out)
				}
			} else {
				if !strings.Contains(out, "RECORD_AUDIO denied") {
					t.Fatalf("missing deny log: %s", out)
				}
				if !strings.Contains(out, "Settings microphone toggle") {
					t.Fatalf("denial log missing consent surface: %s", out)
				}
				if !strings.Contains(out, "TIPSY_DISABLE_MICROPHONE") || !strings.Contains(out, "TIPSY_MICROPHONE") {
					t.Fatalf("denial log missing kill-switches: %s", out)
				}
			}
			if strings.Contains(out, "private audio") {
				t.Fatal("PCM appeared in permission logs")
			}
		})
	}
}
