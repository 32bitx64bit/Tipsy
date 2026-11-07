// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLatestParsesReleaseAndSetsHeaders(t *testing.T) {
	t.Parallel()

	const body = `{
		"tag_name": "v1.5.0",
		"html_url": "https://github.com/32bitx64bit/Tipsy/releases/tag/v1.5.0",
		"assets": [
			{"name": "Tipsy-1.5.0-x86_64.AppImage", "browser_download_url": "https://example.test/Tipsy-1.5.0-x86_64.AppImage"}
		]
	}`
	type requestHeaders struct {
		path, userAgent, accept string
	}
	requests := make(chan requestHeaders, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestHeaders{r.URL.Path, r.Header.Get("User-Agent"), r.Header.Get("Accept")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, HTTP: server.Client(), UserAgent: "tipsy-test"}
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
	if len(release.Assets) != 1 || release.Assets[0].Name != "Tipsy-1.5.0-x86_64.AppImage" {
		t.Fatalf("Assets = %#v", release.Assets)
	}
}

func TestLatestAcceptsFullEndpoint(t *testing.T) {
	t.Parallel()

	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL + "/repos/custom/repo/releases/latest", HTTP: server.Client()}
	if _, err := client.Latest(context.Background()); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if gotPath := <-paths; gotPath != "/repos/custom/repo/releases/latest" {
		t.Fatalf("path = %q", gotPath)
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
