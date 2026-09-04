// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile replaces path with a fully written, fsynced regular file.
// It refuses symbolic-link and special-file destinations.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return fmt.Errorf("config destination is not a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	f, err := os.CreateTemp(dir, ".tipsy-config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return fmt.Errorf("config destination changed to a non-regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		defer d.Close()
		if err := d.Sync(); err != nil {
			return err
		}
	}
	return nil
}
