// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Settings is the Discord-only slice of client settings the hub polls.
type Settings struct {
	Enabled    bool
	JoinButton bool
}

// Options configure a presence hub. Tests substitute Paths, Lookup, Load, and timing.
type Options struct {
	ApplicationID string
	PID           int
	Load          func(context.Context) (Settings, error)
	Lookup        func(context.Context, int64) (Place, error)
	Paths         []string
	DialTimeout   time.Duration
	Retry         time.Duration
	Poll          time.Duration
	HandshakeWait time.Duration
}

// Hub publishes Rich Presence for the live Roblox process. It never fails launch.
type Hub struct {
	opts   Options
	place  atomic.Int64
	closed atomic.Bool

	kick chan struct{}
	done chan struct{}

	mu     sync.Mutex
	client *Client
	cache  map[int64]Place
}

// Start runs the presence loop until ctx is cancelled or Close is called.
func Start(ctx context.Context, opts Options) *Hub {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Retry <= 0 {
		opts.Retry = 15 * time.Second
	}
	if opts.Poll <= 0 {
		opts.Poll = 3 * time.Second
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 200 * time.Millisecond
	}
	if opts.HandshakeWait <= 0 {
		opts.HandshakeWait = 2 * time.Second
	}
	if len(opts.Paths) == 0 {
		opts.Paths = IPCPaths(nil)
	}
	if opts.Lookup == nil {
		opts.Lookup = Catalog{}.Lookup
	}
	h := &Hub{
		opts:  opts,
		kick:  make(chan struct{}, 1),
		done:  make(chan struct{}),
		cache: make(map[int64]Place),
	}
	go h.loop(ctx)
	return h
}

// Seed publishes the launch URI place, including 0 for Home.
func (h *Hub) Seed(placeID int64) {
	if h == nil {
		return
	}
	if placeID < 0 {
		placeID = 0
	}
	h.place.Store(placeID)
	h.signal()
}

// SetPlaceID publishes a JNI-observed experience id. Non-positive values are
// ignored so an empty StartGameParams cannot clobber a live place.
func (h *Hub) SetPlaceID(placeID int64) {
	if h == nil || h.closed.Load() || placeID <= 0 {
		return
	}
	if h.place.Swap(placeID) == placeID {
		return
	}
	h.signal()
}

// SetLoadedPlaceID publishes an onGameLoaded place id, including 0 for Home
// after leaving an experience. Negative values are ignored.
func (h *Hub) SetLoadedPlaceID(placeID int64) {
	if h == nil || h.closed.Load() || placeID < 0 {
		return
	}
	if h.place.Swap(placeID) == placeID {
		return
	}
	h.signal()
}

// Close clears Discord activity and stops the loop.
func (h *Hub) Close() {
	if h == nil || h.closed.Swap(true) {
		return
	}
	h.signal()
	<-h.done
}

func (h *Hub) signal() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

func (h *Hub) loop(ctx context.Context) {
	defer close(h.done)
	defer h.disconnect(true)

	settings := Settings{Enabled: true}
	if loaded, err := h.load(ctx); err == nil {
		settings = loaded
	}
	var (
		publishedPlace int64 = -1
		published      Settings
		place          Place
		started        time.Time
		connected      bool
	)

	retry := time.NewTimer(time.Hour)
	stopTimer(retry)
	defer retry.Stop()

	publish := func() {
		if h.closed.Load() || ctx.Err() != nil {
			return
		}
		if loaded, err := h.load(ctx); err == nil {
			settings = loaded
		}
		if !settings.Enabled || h.opts.ApplicationID == "" {
			h.disconnect(true)
			connected = false
			publishedPlace = -1
			return
		}
		id := h.place.Load()
		prevName := place.Name
		if id != publishedPlace {
			place = h.resolve(ctx, id)
			started = time.Now()
		} else if id > 0 && place.Name == "" {
			place = h.resolve(ctx, id)
			if place.Name == prevName && connected && settings == published {
				return
			}
		} else if connected && settings == published {
			return
		}
		if err := h.ensureClient(); err != nil {
			connected = false
			resetTimer(retry, h.opts.Retry)
			return
		}
		connected = true
		act := BuildActivity(place, settings.JoinButton && id > 0, started)
		if err := h.client.setActivity(act); err != nil {
			logging.Logger(logging.CatRuntime).Info("discord activity update failed")
			h.disconnect(false)
			connected = false
			resetTimer(retry, h.opts.Retry)
			return
		}
		publishedPlace = id
		published = settings
		stopTimer(retry)
	}

	poll := time.NewTicker(h.opts.Poll)
	defer poll.Stop()
	publish()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.kick:
			if h.closed.Load() {
				return
			}
			publish()
		case <-poll.C:
			if h.closed.Load() {
				return
			}
			publish()
		case <-retry.C:
			if h.closed.Load() {
				return
			}
			publish()
		}
	}
}

func (h *Hub) load(ctx context.Context) (Settings, error) {
	if h.opts.Load == nil {
		return Settings{Enabled: true}, nil
	}
	return h.opts.Load(ctx)
}

func (h *Hub) resolve(ctx context.Context, id int64) Place {
	if id <= 0 {
		return Place{}
	}
	h.mu.Lock()
	if cached, ok := h.cache[id]; ok {
		h.mu.Unlock()
		return cached
	}
	h.mu.Unlock()
	place := Place{ID: id}
	if h.opts.Lookup != nil {
		got, err := h.opts.Lookup(ctx, id)
		if err != nil {
			logging.Logger(logging.CatNetwork).Info("discord catalog lookup failed", "placeId", id, "err", err)
			return place
		}
		if got.ID == 0 {
			got.ID = id
		}
		place = got
	}
	if place.Name != "" || place.IconURL != "" {
		h.mu.Lock()
		h.cache[id] = place
		h.mu.Unlock()
	}
	return place
}

func (h *Hub) ensureClient() error {
	h.mu.Lock()
	if h.client != nil {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	conn, _, err := dialIPC(h.opts.Paths, h.opts.DialTimeout)
	if err != nil {
		return err
	}
	client := newClient(conn, h.opts.PID)
	client.setReadDeadline(h.opts.HandshakeWait)
	if err := client.handshake(h.opts.ApplicationID); err != nil {
		_ = client.Close()
		return err
	}
	client.setReadDeadline(0)
	h.mu.Lock()
	if h.client != nil {
		h.mu.Unlock()
		_ = client.Close()
		return nil
	}
	h.client = client
	h.mu.Unlock()
	logging.Logger(logging.CatRuntime).Info("discord rich presence connected")
	return nil
}

func (h *Hub) disconnect(clear bool) {
	h.mu.Lock()
	client := h.client
	h.client = nil
	h.mu.Unlock()
	if client == nil {
		return
	}
	if clear {
		_ = client.setActivity(nil)
	}
	_ = client.Close()
}

func stopTimer(t *time.Timer) {
	if t == nil {
		return
	}
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func resetTimer(t *time.Timer, d time.Duration) {
	stopTimer(t)
	t.Reset(d)
}
