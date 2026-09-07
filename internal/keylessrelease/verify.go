// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package keylessrelease verifies the immutable GitHub Actions OIDC signature
// published beside a public Tipsy AppImage. It never creates, reads, or stores
// a Tipsy signing key.
package keylessrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	sigstoretuf "github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"golang.org/x/sys/unix"

	"github.com/tipsy-linux/tipsy/internal/config"
	"github.com/tipsy-linux/tipsy/internal/version"
)

const (
	artifactEnvironment = "TIPSY_RELEASE_ARTIFACT"
	appDirEnvironment   = "TIPSY_RELEASE_APPDIR"
	maxArtifactBytes    = int64(1 << 30)
	maxBundleBytes      = int64(4 << 20)

	productionOIDCIssuer = "https://token.actions.githubusercontent.com"
	productionIdentity   = `^https://github\.com/32bitx64bit/Tipsy/\.github/workflows/release\.yml@refs/(?:heads/main|tags/v[0-9A-Za-z._+-]+)$`
)

var (
	// ErrUnavailable means this executable was not started by the AppImage
	// launcher, so there is no release artifact whose bytes can be checked.
	ErrUnavailable = errors.New("GitHub keyless release verification is unavailable outside an AppImage")

	releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$`)
)

// Result is intentionally limited to public release facts. No certificate,
// token, account, or local-path data is retained by callers.
type Result struct {
	ArtifactSHA256 string
}

type dependencies struct {
	artifactPath    func() string
	validateProcess func() error
	releaseVersion  func() string
	hashArtifact    func(string) ([]byte, error)
	loadBundle      func(context.Context, string, string, []byte) ([]byte, string, error)
	verifyBundle    func(context.Context, []byte, []byte) error
}

func defaultDependencies() dependencies {
	return dependencies{
		artifactPath:    func() string { return os.Getenv(artifactEnvironment) },
		validateProcess: validateAppImageProcess,
		releaseVersion:  version.String,
		hashArtifact:    hashArtifact,
		loadBundle:      loadBundle,
		verifyBundle:    verifySigstoreBundle,
	}
}

// VerifyCurrent checks the exact AppImage designated by AppRun. A source
// build cannot obtain official status because it has no outer AppImage path.
func VerifyCurrent(ctx context.Context) (Result, error) {
	return verifyCurrent(ctx, defaultDependencies())
}

func verifyCurrent(ctx context.Context, deps dependencies) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if deps.artifactPath == nil || deps.validateProcess == nil || deps.releaseVersion == nil || deps.hashArtifact == nil || deps.loadBundle == nil || deps.verifyBundle == nil {
		return Result{}, errors.New("keyless release verifier is incomplete")
	}
	artifactPath := strings.TrimSpace(deps.artifactPath())
	if artifactPath == "" {
		return Result{}, ErrUnavailable
	}
	if !filepath.IsAbs(artifactPath) {
		return Result{}, fmt.Errorf("keyless release artifact path must be absolute")
	}
	if err := deps.validateProcess(); err != nil {
		return Result{}, fmt.Errorf("validate AppImage process: %w", err)
	}
	digest, err := deps.hashArtifact(artifactPath)
	if err != nil {
		return Result{}, fmt.Errorf("read AppImage for keyless verification: %w", err)
	}
	bundleBytes, _, err := deps.loadBundle(ctx, artifactPath, deps.releaseVersion(), digest)
	if err != nil {
		return Result{}, fmt.Errorf("obtain keyless release bundle: %w", err)
	}
	if err := deps.verifyBundle(ctx, bundleBytes, digest); err != nil {
		return Result{}, fmt.Errorf("GitHub OIDC release verification: %w", err)
	}
	return Result{ArtifactSHA256: hex.EncodeToString(digest)}, nil
}

func validateAppImageProcess() error {
	appDir := strings.TrimSpace(os.Getenv(appDirEnvironment))
	if appDir == "" || !filepath.IsAbs(appDir) {
		return errors.New("AppImage mount directory is unavailable")
	}
	info, err := os.Lstat(appDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("AppImage mount directory is unsafe")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve running executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve running executable links: %w", err)
	}
	binDir := filepath.Join(appDir, "usr", "bin")
	relative, err := filepath.Rel(binDir, executable)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || (relative != "tipsy" && relative != "tipsy-gui") {
		return errors.New("running executable is outside the AppImage payload")
	}
	return nil
}

func hashArtifact(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("cannot own AppImage descriptor")
	}
	defer file.Close()

	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Mode&0o022 != 0 || before.Size <= 0 || before.Size > maxArtifactBytes {
		return nil, errors.New("AppImage must be a bounded, single-link, non-writable regular file")
	}
	hash := sha256.New()
	written, err := io.CopyBuffer(hash, io.LimitReader(file, maxArtifactBytes+1), make([]byte, 128<<10))
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if written != before.Size || written > maxArtifactBytes || after.Dev != before.Dev || after.Ino != before.Ino || after.Size != before.Size || after.Mode != before.Mode || after.Nlink != before.Nlink {
		return nil, errors.New("AppImage changed while hashing")
	}
	return hash.Sum(nil), nil
}

func loadBundle(ctx context.Context, artifactPath, releaseVersion string, digest []byte) ([]byte, string, error) {
	sibling := artifactPath + ".sigstore.json"
	data, err := readRegularFile(sibling, maxBundleBytes)
	if err == nil {
		return data, sibling, nil
	}
	if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("read sibling bundle: %w", err)
	}

	cachePath := filepath.Join(config.Paths().CacheDir, "release-verifier", hex.EncodeToString(digest)+".sigstore.json")
	data, err = readRegularFile(cachePath, maxBundleBytes)
	if err == nil {
		return data, cachePath, nil
	}
	if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("read cached bundle: %w", err)
	}

	uri, err := releaseBundleURL(releaseVersion)
	if err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("User-Agent", "Tipsy release verifier/"+releaseVersion)
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many release-bundle redirects")
			}
			if request.URL.Scheme != "https" {
				return errors.New("release bundle redirect left HTTPS")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("release bundle download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength <= 0 || response.ContentLength > maxBundleBytes {
		return nil, "", errors.New("release bundle has an unsafe size")
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, maxBundleBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) != response.ContentLength || len(data) == 0 || int64(len(data)) > maxBundleBytes {
		return nil, "", errors.New("release bundle changed size while downloading")
	}
	if err := config.AtomicWriteFile(cachePath, data, 0o600); err != nil {
		return nil, "", fmt.Errorf("cache release bundle: %w", err)
	}
	return data, cachePath, nil
}

func releaseBundleURL(releaseVersion string) (string, error) {
	if !releaseVersionPattern.MatchString(releaseVersion) {
		return "", fmt.Errorf("Tipsy version %q is not a public release version", releaseVersion)
	}
	name := "Tipsy-" + releaseVersion + "-x86_64.AppImage.sigstore.json"
	return "https://github.com/32bitx64bit/Tipsy/releases/download/v" + releaseVersion + "/" + name, nil
}

func readRegularFile(path string, maximum int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("cannot own file descriptor")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size <= 0 || before.Size > maximum {
		return nil, errors.New("file must be a bounded, single-link regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if int64(len(data)) != before.Size || int64(len(data)) > maximum || after.Dev != before.Dev || after.Ino != before.Ino || after.Size != before.Size || after.Mode != before.Mode || after.Nlink != before.Nlink {
		return nil, errors.New("file changed while reading")
	}
	return data, nil
}

func verifySigstoreBundle(ctx context.Context, rawBundle, artifactDigest []byte) error {
	if len(rawBundle) == 0 || int64(len(rawBundle)) > maxBundleBytes {
		return errors.New("release bundle has an unsafe size")
	}
	if len(artifactDigest) != sha256.Size {
		return errors.New("AppImage digest is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var signedBundle bundle.Bundle
	if err := signedBundle.UnmarshalJSON(rawBundle); err != nil {
		return fmt.Errorf("decode Sigstore bundle: %w", err)
	}
	bundleVersion, err := signedBundle.Version()
	if err != nil {
		return fmt.Errorf("inspect Sigstore bundle version: %w", err)
	}
	if bundleVersion != "v0.3" {
		return fmt.Errorf("unsupported Sigstore bundle version %s", bundleVersion)
	}

	cacheRoot := filepath.Join(config.Paths().CacheDir, "release-verifier", "sigstore-root")
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return fmt.Errorf("create Sigstore trust cache: %w", err)
	}
	if err := os.Chmod(cacheRoot, 0o700); err != nil {
		return fmt.Errorf("protect Sigstore trust cache: %w", err)
	}
	options := sigstoretuf.DefaultOptions().WithContext(ctx).WithCachePath(cacheRoot).WithCacheValidity(1)
	trustedRoot, err := root.FetchTrustedRootWithOptions(options)
	if err != nil {
		return fmt.Errorf("obtain Sigstore trusted root: %w", err)
	}
	identity, err := verify.NewShortCertificateIdentity(productionOIDCIssuer, "", "", productionIdentity)
	if err != nil {
		return fmt.Errorf("configure GitHub release identity: %w", err)
	}
	verifier, err := verify.NewVerifier(
		trustedRoot,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithObserverTimestamps(1),
		verify.WithTransparencyLog(1),
	)
	if err != nil {
		return fmt.Errorf("configure Sigstore verifier: %w", err)
	}
	if _, err := verifier.Verify(&signedBundle, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", artifactDigest),
		verify.WithCertificateIdentity(identity),
	)); err != nil {
		return err
	}
	return nil
}
