// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUptodownListing(t *testing.T) {
	t.Parallel()
	html := `
<h1 id="detail-app-name" data-code="48053" data-file-id="1211339171">Roblox</h1>
<div class="version">2.737.1584</div>
<button id="detail-download-button" class="button download xapk" data-app-id="48053" data-file-id="1211339171" data-only-xapk="0">Download</button>
<th>Architecture</th><td>armeabi-v7a, arm64-v8a, x86_64</td>
<th>Package Name</th><td>com.roblox.client</td>
<th>File type</th><td>XAPK</td>`
	got, err := parseUptodownListing([]byte(html))
	if err != nil || got.AppID != "48053" || got.FileID != "1211339171" || got.Version != "2.737.1584" || !got.HasX8664 || got.Kind != "xapk" {
		t.Fatalf("listing=%+v err=%v", got, err)
	}
}

func TestParseUptodownListingRejectsARMOnly(t *testing.T) {
	t.Parallel()
	html := `
<h1 id="detail-app-name" data-code="1" data-file-id="2">Roblox</h1>
<button id="detail-download-button" data-app-id="1" data-file-id="2">Download</button>
<th>Architecture</th><td>arm64-v8a, armeabi-v7a</td>
<th>Package Name</th><td>com.roblox.client</td>`
	_, err := parseUptodownListing([]byte(html))
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestParseUptodownDownloadURL(t *testing.T) {
	t.Parallel()
	path, err := parseUptodownDownloadURL([]byte(`{"success":1,"data":{"downloadURL":"aaa/bbb/ccc==/"}}`))
	if err != nil || path != "aaa/bbb/ccc==/" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestUptodownSourceDownloadsX8664APK(t *testing.T) {
	apkBytes := zipBytes(t, map[string]string{
		"AndroidManifest.xml":     "manifest",
		"lib/x86_64/libroblox.so": "native",
	})
	server := newUptodownServer(t, apkBytes, "base.apk")
	source := &UptodownSource{Origin: server.URL, DWOrigin: server.URL, Client: server.Client()}
	if !source.Availability(context.Background()).Available {
		t.Fatal("Uptodown source should be available")
	}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != "base.apk" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	got, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(got, apkBytes) {
		t.Fatalf("downloaded package mismatch: %v", err)
	}
}

func TestUptodownSourceKeepsX8664SplitFromXAPK(t *testing.T) {
	base := zipBytes(t, map[string]string{"AndroidManifest.xml": "base"})
	split := zipBytes(t, map[string]string{"AndroidManifest.xml": "split", "lib/x86_64/libroblox.so": "native"})
	bundle := zipBytes(t, map[string]string{
		"com.roblox.client.apk":  string(base),
		"config.x86_64.apk":      string(split),
		"config.arm64_v8a.apk":   string(base),
		"uptodown-app-store.apk": string(base),
	})
	server := newUptodownServer(t, bundle, "bundle.xapk")
	source := &UptodownSource{Origin: server.URL, DWOrigin: server.URL, Client: server.Client()}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "split_config.x86_64.apk" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestUptodownSourceRejectsARMOnlyAPK(t *testing.T) {
	apkBytes := zipBytes(t, map[string]string{
		"AndroidManifest.xml":        "manifest",
		"lib/arm64-v8a/libroblox.so": "arm",
	})
	server := newUptodownServer(t, apkBytes, "base.apk")
	source := &UptodownSource{Origin: server.URL, DWOrigin: server.URL, Client: server.Client()}
	_, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestUptodownRejectsOffsiteRedirect(t *testing.T) {
	t.Parallel()
	source := &UptodownSource{}
	u, _ := http.NewRequest(http.MethodGet, "https://evil.example/android/download", nil)
	if err := source.trustedURL(u.URL); err == nil {
		t.Fatal("offsite URL was trusted")
	}
}

func TestNewUsesUptodownWhenConfigured(t *testing.T) {
	t.Parallel()
	s := NewWithSource(&UptodownSource{})
	a := s.AutomaticAvailability(context.Background())
	if !a.Available || a.Name != "Uptodown" || !strings.Contains(a.Explanation, "x86-64") {
		t.Fatalf("availability=%+v", a)
	}
}

func TestSelectX86PackageFilesPrefersRobloxNamedBase(t *testing.T) {
	t.Parallel()
	base := zipBytes(t, map[string]string{"AndroidManifest.xml": "base"})
	split := zipBytes(t, map[string]string{"AndroidManifest.xml": "split", "lib/x86_64/libroblox.so": "native"})
	bundle := zipBytes(t, map[string]string{
		"com.roblox.client.apk": string(base),
		"config.x86_64.apk":     string(split),
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "package.bin")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := selectX86PackageFiles(path, dir, "test")
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}

func newUptodownServer(t *testing.T, payload []byte, _ string) *httptest.Server {
	t.Helper()
	listing := `
<h1 id="detail-app-name" data-code="48053" data-file-id="9">Roblox</h1>
<div class="version">2.737.1584</div>
<button id="detail-download-button" data-app-id="48053" data-file-id="9" data-only-xapk="0">Download</button>
<th>Architecture</th><td>arm64-v8a, x86_64</td>
<th>Package Name</th><td>com.roblox.client</td>
<th>File type</th><td>XAPK</td>`
	mux := http.NewServeMux()
	mux.HandleFunc("/android/download", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(listing))
	})
	mux.HandleFunc("/ajax/app/48053/file/9/download-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": 1,
			"data":    map[string]string{"downloadURL": "aaa/bbb/ccc==/"},
		})
	})
	mux.HandleFunc("/dwn/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		_, _ = w.Write(payload)
	})
	return httptest.NewServer(mux)
}
