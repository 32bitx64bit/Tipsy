// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

package updatecheck

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/desktop"
)

const (
	// FlatpakID is the application ID used by the Flatpak build.
	FlatpakID = "io.github.tipsy_linux.Tipsy"
	// PackageName is the native package name used by deb, rpm and pacman.
	PackageName = "tipsy"

	ownershipTimeout   = 3 * time.Second
	maxSHA256FileBytes = 64 << 10
)

// Method is how the running Tipsy binary was installed.
type Method string

const (
	AppImage      Method = "appimage"
	Flatpak       Method = "flatpak"
	Deb           Method = "deb"
	RPM           Method = "rpm"
	Pacman        Method = "pacman"
	SystemUnknown Method = "system-unknown"
	Source        Method = "source"
)

// DetectDeps overrides the environment probes DetectInstallWith uses. Nil
// fields fall back to the real desktop, executable and package-manager checks.
type DetectDeps struct {
	Medium     func() (desktop.Medium, string)
	Executable func() (string, error)
	OwnedBy    func(manager, executable string) bool
}

var (
	packageManagers = []struct {
		name   string
		method Method
	}{
		{"dpkg", Deb},
		{"rpm", RPM},
		{"pacman", Pacman},
	}
)

// DetectInstall reports how the running binary was installed. It starts from
// desktop.CurrentMedium and, for a system install, asks dpkg/rpm/pacman which
// package owns os.Executable.
func DetectInstall() Method {
	return DetectInstallWith(DetectDeps{})
}

// DetectInstallWith is DetectInstall with injectable probes for tests.
func DetectInstallWith(deps DetectDeps) Method {
	medium := desktop.CurrentMedium
	if deps.Medium != nil {
		medium = deps.Medium
	}
	executable := os.Executable
	if deps.Executable != nil {
		executable = deps.Executable
	}
	ownedBy := packageOwned
	if deps.OwnedBy != nil {
		ownedBy = deps.OwnedBy
	}

	current, _ := medium()
	switch current {
	case desktop.MediumAppImage:
		return AppImage
	case desktop.MediumFlatpak:
		return Flatpak
	case desktop.MediumSource:
		return Source
	case desktop.MediumSystem:
		path, err := executable()
		if err != nil {
			return SystemUnknown
		}
		path = strings.TrimSuffix(path, " (deleted)")
		if path == "" {
			return SystemUnknown
		}
		for _, manager := range packageManagers {
			if ownedBy(manager.name, path) {
				return manager.method
			}
		}
		return SystemUnknown
	default:
		return SystemUnknown
	}
}

func packageOwned(manager, executable string) bool {
	var args []string
	switch manager {
	case "dpkg":
		args = []string{"-S", executable}
	case "rpm":
		args = []string{"-qf", executable}
	case "pacman":
		args = []string{"-Qo", executable}
	default:
		return false
	}
	if _, err := exec.LookPath(manager); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownershipTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, manager, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// Instructions describes how to update one install method. command is empty
// when Tipsy cannot safely update that method itself. url is the AppImage asset
// for AppImage installs, and empty otherwise.
func Instructions(method Method, release Release) (message, command, url string) {
	switch method {
	case AppImage:
		return "A newer Tipsy release is available. Download the latest AppImage.", "", appImageURL(release)
	case Flatpak:
		return "A newer Tipsy release is available. Update it with Flatpak.",
			"flatpak update " + FlatpakID, ""
	case Deb:
		return "A newer Tipsy release is available. Upgrade the Debian package.",
			"sudo apt update && sudo apt install --only-upgrade " + PackageName, ""
	case RPM:
		return "A newer Tipsy release is available. Upgrade the RPM package.",
			"sudo dnf upgrade " + PackageName, ""
	case Pacman:
		return "A newer Tipsy release is available. Upgrade the Arch package.",
			"sudo pacman -Syu " + PackageName, ""
	case Source:
		return "This is a source build. Rebuild from the latest source to update.", "", ""
	default:
		return "Tipsy is installed as a system package. Update it with your distribution's package manager.", "", ""
	}
}

func appImageURL(release Release) string {
	var fallback string
	for _, asset := range release.Assets {
		if asset.BrowserDownloadURL == "" {
			continue
		}
		if release.VerifiedAssets != nil {
			if _, ok := release.VerifiedAssets[asset.Name]; !ok {
				continue
			}
		}
		name := strings.ToLower(asset.Name)
		if !strings.HasSuffix(name, ".appimage") {
			continue
		}
		if strings.Contains(name, "x86_64") || strings.Contains(name, "amd64") {
			return asset.BrowserDownloadURL
		}
		if fallback == "" {
			fallback = asset.BrowserDownloadURL
		}
	}
	if fallback != "" {
		return fallback
	}
	return release.HTMLURL
}

// ParseSHA256File parses a sha256sum-formatted checksum file (or bare 64-hex
// digest when expectedFilename is empty) and returns the lowercase SHA-256 hex
// digest for expectedFilename.
func ParseSHA256File(data []byte, expectedFilename string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("updatecheck: empty .sha256 file")
	}
	if len(data) > maxSHA256FileBytes {
		return "", fmt.Errorf("updatecheck: .sha256 file exceeds %d bytes", maxSHA256FileBytes)
	}
	wantBase := ""
	if trimmed := strings.TrimSpace(expectedFilename); trimmed != "" {
		wantBase = filepath.Base(trimmed)
	}

	type entry struct {
		digest string
		name   string
	}
	var entries []entry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// GNU coreutils BSD-tag lines are not produced by release.yml; parse
		// standard "<64-hex>  <path>" or "<64-hex> *<path>" or bare "<64-hex>".
		digestPart := line
		namePart := ""
		if idx := strings.IndexAny(line, " \t"); idx >= 0 {
			digestPart = line[:idx]
			namePart = strings.TrimSpace(line[idx+1:])
			namePart = strings.TrimPrefix(namePart, "*")
			namePart = strings.TrimSpace(namePart)
		}
		normalized, _, err := normalizeSHA256Hex(digestPart)
		if err != nil {
			return "", fmt.Errorf("updatecheck: invalid .sha256 entry: %w", err)
		}
		entries = append(entries, entry{digest: normalized, name: namePart})
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("updatecheck: read .sha256 file: %w", err)
	}
	if len(entries) == 0 {
		return "", errors.New("updatecheck: .sha256 file contains no checksums")
	}

	if len(entries) == 1 && entries[0].name == "" {
		return entries[0].digest, nil
	}
	if wantBase == "" {
		if len(entries) != 1 {
			return "", errors.New("updatecheck: multi-entry .sha256 file requires expectedFilename")
		}
		return entries[0].digest, nil
	}

	matched := ""
	for _, e := range entries {
		if e.name == "" {
			continue
		}
		if filepath.Base(e.name) != wantBase {
			continue
		}
		if matched != "" && matched != e.digest {
			return "", fmt.Errorf("updatecheck: conflicting .sha256 entries for %q", wantBase)
		}
		matched = e.digest
	}
	if matched == "" {
		return "", fmt.Errorf("updatecheck: .sha256 file has no entry matching %q", wantBase)
	}
	return matched, nil
}

