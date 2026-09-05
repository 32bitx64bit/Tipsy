// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLatestReleasePath(t *testing.T) {
	t.Parallel()
	html := `
<a href="/apk/roblox-corporation/roblox/roblox-2-165-50938-release/">stale armeabi filter</a>
<a href="/apk/roblox-corporation/roblox/roblox-2-700-0-release/">older</a>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/">current</a>
<a href="/apk/other-publisher/game/game-1-release/">ignore</a>`
	path, err := parseLatestReleasePath([]byte(html))
	if err != nil || path != "/apk/roblox-corporation/roblox/roblox-2-734-917-release/" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}

func TestSelectX86VariantPrefersNativeX8664(t *testing.T) {
	t.Parallel()
	html := `
<div class="table topmargin variants-table">
<div class="table-row headerFont"><span class="apkm-badge">BUNDLE</span>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-universal-android-apk-download/">Roblox</a>
<div class="table-cell">universal</div></div>
<div class="table-row headerFont"><span class="apkm-badge">APK</span>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-arm64-android-apk-download/">Roblox</a>
<div class="table-cell">arm64-v8a</div></div>
<div class="table-row headerFont"><span class="apkm-badge">APK</span>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-x86-64-android-apk-download/">Roblox</a>
<div class="table-cell">x86_64</div></div>
</div>`
	got, err := selectX86Variant(parseVariants([]byte(html)))
	if err != nil || got.Arch != "x86_64" || got.Kind != "apk" {
		t.Fatalf("variant=%+v err=%v", got, err)
	}
}

