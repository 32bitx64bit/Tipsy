// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package setupsvc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// APKMirrorSource downloads the latest Roblox Android package from APKMirror.
// Bytes are untrusted until Install cryptographically verifies the official
// Roblox signer and x86-64 libroblox.so.
type APKMirrorSource struct {
	Client   *http.Client
	Origin   string
	MaxBytes int64

	clientOnce sync.Once
	httpClient *http.Client
}

func (s *APKMirrorSource) Availability(context.Context) SourceAvailability {
	return SourceAvailability{
		Available:   true,
		Name:        "APKMirror",
		LegalURL:    s.origin() + apkMirrorRobloxPrefix,
		Explanation: apkMirrorExplanation,
	}
}

func (s *APKMirrorSource) Acquire(ctx context.Context, dir string, progress ProgressFunc) ([]string, error) {
	if err := checkContext(ctx, "APKMirror"); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, setupError(ErrInstall, "APKMirror", "cannot create the private download directory", err)
	}
	_ = os.Chmod(dir, 0o700)
	report(progress, PhaseAcquiring, 0, 0, "Looking up the latest Roblox package on APKMirror")

	listing, err := s.getHTML(ctx, apkMirrorRobloxPrefix, s.origin()+"/")
	if err != nil {
		return nil, err
	}
	releasePath, err := parseLatestReleasePath(listing)
	if err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 1, 4, "Selecting an x86-64 Roblox variant")
	releaseHTML, err := s.getHTML(ctx, releasePath, s.origin()+apkMirrorRobloxPrefix)
	if err != nil {
		return nil, err
	}
	variant, err := selectX86Variant(parseVariants(releaseHTML))
	if err != nil {
		return nil, err
	}
	variantHTML, err := s.getHTML(ctx, variant.Path, s.origin()+releasePath)
	if err != nil {
		return nil, err
	}
	buttonPath, err := parseDownloadButtonPath(variantHTML)
	if err != nil {
		return nil, err
	}
	downloadHTML, err := s.getHTML(ctx, buttonPath, s.origin()+variant.Path)
	if err != nil {
		return nil, err
	}
	filePath, err := parseDirectDownloadPath(downloadHTML)
	if err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 2, 4, "Downloading the selected APKMirror package")
	packagePath := filepath.Join(dir, "roblox-package.bin")
	if err := s.downloadFile(ctx, filePath, s.origin()+buttonPath, packagePath, progress); err != nil {
		return nil, err
	}
	report(progress, PhaseAcquiring, 3, 4, "Keeping only x86-64 Roblox package files")
	selected, err := selectX86PackageFiles(packagePath, dir, "APKMirror")
	if err != nil {
		return nil, err
	}
	return selected, nil
}

func (s *APKMirrorSource) origin() string {
	if s != nil && strings.TrimSpace(s.Origin) != "" {
		return strings.TrimRight(s.Origin, "/")
	}
	return apkMirrorDefaultOrigin
}

func (s *APKMirrorSource) maxBytes() int64 {
	if s != nil && s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return DefaultLimits().MaxFileBytes
}

func (s *APKMirrorSource) client() *http.Client {
	if s == nil {
		return http.DefaultClient
	}
	if s.Client != nil {
		return s.Client
	}
	s.clientOnce.Do(func() {
		jar, _ := cookiejar.New(nil)
		s.httpClient = &http.Client{
			Timeout: 2 * time.Minute,
			Jar:     jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 8 {
					return fmt.Errorf("too many redirects")
				}
				return s.trustedURL(req.URL)
			},
		}
	})
	return s.httpClient
}

func (s *APKMirrorSource) getHTML(ctx context.Context, path, referer string) ([]byte, error) {
	u, err := s.resolve(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, setupError(ErrInvalidRequest, "APKMirror", "cannot create APKMirror request", err)
	}
	s.setHeaders(req, referer, "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, apkMirrorNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apkMirrorStatusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil {
		return nil, setupError(ErrNetwork, "APKMirror", "APKMirror page could not be read", err)
	}
	if int64(len(data)) > 2<<20 {
		return nil, setupError(ErrSizeLimit, "APKMirror", "APKMirror page exceeds the safe HTML limit", nil)
	}
	return data, nil
}

