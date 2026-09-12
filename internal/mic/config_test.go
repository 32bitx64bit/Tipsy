// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package mic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultMicrophoneConfig(t *testing.T) {
	c := DefaultMicrophoneConfig()
	if !c.Enabled {
		t.Fatal("default microphone config must be enabled (allowed, not always-open)")
	}
	if c.Source != "" {
		t.Fatalf("default source must be empty (host default), got %q", c.Source)
	}
	if !c.Allowed() {
		t.Fatal("default config must Allow capture")
	}
	if c.SourcePinned() {
		t.Fatal("default config must not report a pinned source")
	}
}

func TestParseMicSectionMissingIsDefaults(t *testing.T) {
	for _, body := range []string{"", "  \n ", "{}", `{"other":1}`, `{"microphone":null}`} {
		c, err := ParseMicSection([]byte(body))
		if err != nil {
			t.Fatalf("body %q: want defaults without error, got %v", body, err)
		}
		if want := DefaultMicrophoneConfig(); c != want {
			t.Fatalf("body %q: want defaults %+v, got %+v", body, want, c)
		}
	}
}

func TestParseMicSectionPartialMerge(t *testing.T) {
	c, err := ParseMicSection([]byte(`{"microphone":{"source":"alsa_input.pci"}}`))
	if err != nil {
		t.Fatalf("partial section: %v", err)
	}
	if c.Source != "alsa_input.pci" {
		t.Fatalf("partial section must overlay the stated source, got %+v", c)
	}
	if !c.Enabled {
		t.Fatalf("absent switch must stay default-on, got %+v", c)
	}
}

func TestParseMicSectionIgnoresUnknownKeys(t *testing.T) {
	c, err := ParseMicSection([]byte(`{"microphone":{"enabled":false,"source":"default","gain":0.9,"agc":true}}`))
	if err != nil {
		t.Fatalf("unknown keys must still load: %v", err)
	}
	if c.Enabled || c.Source != "default" {
		t.Fatalf("kept keys must load, got %+v", c)
	}
}

func TestParseMicSectionInvalidIsHonestError(t *testing.T) {
	for _, body := range []string{
		`{not json`,
		`{"microphone":{"enabled":"yes"}}`,
		`{"microphone":"yes"}`,
		`{"microphone":[]}`,
		`{"microphone":1}`,
	} {
		if _, err := ParseMicSection([]byte(body)); err == nil {
			t.Fatalf("body %q must be an honest error, never silent defaults", body)
		}
	}
}

func TestLoadMicrophoneConfigFileMissingIsDefaults(t *testing.T) {
	c, err := LoadMicrophoneConfigFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file must not error, got %v", err)
	}
	if c != DefaultMicrophoneConfig() {
		t.Fatalf("missing file must yield defaults, got %+v", c)
	}
}

func TestLoadMicrophoneConfigFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := `{"microphone":{"enabled":false,"source":"alsa_input.usb"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadMicrophoneConfigFile(path)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if c.Enabled || c.Source != "alsa_input.usb" {
		t.Fatalf("round trip wrong: %+v", c)
	}
}

func TestParseMicrophoneEnv(t *testing.T) {
	for _, tc := range []struct {
		in      string
		enabled bool
		set     bool
	}{
		{"0", false, true},
		{"off", false, true},
		{"FALSE", false, true},
		{" no ", false, true},
		{"1", true, true},
		{"on", true, true},
		{"TRUE", true, true},
		{"yes", true, true},
		{"", false, false},
		{"   ", false, false},
		{"maybe", false, false},
	} {
		got, set := ParseMicrophoneEnv(tc.in)
		if got != tc.enabled || set != tc.set {
			t.Fatalf("ParseMicrophoneEnv(%q) = (%v,%t), want (%v,%t)", tc.in, got, set, tc.enabled, tc.set)
		}
	}
}

func TestParseDisableMicrophoneEnv(t *testing.T) {
	for _, tc := range []struct {
		in       string
		disabled bool
		set      bool
	}{
		{"1", true, true},
		{"true", true, true},
		{"YES", true, true},
		{"0", false, false},
		{"false", false, false},
		{"no", false, false},
		{"", false, false},
		{"off", false, false},
	} {
		got, set := ParseDisableMicrophoneEnv(tc.in)
		if got != tc.disabled || set != tc.set {
			t.Fatalf("ParseDisableMicrophoneEnv(%q) = (%v,%t), want (%v,%t)", tc.in, got, set, tc.disabled, tc.set)
		}
	}
}

func envLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestWithEnvOverlay(t *testing.T) {
	base := DefaultMicrophoneConfig()
	off := base.WithEnv(envLookup(map[string]string{"TIPSY_MICROPHONE": "0"}))
	if off.Allowed() {
		t.Fatal("TIPSY_MICROPHONE=0 must close the door")
	}
	on := MicrophoneConfig{Enabled: false}.WithEnv(envLookup(map[string]string{"TIPSY_MICROPHONE": "1"}))
	if !on.Allowed() {
		t.Fatal("TIPSY_MICROPHONE=1 must force the door on over file-off")
	}
	alias := base.WithEnv(envLookup(map[string]string{"TIPSY_DISABLE_MICROPHONE": "true"}))
	if alias.Allowed() {
		t.Fatal("TIPSY_DISABLE_MICROPHONE=true must close the door")
	}
	// Newer name wins when both are valid.
	both := base.WithEnv(envLookup(map[string]string{
		"TIPSY_DISABLE_MICROPHONE": "1",
		"TIPSY_MICROPHONE":         "1",
	}))
	if !both.Allowed() {
		t.Fatal("TIPSY_MICROPHONE=1 must win over the DISABLE alias")
	}
	// Invalid env never clobbers the file/default value.
	again := off.WithEnv(envLookup(map[string]string{"TIPSY_MICROPHONE": "maybe"}))
	if again.Allowed() {
		t.Fatalf("invalid env must be ignored, got %+v", again)
	}
	pinned := base.WithEnv(envLookup(map[string]string{"TIPSY_MICROPHONE_SOURCE": "  some.source  "}))
	if pinned.Source != "some.source" || !pinned.SourcePinned() {
		t.Fatalf("source pin must trim and set, got %+v", pinned)
	}
	if same := base.WithEnv(nil); same != base {
		t.Fatalf("nil lookup must be a no-op, got %+v", same)
	}
}

func TestEnabledControl(t *testing.T) {
	file := []byte(`{"microphone":{"enabled":false}}`)
	if got := EnabledControl(file, nil); got != MicrophoneControlFile {
		t.Fatalf("file section control = %q", got)
	}
	if got := EnabledControl(nil, nil); got != MicrophoneControlDefault {
		t.Fatalf("missing JSON control = %q", got)
	}
	if got := EnabledControl(file, envLookup(map[string]string{"TIPSY_MICROPHONE": "0"})); got != MicrophoneControlEnv {
		t.Fatalf("MICROPHONE env control = %q", got)
	}
	if got := EnabledControl(nil, envLookup(map[string]string{"TIPSY_DISABLE_MICROPHONE": "1"})); got != MicrophoneControlDisable {
		t.Fatalf("DISABLE alias control = %q", got)
	}
	if got := EnabledControl([]byte(`{not json`), nil); got != MicrophoneControlDefault {
		t.Fatalf("corrupt file must not count as config file, got %q", got)
	}
}

func TestEffectiveConfigPrecedence(t *testing.T) {
	file := []byte(`{"microphone":{"enabled":true,"source":"from-file","gain":0.5}}`)
	got, err := EffectiveConfig(file, envLookup(map[string]string{"TIPSY_MICROPHONE": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Allowed() {
		t.Fatalf("env must win over file: %+v", got)
	}
	if got.Source != "from-file" {
		t.Fatalf("source must stay from file when SOURCE env unset: %+v", got)
	}
	pinned, err := EffectiveConfig(file, envLookup(map[string]string{"TIPSY_MICROPHONE_SOURCE": "from-env"}))
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Source != "from-env" {
		t.Fatalf("SOURCE env must win over file pin: %+v", pinned)
	}
	again, err := EffectiveConfig(file, envLookup(map[string]string{"TIPSY_MICROPHONE": "maybe"}))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Allowed() {
		t.Fatalf("invalid env must be ignored: %+v", again)
	}
	if _, err := EffectiveConfig([]byte(`{"microphone":{"enabled":"huge"}}`), envLookup(nil)); err == nil {
		t.Fatal("corrupt section must error honestly")
	}
}

func TestUpsertMicSectionRoundTrip(t *testing.T) {
	cfg := DefaultMicrophoneConfig()
	cfg.SetEnabled(false)
	cfg.SetSource("alsa_input.pci")
	out, err := UpsertMicSection(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseMicSection(out)
	if err != nil {
		t.Fatalf("upserted body must parse: %v\n%s", err, out)
	}
	if back != cfg {
		t.Fatalf("round trip = %+v, want %+v", back, cfg)
	}
}

func TestUpsertMicSectionPreservesOtherKeys(t *testing.T) {
	existing := []byte(`{"dataDir":"/opt/tipsy-data","logLevel":"debug","gamepad":{"deadzone":0.2}}`)
	cfg := DefaultMicrophoneConfig()
	cfg.SetEnabled(false)
	out, err := UpsertMicSection(existing, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if string(top["dataDir"]) != `"/opt/tipsy-data"` || string(top["logLevel"]) != `"debug"` {
		t.Fatalf("unrelated keys lost: %s", out)
	}
	if _, ok := top["gamepad"]; !ok {
		t.Fatalf("gamepad section lost: %s", out)
	}
	back, err := ParseMicSection(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Enabled {
		t.Fatalf("section lost: %+v", back)
	}
}

func TestUpsertMicSectionRejectsCorruptFile(t *testing.T) {
	if out, err := UpsertMicSection([]byte("{not json"), DefaultMicrophoneConfig()); err == nil || out != nil {
		t.Fatalf("corrupt file must fail honestly, got %s err=%v", out, err)
	}
}
