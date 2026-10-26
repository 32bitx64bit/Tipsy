// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux && amd64

package android

import (
	"syscall"
	"testing"
)

func TestSystemShimRefusesWithoutHostShell(t *testing.T) {
	if rc, _ := systemShimProbe(nil); rc != 0 {
		t.Fatalf("system(NULL) = %d, want 0", rc)
	}
	command := "true"
	rc, errno := systemShimProbe(&command)
	if rc != -1 {
		t.Fatalf("system(\"true\") = %d, want -1", rc)
	}
	if errno != int(syscall.ENOSYS) {
		t.Fatalf("errno = %d, want ENOSYS (%d)", errno, int(syscall.ENOSYS))
	}
}
