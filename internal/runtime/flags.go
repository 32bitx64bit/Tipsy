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
	"path/filepath"
	"time"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/logging"
)

// Official public client-settings endpoint the Android app uses.
const androidAppSettingsURL = "https://clientsettingscdn.roblox.com/v2/settings/application/AndroidApp"

const desktopAppPolicyFlag = "FStringAppConfigurationOverrideAppPolicy"

// applicationSettingsFromResponse extracts the flag map as a JSON object.
// Does not log flag names or values.
func applicationSettingsFromResponse(body []byte) (string, int, error) {
	return applicationSettingsFromResponseWithOverrides(body, nil)
}

func applicationSettingsFromResponseWithOverrides(body []byte, overrides map[string]any) (string, int, error) {
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
	// Explicit renderer choices own this narrow conflict set. Auto emits
	// PreferVulkan only when the Vulkan WSI path is the resolved platform.
	if _, ok := overrides["FFlagDebugGraphicsPreferOpenGL"]; ok {
		delete(m, "FFlagDebugGraphicsPreferVulkan")
		delete(m, "FFlagDebugGraphicsDisableVulkan")
	}
	if _, ok := overrides["FFlagDebugGraphicsPreferVulkan"]; ok {
		delete(m, "FFlagDebugGraphicsPreferOpenGL")
		delete(m, "FFlagDebugGraphicsDisableVulkan")
	}
	for k, v := range overrides {
		m[k] = v
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
	overrides, overrideErr := clientsettings.New().LoadOverrides(context.Background())
	if overrideErr != nil {
		// A settings-permission/symlink failure must not prevent use of the
		// official client settings; the rejected override is simply not applied.
		overrides = nil
	}
	// The named AppConfiguration override is a terminal whole-policy response,
	// not a field merge. The OS-specific loader therefore returns a complete
	// cached policy or nothing; any malformed, ambiguous, or touch-mode input
	// fails closed and leaves Roblox's ordinary policy path intact.
	var policyApplied bool
	overrides, policyApplied, overrideErr = withDesktopAppPolicyOverride(cachePath, overrides)
	if overrideErr != nil {
		logging.Logger(logging.CatGameActivity).Info("desktop app policy override omitted", "err", overrideErr)
	} else if policyApplied {
		logging.Logger(logging.CatGameActivity).Info("desktop app policy override applied", "presentation_fields", 4)
	}
	body, err := fetchAndroidAppSettings()
	if err != nil {
		if cachePath != "" {
			if cached, rerr := os.ReadFile(cachePath); rerr == nil && len(cached) > 2 {
				return applicationSettingsFromResponseWithOverrides(cached, overrides)
			}
		}
		return "", 0, err
	}
	js, n, err := applicationSettingsFromResponseWithOverrides(body, overrides)
	if err != nil {
		return "", 0, err
	}
	if cachePath != "" && n > 0 {
		_ = os.WriteFile(cachePath, body, 0o600)
	}
	return js, n, nil
}

func withDesktopAppPolicyOverride(cachePath string, overrides map[string]any) (map[string]any, bool, error) {
	policy, err := desktopAppPolicyOverride(filepath.Dir(cachePath))
	if err != nil {
		return overrides, false, err
	}
	if policy == "" {
		return overrides, false, nil
	}
	overrides = cloneFlagMap(overrides)
	overrides[desktopAppPolicyFlag] = policy
	return overrides, true, nil
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