func TestSelectX86VariantRejectsX86_32(t *testing.T) {
	t.Parallel()
	html := `
<div class="table topmargin variants-table">
<div class="table-row headerFont"><span class="apkm-badge">APK</span>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-x86-android-apk-download/">Roblox</a>
<div class="table-cell">x86</div></div>
</div>`
	_, err := selectX86Variant(parseVariants([]byte(html)))
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectX86VariantRejectsARMOnly(t *testing.T) {
	t.Parallel()
	html := `
<div class="table topmargin variants-table">
<div class="table-row headerFont"><span class="apkm-badge">APK</span>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-arm-android-apk-download/">Roblox</a>
<div class="table-cell">arm64-v8a</div></div>
</div>`
	_, err := selectX86Variant(parseVariants([]byte(html)))
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestParseDownloadLinks(t *testing.T) {
	t.Parallel()
	button, err := parseDownloadButtonPath([]byte(`<a class="accent_bg btn downloadButton" href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-x86-64-android-apk-download/download/?key=abc">Download</a>`))
	if err != nil || !strings.Contains(button, "download/?key=abc") {
		t.Fatalf("button=%q err=%v", button, err)
	}
	direct, err := parseDirectDownloadPath([]byte(`<a rel="nofollow" href="/wp-content/themes/APKMirror/download.php?id=9&amp;key=abc">here</a>`))
	if err != nil || !strings.Contains(direct, "download.php?id=9") || !strings.Contains(direct, "key=abc") {
		t.Fatalf("direct=%q err=%v", direct, err)
	}
}

func TestAPKMirrorSourceDownloadsX8664APK(t *testing.T) {
	apkBytes := zipBytes(t, map[string]string{
		"AndroidManifest.xml":     "manifest",
		"lib/x86_64/libroblox.so": "native",
	})
	server := newAPKMirrorServer(t, apkBytes)
	source := &APKMirrorSource{Origin: server.URL, Client: server.Client()}
	if !source.Availability(context.Background()).Available {
		t.Fatal("APKMirror source should be available")
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

func TestAPKMirrorSourceRejectsARMOnlyAPK(t *testing.T) {
	apkBytes := zipBytes(t, map[string]string{
		"AndroidManifest.xml":        "manifest",
		"lib/arm64-v8a/libroblox.so": "arm",
	})
	server := newAPKMirrorServer(t, apkBytes)
	source := &APKMirrorSource{Origin: server.URL, Client: server.Client()}
	_, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestAPKMirrorSourceRejectsARMOnlyBundle(t *testing.T) {
	base := zipBytes(t, map[string]string{"AndroidManifest.xml": "manifest", "lib/arm64-v8a/libroblox.so": "arm"})
	bundle := zipBytes(t, map[string]string{
		"base.apk":                   string(base),
		"split_config.arm64_v8a.apk": string(base),
	})
	server := newAPKMirrorServer(t, bundle)
	source := &APKMirrorSource{Origin: server.URL, Client: server.Client()}
	_, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if ErrorKindOf(err) != ErrMissingX8664 {
		t.Fatalf("err=%v", err)
	}
}

func TestAPKMirrorSourceKeepsX8664SplitFromBundle(t *testing.T) {
	base := zipBytes(t, map[string]string{"AndroidManifest.xml": "base"})
	split := zipBytes(t, map[string]string{"AndroidManifest.xml": "split", "lib/x86_64/libroblox.so": "native"})
	bundle := zipBytes(t, map[string]string{
		"base.apk":                   string(base),
		"split_config.x86_64.apk":    string(split),
		"split_config.arm64_v8a.apk": string(base),
	})
	server := newAPKMirrorServer(t, bundle)
	source := &APKMirrorSource{Origin: server.URL, Client: server.Client()}
	paths, err := source.Acquire(context.Background(), t.TempDir(), nil)
	if err != nil || len(paths) != 2 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if filepath.Base(paths[0]) != "base.apk" || filepath.Base(paths[1]) != "split_config.x86_64.apk" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestAPKMirrorRejectsOffsiteRedirect(t *testing.T) {
	t.Parallel()
	source := &APKMirrorSource{}
	u, _ := http.NewRequest(http.MethodGet, "https://evil.example/apk/roblox-corporation/roblox/", nil)
	if err := source.trustedURL(u.URL); err == nil {
		t.Fatal("offsite URL was trusted")
	}
}

func TestAutomaticInstallStillRejectsUnverifiedPackage(t *testing.T) {
	input := writeTestZIP(t, filepath.Join(t.TempDir(), "base.apk"), map[string][]byte{
		"AndroidManifest.xml":     []byte("manifest"),
		"lib/x86_64/libroblox.so": []byte("native"),
	})
	s := NewWithSource(staticSource{paths: []string{input}})
	s.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	_, err := s.Install(context.Background(), InstallRequest{Mode: InstallAutomatic}, nil)
	kind := ErrorKindOf(err)
	if kind != ErrInvalidArchive && kind != ErrInvalidSignature && kind != ErrWrongPackage {
		t.Fatalf("unverified automatic package err=%v kind=%q", err, kind)
	}
}

type staticSource struct{ paths []string }

func (s staticSource) Availability(context.Context) SourceAvailability {
	return SourceAvailability{Available: true, Name: "fixture"}
}

func (s staticSource) Acquire(context.Context, string, ProgressFunc) ([]string, error) {
	return s.paths, nil
}

func TestNewUsesAPKMirrorWhenConfigured(t *testing.T) {
	t.Parallel()
	s := NewWithSource(&APKMirrorSource{})
	a := s.AutomaticAvailability(context.Background())
	if !a.Available || a.Name != "APKMirror" || !strings.Contains(a.Explanation, "x86-64") {
		t.Fatalf("availability=%+v", a)
	}
}

func newAPKMirrorServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	listing := `<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/">latest</a>`
	variants := `
<div class="table topmargin variants-table">
<div class="table-row headerFont"><span class="apkm-badge">APK</span>
<a href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-x86-64-android-apk-download/">Roblox</a>
<div class="table-cell">x86_64</div></div>
</div>`
	variantPage := `<a class="downloadButton" href="/apk/roblox-corporation/roblox/roblox-2-734-917-release/roblox-x86-64-android-apk-download/download/?key=abc">Download</a>`
	downloadPage := `<a rel="nofollow" href="/wp-content/themes/APKMirror/download.php?id=9&amp;key=abc">here</a>`
	mux := http.NewServeMux()
	mux.HandleFunc("/apk/roblox-corporation/roblox/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "download.php"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/download/") || strings.HasSuffix(r.URL.Path, "/download"):
			_, _ = w.Write([]byte(downloadPage))
		case strings.Contains(r.URL.Path, "android-apk-download"):
			_, _ = w.Write([]byte(variantPage))
		case strings.HasSuffix(r.URL.Path, "-release/") || strings.HasSuffix(r.URL.Path, "-release"):
			_, _ = w.Write([]byte(variants))
		default:
			_, _ = w.Write([]byte(listing))
		}
	})
	mux.HandleFunc("/wp-content/themes/APKMirror/download.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		_, _ = w.Write(payload)
	})
	return httptest.NewServer(mux)
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		fw, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
