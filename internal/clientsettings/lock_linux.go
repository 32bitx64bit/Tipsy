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
