// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package updatecheck fetches Tipsy's latest published release, compares it
// with the running build and explains how that build can be updated.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tipsy-linux/tipsy/internal/version"
)

const (
	// Repository is the official GitHub repository that publishes Tipsy
	// releases. It intentionally differs from the Go module path: the module
	// is hosted at tipsy-linux/tipsy while releases are cut from
	// 32bitx64bit/Tipsy (scripts/release-inputs.lock.json).
	Repository = "32bitx64bit/Tipsy"

	// DefaultBaseURL is the public GitHub API root.
	DefaultBaseURL = "https://api.github.com"

	defaultTimeout  = 10 * time.Second
	maxReleaseBytes = 1 << 20
)

// defaultUserAgent is Tipsy's own User-Agent, derived from the compile-time
// build identity: unofficial builds carry the Unofficial / Local Build marker.
func defaultUserAgent() string {
	return version.UserAgent()
}

// Asset is one downloadable file attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release is the subset of GitHub's latest-release payload Tipsy needs.
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`

	// VerifiedSHA256 is the cryptographically verified SHA-256 hex digest of the
	// primary official AppImage asset populated by Client.Latest.
	VerifiedSHA256 string `json:"-"`
	// VerifiedAssets maps each verified release asset name to its verified
	// Sigstore provenance summary populated by Client.Latest.
	VerifiedAssets map[string]VerifiedBundle `json:"-"`
}

// Status is the result of comparing the running build with the latest release.
type Status string

const (
	// UpToDate means the running build is the latest release (or newer).
	UpToDate Status = "up-to-date"
	// UpdateAvailable means a newer release tag exists.
	UpdateAvailable Status = "update-available"
	// DevBuild means the running binary is an unstamped development build and
	// has no release version to compare.
	DevBuild Status = "dev-build"
)

// Client fetches releases from the GitHub API. Zero values use the official
// repository, the public API, embedded Sigstore Public Good roots, and a ten
// second timeout.
type Client struct {
	// BaseURL is the API root, or a complete releases/latest endpoint.
	BaseURL string
	// HTTP is the client used for requests. Tests pass an httptest client.
	HTTP *http.Client
	// Timeout bounds the whole request through its context.
	Timeout time.Duration
	// UserAgent is sent to GitHub. GitHub rejects requests without one.
	UserAgent string
	// Policy overrides offline Sigstore verification parameters (such as test
	// trust roots). Zero values enforce official 32bitx64bit/Tipsy release.yml
	// provenance against embedded Sigstore Public Good roots.
	Policy BundlePolicy
}

// Latest fetches the latest published release and verifies offline Sigstore
// bundles (.sigstore.json) and checksums (.sha256) for official release assets.
// A non-200 response, malformed body, missing signature bundle, or failed
// provenance check returns an error so the caller keeps its quiet state.
func (c Client) Latest(ctx context.Context) (Release, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	userAgent := strings.TrimSpace(c.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.latestEndpoint(), nil)
	if err != nil {
		return Release{}, fmt.Errorf("updatecheck: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("updatecheck: fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("updatecheck: latest release: github returned %s", resp.Status)
	}

	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBytes)).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("updatecheck: decode latest release: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return Release{}, fmt.Errorf("updatecheck: latest release has no tag_name")
	}
	if err := c.verifyReleaseAssets(ctx, userAgent, &release); err != nil {
		return Release{}, err
	}
	return release, nil
}

func (c Client) verifyReleaseAssets(ctx context.Context, userAgent string, release *Release) error {
	assetsByName := make(map[string]Asset, len(release.Assets))
	var targets []Asset
	hasAppImage := false

	for _, asset := range release.Assets {
		name := strings.TrimSpace(asset.Name)
		if name == "" {
			continue
		}
		if _, dup := assetsByName[name]; dup {
			return fmt.Errorf("updatecheck: duplicate release asset %q", name)
		}
		assetsByName[name] = asset

		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".appimage"):
			hasAppImage = true
			targets = append(targets, asset)
		case strings.HasSuffix(lower, ".appdir.tar.gz"):
			targets = append(targets, asset)
		}
	}
	if !hasAppImage {
		return errors.New("updatecheck: release has no official AppImage asset")
	}

	verified := make(map[string]VerifiedBundle, len(targets))
	var primarySHA256, fallbackSHA256 string

	for _, target := range targets {
		if err := c.validateAssetDownloadURL(target.BrowserDownloadURL); err != nil {
			return fmt.Errorf("updatecheck: asset %q: %w", target.Name, err)
		}
		shaAsset, ok := assetsByName[target.Name+".sha256"]
		if !ok {
			return fmt.Errorf("updatecheck: release asset %q is missing %s.sha256", target.Name, target.Name)
		}
		bundleAsset, ok := assetsByName[target.Name+".sigstore.json"]
		if !ok {
			return fmt.Errorf("updatecheck: release asset %q is missing %s.sigstore.json", target.Name, target.Name)
		}

		shaBytes, err := c.fetchAssetBytes(ctx, userAgent, shaAsset, maxSHA256FileBytes)
		if err != nil {
			return fmt.Errorf("updatecheck: fetch %s: %w", shaAsset.Name, err)
		}
		declaredHex, err := ParseSHA256File(shaBytes, target.Name)
		if err != nil {
			return fmt.Errorf("updatecheck: %s: %w", shaAsset.Name, err)
		}

		bundleBytes, err := c.fetchAssetBytes(ctx, userAgent, bundleAsset, maxSigstoreBundleSz)
		if err != nil {
			return fmt.Errorf("updatecheck: fetch %s: %w", bundleAsset.Name, err)
		}

		policy := c.Policy
		policy.Tag = release.TagName
		policy.ArtifactName = target.Name
		vb, err := VerifySigstoreBundle(bundleBytes, declaredHex, policy)
		if err != nil {
			return fmt.Errorf("updatecheck: verify %s: %w", bundleAsset.Name, err)
		}
		verified[target.Name] = vb

		lower := strings.ToLower(target.Name)
		if strings.HasSuffix(lower, ".appimage") {
			if strings.Contains(lower, "x86_64") || strings.Contains(lower, "amd64") {
				if primarySHA256 == "" {
					primarySHA256 = vb.ArtifactSHA256
				}
			} else if fallbackSHA256 == "" {
				fallbackSHA256 = vb.ArtifactSHA256
			}
		}
	}

	if primarySHA256 == "" {
		primarySHA256 = fallbackSHA256
	}
	release.VerifiedSHA256 = primarySHA256
	release.VerifiedAssets = verified
	return nil
}

func (c Client) fetchAssetBytes(ctx context.Context, userAgent string, asset Asset, maxBytes int64) ([]byte, error) {
	if err := c.validateAssetDownloadURL(asset.BrowserDownloadURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("asset exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func (c Client) validateAssetDownloadURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid asset download URL %q", raw)
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		baseParsed, err := url.Parse(strings.TrimSpace(c.BaseURL))
		if err == nil && baseParsed.Scheme == "http" && baseParsed.Host != "" && strings.EqualFold(parsed.Host, baseParsed.Host) {
			return nil
		}
	}
	return fmt.Errorf("refusing non-HTTPS asset download URL %q", raw)
}

func (c Client) latestEndpoint() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	if strings.HasSuffix(base, "/releases/latest") {
		return base
	}
	return base + "/repos/" + Repository + "/releases/latest"
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: defaultTimeout}
}

// Compare reports whether releaseTag is newer than the running localVersion.
// It accepts tags with or without the "v" prefix. A local build marked "dev"
// (including internal/version's default 0.0.0-dev) is always DevBuild.
func Compare(localVersion, releaseTag string) Status {
	localVersion = strings.TrimSpace(localVersion)
	if isDevVersion(localVersion) {
		return DevBuild
	}
	local, ok := parseSemver(localVersion)
	if !ok {
		// An unparseable local version is an unversioned development build.
		return DevBuild
	}
	latest, ok := parseSemver(releaseTag)
	if !ok {
		// Never offer a downgrade or an update from a malformed tag.
		return UpToDate
	}
	if compareSemver(latest, local) > 0 {
		return UpdateAvailable
	}
	return UpToDate
}

func isDevVersion(v string) bool {
	v = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	return v == "" || v == "0.0.0-dev" || strings.Contains(v, "-dev")
}

type semver struct {
	core [3]uint64
	pre  []string
}

func parseSemver(raw string) (semver, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return semver{}, false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	if core == "" {
		return semver{}, false
	}
	parts := strings.Split(core, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return semver{}, false
	}
	var parsed semver
	for i, part := range parts {
		if part == "" {
			return semver{}, false
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semver{}, false
		}
		parsed.core[i] = n
	}
	if hasPre {
		if pre == "" {
			return semver{}, false
		}
		parsed.pre = strings.Split(pre, ".")
		for _, part := range parsed.pre {
			if part == "" {
				return semver{}, false
			}
		}
	}
	return parsed, true
}

func compareSemver(a, b semver) int {
	for i := range a.core {
		if a.core[i] < b.core[i] {
			return -1
		}
		if a.core[i] > b.core[i] {
			return 1
		}
	}
	return comparePrerelease(a.pre, b.pre)
}

func comparePrerelease(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if cmp := compareIdentifier(a[i], b[i]); cmp != 0 {
			return cmp
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

func compareIdentifier(a, b string) int {
	an, aNum := strconv.ParseUint(a, 10, 64)
	bn, bNum := strconv.ParseUint(b, 10, 64)
	switch {
	case aNum == nil && bNum == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	case aNum == nil:
		return -1
	case bNum == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