func (s *APKMirrorSource) downloadFile(ctx context.Context, path, referer, dest string, progress ProgressFunc) error {
	u, err := s.resolve(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return setupError(ErrInvalidRequest, "APKMirror", "cannot create APKMirror download request", err)
	}
	s.setHeaders(req, referer, "application/vnd.android.package-archive, application/zip, */*")
	resp, err := s.client().Do(req)
	if err != nil {
		return apkMirrorNetworkError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apkMirrorStatusError(resp.StatusCode)
	}
	max := s.maxBytes()
	if resp.ContentLength > max {
		return setupError(ErrSizeLimit, "APKMirror", "APKMirror package exceeds the safe download limit", nil)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return setupError(ErrInstall, "APKMirror", "cannot create a private download file", err)
	}
	var done int64
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	reader := &progressReader{ctx: ctx, r: io.LimitReader(resp.Body, max+1), onRead: func(n int64) {
		done += n
		report(progress, PhaseAcquiring, done, total, "Downloading the selected APKMirror package")
	}}
	n, copyErr := io.Copy(f, reader)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(dest)
		if copyErr == nil {
			copyErr = syncErr
		}
		if copyErr == nil {
			copyErr = closeErr
		}
		return setupError(ErrNetwork, "APKMirror", "APKMirror package transfer did not complete", copyErr)
	}
	if n <= 0 || n > max {
		_ = os.Remove(dest)
		return setupError(ErrSizeLimit, "APKMirror", "APKMirror package has an invalid size", nil)
	}
	return nil
}

func (s *APKMirrorSource) setHeaders(req *http.Request, referer, accept string) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
}

func (s *APKMirrorSource) resolve(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	base, err := url.Parse(s.origin() + "/")
	if err != nil {
		return nil, setupError(ErrSourceTrust, "APKMirror", "APKMirror origin is invalid", err)
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, setupError(ErrSourceTrust, "APKMirror", "APKMirror returned an invalid URL", err)
	}
	resolved := base.ResolveReference(ref)
	if err := s.trustedURL(resolved); err != nil {
		return nil, setupError(ErrSourceTrust, "APKMirror", "APKMirror URL is outside the Roblox listing or download path", err)
	}
	return resolved, nil
}

func (s *APKMirrorSource) trustedURL(u *url.URL) error {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("HTTPS URL without credentials required")
	}
	origin, err := url.Parse(s.origin() + "/")
	if err != nil {
		return err
	}
	if !strings.EqualFold(u.Scheme, origin.Scheme) {
		return fmt.Errorf("unexpected URL scheme")
	}
	host := strings.ToLower(u.Hostname())
	allowed := map[string]struct{}{
		"www.apkmirror.com": {},
		"apkmirror.com":     {},
	}
	if origin.Hostname() != "" {
		allowed[strings.ToLower(origin.Hostname())] = struct{}{}
	}
	if _, ok := allowed[host]; !ok {
		return fmt.Errorf("host not trusted")
	}
	path := u.EscapedPath()
	if strings.EqualFold(path, apkMirrorDownloadPHP) || strings.HasSuffix(strings.ToLower(path), "/download.php") {
		return nil
	}
	if isRobloxAppPath(path) {
		return nil
	}
	return fmt.Errorf("path not allowed")
}

func apkMirrorNetworkError(err error) error {
	return setupError(ErrNetwork, "APKMirror", "APKMirror request failed; the site may be blocking automated downloads", err)
}

func apkMirrorStatusError(status int) error {
	if status == http.StatusForbidden || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		return setupError(ErrNetwork, "APKMirror", "APKMirror blocked or rate-limited the request", nil)
	}
	return setupError(ErrNetwork, "APKMirror", fmt.Sprintf("APKMirror returned HTTP %d", status), nil)
}

var _ Source = (*APKMirrorSource)(nil)
