// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package gamepad

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestIsGamepadKeyBitsSouthDetects(t *testing.T) {
	keyBits := make([]byte, (KeyMax+8)/8)
	if IsGamepadKeyBits(keyBits) {
		t.Fatal("empty caps must not detect a gamepad")
	}
	keyBits[BtnSouth/8] |= 1 << (BtnSouth % 8)
	if !IsGamepadKeyBits(keyBits) {
		t.Fatal("BTN_SOUTH must detect a gamepad")
	}
}

func TestScanEmptyDirIsHonestEmpty(t *testing.T) {
	dir := t.TempDir()
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan empty dir: %v", err)
	}
	if len(res.Pads) != 0 {
		t.Fatalf("empty dir must yield zero pads, got %d", len(res.Pads))
	}
	if len(res.Denied) != 0 {
		t.Fatalf("empty dir must yield zero denied, got %v", res.Denied)
	}
}

func TestScanMissingDirIsHonestEmpty(t *testing.T) {
	res, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("Scan missing dir: %v", err)
	}
	if len(res.Pads) != 0 {
		t.Fatalf("missing dir must yield zero pads, got %d", len(res.Pads))
	}
}

func TestOpenFirstGamepadEmptyIsNoGamepad(t *testing.T) {
	_, err := OpenFirstGamepad(t.TempDir())
	if !errors.Is(err, ErrNoGamepad) {
		t.Fatalf("want ErrNoGamepad, got %v", err)
	}
}

func TestErrorForErrnoPermissionIsActionable(t *testing.T) {
	err := ErrorForErrno("/dev/input/event0", unix.EACCES)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("EACCES must map to ErrPermissionDenied, got %v", err)
	}
	if !strings.Contains(err.Error(), "input group") {
		t.Fatalf("EACCES error must carry the actionable hint, got %q", err.Error())
	}
	// Other errnos must not masquerade as permission errors.
	other := ErrorForErrno("/dev/input/event0", unix.ENODEV)
	if errors.Is(other, ErrPermissionDenied) {
		t.Fatalf("ENODEV must not map to ErrPermissionDenied: %v", other)
	}
}

func TestOpenDeviceMissingIsHonestFailure(t *testing.T) {
	_, err := OpenDevice(filepath.Join(t.TempDir(), "event0"))
	if err == nil {
		t.Fatal("missing node must fail, never yield a fake pad")
	}
	if errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("missing node must not report permission denied: %v", err)
	}
}

func TestPermissionDeniedNeverYieldsPad(t *testing.T) {
	// Deterministic shape check without touching real ACLs: an
	// EACCES-flavoured ScanResult must still carry zero pads.
	res := ScanResult{Denied: []string{"/dev/input/event0"}}
	if len(res.Pads) != 0 {
		t.Fatal("denied result must carry zero pads")
	}
	if !strings.Contains(PermissionHint, "input group") {
		t.Fatalf("hint must name the input group, got %q", PermissionHint)
	}
}

func TestScanIgnoresNonEventFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"mice", "mouse0", "js0", "eventX", "event"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Pads) != 0 || len(res.Denied) != 0 {
		t.Fatalf("non-event files must be ignored, got %+v", res)
	}
}
