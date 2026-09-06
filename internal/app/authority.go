// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/releasemeta"
	"github.com/tipsy-linux/tipsy/internal/securitypolicy"
	"github.com/tipsy-linux/tipsy/internal/setupsvc"
)

var ErrDevelopmentConsentRequired = errors.New("official release authority is unavailable; run once with --development to explicitly authorize a non-official source-build launch")

type authorityResolution struct {
	Mode    setupsvc.AuthorizationMode
	Trust   setupsvc.TrustPolicy
	Release releasemeta.Result
}

type authorityDependencies struct {
	now           func() time.Time
	readRoot      func(string) ([]byte, error)
	verifyRelease func(releasemeta.VerifyOptions) (releasemeta.Result, error)
	readPolicy    func(string, releasemeta.Result, time.Time) (securitypolicy.RobloxPolicy, error)
}

func defaultAuthorityDependencies() authorityDependencies {
	return authorityDependencies{
		now:           func() time.Time { return time.Now().UTC() },
		readRoot:      releasemeta.ReadInitialRoot,
		verifyRelease: releasemeta.VerifyLocal,
		readPolicy:    readAuthenticatedRobloxPolicy,
	}
}

// resolveAuthority is the single app-level trust decision. Development is
// reachable only through persisted explicit consent. Official is reachable
// only after releasemeta returns its production-bound state and the exact TUF
// authenticated Roblox policy is re-read by digest.
func resolveAuthority(ctx context.Context, cfg *config.Config, installedVersionCode int64, deps authorityDependencies) (authorityResolution, error) {
	if cfg != nil && cfg.DevelopmentApproved() {
		return authorityResolution{Mode: setupsvc.DevelopmentUnrestricted, Trust: setupsvc.DevelopmentTrustPolicy()}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return authorityResolution{}, err
	}
	if deps.now == nil || deps.readRoot == nil || deps.verifyRelease == nil || deps.readPolicy == nil {
		return authorityResolution{}, fmt.Errorf("release authority resolver is incomplete")
	}
	if cfg == nil || cfg.OfficialVerification == nil {
		return authorityResolution{}, ErrDevelopmentConsentRequired
	}
	official := cfg.OfficialVerification
	if strings.TrimSpace(official.InitialRootPath) == "" || strings.TrimSpace(official.MetadataDir) == "" ||
		strings.TrimSpace(official.TargetsDir) == "" || strings.TrimSpace(official.ArtifactPath) == "" ||
		strings.TrimSpace(official.TargetPath) == "" {
		return authorityResolution{}, ErrDevelopmentConsentRequired
	}
	stateDir := official.StateDir
	if stateDir == "" {
		var err error
		stateDir, err = releasemeta.DefaultStateDir()
		if err != nil {
			return authorityResolution{}, fmt.Errorf("release rollback state: %w", err)
		}
	}
	root, err := deps.readRoot(official.InitialRootPath)
	if err != nil {
		return authorityResolution{}, fmt.Errorf("release root: %w", err)
	}
	now := deps.now().UTC()
	result, err := deps.verifyRelease(releasemeta.VerifyOptions{
		InitialRoot: root, MetadataDir: official.MetadataDir, TargetsDir: official.TargetsDir,
		ArtifactPath: official.ArtifactPath, TargetPath: official.TargetPath, Channel: official.Channel,
		StateDir: stateDir, Now: now,
	})
	if err != nil {
		return authorityResolution{}, fmt.Errorf("official release verification: %w", err)
	}
	if !result.Verified || result.State != releasemeta.OfficialVerified || !result.ProductionRootBound || !result.ProductionEvidenceBound {
		return authorityResolution{}, fmt.Errorf("release verification did not grant OfficialVerified")
	}
	policy, err := deps.readPolicy(official.TargetsDir, result, now)
	if err != nil {
		return authorityResolution{}, fmt.Errorf("authenticated Roblox policy: %w", err)
	}
	trust := setupsvc.WithAuthenticatedRobloxPolicy(policy, installedVersionCode, policy.Validity.Sequence)
	return authorityResolution{Mode: setupsvc.OfficialVerified, Trust: trust, Release: result}, nil
}

func readAuthenticatedRobloxPolicy(targetsDir string, result releasemeta.Result, now time.Time) (securitypolicy.RobloxPolicy, error) {
	var evidence *releasemeta.EvidenceStatus
	for i := range result.Evidence {
		if result.Evidence[i].Kind != "roblox_policy" {
			continue
		}
		if evidence != nil {
			return securitypolicy.RobloxPolicy{}, fmt.Errorf("release evidence repeats the Roblox policy")
		}
		evidence = &result.Evidence[i]
	}
	if evidence == nil || len(evidence.SHA256) != 64 || strings.ToLower(evidence.SHA256) != evidence.SHA256 {
		return securitypolicy.RobloxPolicy{}, fmt.Errorf("release evidence omits the Roblox policy")
	}
	if err := securitypolicy.ValidateTargetPath(evidence.Target, "policies/roblox/"); err != nil {
		return securitypolicy.RobloxPolicy{}, err
	}
	raw, err := readPolicyTargetNoFollow(targetsDir, evidence.Target)
	if err != nil {
		return securitypolicy.RobloxPolicy{}, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != evidence.SHA256 {
		return securitypolicy.RobloxPolicy{}, fmt.Errorf("Roblox policy differs from authenticated TUF evidence")
	}
	return securitypolicy.DecodeRoblox(raw, now, 0)
}

func readPolicyTargetNoFollow(root, target string) ([]byte, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := rootFD
	defer func() { _ = unix.Close(current) }()
	parts := strings.Split(target, "/")
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, part, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, openErr
		}
		_ = unix.Close(current)
		current = next
	}
	fd, err := unix.Openat(current, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "roblox-policy")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("cannot own policy descriptor")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 ||
		before.Mode&0o022 != 0 || before.Size <= 0 || before.Size > securitypolicy.MaxPolicyBytes {
		return nil, fmt.Errorf("Roblox policy is not a bounded non-writable owner file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, securitypolicy.MaxPolicyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Roblox policy: %w", err)
	}
	if int64(len(raw)) != before.Size {
		return nil, fmt.Errorf("Roblox policy changed size while reading")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mode != after.Mode ||
		before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return nil, fmt.Errorf("Roblox policy changed while reading")
	}
	return raw, nil
}

func generationStoreRoot() string {
	return setupsvc.GenerationStoreDir(filepath.Clean(runtimeDir()))
}

// runtimeDir is factored for app tests while retaining runtime.RuntimeDir as
// the production authority for the XDG installation location.
var runtimeDir = func() string {
	return filepath.Join(config.Paths().DataDir, "runtime")
}
