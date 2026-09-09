// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package appimage_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAppRunForwardsPrivateServerShareProtocolUnchanged(t *testing.T) {
	const uri = "roblox://navigation/share_links?code=SYNTHETIC-PRIVATE-SERVER-CODE&type=Server"
	appdir := fakeRunnableAppDir(t)
	logPath := filepath.Join(t.TempDir(), "stub.log")
	stub := "#!/bin/sh\n" +
		"[ \"$#\" -eq 2 ] || exit 40\n" +
		"[ \"$1\" = --play ] || exit 41\n" +
		"[ \"$2\" = \"$TIPSY_EXPECTED_URI\" ] || exit 42\n" +
		"printf '%s' ok > \"$TIPSY_STUB_LOG\"\n"
	if err := os.WriteFile(filepath.Join(appdir, "usr", "bin", "tipsy-gui"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "tipsy", "runtime", "lib", "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(appdir, "AppRun"), "--no-integrate", uri)
	command.Dir = appdir
	command.Env = append(stubEnv(t, logPath, xdg), "TIPSY_EXPECTED_URI="+uri)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("private-server protocol dispatch: %v\n%s", err, output)
	}
	assertStubLog(t, logPath, "ok")
}
