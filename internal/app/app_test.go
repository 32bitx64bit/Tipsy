// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tipsy-linux/tipsy/internal/config"
)

func TestRunNoArgs(t *testing.T) {
	code, _, errOut := runArgs(t)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Fatalf("expected usage, got %s", errOut)
	}
}

func TestRunUnknownCommand(t *testing.T) {
	code, _, errOut := runArgs(t, "not-a-command")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("stderr=%s", errOut)
	}
}

func TestRunHelp(t *testing.T) {
	for _, cmd := range []string{"help", "-h", "--help"} {
		code, out, _ := runArgs(t, cmd)
		if code != 0 {
			t.Fatalf("%s: exit %d", cmd, code)
		}
		if !strings.Contains(out, "Usage:") {
			t.Fatalf("%s: missing usage: %s", cmd, out)
		}
	}
}

func TestRunVersion(t *testing.T) {
	code, out, _ := runArgs(t, "version")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(out) != "0.0.0-dev" {
		t.Fatalf("version = %q", out)
	}
}

func TestRunLaunchNeedsSetup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	code, _, errOut := runArgs(t, "launch", "--probe")
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "runtime not set up") {
		t.Fatalf("stderr=%s", errOut)
	}
}

func TestRunRepairNotImplemented(t *testing.T) {
	code, _, errOut := runArgs(t, "repair")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "not implemented") {
		t.Fatalf("stderr=%s", errOut)
	}
}

func TestRunSetupRequiresPath(t *testing.T) {
	code, _, errOut := runArgs(t, "setup")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
	if !strings.Contains(errOut, "pass official APK/dir") {
		t.Fatalf("stderr=%s", errOut)
	}
}

func TestRunSetupRejectsUnverifiedPackage(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache-home"))
	src := t.TempDir()
	apkPath := filepath.Join(src, "base.apk")
	writeCLITestAPK(t, apkPath, map[string][]byte{
		"lib/x86_64/libdummy.so": []byte("cli-setup-lib"),
		"assets/a.txt":           []byte("asset"),
	})

	code, out, errOut := runArgs(t, "setup", apkPath)
	if code != 1 {
		t.Fatalf("exit %d stdout=%s stderr=%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "package inspection failed") && !strings.Contains(errOut, "signature") {
		t.Fatalf("expected actionable verification failure: %s", errOut)
	}
	if _, err := os.Stat(filepath.Join(xdg, "tipsy", "runtime")); !os.IsNotExist(err) {
		t.Fatalf("unverified package created runtime: %v", err)
	}
}

func TestRunLogs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	code, out, _ := runArgs(t, "logs")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "TIPSY_LOG") {
		t.Fatalf("missing TIPSY_LOG help: %s", out)
	}
	if !strings.Contains(out, filepath.Join(tmp, "tipsy")) {
		t.Fatalf("missing log dir: %s", out)
	}
}

func TestRunDoctor(t *testing.T) {
	code, out, errOut := runArgs(t, "doctor")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "Tipsy Doctor") {
		t.Fatalf("output=%s", out)
	}
}

func TestRunDoctorJSON(t *testing.T) {
	code, out, errOut := runArgs(t, "doctor", "--json")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	if !json.Valid([]byte(strings.TrimSpace(out))) {
		t.Fatalf("invalid JSON: %s", out)
	}
}

func TestRunDiagnoseX11(t *testing.T) {
	code, out, errOut := runArgs(t, "diagnose", "x11")
	if code != 0 {
		t.Fatalf("exit %d stderr=%s", code, errOut)
	}
	if !strings.Contains(out, "not implemented yet") {
		t.Fatalf("output=%s", out)
	}
}

func TestRunDiagnoseUnknown(t *testing.T) {
	code, _, errOut := runArgs(t, "diagnose", "floppy")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
}

func TestRunConfigPathAndSet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	code, out, errOut := runArgs(t, "config", "path")
	if code != 0 {
		t.Fatalf("path exit %d stderr=%s", code, errOut)
	}
	want := filepath.Join(tmp, "tipsy", "config.json")
	if strings.TrimSpace(out) != want {
		t.Fatalf("path = %q want %q", strings.TrimSpace(out), want)
	}

	code, _, errOut = runArgs(t, "config", "set", "logLevel", "debug")
	if code != 0 {
		t.Fatalf("set exit %d stderr=%s", code, errOut)
	}
	code, out, errOut = runArgs(t, "config", "get", "logLevel")
	if code != 0 {
		t.Fatalf("get exit %d stderr=%s", code, errOut)
	}
	if strings.TrimSpace(out) != "debug" {
		t.Fatalf("get = %q", out)
	}

	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "debug" {
		t.Fatalf("saved %+v", c)
	}
}

func TestInspectRequiresPath(t *testing.T) {
	code, _, errOut := runArgs(t, "inspect")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
}

func TestDiagnoseNativeRequiresPath(t *testing.T) {
	code, _, errOut := runArgs(t, "diagnose-native")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
}

func TestCompareRequiresTwoPaths(t *testing.T) {
	code, _, errOut := runArgs(t, "compare-roblox", "only-one.apk")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
}

func TestReportRequiresPath(t *testing.T) {
	code, _, errOut := runArgs(t, "report")
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errOut)
	}
}

func runArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, bytes.NewReader(nil), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func writeCLITestAPK(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fixed := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	for n, body := range files {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate, Modified: fixed}
		fw, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
