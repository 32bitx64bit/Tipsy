// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/apk"
)

func TestRuntimeDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)
	want := filepath.Join(tmp, "tipsy", "runtime")
	if got := RuntimeDir(); got != want {
		t.Fatalf("RuntimeDir()=%s want %s", got, want)
	}
}

func TestSetupNoPaths(t *testing.T) {
	_, err := Setup(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "pass official APK/dir") {
		t.Fatalf("err=%v", err)
	}
	_, err = Setup(context.Background(), []string{})
	if err == nil || !strings.Contains(err.Error(), "pass official APK/dir") {
		t.Fatalf("err=%v", err)
	}
}

func TestSetupExtractsIntoRuntimeDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache-home"))
	src := t.TempDir()
	lib := []byte("runtime-setup-lib")
	apkPath := writeZip(t, filepath.Join(src, "base.apk"), map[string][]byte{
		"lib/x86_64/libdummy.so": lib,
		"assets/hello.txt":       []byte("hi"),
	})

	res, err := Setup(context.Background(), []string{apkPath})
	if err != nil {
		t.Fatal(err)
	}
	wantDest := RuntimeDir()
	if res.DestDir != wantDest {
		t.Fatalf("DestDir=%s want %s", res.DestDir, wantDest)
	}
	if len(res.Libraries) != 1 {
		t.Fatalf("libraries=%d", len(res.Libraries))
	}
	got, err := os.ReadFile(res.Libraries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, lib) {
		t.Fatal("extracted lib mismatch")
	}
	if _, err := os.Stat(filepath.Join(wantDest, "apk", "base.apk")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wantDest, "assets", "hello.txt")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(res.MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var meta apk.Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Libraries) != 1 || meta.Libraries[0].Name != "libdummy.so" {
		t.Fatalf("meta %+v", meta)
	}
}

func TestSetupUpdateReplacesRuntimeButPreservesPersistentFiles(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data-home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache-home"))
	src := t.TempDir()
	v1 := writeZip(t, filepath.Join(src, "v1.apk"), map[string][]byte{
		"lib/x86_64/libdummy.so": []byte("runtime-v1"),
		"assets/version.txt":     []byte("v1"),
	})
	if _, err := Setup(context.Background(), []string{v1}); err != nil {
		t.Fatal(err)
	}
	layout := AppStorage()
	sentinel := filepath.Join(layout.FilesDir, "appData", "synthetic-account-state.bin")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 0xff, 'o', 'p', 'a', 'q', 'u', 'e'}
	if err := os.WriteFile(sentinel, want, 0o600); err != nil {
		t.Fatal(err)
	}

	v2 := writeZip(t, filepath.Join(src, "v2.apk"), map[string][]byte{
		"lib/x86_64/libdummy.so": []byte("runtime-v2"),
		"assets/version.txt":     []byte("v2"),
	})
	if _, err := Setup(context.Background(), []string{v2}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("persistent sentinel after update=%x err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(RuntimeDir(), "lib", "x86_64", "libdummy.so")); err != nil || string(got) != "runtime-v2" {
		t.Fatalf("runtime library=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(RuntimeDir(), "assets", "version.txt")); err != nil || string(got) != "v2" {
		t.Fatalf("runtime asset=%q err=%v", got, err)
	}
	assertMode(t, layout.FilesDir, 0o700)
	assertMode(t, sentinel, 0o600)
}

func TestSetupContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Setup(ctx, []string{"ignored.apk"})
	if err == nil {
		t.Fatal("expected context error")
	}
}

func writeZip(t *testing.T, path string, files map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	fixed := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	for _, n := range names {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate, Modified: fixed}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(files[n]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplicationSettingsFromResponse(t *testing.T) {
	raw := []byte(`{"applicationSettings":{"FFlagFoo":"True","FIntBar":"1"}}`)
	got, n, err := applicationSettingsFromResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count=%d want 2", n)
	}
	if !strings.Contains(got, `"ClientAppSettings"`) || !strings.Contains(got, `"applicationSettings"`) {
		t.Fatalf("missing JSON keys: %s", got)
	}
	if !strings.Contains(got, `"ShadowFValuesEnabled":"True"`) {
		t.Fatalf("missing commit-gate overlay: %s", got)
	}
	if !strings.Contains(got, `"FFlagFoo":"True"`) || !strings.Contains(got, `"FIntBar":"1"`) {
		t.Fatalf("json=%s", got)
	}
	empty, n, err := applicationSettingsFromResponse([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || !strings.Contains(empty, `"ClientAppSettings"`) || !strings.Contains(empty, `"applicationSettings"`) {
		t.Fatalf("empty envelope: n=%d json=%s", n, empty)
	}
}
