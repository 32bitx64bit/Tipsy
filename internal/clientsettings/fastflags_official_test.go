// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package clientsettings

import (
	"strconv"
	"testing"
)

// officialPresetFlags collects every Fast Flag Tipsy itself generates across
// the Settings surface plus the persistent startup defaults.
func officialPresetFlags(t *testing.T) []FastFlag {
	t.Helper()
	seen := make(map[string]struct{})
	var flags []FastFlag
	add := func(name, value string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		flags = append(flags, FastFlag{Name: name, Value: value})
	}
	variants := []Settings{
		Default(),
		{Renderer: RendererOpenGL, FrameRate: FrameRate{Mode: FrameRateAuto}},
		{Renderer: RendererVulkan, FrameRate: FrameRate{Mode: FrameRateAuto}},
		{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: MinFrameRate}},
		{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateLimited, Limit: MaxFrameRate}},
		{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateUnlimited}},
		{Renderer: RendererAuto, FrameRate: FrameRate{Mode: FrameRateAuto}, LowTextureMode: true},
	}
	for _, s := range variants {
		overrides, err := Overrides(s)
		if err != nil {
			// A renderer the host cannot provide is not a preset this
			// machine can generate; the remaining variants still cover it.
			continue
		}
		for name, value := range overrides {
			add(name, value.(string))
		}
	}
	for name, value := range PersistentStartupDefaults() {
		add(name, value)
	}
	if len(flags) == 0 {
		t.Fatal("no Tipsy-generated presets collected")
	}
	return flags
}

func TestValidateOfficialFastFlagsAcceptsEveryTipsyPreset(t *testing.T) {
	flags := officialPresetFlags(t)
	if err := ValidateOfficialFastFlags(flags); err != nil {
		t.Fatalf("Tipsy-generated presets rejected by the official allowlist: %v", err)
	}
}

func TestValidateOfficialFastFlagsAcceptsAllowlistBoundaries(t *testing.T) {
	var flags []FastFlag
	for name, rule := range officialFastFlagAllowlist {
		if !rule.integer {
			flags = append(flags, FastFlag{Name: name, Value: "True"})
			continue
		}
		flags = append(flags,
			FastFlag{Name: name, Value: strconv.FormatInt(rule.min, 10)},
		)
		// One entry per name: keep the max bound in a second pass below.
	}
	if err := ValidateOfficialFastFlags(flags); err != nil {
		t.Fatalf("allowlist minimum bounds rejected: %v", err)
	}
	flags = flags[:0]
	for name, rule := range officialFastFlagAllowlist {
		if rule.integer {
			flags = append(flags, FastFlag{Name: name, Value: strconv.FormatInt(rule.max, 10)})
		}
	}
	if err := ValidateOfficialFastFlags(flags); err != nil {
		t.Fatalf("allowlist maximum bounds rejected: %v", err)
	}
}

func TestValidateOfficialFastFlagsRejectsUnallowlistedAndStringFlags(t *testing.T) {
	for _, flags := range [][]FastFlag{
		{{Name: "FFlagExample", Value: "True"}},
		{{Name: "FFlagDebugGraphicsPreferD3D11", Value: "True"}},
		{{Name: "DFFlagSomeEngineGate", Value: "False"}},
		{{Name: "FIntExample", Value: "1"}},
		{{Name: "DFIntTaskSchedulerTargetFps999", Value: "60"}},
		// String flags are never part of the official custom surface; the
		// runtime-only Lua log strings are merged by internal/runtime.
		{{Name: "FStringExample", Value: "a printable value"}},
		{{Name: "DFStringExample", Value: "another value"}},
		{{Name: "FStringDebugLuaLogLevel", Value: "debug"}},
		{{Name: "FStringDebugLuaLogPattern", Value: "Voice"}},
		// An allowlisted flag may not smuggle an unlisted sibling.
		{{Name: flagPreferVulkan, Value: "True"}, {Name: "FFlagIndependentCustom", Value: "True"}},
	} {
		if err := ValidateOfficialFastFlags(flags); err == nil {
			t.Fatalf("official policy accepted %+v", flags)
		}
	}
}

func TestValidateOfficialFastFlagsRejectsOutOfBoundsIntegers(t *testing.T) {
	for _, flags := range [][]FastFlag{
		{{Name: intTaskSchedulerTargetFPS, Value: "241"}},
		{{Name: intTaskSchedulerTargetFPS, Value: "-2"}},
		{{Name: intTaskSchedulerTargetFPS, Value: "9999"}},
		{{Name: intTextureQualityOverride, Value: "0"}},
		{{Name: intTextureQualityOverride, Value: "4"}},
		{{Name: intRenderTextureTotalBudgetMB, Value: "8"}},
		{{Name: intRenderTextureTotalBudgetMB, Value: "8192"}},
		{{Name: intRenderTextureMipBias, Value: "-3"}},
		{{Name: intRenderTextureMipBias, Value: "5"}},
		{{Name: intDebugTc1MaxAllowedMemoryBudget, Value: "1024"}},
		{{Name: intDebugTc1MaxAllowedMemoryBudget, Value: "2147483647"}},
		{{Name: intRenderForceVideoMemorySize, Value: "1024"}},
		{{Name: IntEnableRenderPipelineHundredthsPercent, Value: "10001"}},
		{{Name: IntEnableRenderPipelineHundredthsPercent, Value: "-1"}},
	} {
		if err := ValidateOfficialFastFlags(flags); err == nil {
			t.Fatalf("official policy accepted out-of-bounds %+v", flags)
		}
	}
}

func TestValidateOfficialFastFlagsKeepsSyntaxRules(t *testing.T) {
	for _, flags := range [][]FastFlag{
		{{Name: "FFlag", Value: "True"}},
		{{Name: flagPreferVulkan, Value: "true"}},
		{{Name: intTaskSchedulerTargetFPS, Value: "2147483648"}},
		{{Name: flagPreferVulkan, Value: "True"}, {Name: flagPreferVulkan, Value: "False"}},
	} {
		if err := ValidateOfficialFastFlags(flags); err == nil {
			t.Fatalf("official policy accepted invalid syntax %+v", flags)
		}
	}
}

// TestValidateFastFlagsOfficialGate pins the build-mode contract: the curated
// policy is enforced by ValidateFastFlags only under -tags official_release,
// while development builds keep the full typed surface.
func TestValidateFastFlagsOfficialGate(t *testing.T) {
	presets := officialPresetFlags(t)
	if err := ValidateFastFlags(presets); err != nil {
		t.Fatalf("ValidateFastFlags rejected Tipsy presets: %v", err)
	}
	unlisted := []FastFlag{{Name: "FFlagIndependentCustom", Value: "True"}}
	err := ValidateFastFlags(unlisted)
	if officialFastFlagsEnforced {
		if err == nil {
			t.Fatal("official_release ValidateFastFlags accepted an unallowlisted flag")
		}
		if err := ValidateFastFlags([]FastFlag{{Name: intTaskSchedulerTargetFPS, Value: "9999"}}); err == nil {
			t.Fatal("official_release ValidateFastFlags accepted an out-of-bounds value")
		}
		if err := ValidateFastFlags([]FastFlag{{Name: "FStringExample", Value: "value"}}); err == nil {
			t.Fatal("official_release ValidateFastFlags accepted an FString flag")
		}
	} else if err != nil {
		t.Fatalf("development ValidateFastFlags rejected a well-formed flag: %v", err)
	}
}
