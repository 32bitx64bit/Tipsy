// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	universeURLFmt = "https://apis.roblox.com/universes/v1/places/%d/universe"
	gamesListURL   = "https://games.roblox.com/v1/games"
	thumbnailsURL  = "https://thumbnails.roblox.com/v1/places/gameicons"
	catalogUA      = "Tipsy-Linux"
	catalogLimit   = 1 << 20
)

type Catalog struct {
	Client      *http.Client
	UniverseURL string
	GamesURL    string
	ThumbsURL   string
}

func (c Catalog) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// Lookup returns a public place name and icon. It uses unauthenticated catalog
// APIs only: never cookies, tickets, or .ROBLOSECURITY.
// Failures are honest: the caller keeps the fallback display name "Roblox".
func (c Catalog) Lookup(ctx context.Context, placeID int64) (Place, error) {
	if placeID <= 0 {
		return Place{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	place := Place{ID: placeID}
	name, nameErr := c.placeName(ctx, placeID)
	if nameErr == nil {
		place.Name = name
	}
	icon, iconErr := c.placeIcon(ctx, placeID)
	if iconErr == nil {
		place.IconURL = icon
	}
	if nameErr != nil && iconErr != nil {
		return place, nameErr
	}
	return place, nil
}

func (c Catalog) placeName(ctx context.Context, placeID int64) (string, error) {
	uni, err := c.universeID(ctx, placeID)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(c.gamesListURL())
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("universeIds", strconv.FormatInt(uni, 10))
	u.RawQuery = q.Encode()
	payload, err := c.get(ctx, u.String())
	if err != nil {
		return "", err
	}
	var body struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", err
	}
	if len(body.Data) == 0 {
		return "", fmt.Errorf("place name missing")
	}
	name := sanitizeName(body.Data[0].Name)
	if name == "" {
		return "", fmt.Errorf("place name missing")
	}
	return name, nil
}

func (c Catalog) universeID(ctx context.Context, placeID int64) (int64, error) {
	raw := c.universeURL(placeID)
	payload, err := c.get(ctx, raw)
	if err != nil {
		return 0, err
	}
	var body struct {
		UniverseID int64 `json:"universeId"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return 0, err
	}
	if body.UniverseID <= 0 {
		return 0, fmt.Errorf("universe missing")
	}
	return body.UniverseID, nil
}

func (c Catalog) placeIcon(ctx context.Context, placeID int64) (string, error) {
	u, err := url.Parse(c.thumbsURL())
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("placeIds", strconv.FormatInt(placeID, 10))
	q.Set("returnPolicy", "PlaceHolder")
	q.Set("size", "512x512")
	q.Set("format", "Png")
	q.Set("isCircular", "false")
	u.RawQuery = q.Encode()
	payload, err := c.get(ctx, u.String())
	if err != nil {
		return "", err
	}
	var body struct {
		Data []struct {
			TargetID int64  `json:"targetId"`
			State    string `json:"state"`
			ImageURL string `json:"imageUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "", err
	}
	for _, row := range body.Data {
		if row.TargetID != placeID && row.TargetID != 0 {
			continue
		}
		if row.State != "" && row.State != "Completed" && row.State != "Placeholder" {
			continue
		}
		if icon := publicHTTPS(row.ImageURL); icon != "" {
			return icon, nil
		}
	}
	return "", fmt.Errorf("place icon missing")
}

func (c Catalog) universeURL(placeID int64) string {
	tmpl := universeURLFmt
	if c.UniverseURL != "" {
		tmpl = c.UniverseURL
	}
	if strings.Contains(tmpl, "%d") {
		return fmt.Sprintf(tmpl, placeID)
	}
	return tmpl
}

func (c Catalog) gamesListURL() string {
	if c.GamesURL != "" {
		return c.GamesURL
	}
	return gamesListURL
}

func (c Catalog) thumbsURL() string {
	if c.ThumbsURL != "" {
		return c.ThumbsURL
	}
	return thumbnailsURL
}

func (c Catalog) get(ctx context.Context, raw string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", catalogUA)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, catalogLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("catalog http %d", resp.StatusCode)
	}
	return payload, nil
}
