// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package runtime

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func assetContentDir(assetsDir string) string { return filepath.Join(assetsDir, "content") }

func absExistingDir(path string) string {
	if strings.TrimSpace(path) == "" || os.MkdirAll(path, 0o700) != nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return abs
}

// prepareRuntimeFiles installs the CA bundle the official APK shipped under
// Android FilesDir. It deliberately does not alter process-global CWD: normal
// X11 startup depends on the caller's existing relative-resource context.
func prepareRuntimeFiles(filesDir, assetsDir string) error {
	return ensureRobloxCABundle(filesDir, assetsDir)
}

// ensureRobloxCABundle prepares the exact relative path libroblox opens.
// The official APK asset is authoritative; an absent or unusable asset is a
// startup error.  Falling back to the host CA store could silently change the
// client's trust configuration, so it is deliberately not supported here.
func ensureRobloxCABundle(filesDir, assetsDir string) error {
	if strings.TrimSpace(filesDir) == "" {
		return fmt.Errorf("files directory is empty")
	}
	if strings.TrimSpace(assetsDir) == "" {
		return fmt.Errorf("official CA bundle assets directory is empty")
	}
	official := filepath.Join(assetsDir, "ssl", "cacert.pem")
	st, err := os.Stat(official)
	if err != nil {
		return fmt.Errorf("inspect official CA bundle: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 {
		return fmt.Errorf("official CA bundle is not a nonempty regular file: %s", official)
	}
	dest := filepath.Join(filesDir, "exe", "cacert.pem")
	if err := copyFileAtomically(official, dest); err != nil {
		return fmt.Errorf("install official CA bundle: %w", err)
	}
	return nil
}

func copyFileAtomically(src, dest string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return fmt.Errorf("source is empty: %s", src)
	}
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, b) {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".cacert-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}
