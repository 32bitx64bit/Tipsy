// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAPKPureListing(t *testing.T) {
	t.Parallel()
	body := []byte("com.roblox.client\x002.737.1584:XAPKJ\x8b\x03https://download.pureapk.com/b/XAPK/roblox?_fn=Um9ibG94&as=abc")
	got, err := parseAPKPureListing(body)
	if err != nil || !got.XAPK || got.Version != "2.737.1584" || !strings.Contains(got.URL, "/b/XAPK/roblox") {
		t.Fatalf("listing=%+v err=%v", got, err)
	}
}

func TestParseAPKPureListingSkipsXAPKSuffixAsAPK(t *testing.T) {
	t.Parallel()
	body := []byte("com.roblox.client 2.737.1584:XAPKJ\x00\x00https://download.pureapk.com/b/XAPK/latest")
	got, err := parseAPKPureListing(body)
	if err != nil || !got.XAPK || got.URL != "https://download.pureapk.com/b/XAPK/latest" {
		t.Fatalf("listing=%+v err=%v", got, err)
	}
}

func TestParseAPKPureListingRejectsWrongPackage(t *testing.T) {
	t.Parallel()
	_, err := parseAPKPureListing([]byte("com.other.app APKJ\x00\x00https://download.pureapk.com/b/APK/x"))
	if ErrorKindOf(err) != ErrWrongPackage {
		t.Fatalf("err=%v", err)
	}
}

func TestParseAPKPureListingRejectsMissingDownload(t *testing.T) {
	t.Parallel()
	_, err := parseAPKPureListing([]byte("com.roblox.client 2.737.1584"))
	if ErrorKindOf(err) != ErrSourceUnavailable {
		t.Fatalf("err=%v", err)
	}
}

func TestAPKPureSourceDownloadsX8664APK(t *testing.T) {
	apkBytes := zipBytes(t, map[string]string{
		"AndroidManifest.xml":     "manifest",
		"lib/x86_64/libroblox.so": "native",
	})
	server := newAPKPureServer(t, apkBytes, apkBytes)
	source := &APKPureSource{Origin: server.URL, Client: server.Client()}
	if !source.Availability(context.Background()).Available {
		t.Fatal("APKPure source should be available")
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

func TestAPKPureSourceKeepsX8664SplitFromXAPK(t *testing.T) {
	base := zipBytes(t, map[string]string{"AndroidManifest.xml": "base"})
	split := zipBytes(t, map[string]string{"AndroidManifest.xml": "split", "lib/x86_64/libroblox.so": "native"})
	bundle := zipBytes(t, map[string]string{
		"com.roblox.client.apk": string(base),
		"config.x86_64.apk":     string(split),
		"config.arm64_v8a.apk":  string(base),
	})
	server := newAPKPureServer(t, bundle, bundle)
	source := &APKPureSource{Origin: server.URL, Client: server.Client()}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "split_config.x86_64.apk" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestAPKPureSourceFallsBackToX86APK(t *testing.T) {
	arm := zipBytes(t, map[string]string{
		"AndroidManifest.xml":        "manifest",
		"lib/arm64-v8a/libroblox.so": "arm",
	})
	x86 := zipBytes(t, map[string]string{
		"AndroidManifest.xml":     "manifest",
		"lib/x86_64/libroblox.so": "native",
	})
	server := newAPKPureServer(t, arm, x86)
	source := &APKPureSource{Origin: server.URL, Client: server.Client()}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 1 || filepath.Base(paths[0]) != "base.apk" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	got, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(got, x86) {
		t.Fatalf("fallback package mismatch: %v", err)
	}
}

func TestAPKPureSourceRejectsARMOnlyAPK(t *testing.T) {
	arm := zipBytes(t, map[string]string{
		"AndroidManifest.xml":        "manifest",
		"lib/arm64-v8a/libroblox.so": "arm",
	})
	server := newAPKPureServer(t, arm, arm)
	source := &APKPureSource{Origin: server.URL, Client: server.Client()}
	_, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestAPKPureRejectsOffsiteRedirect(t *testing.T) {
	t.Parallel()
	source := &APKPureSource{}
	u, _ := http.NewRequest(http.MethodGet, "https://evil.example/b/XAPK/x", nil)
	if err := source.trustedURL(u.URL); err == nil {
		t.Fatal("offsite URL was trusted")
	}
}

func TestNewUsesAPKPure(t *testing.T) {
	t.Parallel()
	s := New()
	a := s.AutomaticAvailability(context.Background())
	if !a.Available || a.Name != "APKPure" || !strings.Contains(a.Explanation, "x86-64") {
		t.Fatalf("availability=%+v", a)
	}
}

func newAPKPureServer(t *testing.T, allABI, x86 []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var serverURL string
	mux.HandleFunc("/m/v3/cms/app_version", func(w http.ResponseWriter, r *http.Request) {
		path := "/b/XAPK/all"
		listing := "com.roblox.client 2.737.1584:XAPKJ\x00\x00" + serverURL + path
		if r.Header.Get("x-abis") == apkpureX86ABI {
			listing = "com.roblox.client 2.736.1408:APKJ\x00\x00" + serverURL + "/b/APK/x86"
		}
		_, _ = w.Write([]byte(listing))
	})
	mux.HandleFunc("/b/XAPK/all", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		_, _ = w.Write(allABI)
	})
	mux.HandleFunc("/b/APK/x86", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		_, _ = w.Write(x86)
	})
	server := httptest.NewServer(mux)
	serverURL = server.URL
	t.Cleanup(server.Close)
	return server
}
