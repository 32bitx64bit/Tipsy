//go:build linux

package clientsettings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/tipsy-linux/tipsy/internal/config"
)

// AcquireClientLock prevents settings XML writes while the client is active.
func AcquireClientLock() (func(), error) {
	dir := filepath.Join(config.Paths().StateDir, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "client.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("Roblox is running; close it before changing settings")
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// AcquireSettingsDocumentLock serializes client-settings.json read-modify-write
// cycles across the GUI and launcher. It is separate from the nonblocking
// client lock: document-only host preferences may save while Roblox runs, but
// no writer may lose another owner's newer JSON section or Fast Flag list.
func AcquireSettingsDocumentLock() (func(), error) {
	dir := filepath.Join(config.Paths().StateDir, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "client-settings.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
