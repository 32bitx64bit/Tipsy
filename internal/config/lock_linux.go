// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package config

import (
	"os"
	"path/filepath"
	"syscall"
)

// acquireConfigLock serializes config read-modify-write cycles across
// processes, including `tipsy config set` and launch consent persistence.
// Plain reads stay lock-free because writes are atomic renames.
func acquireConfigLock() (func(), error) {
	dir := Paths().ConfigDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "config.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
