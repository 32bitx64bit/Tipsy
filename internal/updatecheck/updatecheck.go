// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

// Package updatecheck fetches Tipsy's latest published release, compares it
// with the running build and explains how that build can be updated.
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	defaultUserAgent = "Tipsy-Linux"
	defaultTimeout   = 10 * time.Second
	maxReleaseBytes  = 1 << 20
)

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
// repository, the public API and a ten second timeout.
type Client struct {
	// BaseURL is the API root, or a complete releases/latest endpoint.
	BaseURL string
	// HTTP is the client used for requests. Tests pass an httptest client.
	HTTP *http.Client
	// Timeout bounds the whole request through its context.
	Timeout time.Duration
	// UserAgent is sent to GitHub. GitHub rejects requests without one.
	UserAgent string
}

// NewClient returns a Client for baseURL using httpClient. Empty values fall
// back to the official API and default transport.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	return &Client{BaseURL: baseURL, HTTP: httpClient}
}

// Latest fetches the latest published release. It sets a context timeout and
// the GitHub-recommended Accept and User-Agent headers. A non-200 response or
// malformed body is an error; the caller keeps its previous quiet state.
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.latestEndpoint(), nil)
	if err != nil {
		return Release{}, fmt.Errorf("updatecheck: build request: %w", err)
	}
	userAgent := strings.TrimSpace(c.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
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
	return release, nil
}

// FetchLatest is a convenience wrapper around Client.Latest.
func FetchLatest(ctx context.Context) (Release, error) {
	return Client{}.Latest(ctx)
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

// CompareVersions is an explicit alias for Compare.
func CompareVersions(localVersion, releaseTag string) Status {
	return Compare(localVersion, releaseTag)
}

// CompareCurrent compares releaseTag with the running build's version.
func CompareCurrent(releaseTag string) Status {
	return Compare(version.Version, releaseTag)
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
