// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// The pad diagnose section runs against the real host evdev tree, so these
// tests pin CLI wiring only (exit codes, aliases, JSON validity), never pad
// presence: the host may honestly report active, degraded (EACCES), or the
// no-pad empty.

func TestRunDiagnoseGamepadAliases(t *testing.T) {
	t.Setenv("TIPSY_GAMEPAD", "1")
	for _, alias := range []string{"gamepad", "pad", "controller"} {
		code, out, errOut := runArgs(t, "diagnose", alias)
		if code != 0 {
			t.Fatalf("diagnose %s: exit %d stderr=%s", alias, code, errOut)
		}
		if !strings.Contains(out, "Tipsy diagnose gamepad") {
			t.Fatalf("diagnose %s: missing canonical section:\n%s", alias, out)
		}
		if !strings.Contains(out, "TIPSY_GAMEPAD") {
			t.Fatalf("diagnose %s: missing env state:\n%s", alias, out)
		}
		if strings.Contains(out, "not implemented yet") {
			t.Fatalf("diagnose %s: stale unimplemented claim:\n%s", alias, out)
		}
	}
}

func TestRunDiagnoseAllIncludesGamepad(t *testing.T) {
	t.Setenv("TIPSY_GAMEPAD", "1")
	code, out, errOut := runArgs(t, "diagnose")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "Tipsy diagnose gamepad") {
		t.Fatalf("diagnose all omits gamepad section:\n%s", out)
	}
}

func TestRunDiagnoseGamepadJSON(t *testing.T) {
	t.Setenv("TIPSY_GAMEPAD", "1")
	code, out, errOut := runArgs(t, "diagnose", "--json", "pad")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatalf("invalid JSON:\n%s", out)
	}
	if !strings.Contains(out, `"gamepad"`) {
		t.Fatalf("JSON omits gamepad subsystem:\n%s", out)
	}
}
