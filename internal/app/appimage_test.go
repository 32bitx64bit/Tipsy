// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentifyOfficialAppImageRequiresAppRunHandoff(t *testing.T) {
	t.Setenv(artifactEnvironment, "")
	if err := identifyOfficialAppImage(context.Background()); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestOfficialReleaseKindTreatsDevelopmentPayloadAsUnavailable(t *testing.T) {
	if err := officialReleaseKind("development-unrestricted"); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if err := officialReleaseKind(""); !errors.Is(err, errOfficialReleaseUnavailable) {
		t.Fatalf("empty err=%v", err)
	}
	if err := officialReleaseKind("release-candidate-keyless"); err != nil {
		t.Fatalf("github-signed err=%v", err)
	}
}

func TestPayloadReleaseKindReadsDevelopmentBuildInfo(t *testing.T) {
	appDir := t.TempDir()
	path := filepath.Join(appDir, "usr", "share", "tipsy", "build-info.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"format":"tipsy.build-info.v1","releaseKind":"development-unrestricted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(appDirEnvironment, appDir)
	if got := payloadReleaseKind(); got != "development-unrestricted" {
		t.Fatalf("kind=%q", got)
	}
}