// HashArtifactFile computes the lowercase SHA-256 hex digest of a non-empty,
// non-symlink regular file on disk.
func HashArtifactFile(path string) (string, error) {
	lstat, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("updatecheck: stat artifact: %w", err)
	}
	if !lstat.Mode().IsRegular() {
		return "", fmt.Errorf("updatecheck: artifact %q is not a regular file", path)
	}
	if lstat.Size() <= 0 {
		return "", fmt.Errorf("updatecheck: artifact %q is empty", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("updatecheck: open artifact: %w", err)
	}
	defer f.Close()

	fstat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("updatecheck: fstat artifact: %w", err)
	}
	if !os.SameFile(lstat, fstat) || !fstat.Mode().IsRegular() {
		return "", fmt.Errorf("updatecheck: artifact %q changed during open", path)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", fmt.Errorf("updatecheck: hash artifact: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// VerifyArtifactBytes verifies in-memory artifact bytes against a .sha256
// checksum payload and an offline .sigstore.json bundle.
func VerifyArtifactBytes(artifact, sha256Data, bundleJSON []byte, policy BundlePolicy) (VerifiedBundle, error) {
	if len(artifact) == 0 {
		return VerifiedBundle{}, errors.New("updatecheck: empty artifact payload")
	}
	sum := sha256.Sum256(artifact)
	actualHex := hex.EncodeToString(sum[:])

	declaredHex, err := ParseSHA256File(sha256Data, policy.ArtifactName)
	if err != nil {
		return VerifiedBundle{}, err
	}
	if actualHex != declaredHex {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: artifact SHA-256 %s does not match .sha256 %s", actualHex, declaredHex)
	}
	return VerifySigstoreBundle(bundleJSON, actualHex, policy)
}

// VerifyArtifactFile hashes artifactPath on disk, verifies it matches
// sha256Path, and cryptographically verifies bundlePath offline against policy.
func VerifyArtifactFile(artifactPath, sha256Path, bundlePath string, policy BundlePolicy) (VerifiedBundle, error) {
	actualHex, err := HashArtifactFile(artifactPath)
	if err != nil {
		return VerifiedBundle{}, err
	}
	sha256Bytes, err := readBoundedRegularFile(sha256Path, maxSHA256FileBytes)
	if err != nil {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: read .sha256 file: %w", err)
	}
	expectedName := strings.TrimSpace(policy.ArtifactName)
	if expectedName == "" {
		expectedName = filepath.Base(artifactPath)
		policy.ArtifactName = expectedName
	}
	declaredHex, err := ParseSHA256File(sha256Bytes, expectedName)
	if err != nil {
		return VerifiedBundle{}, err
	}
	if actualHex != declaredHex {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: artifact SHA-256 %s does not match .sha256 %s", actualHex, declaredHex)
	}
	bundleBytes, err := readBoundedRegularFile(bundlePath, maxSigstoreBundleSz)
	if err != nil {
		return VerifiedBundle{}, fmt.Errorf("updatecheck: read Sigstore bundle: %w", err)
	}
	return VerifySigstoreBundle(bundleBytes, actualHex, policy)
}

func readBoundedRegularFile(path string, maxBytes int64) ([]byte, error) {
	lstat, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !lstat.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	if lstat.Size() <= 0 {
		return nil, fmt.Errorf("%q is empty", path)
	}
	if lstat.Size() > maxBytes {
		return nil, fmt.Errorf("%q exceeds %d bytes", path, maxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fstat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(lstat, fstat) || !fstat.Mode().IsRegular() {
		return nil, fmt.Errorf("%q changed during open", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%q exceeds %d bytes", path, maxBytes)
	}
	return data, nil
}
