// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ADBSource imports the already-installed official package from a user-owned,
// authorized Android device or emulator. It never authenticates to, scrapes,
// or bypasses Google Play.
type ADBSource struct {
	ADBPath string
	Serial  string
	run     func(context.Context, string, ...string) ([]byte, error)
}

func (s *ADBSource) Availability(ctx context.Context) SourceAvailability {
	availability := SourceAvailability{
		Name:     "Authorized Android device (ADB)",
		LegalURL: "https://play.google.com/store/apps/details?id=com.roblox.client",
	}
	path, err := s.commandPath()
	if err != nil {
		availability.Reason = "ADB is not installed. Choose local APK files, or install Android platform-tools and connect your own authorized device."
		return availability
	}
	out, err := s.runCommand(ctx, path, s.args("get-state")...)
	if err != nil || strings.TrimSpace(string(out)) != "device" {
		availability.Reason = "No authorized ADB device is ready. Connect your own Play-enabled x86_64 device or emulator, then approve its debugging prompt."
		return availability
	}
	availability.Available = true
	return availability
}

func (s *ADBSource) Acquire(ctx context.Context, dir string, progress ProgressFunc) ([]string, error) {
	a := s.Availability(ctx)
	if !a.Available {
		return nil, setupError(ErrSourceUnavailable, "ADB import", a.Reason, nil)
	}
	path, _ := s.commandPath()
	out, err := s.runCommand(ctx, path, s.args("shell", "pm", "path", "com.roblox.client")...)
	if err != nil {
		return nil, setupError(ErrNetwork, "ADB import", "could not query com.roblox.client on the authorized device", err)
	}
	if len(out) > 64<<10 {
		return nil, setupError(ErrSizeLimit, "ADB import", "device package path response is too large", nil)
	}
	remote, err := selectADBPackages(string(out))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, "ADB import", "cannot create private import directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	paths := make([]string, 0, len(remote))
	for i, remotePath := range remote {
		if err := checkContext(ctx, "ADB import"); err != nil {
			return nil, err
		}
		name := filepath.Base(remotePath)
		dest := filepath.Join(dir, name)
		report(progress, PhaseAcquiring, int64(i), int64(len(remote)), "Importing package from authorized Android device")
		if _, err := s.runCommand(ctx, path, s.args("pull", remotePath, dest)...); err != nil {
			_ = os.Remove(dest)
			return nil, setupError(ErrNetwork, "ADB import", "package import from the authorized device failed", err)
		}
		st, err := os.Lstat(dest)
		if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return nil, setupError(ErrUnsafePath, "ADB import", "ADB did not produce a safe regular package file", err)
		}
		_ = os.Chmod(dest, 0o600)
		paths = append(paths, dest)
	}
	report(progress, PhaseAcquiring, int64(len(remote)), int64(len(remote)), "Package import complete")
	return paths, nil
}

func (s *ADBSource) commandPath() (string, error) {
	if s != nil && s.ADBPath != "" {
		return s.ADBPath, nil
	}
	return exec.LookPath("adb")
}

func (s *ADBSource) runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	if s != nil && s.run != nil {
		return s.run(ctx, path, args...)
	}
	return exec.CommandContext(ctx, path, args...).Output()
}

func (s *ADBSource) args(args ...string) []string {
	if s != nil && strings.TrimSpace(s.Serial) != "" {
		return append([]string{"-s", s.Serial}, args...)
	}
	return args
}

func selectADBPackages(output string) ([]string, error) {
	var base, x86 string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "package:/data/app/") {
			return nil, setupError(ErrSourceTrust, "ADB import", "device returned an unsafe application path", nil)
		}
		path := strings.TrimPrefix(line, "package:")
		if strings.ContainsAny(path, "\x00\r\n\t ") || filepath.Clean(path) != path || !strings.HasPrefix(path, "/data/app/") || !strings.HasSuffix(strings.ToLower(path), ".apk") {
			return nil, setupError(ErrSourceTrust, "ADB import", "device returned an invalid application path", nil)
		}
		name := filepath.Base(path)
		switch {
		case name == "base.apk":
			if base != "" {
				return nil, setupError(ErrSourceTrust, "ADB import", "device returned multiple base APKs", nil)
			}
			base = path
		case name == "split_config.x86_64.apk":
			x86 = path
		}
	}
	if base == "" {
		return nil, setupError(ErrWrongPackage, "ADB import", "official Roblox base APK was not found on the device", nil)
	}
	out := []string{base}
	if x86 != "" {
		out = append(out, x86)
	}
	return out, nil
}

var _ Source = (*ADBSource)(nil)
