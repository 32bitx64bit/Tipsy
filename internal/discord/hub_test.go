// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubPublishesSeededPlaceAndClearsOnClose(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var payloads []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			if op == opHandshake {
				ready, _ := json.Marshal(map[string]any{"evt": "READY"})
				_ = writeFrame(conn, opFrame, ready)
				continue
			}
			mu.Lock()
			payloads = append(payloads, string(body))
			mu.Unlock()
		}
	}()

	settings := Settings{Enabled: true, JoinButton: true}
	hub := Start(context.Background(), Options{
		ApplicationID: "42",
		PID:           7,
		Paths:         []string{sock},
		DialTimeout:   time.Second,
		HandshakeWait: time.Second,
		Poll:          time.Hour,
		Retry:         time.Hour,
		Load: func(context.Context) (Settings, error) {
			return settings, nil
		},
		Lookup: func(_ context.Context, id int64) (Place, error) {
			return Place{ID: id, Name: "Crossroads", IconURL: "https://tr.rbxcdn.com/x.png"}, nil
		},
	})
	hub.Seed(1818)
	waitPayload(t, &mu, &payloads, "Crossroads - Tipsy")
	hub.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(payloads, "\n")
	if !strings.Contains(joined, `"Crossroads - Tipsy"`) || !strings.Contains(joined, `"https://www.roblox.com/games/start?placeId=1818"`) {
		t.Fatalf("payloads=%v", payloads)
	}
	if !strings.Contains(joined, `"activity":null`) && !strings.Contains(joined, `"activity": null`) {
		t.Fatalf("close did not clear activity: %v", payloads)
	}
}

func TestHubSkipsZeroJNIPlaceAndHonorsDisable(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var payloads []string
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			if op == opHandshake {
				ready, _ := json.Marshal(map[string]any{"evt": "READY"})
				_ = writeFrame(conn, opFrame, ready)
				continue
			}
			mu.Lock()
			payloads = append(payloads, string(body))
			mu.Unlock()
		}
	}()

	var settingsMu sync.Mutex
	settings := Settings{Enabled: true}
	hub := Start(context.Background(), Options{
		ApplicationID: "42",
		Paths:         []string{sock},
		DialTimeout:   time.Second,
		HandshakeWait: time.Second,
		Poll:          20 * time.Millisecond,
		Retry:         time.Hour,
		Load: func(context.Context) (Settings, error) {
			settingsMu.Lock()
			defer settingsMu.Unlock()
			return settings, nil
		},
		Lookup: func(context.Context, int64) (Place, error) {
			t.Fatal("catalog lookup should not run for Home")
			return Place{}, nil
		},
	})
	defer hub.Close()
	hub.Seed(0)
	hub.SetPlaceID(0)
	waitPayload(t, &mu, &payloads, "Home - Tipsy")
	settingsMu.Lock()
	settings.Enabled = false
	settingsMu.Unlock()
	waitPayload(t, &mu, &payloads, `"activity":null`)
}

func TestHubLoadedPlaceReturnsHome(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var payloads []string
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			if op == opHandshake {
				ready, _ := json.Marshal(map[string]any{"evt": "READY"})
				_ = writeFrame(conn, opFrame, ready)
				continue
			}
			mu.Lock()
			payloads = append(payloads, string(body))
			mu.Unlock()
		}
	}()

	hub := Start(context.Background(), Options{
		ApplicationID: "42",
		Paths:         []string{sock},
		DialTimeout:   time.Second,
		HandshakeWait: time.Second,
		Poll:          time.Hour,
		Retry:         time.Hour,
		Load: func(context.Context) (Settings, error) {
			return Settings{Enabled: true}, nil
		},
		Lookup: func(_ context.Context, id int64) (Place, error) {
			return Place{ID: id, Name: "Crossroads"}, nil
		},
	})
	defer hub.Close()
	hub.Seed(0)
	waitPayload(t, &mu, &payloads, "Home - Tipsy")
	hub.SetLoadedPlaceID(1818)
	waitPayload(t, &mu, &payloads, "Crossroads - Tipsy")
	hub.SetPlaceID(0)
	hub.SetLoadedPlaceID(0)
	waitPayload(t, &mu, &payloads, "Home - Tipsy")
}

