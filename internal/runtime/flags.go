// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Official public client-settings endpoint the Android app uses.
const androidAppSettingsURL = "https://clientsettingscdn.roblox.com/v2/settings/application/AndroidApp"

// applicationSettingsFromResponse extracts the flag map as a JSON object.
// Does not log flag names or values.
func applicationSettingsFromResponse(body []byte) (string, int, error) {
	var wrap struct {
		ApplicationSettings map[string]any `json:"applicationSettings"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return "", 0, err
	}
	m := wrap.ApplicationSettings
	if len(m) == 0 {
		var direct map[string]any
		if err := json.Unmarshal(body, &direct); err == nil {
			if _, has := direct["applicationSettings"]; !has {
				m = direct
			}
		}
	}
	n := len(m)
	if n == 0 {
		m = map[string]any{}
	} else {
		m = cloneFlagMap(m)
	}
	// 7a14858 / ShadowFValuesEnabled defaults false and is absent from CDN.
	// Commit 0x2e77680 runs apply (store+0x10 list → shadow maps) and sets
	// 7a148d8 only when that C++ byte is already 1. JSON overlay does not
	// set the byte in time; launch pokes it. JNI nativeGetFFlag still reads
	// a different map (store+0x1a8).
	m["ShadowFValuesEnabled"] = "True"
	raw, err := json.Marshal(map[string]any{
		"applicationSettings": m,
		"ClientAppSettings":   m,
	})
	if err != nil {
		return "", 0, err
	}
	return string(raw), n, nil
}

func cloneFlagMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func loadAndroidAppSettings(cachePath string) (string, int, error) {
	body, err := fetchAndroidAppSettings()
	if err != nil {
		if cachePath != "" {
			if cached, rerr := os.ReadFile(cachePath); rerr == nil && len(cached) > 2 {
				return applicationSettingsFromResponse(cached)
			}
		}
		return "", 0, err
	}
	js, n, err := applicationSettingsFromResponse(body)
	if err != nil {
		return "", 0, err
	}
	if cachePath != "" && n > 0 {
		_ = os.WriteFile(cachePath, body, 0o600)
	}
	return js, n, nil
}

func fetchAndroidAppSettings() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, androidAppSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Roblox/2.734.917 (Linux; Android 8.0.0; tipsy)")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client settings HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
