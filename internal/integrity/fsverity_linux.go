// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package integrity

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

type FSVerityStatus string

const (
	FSVerityEnabled     FSVerityStatus = "enabled"
	FSVerityNotEnabled  FSVerityStatus = "not_enabled"
	FSVerityUnavailable FSVerityStatus = "unavailable"
)

// VerityStatus is queried only on an already authenticated pinned descriptor.
// It is a capability result, never package identity and never a reason to
// reopen the path. Enabling fs-verity is deliberately left to a later
// filesystem-specific staging adapter.
func (p *PinnedFile) VerityStatus() (FSVerityStatus, error) {
	if p == nil || p.File == nil {
		return FSVerityUnavailable, fmt.Errorf("integrity: pinned file is closed")
	}
	flags, err := unix.IoctlGetInt(int(p.File.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return FSVerityUnavailable, nil
		}
		return FSVerityUnavailable, err
	}
	if flags&unix.FS_VERITY_FL != 0 {
		return FSVerityEnabled, nil
	}
	return FSVerityNotEnabled, nil
}
