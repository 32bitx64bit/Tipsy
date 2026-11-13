// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLatestParsesReleaseAndSetsHeaders(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := opts.notBefore.Add(2 * time.Minute)
	const appImageName = "Tipsy-1.5.0-x86_64.AppImage"
	bundleJSON, digestHex := pki.makeBlobBundle(t, []byte("release-appimage-v1.5.0"), opts, integratedAt)
	sha256Text := fmt.Sprintf("%s  %s\n", digestHex, appImageName)

	type requestHeaders struct {
		path, userAgent, accept string
	}
	requests := make(chan requestHeaders, 8)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestHeaders{r.URL.Path, r.Header.Get("User-Agent"), r.Header.Get("Accept")}
		switch r.URL.Path {
		case "/repos/" + Repository + "/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			payload := Release{
				TagName: "v1.5.0",
				HTMLURL: "https://github.com/32bitx64bit/Tipsy/releases/tag/v1.5.0",
				Assets: []Asset{
					{Name: appImageName, BrowserDownloadURL: server.URL + "/dl/" + appImageName},
					{Name: appImageName + ".sha256", BrowserDownloadURL: server.URL + "/dl/" + appImageName + ".sha256"},
					{Name: appImageName + ".sigstore.json", BrowserDownloadURL: server.URL + "/dl/" + appImageName + ".sigstore.json"},
				},
			}
			_ = json.NewEncoder(w).Encode(payload)
		case "/dl/" + appImageName + ".sha256":
			_, _ = w.Write([]byte(sha256Text))
		case "/dl/" + appImageName + ".sigstore.json":
			_, _ = w.Write(bundleJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{
		BaseURL:   server.URL,
		HTTP:      server.Client(),
		UserAgent: "tipsy-test",
		Policy:    BundlePolicy{Trust: &pki.trust},
	}
	release, err := client.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	got := <-requests
	gotPath, gotUA, gotAccept := got.path, got.userAgent, got.accept
	if want := "/repos/" + Repository + "/releases/latest"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotUA != "tipsy-test" {
		t.Fatalf("User-Agent = %q", gotUA)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if release.TagName != "v1.5.0" {
		t.Fatalf("TagName = %q", release.TagName)
	}
	if release.HTMLURL != "https://github.com/32bitx64bit/Tipsy/releases/tag/v1.5.0" {
		t.Fatalf("HTMLURL = %q", release.HTMLURL)
	}
	if release.VerifiedSHA256 != digestHex {
		t.Fatalf("VerifiedSHA256 = %q, want %q", release.VerifiedSHA256, digestHex)
	}
	if _, ok := release.VerifiedAssets[appImageName]; !ok {
		t.Fatalf("VerifiedAssets missing %q: %#v", appImageName, release.VerifiedAssets)
	}
}

func TestLatestAcceptsFullEndpoint(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v2.0.0")
	integratedAt := opts.notBefore.Add(time.Minute)
	const appImageName = "Tipsy-2.0.0-x86_64.AppImage"
	bundleJSON, digestHex := pki.makeBlobBundle(t, []byte("release-appimage-v2.0.0"), opts, integratedAt)

	paths := make(chan string, 8)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		switch r.URL.Path {
		case "/repos/custom/repo/releases/latest":
			_ = json.NewEncoder(w).Encode(Release{
				TagName: "v2.0.0",
				Assets: []Asset{
					{Name: appImageName, BrowserDownloadURL: server.URL + "/dl/" + appImageName},
					{Name: appImageName + ".sha256", BrowserDownloadURL: server.URL + "/dl/" + appImageName + ".sha256"},
					{Name: appImageName + ".sigstore.json", BrowserDownloadURL: server.URL + "/dl/" + appImageName + ".sigstore.json"},
				},
			})
		case "/dl/" + appImageName + ".sha256":
			_, _ = fmt.Fprintf(w, "%s  %s\n", digestHex, appImageName)
		case "/dl/" + appImageName + ".sigstore.json":
			_, _ = w.Write(bundleJSON)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{
		BaseURL: server.URL + "/repos/custom/repo/releases/latest",
		HTTP:    server.Client(),
		Policy:  BundlePolicy{Trust: &pki.trust},
	}
	if _, err := client.Latest(context.Background()); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if gotPath := <-paths; gotPath != "/repos/custom/repo/releases/latest" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestLatestRejectsUnsignedOrTamperedReleaseAssets(t *testing.T) {
	t.Parallel()

	pki := newSyntheticSigstorePKI(t)
	opts := defaultSyntheticCertOptions("v1.5.0")
	integratedAt := opts.notBefore.Add(time.Minute)
	const appImageName = "Tipsy-1.5.0-x86_64.AppImage"
	validBundle, validHex := pki.makeBlobBundle(t, []byte("official-v1.5.0"), opts, integratedAt)

	wrongRepoOpts := opts
	wrongURI := "https://github.com/fork/Tipsy/.github/workflows/release.yml@refs/tags/v1.5.0"
	wrongRepoOpts.sanURI = wrongURI
	wrongRepoOpts.buildSignerURI = wrongURI
	wrongRepoOpts.buildConfigURI = wrongURI
	wrongRepoBundle, _ := pki.makeBlobBundle(t, []byte("official-v1.5.0"), wrongRepoOpts, integratedAt)

	tests := []struct {
		name       string
		omitSHA256 bool
		omitBundle bool
		omitAsset  bool
		sha256Line string
		bundle     []byte
		wantErr    string
	}{
		{
			name:      "no AppImage asset",
			omitAsset: true,
			wantErr:   "no official AppImage asset",
		},
		{
			name:       "missing sha256 asset",
			omitSHA256: true,
			bundle:     validBundle,
			wantErr:    "missing " + appImageName + ".sha256",
		},
		{
			name:       "missing sigstore bundle asset",
			omitBundle: true,
			sha256Line: fmt.Sprintf("%s  %s\n", validHex, appImageName),
			wantErr:    "missing " + appImageName + ".sigstore.json",
		},
		{
			name:       "tampered sha256 digest",
			sha256Line: fmt.Sprintf("%s  %s\n", strings.Repeat("a", 64), appImageName),
			bundle:     validBundle,
			wantErr:    "does not match expected SHA-256",
		},
		{
			name:       "wrong workflow identity",
			sha256Line: fmt.Sprintf("%s  %s\n", validHex, appImageName),
			bundle:     wrongRepoBundle,
			wantErr:    "not pinned",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/" + Repository + "/releases/latest":
					var assets []Asset
					if !tt.omitAsset {
						assets = append(assets, Asset{Name: appImageName, BrowserDownloadURL: server.URL + "/dl/" + appImageName})
						if !tt.omitSHA256 {
							assets = append(assets, Asset{Name: appImageName + ".sha256", BrowserDownloadURL: server.URL + "/dl/" + appImageName + ".sha256"})
						}
						if !tt.omitBundle {
							assets = append(assets, Asset{Name: appImageName + ".sigstore.json", BrowserDownloadURL: server.URL + "/dl/" + appImageName + ".sigstore.json"})
						}
					}
					_ = json.NewEncoder(w).Encode(Release{TagName: "v1.5.0", Assets: assets})
				case "/dl/" + appImageName + ".sha256":
					_, _ = w.Write([]byte(tt.sha256Line))
				case "/dl/" + appImageName + ".sigstore.json":
					_, _ = w.Write(tt.bundle)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client := Client{
				BaseURL: server.URL,
				HTTP:    server.Client(),
				Policy:  BundlePolicy{Trust: &pki.trust},
			}
			_, err := client.Latest(context.Background())
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestLatestHTTPErrorIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := (Client{BaseURL: server.URL, HTTP: server.Client()}).Latest(context.Background())
	if err == nil {
		t.Fatal("expected an error for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error = %v, want HTTP 500", err)
	}
}

func TestLatestTimeoutIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, HTTP: server.Client(), Timeout: 25 * time.Millisecond}
	start := time.Now()
	_, err := client.Latest(context.Background())
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestLatestRejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{`, `{}`} {
		body := body
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		_, err := (Client{BaseURL: server.URL, HTTP: server.Client()}).Latest(context.Background())
		server.Close()
		if err == nil {
			t.Fatalf("expected an error for body %q", body)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		local string
		tag   string
		want  Status
	}{
		{"equal with v prefix", "1.2.3", "v1.2.3", UpToDate},
		{"newer release", "1.2.3", "v1.2.4", UpdateAvailable},
		{"newer minor", "1.2.3", "v1.3.0", UpdateAvailable},
		{"newer major", "1.2.3", "v2.0.0", UpdateAvailable},
		{"older release", "2.0.0", "v1.9.9", UpToDate},
		{"local v prefix", "v1.2.3", "v1.2.3", UpToDate},
		{"short local version", "1.2", "v1.2.0", UpToDate},
		{"dev build", "0.0.0-dev", "v1.5.0", DevBuild},
		{"empty build", "", "v1.5.0", DevBuild},
		{"dev suffix", "1.2.3-dev", "v1.2.3", DevBuild},
		{"local prerelease", "1.2.3-rc.1", "v1.2.3", UpdateAvailable},
		{"release beats prerelease", "1.2.3", "v1.2.3-rc.1", UpToDate},
		{"prerelease numeric", "1.2.3-rc.1", "v1.2.3-rc.2", UpdateAvailable},
		{"malformed local", "not-a-version", "v1.5.0", DevBuild},
		{"malformed tag", "1.2.3", "not-a-version", UpToDate},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Compare(tt.local, tt.tag); got != tt.want {
				t.Fatalf("Compare(%q, %q) = %q, want %q", tt.local, tt.tag, got, tt.want)
			}
		})
	}
}

func TestLatestEndpointDefault(t *testing.T) {
	t.Parallel()

	got := (Client{}).latestEndpoint()
	want := DefaultBaseURL + "/repos/" + Repository + "/releases/latest"
	if got != want {
		t.Fatalf("latestEndpoint = %q, want %q", got, want)
	}
	if Repository != "32bitx64bit/Tipsy" {
		t.Fatalf("Repository = %q", Repository)
	}
}
