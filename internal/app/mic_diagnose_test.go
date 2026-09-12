// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnoseHelpDocumentsMicrophoneEnv(t *testing.T) {
	code, out, errOut := runArgs(t, "diagnose", "--help")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	for _, want := range []string{
		"TIPSY_MICROPHONE",
		"TIPSY_DISABLE_MICROPHONE",
		"TIPSY_MICROPHONE_SOURCE",
		"deprecated alias",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("diagnose help missing %q:\n%s", want, out)
		}
	}
}

func TestRunDiagnoseAudioJSON(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TIPSY_MICROPHONE", "")
	t.Setenv("TIPSY_DISABLE_MICROPHONE", "")
	code, out, errOut := runArgs(t, "diagnose", "--json", "audio")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatalf("invalid JSON:\n%s", out)
	}
	if !strings.Contains(out, `"audio"`) {
		t.Fatalf("JSON omits audio subsystem:\n%s", out)
	}
	if strings.Contains(out, "does not verify microphone capture") {
		t.Fatalf("stale capture-unverified disclaimer:\n%s", out)
	}
}
