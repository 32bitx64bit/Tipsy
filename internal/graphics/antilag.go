// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package graphics

import (
	"os"
	"strings"
)

// EnvAntiLag enables VK_AMD_anti_lag injection in the Vulkan adapter. It is
// honoured only after ProbeAntiLagSupport proved RADV on this machine.
const EnvAntiLag = "TIPSY_ANTILAG"

// AntiLagSupport is the result of the isolated VK_AMD_anti_lag probe.
//
// Available is true only for VK_DRIVER_ID_MESA_RADV on vendor 0x1002 with
// VK_AMD_anti_lag advertised on that same physical device. NeedsLayerEnv is
// true when the driver exposes the extension only with
// ENABLE_LAYER_MESA_ANTI_LAG=1 (Mesa 25.3 ships the layer disabled by default),
// so the adapter must not claim anti-lag for a child without that switch.
type AntiLagSupport struct {
	Available     bool
	NeedsLayerEnv bool
	Driver        string
	Reason        string
}

// Default reports whether the settings row should start enabled. Anti-lag is
// opt-in: the probe only decides whether the row is usable, never whether the
// switch starts on.
func (s AntiLagSupport) Default() bool {
	return false
}

// ToggleLabel is the settings-row text for this result.
func (s AntiLagSupport) ToggleLabel() string {
	if s.Available {
		return "AMD anti-lag (VK_AMD_anti_lag)"
	}
	if s.Reason != "" {
		return "AMD anti-lag unavailable: " + s.Reason
	}
	return "AMD anti-lag unavailable"
}

// EnvAntiLagLayer is the Mesa switch that exposes VK_AMD_anti_lag through
// VkLayer_MESA_anti_lag. The runtime sets it before the client starts when the
// probe reports NeedsLayerEnv and the user set neither Mesa variable.
const EnvAntiLagLayer = "ENABLE_LAYER_MESA_ANTI_LAG"

// AntiLagEnabled reports whether the adapter may issue VK_AMD_anti_lag calls:
// the env switch must be set and the probe must have proved support. A device
// that exposes the extension only through VkLayer_MESA_anti_lag is usable once
// that layer is enabled in the environment the client inherits.
func AntiLagEnabled(support AntiLagSupport, envEnabled bool) bool {
	return AntiLagEnabledWithLayer(support, envEnabled, os.Getenv(EnvAntiLagLayer))
}

// AntiLagEnabledWithLayer is AntiLagEnabled for a caller that knows the layer
// switch the client will run with (the settings view, or a test).
func AntiLagEnabledWithLayer(support AntiLagSupport, envEnabled bool, layerEnv string) bool {
	if !envEnabled || !support.Available {
		return false
	}
	if support.NeedsLayerEnv && !antiLagLayerEnabled(layerEnv) {
		return false
	}
	return true
}

// antiLagLayerEnabled reads the layer switch with Mesa's own truthiness: any
// value except empty, "0" or "false" enables an implicit layer.
func antiLagLayerEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false":
		return false
	}
	return true
}

// envSwitchEnabled reports whether an environment switch holds a truthy value.
func envSwitchEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// AntiLagRequested reports whether TIPSY_ANTILAG=1 was requested. It does not
// imply support; AntiLagEnabled combines it with the probe result.
func AntiLagRequested(value string) bool {
	return envSwitchEnabled(value)
}
