// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package keylessrelease

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyCurrentRequiresAppImageHandoff(t *testing.T) {
	_, err := verifyCurrent(context.Background(), dependencies{
		artifactPath:    func() string { return "" },
		validateProcess: func() error { t.Fatal("process validation called"); return nil },
		releaseVersion:  func() string { return "1.2.3" },
		hashArtifact:    func(string) ([]byte, error) { t.Fatal("hash called"); return nil, nil },
		loadBundle: func(context.Context, string, string, []byte) ([]byte, string, error) {
			t.Fatal("bundle called")
			return nil, "", nil
		},
		verifyBundle: func(context.Context, []byte, []byte) error { t.Fatal("verify called"); return nil },
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyCurrentBindsExactArtifactBeforeOfficialResult(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "Tipsy.AppImage")
	if err := os.WriteFile(artifact, []byte("signed-artifact"), 0o500); err != nil {
		t.Fatal(err)
	}
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i)
	}
	loaded, verified := false, false
	result, err := verifyCurrent(context.Background(), dependencies{
		artifactPath:    func() string { return artifact },
		validateProcess: func() error { return nil },
		releaseVersion:  func() string { return "1.2.3" },
		hashArtifact: func(path string) ([]byte, error) {
			if path != artifact {
				t.Fatalf("artifact path=%q", path)
			}
			return digest, nil
		},
		loadBundle: func(_ context.Context, path, releaseVersion string, gotDigest []byte) ([]byte, string, error) {
			loaded = true
			if path != artifact || releaseVersion != "1.2.3" || string(gotDigest) != string(digest) {
				t.Fatalf("bundle inputs=%q %q %x", path, releaseVersion, gotDigest)
			}
			return []byte("bundle"), "sibling", nil
		},
		verifyBundle: func(_ context.Context, raw, gotDigest []byte) error {
			verified = true
			if string(raw) != "bundle" || string(gotDigest) != string(digest) {
				t.Fatalf("verify inputs=%q %x", raw, gotDigest)
			}
			return nil
		},
	})
	if err != nil || !loaded || !verified || result.ArtifactSHA256 != "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("result=%+v err=%v loaded=%v verified=%v", result, err, loaded, verified)
	}
}

func TestVerifyCurrentDoesNotTreatVerificationFailureAsOfficial(t *testing.T) {
	want := errors.New("identity mismatch")
	_, err := verifyCurrent(context.Background(), dependencies{
		artifactPath:    func() string { return "/tmp/Tipsy.AppImage" },
		validateProcess: func() error { return nil },
		releaseVersion:  func() string { return "1.2.3" },
		hashArtifact:    func(string) ([]byte, error) { return make([]byte, 32), nil },
		loadBundle: func(context.Context, string, string, []byte) ([]byte, string, error) {
			return []byte("bundle"), "sibling", nil
		},
		verifyBundle: func(context.Context, []byte, []byte) error { return want },
	})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "GitHub OIDC release verification") {
		t.Fatalf("err=%v", err)
	}
}

func TestVerifyCurrentRejectsArtifactOutsideAppImagePayload(t *testing.T) {
	want := errors.New("running executable is outside the AppImage payload")
	_, err := verifyCurrent(context.Background(), dependencies{
		artifactPath:    func() string { return "/tmp/Tipsy.AppImage" },
		validateProcess: func() error { return want },
		releaseVersion:  func() string { return "1.2.3" },
		hashArtifact: func(string) ([]byte, error) {
			t.Fatal("hash called")
			return nil, nil
		},
		loadBundle: func(context.Context, string, string, []byte) ([]byte, string, error) {
			t.Fatal("bundle called")
			return nil, "", nil
		},
		verifyBundle: func(context.Context, []byte, []byte) error { t.Fatal("verify called"); return nil },
	})
	if !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestReleaseBundleURLAcceptsOnlyReleaseVersions(t *testing.T) {
	url, err := releaseBundleURL("1.2.3")
	if err != nil || url != "https://github.com/32bitx64bit/Tipsy/releases/download/v1.2.3/Tipsy-1.2.3-x86_64.AppImage.sigstore.json" {
		t.Fatalf("url=%q err=%v", url, err)
	}
	for _, version := range []string{"", "v1.2.3", "1.2", "1.2.3/other"} {
		if _, err := releaseBundleURL(version); err == nil {
			t.Fatalf("accepted %q", version)
		}
	}
}

func TestReadRegularFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "bundle")
	if err := os.WriteFile(regular, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "bundle-link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFile(link, maxBundleBytes); err == nil {
		t.Fatal("symlink bundle was accepted")
	}
}
