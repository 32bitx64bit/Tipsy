// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/android"
	"github.com/tipsy-linux/tipsy/internal/clientsettings"
	"github.com/tipsy-linux/tipsy/internal/graphics"
)

type experimentalTestEnv struct {
	values map[string]string
	start  map[string]string
}

func newExperimentalTestEnv(values ...string) *experimentalTestEnv {
	env := &experimentalTestEnv{values: map[string]string{}, start: map[string]string{}}
	for i := 0; i+1 < len(values); i += 2 {
		env.values[values[i]] = values[i+1]
		env.start[values[i]] = values[i+1]
	}
	return env
}

func (e *experimentalTestEnv) lookup(name string) (string, bool) {
	value, ok := e.values[name]
	return value, ok
}

func (e *experimentalTestEnv) set(name, value string) error {
	e.values[name] = value
	return nil
}

func (e *experimentalTestEnv) want(t *testing.T, name, value string) {
	t.Helper()
	if got := e.values[name]; got != value {
		t.Fatalf("%s=%q want %q (env=%v)", name, got, value, e.values)
	}
}

func (e *experimentalTestEnv) absent(t *testing.T, name string) {
	t.Helper()
	if value, ok := e.values[name]; ok {
		t.Fatalf("%s=%q unexpectedly set", name, value)
	}
}

func runExperimentalPrefs(t *testing.T, settings clientsettings.Settings, env *experimentalTestEnv, support graphics.AntiLagSupport) int {
	t.Helper()
	probes := 0
	if err := applyExperimentalPrefs(settings, env.lookup, env.set, func() graphics.AntiLagSupport {
		probes++
		return support
	}, env.start); err != nil {
		t.Fatalf("apply experimental prefs: %v", err)
	}
	return probes
}

