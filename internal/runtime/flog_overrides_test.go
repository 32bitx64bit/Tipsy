// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package runtime

import (
	"encoding/json"
	"testing"
)

func TestFlogOverridesParsesGroupsAndLevels(t *testing.T) {
	got := flogOverrides(" VoiceChatLogs=Verbose,7 ; SoundTrace=12 ;;")
	wantValues := map[string]string{
		"FLogVoiceChatLogs":  "Verbose,7",
		"DFLogVoiceChatLogs": "Verbose,7",
		"FLogSoundTrace":     "12",
		"DFLogSoundTrace":    "12",
	}
	for k, v := range wantValues {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	// Raising a group also deletes its shipped place / datacenter filters.
	for _, k := range []string{
		"FLogVoiceChatLogs_PlaceFilter", "DFLogVoiceChatLogs_PlaceFilter",
		"FLogVoiceChatLogs_DataCenterFilter", "DFLogSoundTrace_DataCenterFilter",
	} {
		if v, present := got[k]; !present || v != nil {
			t.Errorf("%s = %v (present %v), want nil deletion marker", k, v, present)
		}
	}
	if n := flogGroupCount(got); n != 2 {
		t.Fatalf("group count = %d, want 2 (%v)", n, got)
	}
}

func TestFlogOverridesRejectsAnythingButLogLevels(t *testing.T) {
	for _, spec := range []string{
		"",
		"VoiceChatLogs",           // no level
		"VoiceChatLogs=",          // empty level
		"VoiceChatLogs=seven",     // unknown level name
		"VoiceChatLogs=1000",      // too long
		"Voice-Chat=7",            // punctuation in group
		"7Voice=7",                // leading digit
		"VoiceChatLogs=Info;42",   // `42` alone is not an assignment
		"VoiceChatLogs=7,Verbose", // number first is not the official form
		"VoiceChatLogs=Verbose,",  // dangling comma
	} {
		// The `;42` case also yields the valid `VoiceChatLogs=Info` item.
		if spec == "VoiceChatLogs=Info;42" {
			if got := flogOverrides(spec); flogGroupCount(got) != 1 || got["FLogVoiceChatLogs"] != "Info" {
				t.Errorf("spec %q produced %v", spec, got)
			}
			continue
		}
		if got := flogOverrides(spec); len(got) != 0 {
			t.Errorf("spec %q produced %v, want nothing", spec, got)
		}
	}
	// A flag name is accepted only as a *log group* name: it can never
	// produce anything but FLog/DFLog keys.
	for k := range flogOverrides("FFlagDebugNeverAllowVoiceChat=1") {
		if k != "FLogFFlagDebugNeverAllowVoiceChat" && k != "DFLogFFlagDebugNeverAllowVoiceChat" &&
			k != "FLogFFlagDebugNeverAllowVoiceChat_PlaceFilter" && k != "DFLogFFlagDebugNeverAllowVoiceChat_PlaceFilter" &&
			k != "FLogFFlagDebugNeverAllowVoiceChat_DataCenterFilter" && k != "DFLogFFlagDebugNeverAllowVoiceChat_DataCenterFilter" {
			t.Errorf("unexpected key %s", k)
		}
	}
}

func TestLuaLogOverrides(t *testing.T) {
	got := luaLogOverrides("Debug:Voice")
	if got[luaLogLevelFlag] != "debug" || got[luaLogPatternFlag] != "Voice" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got := luaLogOverrides("trace"); got[luaLogLevelFlag] != "trace" || len(got) != 1 {
		t.Fatalf("level-only got %v", got)
	}
	for _, bad := range []string{"", "verbose", "debug:has space", "debug:" + string(make([]byte, 65)), "7"} {
		if got := luaLogOverrides(bad); got != nil {
			t.Errorf("%q accepted: %v", bad, got)
		}
	}
}

func TestWithFlogOverridesMergesWithoutMutatingInput(t *testing.T) {
	base := map[string]any{"Existing": "kept"}
	merged, groups, lua := withFlogOverrides(base, "VoiceChatLogs=Verbose,7", "debug:Voice")
	if groups != 1 || !lua {
		t.Fatalf("groups = %d lua = %v, want 1 true", groups, lua)
	}
	if _, leaked := base["FLogVoiceChatLogs"]; leaked {
		t.Fatal("input map was mutated")
	}
	if merged["Existing"] != "kept" || merged["FLogVoiceChatLogs"] != "Verbose,7" || merged["DFLogVoiceChatLogs"] != "Verbose,7" ||
		merged[luaLogLevelFlag] != "debug" || merged[luaLogPatternFlag] != "Voice" {
		t.Fatalf("merged = %v", merged)
	}
	same, groups, lua := withFlogOverrides(base, "", "")
	if groups != 0 || lua || len(same) != 1 {
		t.Fatalf("empty spec changed overrides: %v (%d %v)", same, groups, lua)
	}

	// The shipped settings carry the group at 0 plus a place filter; the
	// override must win and the filter must be gone.
	shipped := `{"applicationSettings":{"Official":"kept","FLogVoiceChatLogs":"0","FLogVoiceChatLogs_PlaceFilter":"Verbose;11116930529"}}`
	raw, _, err := applicationSettingsFromResponseWithOverrides([]byte(shipped), merged)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ApplicationSettings map[string]any `json:"applicationSettings"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	s := out.ApplicationSettings
	if s["Official"] != "kept" || s["FLogVoiceChatLogs"] != "Verbose,7" {
		t.Fatalf("settings = %v", s)
	}
	if _, still := s["FLogVoiceChatLogs_PlaceFilter"]; still {
		t.Fatalf("place filter survived: %v", s)
	}
	if _, invented := s["DFLogVoiceChatLogs_PlaceFilter"]; invented {
		t.Fatalf("deletion marker leaked into output: %v", s)
	}
}
