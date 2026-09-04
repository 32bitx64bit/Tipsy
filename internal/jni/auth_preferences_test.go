// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package jni

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthCookieStoreRoutingExpiryAndDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cookies")
	s, err := openAuthCookieStore(path, "https://www.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err = s.set("https://www.example.test/login", []string{
		"normal=synthetic; Domain=example.test; Path=/; Secure; HttpOnly; Max-Age=3600",
		"wrong=synthetic; Domain=other.test; Path=/", "parent=synthetic; Domain=test; Path=/",
		"sub=synthetic; Domain=auth.example.test; Path=/", "path=synthetic; Path=/other",
		"short=synthetic; Path=/; Max-Age=1", "__Host-good=synthetic; Path=/; Secure",
		"__Host-bad=synthetic; Domain=example.test; Path=/; Secure",
	}); err != nil {
		t.Fatal(err)
	}
	got := s.header()
	for _, part := range []string{"normal=synthetic", "short=synthetic", "__Host-good=synthetic"} {
		if !strings.Contains(got, part) {
			t.Fatal("valid cookie not restored")
		}
	}
	for _, part := range []string{"wrong=", "parent=", "sub=", "path=", "__Host-bad="} {
		if strings.Contains(got, part) {
			t.Fatal("cookie scope violation")
		}
	}
	now = now.Add(2 * time.Second)
	if strings.Contains(s.header(), "short=") {
		t.Fatal("expired cookie restored")
	}
	if err = s.set("https://www.example.test/", []string{"normal=; Domain=example.test; Path=/; Max-Age=0"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.header(), "normal=") {
		t.Fatal("logout removal ignored")
	}
	reloaded, err := openAuthCookieStore(path, "https://www.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reloaded.header(), "normal=") {
		t.Fatal("deleted cookie revived after restart")
	}
	if err = s.set("http://www.example.test/", []string{"insecure=synthetic; Secure; Path=/"}); err != nil {
		t.Fatal(err)
	}
	if err = s.set("http://www.example.test/", []string{"__Host-good=; Path=/; Max-Age=0"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.header(), "__Host-good=") {
		t.Fatal("HTTP deleted a secure cookie")
	}
	if strings.Contains(s.header(), "insecure=") {
		t.Fatal("secure cookie accepted from HTTP")
	}
}

func TestAuthCookieStoreRejectsUnsafeFilesAndPrivateModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies")
	s, err := openAuthCookieStore(path, "https://www.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.set("https://www.example.test/", []string{"fixture=synthetic; Path=/"}); err != nil {
		t.Fatal(err)
	}
	for p, mode := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatal("private mode missing")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("not-cookie-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err = openAuthCookieStore(path, "https://www.example.test/"); err == nil {
		t.Fatal("symlink followed")
	}
	if err = s.set("https://www.example.test/", []string{"fixture=changed; Path=/"}); err == nil {
		t.Fatal("symlink replaced")
	}
	if string(mustReadAuthTest(t, outside)) != "not-cookie-data" {
		t.Fatal("outside file changed")
	}
	os.Remove(path)
	os.WriteFile(path, []byte(`{"Version":1,"Cookies":[{"Name":"secret-invalid-data`), 0o600)
	if _, err = openAuthCookieStore(path, "https://www.example.test/"); err == nil || strings.Contains(err.Error(), "secret-invalid") {
		t.Fatal("invalid file accepted or exposed")
	}
}
func mustReadAuthTest(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAuthCookieBridgeAcrossProcesses(t *testing.T) {
	if stage := os.Getenv("TIPSY_AUTH_SYNTHETIC_STAGE"); stage != "" {
		path := os.Getenv("TIPSY_AUTH_SYNTHETIC_FILE")
		vm, err := NewVM()
		if err != nil {
			t.Fatal(err)
		}
		if err = vm.ConfigureAuthCookies(path, "https://www.example.test/"); err != nil {
			t.Fatal(err)
		}
		logs := captureLogs(t)
		if stage == "write" || stage == "delete" {
			value := "fixture=synthetic-secret; Domain=example.test; Path=/; Secure; Max-Age=3600"
			if stage == "delete" {
				value = "fixture=; Domain=example.test; Path=/; Max-Age=0"
			}
			vm.mu.Lock()
			str := vm.newStringLocked(value)
			url := vm.newStringLocked("https://www.example.test/")
			arr := vm.newObjectLocked(vm.classes["java/lang/Object"])
			arr.elems = []int64{str.id}
			handler := vm.newObjectLocked(vm.classes[cookieHandlerClass])
			vm.mu.Unlock()
			if _, handled := callDispatchOrStub(vm, idToJobject(handler.id), cookieHandlerClass, "onSetCookie", "([Ljava/lang/String;Ljava/lang/String;)V", testAuthCookieArgs(arr.id, url.id), 'V'); !handled {
				t.Fatal("real callback dispatch missing")
			}
		} else {
			header, err := vm.RestoreAuthCookies()
			if err != nil {
				t.Fatal(err)
			}
			if stage == "read" && header != "fixture=synthetic-secret" {
				t.Fatal("cross-process restore failed")
			}
			if stage == "empty" && header != "" {
				t.Fatal("logout revived")
			}
		}
		if strings.Contains(logs.String(), "synthetic-secret") || strings.Contains(logs.String(), "example.test") || strings.Contains(logs.String(), "fixture=") {
			t.Fatal("cookie data leaked in log")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "cookies")
	for _, stage := range []string{"write", "read", "delete", "empty"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAuthCookieBridgeAcrossProcesses$")
		cmd.Env = append(os.Environ(), "TIPSY_AUTH_SYNTHETIC_STAGE="+stage, "TIPSY_AUTH_SYNTHETIC_FILE="+path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic subprocess %s failed: %v %s", stage, err, out)
		}
	}
}

func TestAuthCookieBridgeRegistersOnlyAfterSettingsInitializationOnce(t *testing.T) {
	vm, err := NewVM()
	if err != nil {
		t.Fatal(err)
	}
	if err = vm.ConfigureAuthCookies(filepath.Join(t.TempDir(), "cookies"), "https://www.example.test/"); err != nil {
		t.Fatal(err)
	}
	count := 0
	vm.SetAuthCookieRegistration(func() {
		count++
		if _, err := vm.RestoreAuthCookies(); err != nil {
			t.Fatal(err)
		}
	})
	if count != 0 {
		t.Fatal("registered before settings initialization")
	}
	for i := 0; i < 2; i++ {
		vm.Env().CompleteAuthCookieInitialization()
	}
	if count != 1 {
		t.Fatal("registration count is not one")
	}
}