func TestExperimentalPrefsAntiLagOffByDefault(t *testing.T) {
	env := newExperimentalTestEnv()
	probes := runExperimentalPrefs(t, clientsettings.Default(), env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	if probes != 0 {
		t.Fatalf("probe calls=%d want 0: anti-lag is opt-in", probes)
	}
	env.want(t, graphics.EnvAntiLag, "0")
	env.absent(t, mesaAntiLagLayerEnable)
}

func TestExperimentalPrefsAntiLagOptInEnablesWhenAvailable(t *testing.T) {
	env := newExperimentalTestEnv()
	settings := clientsettings.Default()
	settings.AntiLag = true
	probes := runExperimentalPrefs(t, settings, env, graphics.AntiLagSupport{Available: true, Driver: "RADV NAVI32"})
	if probes != 1 {
		t.Fatalf("probe calls=%d want 1 for an opt-in", probes)
	}
	env.want(t, graphics.EnvAntiLag, "1")
	env.absent(t, mesaAntiLagLayerEnable)
}

func TestExperimentalPrefsAntiLagEnablesMesaLayerOnlyWhenNeeded(t *testing.T) {
	env := newExperimentalTestEnv()
	settings := clientsettings.Default()
	settings.AntiLag = true
	probes := runExperimentalPrefs(t, settings, env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	if probes != 1 {
		t.Fatalf("probe calls=%d want 1 for an opt-in", probes)
	}
	env.want(t, graphics.EnvAntiLag, "1")
	env.want(t, mesaAntiLagLayerEnable, "1")
}

func TestExperimentalPrefsAntiLagOptInUnavailableStaysOff(t *testing.T) {
	env := newExperimentalTestEnv()
	settings := clientsettings.Default()
	settings.AntiLag = true
	probes := runExperimentalPrefs(t, settings, env, graphics.AntiLagSupport{Reason: "driver is not VK_DRIVER_ID_MESA_RADV"})
	if probes != 1 {
		t.Fatalf("probe calls=%d want 1 for an opt-in", probes)
	}
	env.want(t, graphics.EnvAntiLag, "0")
	env.absent(t, mesaAntiLagLayerEnable)
}

func TestExperimentalPrefsEnvOptInProbesAndEnablesLayer(t *testing.T) {
	env := newExperimentalTestEnv(graphics.EnvAntiLag, "1")
	probes := runExperimentalPrefs(t, clientsettings.Default(), env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	if probes != 1 {
		t.Fatalf("probe calls=%d want 1 when the user opted in through the environment", probes)
	}
	// The environment value is authoritative and is never rewritten.
	env.want(t, graphics.EnvAntiLag, "1")
	env.want(t, mesaAntiLagLayerEnable, "1")
}

func TestExperimentalPrefsEnvOffWinsOverStoredOptIn(t *testing.T) {
	env := newExperimentalTestEnv(graphics.EnvAntiLag, "0")
	settings := clientsettings.Default()
	settings.AntiLag = true
	probes := runExperimentalPrefs(t, settings, env, graphics.AntiLagSupport{Available: true})
	if probes != 0 {
		t.Fatalf("probe calls=%d want 0 when the user forced anti-lag off", probes)
	}
	env.want(t, graphics.EnvAntiLag, "0")
	env.absent(t, mesaAntiLagLayerEnable)
}

func TestExperimentalPrefsEnvAntiLagNeverOverridesAvailability(t *testing.T) {
	env := newExperimentalTestEnv(graphics.EnvAntiLag, "1")
	runExperimentalPrefs(t, clientsettings.Default(), env, graphics.AntiLagSupport{Reason: "driver is not VK_DRIVER_ID_MESA_RADV"})
	// The user's switch is preserved, but no Mesa layer is armed for a
	// driver the probe rejected; the Vulkan adapter still gates on support.
	env.want(t, graphics.EnvAntiLag, "1")
	env.absent(t, mesaAntiLagLayerEnable)
}

func TestExperimentalPrefsNeverOverwritesUserLayerSwitches(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		env   *experimentalTestEnv
		check func(*testing.T, *experimentalTestEnv)
	}{
		{
			name: "enable set by user",
			env:  newExperimentalTestEnv(mesaAntiLagLayerEnable, "0"),
			check: func(t *testing.T, env *experimentalTestEnv) {
				env.want(t, mesaAntiLagLayerEnable, "0")
			},
		},
		{
			name: "disable set by user",
			env:  newExperimentalTestEnv(mesaAntiLagLayerDisable, "1"),
			check: func(t *testing.T, env *experimentalTestEnv) {
				env.absent(t, mesaAntiLagLayerEnable)
				env.want(t, mesaAntiLagLayerDisable, "1")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			settings := clientsettings.Default()
			settings.AntiLag = true
			runExperimentalPrefs(t, settings, testCase.env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
			testCase.env.want(t, graphics.EnvAntiLag, "1")
			testCase.check(t, testCase.env)
			runExperimentalPrefs(t, clientsettings.Default(), testCase.env, graphics.AntiLagSupport{})
			runExperimentalPrefs(t, settings, testCase.env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
			testCase.check(t, testCase.env)
		})
	}
}

func TestConfigureVulkanAntiLagDisarmsOnDisabledLaunch(t *testing.T) {
	t.Setenv(graphics.EnvAntiLag, "1")
	if !android.ArmVulkanAntiLag("radv", true) {
		t.Fatal("failed to arm anti-lag for earlier launch")
	}
	t.Cleanup(func() { android.ArmVulkanAntiLag("", false) })
	t.Setenv(graphics.EnvAntiLag, "0")
	configureVulkanAntiLag()
	if android.VulkanAntiLagArmed() {
		t.Fatal("disabled launch left anti-lag armed")
	}
}

func TestExperimentalPrefsSecondLaunchDisarmsTipsyLayer(t *testing.T) {
	env := newExperimentalTestEnv()
	on := clientsettings.Default()
	on.AntiLag = true
	runExperimentalPrefs(t, on, env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	env.want(t, mesaAntiLagLayerEnable, "1")

	runExperimentalPrefs(t, clientsettings.Default(), env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	env.want(t, graphics.EnvAntiLag, "0")
	env.want(t, mesaAntiLagLayerEnable, "0")

	runExperimentalPrefs(t, on, env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	env.want(t, graphics.EnvAntiLag, "1")
	env.want(t, mesaAntiLagLayerEnable, "1")
}

func TestExperimentalPrefsKeepsUserLayerWhenAntiLagOff(t *testing.T) {
	env := newExperimentalTestEnv(mesaAntiLagLayerEnable, "1")
	runExperimentalPrefs(t, clientsettings.Default(), env, graphics.AntiLagSupport{Available: true, NeedsLayerEnv: true})
	env.want(t, graphics.EnvAntiLag, "0")
	env.want(t, mesaAntiLagLayerEnable, "1")
}

func TestExperimentalPrefsEmptyEnvironmentDoesNotShadowStored(t *testing.T) {
	settings := clientsettings.Default()
	settings.AntiLag = true
	env := newExperimentalTestEnv(graphics.EnvAntiLag, "")
	runExperimentalPrefs(t, settings, env, graphics.AntiLagSupport{Available: true})
	env.want(t, graphics.EnvAntiLag, "1")
}

// processEnv is a fake process environment whose writes behave like os.Setenv,
// so two in-process launches share one environment and one process-start
// baseline.
type processEnv struct {
	lookup func(string) (string, bool)
	set    func(string, string) error
	start  map[string]string
}

func newProcessEnv(userValues ...string) *processEnv {
	values := map[string]string{}
	start := map[string]string{}
	for i := 0; i+1 < len(userValues); i += 2 {
		values[userValues[i]] = userValues[i+1]
		start[userValues[i]] = userValues[i+1]
	}
	return &processEnv{
		lookup: func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		},
		// Mirror the write into the fake process environment so two launches
		// share one env, as os.Setenv would.
		set: func(name, value string) error {
			values[name] = value
			return nil
		},
		start: start,
	}
}

func (p *processEnv) get(name string) string {
	value, _ := p.lookup(name)
	return value
}

func (p *processEnv) apply(t *testing.T, settings clientsettings.Settings) {
	t.Helper()
	if err := applyExperimentalPrefs(settings, p.lookup, p.set, func() graphics.AntiLagSupport {
		return graphics.AntiLagSupport{Available: true}
	}, p.start); err != nil {
		t.Fatalf("apply experimental prefs: %v", err)
	}
}

func TestExperimentalPrefsSecondInProcessLaunchUsesNewStoredValue(t *testing.T) {
	env := newProcessEnv()
	on := clientsettings.Default()
	on.AntiLag = true
	env.apply(t, on)
	if got := env.get(graphics.EnvAntiLag); got != "1" {
		t.Fatalf("%s=%q want 1 after the first launch", graphics.EnvAntiLag, got)
	}
	// The GUI stores a new choice and launches again in the same process.
	off := clientsettings.Default()
	env.apply(t, off)
	if got := env.get(graphics.EnvAntiLag); got != "0" {
		t.Fatalf("%s=%q want 0: a Tipsy-written value must not shadow the new stored one", graphics.EnvAntiLag, got)
	}
	// ...and back on again.
	env.apply(t, on)
	if got := env.get(graphics.EnvAntiLag); got != "1" {
		t.Fatalf("%s=%q want 1 on the third launch", graphics.EnvAntiLag, got)
	}
}

func TestExperimentalPrefsUserEnvironmentStillWinsAfterInProcessLaunch(t *testing.T) {
	env := newProcessEnv(graphics.EnvAntiLag, "on")
	on := clientsettings.Default()
	on.AntiLag = true
	env.apply(t, on)
	off := clientsettings.Default()
	env.apply(t, off)
	if got := env.get(graphics.EnvAntiLag); got != "on" {
		t.Fatalf("%s=%q want the user's value to survive", graphics.EnvAntiLag, got)
	}
	if !experimentalEnvRequested(env.get(graphics.EnvAntiLag)) {
		t.Fatal("the user's on-switch was parsed as off at launch")
	}
}

func TestExperimentalEnvRequestedVocabulary(t *testing.T) {
	for _, value := range []string{"1", "on", "TRUE", " yes "} {
		if !experimentalEnvRequested(value) {
			t.Fatalf("%q was not treated as on", value)
		}
	}
	for _, value := range []string{"", "0", "off", "false", "no", "maybe"} {
		if experimentalEnvRequested(value) {
			t.Fatalf("%q was not treated as off", value)
		}
	}
}