func TestCatalogLookupUsesPublicAPIs(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/games/multiget-place-details", func(http.ResponseWriter, *http.Request) {
		t.Fatal("authenticated place-details endpoint must not be used")
	})
	mux.HandleFunc("/universes/v1/places/1818/universe", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"universeId":99}`)
	})
	mux.HandleFunc("/v1/games", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("universeIds") != "99" {
			t.Fatalf("universeIds=%q", r.URL.Query().Get("universeIds"))
		}
		if r.URL.Query().Get("placeIds") != "" {
			t.Fatal("games list must not send placeIds")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":99,"rootPlaceId":1818,"name":"Crossroads"}]}`)
	})
	mux.HandleFunc("/v1/places/gameicons", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"targetId":1818,"state":"Completed","imageUrl":"https://tr.rbxcdn.com/icon.png"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := Catalog{
		Client:      srv.Client(),
		UniverseURL: srv.URL + "/universes/v1/places/%d/universe",
		GamesURL:    srv.URL + "/v1/games",
		ThumbsURL:   srv.URL + "/v1/places/gameicons",
	}
	got, err := c.Lookup(context.Background(), 1818)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Crossroads" || got.IconURL != "https://tr.rbxcdn.com/icon.png" || got.ID != 1818 {
		t.Fatalf("lookup=%+v", got)
	}
}

func TestCatalogLookupKeepsIconWhenNameFails(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/universes/v1/places/1818/universe", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"code":9002,"message":"Authentication token is missing"}]}`)
	})
	mux.HandleFunc("/v1/places/gameicons", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"targetId":1818,"state":"Completed","imageUrl":"https://tr.rbxcdn.com/icon.png"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := Catalog{
		Client:      srv.Client(),
		UniverseURL: srv.URL + "/universes/v1/places/%d/universe",
		GamesURL:    srv.URL + "/v1/games",
		ThumbsURL:   srv.URL + "/v1/places/gameicons",
	}
	got, err := c.Lookup(context.Background(), 1818)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "" || got.IconURL != "https://tr.rbxcdn.com/icon.png" || got.ID != 1818 {
		t.Fatalf("lookup=%+v", got)
	}
}

func TestHubRetriesFailedCatalogLookup(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var payloads []string
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			op, body, err := readFrame(conn)
			if err != nil {
				return
			}
			if op == opHandshake {
				ready, _ := json.Marshal(map[string]any{"evt": "READY"})
				_ = writeFrame(conn, opFrame, ready)
				continue
			}
			mu.Lock()
			payloads = append(payloads, string(body))
			mu.Unlock()
		}
	}()

	var lookups atomic.Int32
	hub := Start(context.Background(), Options{
		ApplicationID: "42",
		Paths:         []string{sock},
		DialTimeout:   time.Second,
		HandshakeWait: time.Second,
		Poll:          20 * time.Millisecond,
		Retry:         time.Hour,
		Load: func(context.Context) (Settings, error) {
			return Settings{Enabled: true}, nil
		},
		Lookup: func(_ context.Context, id int64) (Place, error) {
			if lookups.Add(1) == 1 {
				return Place{}, fmt.Errorf("catalog http 401")
			}
			return Place{ID: id, Name: "Crossroads", IconURL: "https://tr.rbxcdn.com/icon.png"}, nil
		},
	})
	defer hub.Close()
	hub.SetPlaceID(1818)
	waitPayload(t, &mu, &payloads, "Roblox - Tipsy")
	waitPayload(t, &mu, &payloads, "Crossroads - Tipsy")
	waitPayload(t, &mu, &payloads, "https://tr.rbxcdn.com/icon.png")
}

func TestResolvedApplicationIDUsesEnvOverride(t *testing.T) {
	t.Setenv(envApplication, " 998877 ")
	if got := ResolvedApplicationID(); got != "998877" {
		t.Fatalf("resolved=%q", got)
	}
}

func TestApplicationIDIsPublicSnowflake(t *testing.T) {
	if ApplicationID != "1546068043614916629" {
		t.Fatalf("ApplicationID=%q", ApplicationID)
	}
	t.Setenv(envApplication, "")
	if got := ResolvedApplicationID(); got != ApplicationID {
		t.Fatalf("resolved=%q", got)
	}
}

func waitPayload(t *testing.T, mu *sync.Mutex, payloads *[]string, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		joined := strings.Join(*payloads, "\n")
		mu.Unlock()
		if strings.Contains(joined, needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("missing %q in %v", needle, *payloads)
}
