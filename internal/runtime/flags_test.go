// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fetch must already be running on its own goroutine before any join, and
// the join must return that goroutine's result.
func TestAndroidAppSettingsFetchStartsBeforeJoin(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	body := []byte(`{"applicationSettings":{"FFlagEarlyStart":"True"}}`)
	f := startAndroidAppSettingsFetchWith(func() ([]byte, error) {
		close(started)
		<-release
		return body, nil
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not start before the join")
	}

	type joined struct {
		js  string
		n   int
		err error
	}
	results := make(chan joined, 1)
	go func() {
		js, n, err := f.join(context.Background(), "", "", androidAppOverrides{})
		results <- joined{js: js, n: n, err: err}
	}()
	select {
	case got := <-results:
		t.Fatalf("join returned before the fetch finished: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	got := <-results
	if got.err != nil {
		t.Fatalf("join: %v", got.err)
	}
	if got.n != 1 || !strings.Contains(got.js, `"FFlagEarlyStart":"True"`) {
		t.Fatalf("join did not return the fetched JSON: n=%d js=%s", got.n, got.js)
	}
}

// A failed fetch keeps the last good cache, and a cache miss still reports the
// fetch error rather than pretending success.
func TestAndroidAppSettingsFetchJoinFallsBackToCacheOnError(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "ClientAppSettings.json")
	cached := []byte(`{"applicationSettings":{"FFlagCachedFallback":"True"}}`)
	if err := os.WriteFile(cachePath, cached, 0o600); err != nil {
		t.Fatal(err)
	}
	fetchErr := errors.New("settings CDN unreachable")
	f := startAndroidAppSettingsFetchWith(func() ([]byte, error) { return nil, fetchErr })

	js, n, err := f.join(context.Background(), cachePath, "2.734.917", androidAppOverrides{})
	if err != nil {
		t.Fatalf("join with cache: %v", err)
	}
	if n != 1 || !strings.Contains(js, `"FFlagCachedFallback":"True"`) {
		t.Fatalf("join did not return cached JSON: n=%d js=%s", n, js)
	}
	if _, _, err := f.join(context.Background(), "", "2.734.917", androidAppOverrides{}); !errors.Is(err, fetchErr) {
		t.Fatalf("join without cache = %v, want %v", err, fetchErr)
	}
}

// A fresh response wins over the cache and is written back; a malformed fresh
// response falls back to the cache instead of dropping client settings.
func TestAndroidAppSettingsFetchResultCacheSemantics(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "ClientAppSettings.json")
	if err := os.WriteFile(cachePath, []byte(`{"applicationSettings":{"FFlagStale":"True"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := []byte(`{"applicationSettings":{"FFlagFresh":"True"}}`)
	js, n, err := androidAppSettingsResult(fresh, nil, cachePath, androidAppOverrides{})
	if err != nil {
		t.Fatalf("fresh result: %v", err)
	}
	if n != 1 || !strings.Contains(js, `"FFlagFresh":"True"`) || strings.Contains(js, `"FFlagStale"`) {
		t.Fatalf("fresh JSON did not win: n=%d js=%s", n, js)
	}
	written, err := os.ReadFile(cachePath)
	if err != nil || string(written) != string(fresh) {
		t.Fatalf("cache write = %q, %v", written, err)
	}
	js, n, err = androidAppSettingsResult([]byte("not-json"), nil, cachePath, androidAppOverrides{})
	if err != nil {
		t.Fatalf("parse-failure fallback: %v", err)
	}
	if n != 1 || !strings.Contains(js, `"FFlagFresh":"True"`) {
		t.Fatalf("parse failure did not return the last good cache: n=%d js=%s", n, js)
	}
}
