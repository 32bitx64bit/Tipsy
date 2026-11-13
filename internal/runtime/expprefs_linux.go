// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"errors"
	"os"
	"strings"

	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
)

const (
	// Mesa exports VK_AMD_anti_lag through a layer that Mesa 25.3 disables by
	// default. Tipsy sets the enable switch only for a launch whose probe
	// proved the extension appears without it, and only when the user has not
	// already chosen either layer switch themselves.
	mesaAntiLagLayerEnable  = "ENABLE_LAYER_MESA_ANTI_LAG"
	mesaAntiLagLayerDisable = "DISABLE_LAYER_MESA_ANTI_LAG"
)

// ApplyExperimentalPrefs exports the stored anti-lag preference
// into this process's environment before any Vulkan instance is created and
// before guest startup.
//
// Precedence is environment > stored setting > detection:
//   - A non-empty environment variable the process started with is never
//     overwritten. That also keeps a user-set ENABLE_/DISABLE_LAYER_MESA_ANTI_LAG
//     untouched.
//   - Otherwise the stored boolean is exported as 1/0, including on a later
//     in-process launch where Tipsy wrote the variable itself.
//   - Anti-lag is opt-in. The stored setting defaults off, and only an opt-in
//     (or TIPSY_ANTILAG=1) runs the probe. The probe's availability must still
//     pass before TIPSY_ANTILAG or the Mesa layer switch is exported.
func ApplyExperimentalPrefs(settings clientsettings.Settings) error {
	return applyExperimentalPrefs(settings, os.LookupEnv, setExperimentalEnvValue, graphics.ProbeAntiLagSupport, processEnvironmentBaseline)
}

// processEnvironmentBaseline is the environment this process started with. It
// is captured once at package initialization and never mutated, so a later
// in-process launch can tell the user's original value from one Tipsy wrote.
var processEnvironmentBaseline = captureEnvironment(os.Environ())

func applyExperimentalPrefs(
	settings clientsettings.Settings,
	lookup func(string) (string, bool),
	setenv func(string, string) error,
	probe func() graphics.AntiLagSupport,
	baseline map[string]string,
) error {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if setenv == nil {
		setenv = os.Setenv
	}
	if probe == nil {
		probe = graphics.ProbeAntiLagSupport
	}

	var errs []error
	antiLagRequested := settings.AntiLag
	if value, ok := experimentalEnvValue(lookup, baseline, graphics.EnvAntiLag); ok {
		antiLagRequested = experimentalEnvRequested(value)
	}
	var support graphics.AntiLagSupport
	if antiLagRequested {
		support = probe()
		antiLagRequested = support.Available
	}
	if err := setExperimentalEnv(lookup, baseline, setenv, graphics.EnvAntiLag, antiLagRequested); err != nil {
		errs = append(errs, err)
	}
	if antiLagRequested && support.NeedsLayerEnv {
		value, enableSet := lookup(mesaAntiLagLayerEnable)
		_, userDisable := lookup(mesaAntiLagLayerDisable)
		if (!enableSet || tipsyWroteEnv(baseline, mesaAntiLagLayerEnable, value)) && !userDisable {
			if err := setenv(mesaAntiLagLayerEnable, "1"); err != nil {
				errs = append(errs, err)
			}
		}
	} else if !antiLagRequested {
		// A layer switch Tipsy exported for an earlier in-process launch must
		// not stay armed after anti-lag is turned off or its probe fails.
		if value, ok := lookup(mesaAntiLagLayerEnable); ok && tipsyWroteEnv(baseline, mesaAntiLagLayerEnable, value) {
			if err := setenv(mesaAntiLagLayerEnable, "0"); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// captureEnvironment turns os.Environ into the immutable per-name baseline.
func captureEnvironment(environ []string) map[string]string {
	baseline := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			baseline[name] = value
		}
	}
	return baseline
}

// experimentalEnvValue reports an environment value that should win over the
// stored setting. Empty and whitespace-only values do not shadow the saved choice.
//
// A value that differs from the process-start baseline was written by Tipsy
// for an earlier launch in this process (the GUI's Play button); it must not
// shadow the newly stored settings. A user export made after startup looks the
// same, but users cannot normally write a running process's environment.
func experimentalEnvValue(lookup func(string) (string, bool), baseline map[string]string, name string) (string, bool) {
	value, ok := lookup(name)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if original, ok := baseline[name]; !ok || strings.TrimSpace(original) != value {
		return "", false
	}
	return value, true
}

// tipsyWroteEnv reports whether the current value differs from the
// process-start baseline, i.e. Tipsy exported it for an earlier launch.
func tipsyWroteEnv(baseline map[string]string, name, value string) bool {
	original, ok := baseline[name]
	return !ok || strings.TrimSpace(original) != strings.TrimSpace(value)
}

// setExperimentalEnvValue exports a value Tipsy computed.
func setExperimentalEnvValue(name, value string) error {
	return os.Setenv(name, value)
}

func setExperimentalEnv(lookup func(string) (string, bool), baseline map[string]string, setenv func(string, string) error, name string, enabled bool) error {
	if _, ok := experimentalEnvValue(lookup, baseline, name); ok {
		return nil
	}
	value := "0"
	if enabled {
		value = "1"
	}
	return setenv(name, value)
}

// experimentalEnvRequested parses the anti-lag switch.
func experimentalEnvRequested(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}
